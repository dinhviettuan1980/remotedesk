// RemoteDesk viewer for iPhone/iPad: a fullscreen WKWebView around the web viewer.
// What the app adds over Safari:
//  • no address bar or status bar, and the bottom-edge swipe goes to the page
//    first (touchpad drags near the edge don't send you to the home screen);
//  • no iOS form bar above the keyboard, so the page's key row sits right on it;
//  • the keyboard opens by itself when a click lands in a remote text field
//    (Safari only opens it from a tap, so there the page shows a button);
//  • the screen stays awake during a session;
//  • downloaded files open the share sheet (save to Files, AirDrop, …).
import UIKit
import WebKit
import ObjectiveC

let startURL: URL = {
    let s = Bundle.main.object(forInfoDictionaryKey: "RDServer") as? String ?? ""
    return URL(string: s) ?? URL(string: "https://remote.tuandv.id.vn/")!
}()

@main
final class AppDelegate: UIResponder, UIApplicationDelegate {}

final class SceneDelegate: UIResponder, UIWindowSceneDelegate {
    var window: UIWindow?

    func scene(_ scene: UIScene, willConnectTo session: UISceneSession, options: UIScene.ConnectionOptions) {
        guard let ws = scene as? UIWindowScene else { return }
        let w = UIWindow(windowScene: ws)
        w.backgroundColor = .black
        w.rootViewController = WebController()
        w.makeKeyAndVisible()
        window = w
    }
}

final class WebController: UIViewController, WKNavigationDelegate, WKUIDelegate, WKDownloadDelegate {
    private var web: WKWebView!
    private var urlObservation: NSKeyValueObservation?
    private var downloads: [ObjectIdentifier: URL] = [:]

    override var prefersStatusBarHidden: Bool { true }
    override var prefersHomeIndicatorAutoHidden: Bool { true }
    override var preferredScreenEdgesDeferringSystemGestures: UIRectEdge { .bottom }

    override func loadView() {
        let cfg = WKWebViewConfiguration()
        cfg.websiteDataStore = .default() // persistent: log in once
        cfg.allowsInlineMediaPlayback = true // the P2P <video> decodes off-screen
        cfg.mediaTypesRequiringUserActionForPlayback = []
        cfg.applicationNameForUserAgent = "RemoteDeskApp/0.1"
        let ucc = WKUserContentController()
        let auto = WebController.allowProgrammaticKeyboard()
        ucc.addUserScript(WKUserScript(source: "window.rdNative = { platform: 'ios', autoKeyboard: \(auto) };",
                                       injectionTime: .atDocumentStart, forMainFrameOnly: true))
        // Pinch belongs to the remote screen, never to the page itself.
        ucc.addUserScript(WKUserScript(source: """
            (() => { const m = document.querySelector('meta[name=viewport]');
              if (m) m.content += ', maximum-scale=1, user-scalable=no'; })();
            """, injectionTime: .atDocumentEnd, forMainFrameOnly: true))
        cfg.userContentController = ucc

        web = WKWebView(frame: .zero, configuration: cfg)
        web.navigationDelegate = self
        web.uiDelegate = self
        web.isOpaque = false
        web.backgroundColor = .black
        web.allowsBackForwardNavigationGestures = false
        web.scrollView.contentInsetAdjustmentBehavior = .never // the page pads for the notch itself
        web.scrollView.isScrollEnabled = false
        web.scrollView.bounces = false
        #if DEBUG
        if #available(iOS 16.4, *) { web.isInspectable = true } // Safari → Develop
        #endif
        view = web
    }

    override func viewDidLoad() {
        super.viewDidLoad()
        hideInputAccessory()
        // Keep the phone awake while a session (#/d/<id>) is open.
        urlObservation = web.observe(\.url, options: [.new]) { web, _ in
            UIApplication.shared.isIdleTimerDisabled = web.url?.fragment?.hasPrefix("/d/") ?? false
        }
        web.load(URLRequest(url: startURL))
    }

    // WebKit shows the keyboard for focus() only during a tap. The page focuses
    // its hidden textarea when the agent reports a text field (after the tap,
    // once the answer is back), so let focus from script count as a tap.
    // Private WebKit method: if it is gone, the page falls back to its button.
    private static var keyboardPatched: Bool?
    static func allowProgrammaticKeyboard() -> Bool {
        if let done = keyboardPatched { return done }
        let sel = sel_getUid("_elementDidFocus:userIsInteracting:blurPreviousNode:activityStateChanges:userObject:")
        guard let cls = NSClassFromString("WKContentView"), let m = class_getInstanceMethod(cls, sel) else {
            keyboardPatched = false
            return false
        }
        typealias Fn = @convention(c) (AnyObject, Selector, UnsafeRawPointer?, Bool, Bool, UInt64, AnyObject?) -> Void
        let orig = unsafeBitCast(method_getImplementation(m), to: Fn.self)
        let block: @convention(block) (AnyObject, UnsafeRawPointer?, Bool, Bool, UInt64, AnyObject?) -> Void = {
            me, info, _, blur, changes, obj in orig(me, sel, info, true, blur, changes, obj)
        }
        method_setImplementation(m, imp_implementationWithBlock(block))
        keyboardPatched = true
        return true
    }

    // The bar with ‹ › Done that iOS puts above the keyboard for web text fields:
    // swap the web content view for a subclass without it.
    private func hideInputAccessory() {
        guard let content = web.scrollView.subviews.first(where: { String(describing: type(of: $0)).hasPrefix("WKContent") }) else { return }
        let base: AnyClass = type(of: content)
        let name = "\(NSStringFromClass(base))_RDNoAccessory"
        var cls: AnyClass? = NSClassFromString(name)
        if cls == nil, let c = objc_allocateClassPair(base, name, 0) {
            let sel = #selector(getter: UIResponder.inputAccessoryView)
            let block: @convention(block) (AnyObject) -> AnyObject? = { _ in nil }
            if let m = class_getInstanceMethod(UIResponder.self, sel) {
                class_addMethod(c, sel, imp_implementationWithBlock(block), method_getTypeEncoding(m))
            }
            objc_registerClassPair(c)
            cls = c
        }
        if let cls { object_setClass(content, cls) }
    }

    // MARK: navigation

    func webView(_ webView: WKWebView, decidePolicyFor action: WKNavigationAction,
                 decisionHandler: @escaping (WKNavigationActionPolicy) -> Void) {
        if action.shouldPerformDownload { decisionHandler(.download); return }
        // Installer links and other sites open in Safari.
        if let url = action.request.url, action.navigationType == .linkActivated,
           url.host != startURL.host || url.path.hasPrefix("/dl/") {
            UIApplication.shared.open(url)
            decisionHandler(.cancel)
            return
        }
        decisionHandler(.allow)
    }

    func webView(_ webView: WKWebView, decidePolicyFor response: WKNavigationResponse,
                 decisionHandler: @escaping (WKNavigationResponsePolicy) -> Void) {
        decisionHandler(response.canShowMIMEType ? .allow : .download)
    }

    func webView(_ webView: WKWebView, createWebViewWith cfg: WKWebViewConfiguration,
                 for action: WKNavigationAction, windowFeatures: WKWindowFeatures) -> WKWebView? {
        if let url = action.request.url { UIApplication.shared.open(url) }
        return nil
    }

    // A test server on the LAN (build.sh --dev) has a self-signed certificate.
    func webView(_ webView: WKWebView, didReceive challenge: URLAuthenticationChallenge,
                 completionHandler: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
        let space = challenge.protectionSpace
        if space.authenticationMethod == NSURLAuthenticationMethodServerTrust,
           space.host == startURL.host, isPrivateHost(space.host), let trust = space.serverTrust {
            completionHandler(.useCredential, URLCredential(trust: trust))
            return
        }
        completionHandler(.performDefaultHandling, nil)
    }

    private func isPrivateHost(_ h: String) -> Bool {
        let p = h.split(separator: ".").compactMap { Int($0) }
        guard p.count == 4 else { return h.hasSuffix(".local") }
        return p[0] == 10 || (p[0] == 192 && p[1] == 168) || (p[0] == 172 && (16...31).contains(p[1]))
    }

    // Retry when offline at launch instead of leaving a blank screen.
    func webView(_ webView: WKWebView, didFailProvisionalNavigation nav: WKNavigation!, withError error: Error) {
        DispatchQueue.main.asyncAfter(deadline: .now() + 3) { webView.load(URLRequest(url: startURL)) }
    }

    // The web process can be killed in the background; reload instead of a white page.
    func webViewWebContentProcessDidTerminate(_ webView: WKWebView) { webView.reload() }

    // MARK: downloads → share sheet

    func webView(_ webView: WKWebView, navigationAction: WKNavigationAction, didBecome download: WKDownload) {
        download.delegate = self
    }

    func webView(_ webView: WKWebView, navigationResponse: WKNavigationResponse, didBecome download: WKDownload) {
        download.delegate = self
    }

    func download(_ download: WKDownload, decideDestinationUsing response: URLResponse,
                  suggestedFilename: String, completionHandler: @escaping (URL?) -> Void) {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let url = dir.appendingPathComponent(suggestedFilename)
        downloads[ObjectIdentifier(download)] = url
        completionHandler(url)
    }

    func downloadDidFinish(_ download: WKDownload) {
        guard let url = downloads.removeValue(forKey: ObjectIdentifier(download)) else { return }
        let share = UIActivityViewController(activityItems: [url], applicationActivities: nil)
        share.popoverPresentationController?.sourceView = view
        share.popoverPresentationController?.sourceRect = CGRect(x: view.bounds.midX, y: view.bounds.midY, width: 0, height: 0)
        topController().present(share, animated: true)
    }

    func download(_ download: WKDownload, didFailWithError error: Error, resumeData: Data?) {
        downloads.removeValue(forKey: ObjectIdentifier(download))
    }

    // MARK: alert() / confirm(): WKWebView shows nothing unless the host does.

    func webView(_ webView: WKWebView, runJavaScriptAlertPanelWithMessage message: String,
                 initiatedByFrame frame: WKFrameInfo, completionHandler: @escaping () -> Void) {
        let a = UIAlertController(title: nil, message: message, preferredStyle: .alert)
        a.addAction(UIAlertAction(title: "OK", style: .default) { _ in completionHandler() })
        topController().present(a, animated: true)
    }

    func webView(_ webView: WKWebView, runJavaScriptConfirmPanelWithMessage message: String,
                 initiatedByFrame frame: WKFrameInfo, completionHandler: @escaping (Bool) -> Void) {
        let a = UIAlertController(title: nil, message: message, preferredStyle: .alert)
        a.addAction(UIAlertAction(title: "Huỷ", style: .cancel) { _ in completionHandler(false) })
        a.addAction(UIAlertAction(title: "OK", style: .default) { _ in completionHandler(true) })
        topController().present(a, animated: true)
    }

    private func topController() -> UIViewController {
        var c: UIViewController = self
        while let p = c.presentedViewController { c = p }
        return c
    }
}
