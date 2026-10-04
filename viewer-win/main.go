// RemoteDesk viewer for Windows: a WebView2 window around the web viewer, so it
// runs as its own app (taskbar icon, own login cookie, no browser chrome).
// WebView2 ships with Windows 10/11; this builds without cgo.
package main

import (
	"os"
	"path/filepath"
	"syscall"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
)

const startURL = "https://remote.tuandv.id.vn/"

var (
	user32               = syscall.NewLazyDLL("user32.dll")
	procGetWindowLongPtr = user32.NewProc("GetWindowLongPtrW")
	procSetWindowLongPtr = user32.NewProc("SetWindowLongPtrW")
	procSetWindowPos     = user32.NewProc("SetWindowPos")
	procGetWindowRect    = user32.NewProc("GetWindowRect")
	procMonitorFromWnd   = user32.NewProc("MonitorFromWindow")
	procGetMonitorInfo   = user32.NewProc("GetMonitorInfoW")
	procMessageBox       = user32.NewProc("MessageBoxW")
)

const (
	gwlStyle           = ^uintptr(15) // GWL_STYLE (-16)
	wsOverlapped       = 0x00CF0000   // WS_OVERLAPPEDWINDOW
	swpFrameChanged    = 0x0020
	swpNoZOrder        = 0x0004
	swpNoOwnerZOrder   = 0x0200
	monitorDefaultNear = 2
)

type rect struct{ L, T, R, B int32 }

type monitorInfo struct {
	Size    uint32
	Monitor rect
	Work    rect
	Flags   uint32
}

// Borderless window covering the monitor, toggled from the page's ⛶ button.
type fullscreen struct {
	hwnd  uintptr
	on    bool
	style uintptr
	saved rect
}

func (f *fullscreen) toggle() {
	if !f.on {
		f.style, _, _ = procGetWindowLongPtr.Call(f.hwnd, gwlStyle)
		procGetWindowRect.Call(f.hwnd, uintptr(unsafe.Pointer(&f.saved)))
		mon, _, _ := procMonitorFromWnd.Call(f.hwnd, monitorDefaultNear)
		mi := monitorInfo{Size: uint32(unsafe.Sizeof(monitorInfo{}))}
		procGetMonitorInfo.Call(mon, uintptr(unsafe.Pointer(&mi)))
		procSetWindowLongPtr.Call(f.hwnd, gwlStyle, f.style&^wsOverlapped)
		m := mi.Monitor
		procSetWindowPos.Call(f.hwnd, 0, uintptr(m.L), uintptr(m.T), uintptr(m.R-m.L), uintptr(m.B-m.T),
			swpNoZOrder|swpNoOwnerZOrder|swpFrameChanged)
	} else {
		procSetWindowLongPtr.Call(f.hwnd, gwlStyle, f.style)
		s := f.saved
		procSetWindowPos.Call(f.hwnd, 0, uintptr(s.L), uintptr(s.T), uintptr(s.R-s.L), uintptr(s.B-s.T),
			swpNoZOrder|swpNoOwnerZOrder|swpFrameChanged)
	}
	f.on = !f.on
}

func main() {
	dataDir := filepath.Join(os.Getenv("LOCALAPPDATA"), "RemoteDesk", "Viewer")
	_ = os.MkdirAll(dataDir, 0o755)

	w := webview2.NewWithOptions(webview2.WebViewOptions{
		AutoFocus: true,
		DataPath:  dataDir, // keeps the login cookie between launches
		WindowOptions: webview2.WindowOptions{
			Title:  "RemoteDesk",
			Width:  1366,
			Height: 860,
			IconId: 1, // from rsrc_windows_amd64.syso
			Center: true,
		},
	})
	if w == nil {
		msg, _ := syscall.UTF16PtrFromString("Không mở được WebView2. Cài Microsoft Edge WebView2 Runtime:\nhttps://go.microsoft.com/fwlink/p/?LinkId=2124703")
		title, _ := syscall.UTF16PtrFromString("RemoteDesk")
		procMessageBox.Call(0, uintptr(unsafe.Pointer(msg)), uintptr(unsafe.Pointer(title)), 0x10)
		os.Exit(1)
	}
	defer w.Destroy()

	fs := &fullscreen{hwnd: uintptr(w.Window())}
	_ = w.Bind("rdNativeFullscreen", func() bool {
		w.Dispatch(fs.toggle)
		return !fs.on
	})
	// The page calls window.rdNative.fullscreen() instead of the Fullscreen API
	// (which would only fill the WebView, not the screen).
	w.Init(`window.rdNative = { platform: 'windows', fullscreen: () => window.rdNativeFullscreen() };`)
	w.Navigate(startURL)
	w.Run()
}
