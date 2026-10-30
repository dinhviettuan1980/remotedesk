package vn.tuandv.remotedesk.android

import android.accessibilityservice.AccessibilityService
import android.accessibilityservice.GestureDescription
import android.content.ClipData
import android.content.ClipboardManager
import android.graphics.Path
import android.graphics.Rect
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.os.SystemClock
import android.util.DisplayMetrics
import android.view.WindowManager
import android.view.accessibility.AccessibilityEvent
import android.view.accessibility.AccessibilityNodeInfo
import org.json.JSONObject
import kotlin.math.hypot

/**
 * Remote input through the accessibility API (no root, no ADB):
 *  - mouse / touch (md → mm… → mu, normalized x/y) become one gesture,
 *    replayed when the button comes up: tap, long press, swipe, or long press
 *    then drag. Right button = Back, middle = Home. Wheel = a short swipe.
 *  - "txt" types into the focused field; Backspace, arrows, Ctrl/⌘+A/C/V/X
 *    work on it too. Enter submits on Android 11+, else adds a line break in
 *    multi-line fields (the phone's own keyboard is on screen for the rest).
 *  - "nav": Back / Home / Recents / notifications / power menu / wake.
 * It also taps "Start now" on our own screen-capture request, and "Install" on
 * our own update, while one of those is pending.
 */
class InputService : AccessibilityService() {
    private val main = Handler(Looper.getMainLooper())

    private var x = 0.5f
    private var y = 0.5f
    private var downAt = 0L
    private var firstMoveAt = 0L
    private val path = ArrayList<FloatArray>() // x, y in pixels while the button is down
    private var wheelX = 0f
    private var wheelY = 0f
    private var wheelPending = false
    private val mods = HashSet<String>()

    override fun onServiceConnected() {
        instance = this
    }

    override fun onDestroy() {
        if (instance === this) instance = null
        super.onDestroy()
    }

    override fun onInterrupt() {}

    /** From the agent thread; all state lives on the main thread. */
    fun onMessage(m: JSONObject) = main.post { handle(m) }

    fun releaseAll() = main.post {
        path.clear()
        downAt = 0
        mods.clear()
    }

    private fun handle(m: JSONObject) {
        when (m.optString("t")) {
            "mm" -> {
                x = m.optDouble("x", 0.5).toFloat().coerceIn(0f, 1f)
                y = m.optDouble("y", 0.5).toFloat().coerceIn(0f, 1f)
                if (downAt != 0L) {
                    val (px, py) = px()
                    val last = path.last()
                    if (hypot(px - last[0], py - last[1]) >= 2f) {
                        if (firstMoveAt == 0L && hypot(px - path[0][0], py - path[0][1]) >= TOUCH_SLOP) firstMoveAt = SystemClock.uptimeMillis()
                        path.add(floatArrayOf(px, py))
                    }
                }
            }
            "md" -> if (m.optInt("b") == 0) {
                downAt = SystemClock.uptimeMillis()
                firstMoveAt = 0L
                path.clear()
                val (px, py) = px()
                path.add(floatArrayOf(px, py))
            }
            "mu" -> when (m.optInt("b")) {
                0 -> if (downAt != 0L) finishGesture()
                1 -> performGlobalAction(GLOBAL_ACTION_HOME)
                2 -> performGlobalAction(GLOBAL_ACTION_BACK)
            }
            "wh" -> {
                wheelX += m.optDouble("dx", 0.0).toFloat()
                wheelY += m.optDouble("dy", 0.0).toFloat()
                if (!wheelPending) {
                    wheelPending = true
                    main.postDelayed(::flushWheel, 80)
                }
            }
            "kd" -> keyDown(m.optString("c"))
            "ku" -> mods.remove(m.optString("c"))
            "txt" -> insert(m.optString("text"))
            "ra" -> releaseAll()
            "clip" -> clipboard().setPrimaryClip(ClipData.newPlainText("RemoteDesk", m.optString("text")))
            "nav" -> nav(m.optString("k"))
        }
    }

    private fun screen(): Pair<Int, Int> {
        val dm = DisplayMetrics()
        @Suppress("DEPRECATION")
        (getSystemService(WINDOW_SERVICE) as WindowManager).defaultDisplay.getRealMetrics(dm)
        return Pair(dm.widthPixels, dm.heightPixels)
    }

    private fun px(): Pair<Float, Float> {
        val (w, h) = screen()
        return Pair((x * (w - 1)), (y * (h - 1)))
    }

    // ------------------------------------------------------------ gestures

    private fun finishGesture() {
        val now = SystemClock.uptimeMillis()
        val held = (now - downAt).coerceIn(1, 30_000)
        val pts = ArrayList(path)
        val moved = firstMoveAt != 0L
        val holdBeforeMove = if (moved) firstMoveAt - downAt else held
        downAt = 0
        path.clear()
        val start = pts[0]
        if (!moved) {
            // Tap or long press, as long as the button was really held.
            stroke(Path().apply { moveTo(start[0], start[1]) }, held)
            main.postDelayed({ reportEdit() }, 350)
            return
        }
        val swipe = Path().apply {
            moveTo(start[0], start[1])
            for (p in pts.drop(1)) lineTo(p[0], p[1])
        }
        val moveTime = (now - firstMoveAt).coerceIn(50, 10_000)
        if (holdBeforeMove >= LONG_PRESS) {
            // Long press, then drag (move an icon, select text): two strokes,
            // the second continuing the first without lifting the finger.
            val hold = GestureDescription.StrokeDescription(Path().apply { moveTo(start[0], start[1]) }, 0, holdBeforeMove, true)
            dispatch(hold) { dispatch(hold.continueStroke(swipe, 0, moveTime, false)) }
        } else {
            stroke(swipe, moveTime)
        }
    }

    private fun stroke(p: Path, ms: Long) = dispatch(GestureDescription.StrokeDescription(p, 0, ms))

    private fun dispatch(s: GestureDescription.StrokeDescription, then: (() -> Unit)? = null) {
        val g = GestureDescription.Builder().addStroke(s).build()
        dispatchGesture(g, object : GestureResultCallback() {
            override fun onCompleted(gestureDescription: GestureDescription?) {
                then?.invoke()
            }
        }, null)
    }

    /** Wheel: content follows the fingers, so scrolling down = swipe up. */
    private fun flushWheel() {
        wheelPending = false
        val (w, h) = screen()
        val dy = (wheelY * h / 1500f).coerceIn(-h * 0.6f, h * 0.6f)
        val dx = (wheelX * w / 1500f).coerceIn(-w * 0.6f, w * 0.6f)
        wheelX = 0f
        wheelY = 0f
        if (kotlin.math.abs(dx) < 4 && kotlin.math.abs(dy) < 4) return
        val (px, py) = px()
        val m = 0.05f
        val sx = px.coerceIn(w * m + maxOf(dx, 0f), w * (1 - m) + minOf(dx, 0f))
        val sy = py.coerceIn(h * m + maxOf(dy, 0f), h * (1 - m) + minOf(dy, 0f))
        stroke(Path().apply { moveTo(sx, sy); lineTo(sx - dx, sy - dy) }, 250)
    }

    // ------------------------------------------------------------ text

    private fun focusedEdit(): AccessibilityNodeInfo? =
        rootInActiveWindow?.findFocus(AccessibilityNodeInfo.FOCUS_INPUT)?.takeIf { it.isEditable }

    private fun currentText(n: AccessibilityNodeInfo): String =
        if (n.isShowingHintText) "" else n.text?.toString() ?: ""

    private fun selection(n: AccessibilityNodeInfo, len: Int): Pair<Int, Int> {
        var s = n.textSelectionStart
        var e = n.textSelectionEnd
        if (s < 0 || e < 0 || s > len || e > len) { s = len; e = len }
        return Pair(minOf(s, e), maxOf(s, e))
    }

    private fun setText(n: AccessibilityNodeInfo, text: String, caret: Int) {
        n.performAction(AccessibilityNodeInfo.ACTION_SET_TEXT, Bundle().apply {
            putCharSequence(AccessibilityNodeInfo.ACTION_ARGUMENT_SET_TEXT_CHARSEQUENCE, text)
        })
        setSelection(n, caret, caret)
    }

    private fun setSelection(n: AccessibilityNodeInfo, s: Int, e: Int) {
        n.performAction(AccessibilityNodeInfo.ACTION_SET_SELECTION, Bundle().apply {
            putInt(AccessibilityNodeInfo.ACTION_ARGUMENT_SELECTION_START_INT, s)
            putInt(AccessibilityNodeInfo.ACTION_ARGUMENT_SELECTION_END_INT, e)
        })
    }

    private fun insert(t: String) {
        if (t.isEmpty()) return
        val n = focusedEdit() ?: return
        if (n.isPassword) { // its text reads back as dots: paste instead of rewriting it
            clipboard().setPrimaryClip(ClipData.newPlainText("", t))
            n.performAction(AccessibilityNodeInfo.ACTION_PASTE)
            return
        }
        val cur = currentText(n)
        val (s, e) = selection(n, cur.length)
        setText(n, cur.substring(0, s) + t + cur.substring(e), s + t.length)
    }

    private fun keyDown(code: String) {
        when (code) {
            "ControlLeft", "ControlRight", "MetaLeft", "MetaRight", "ShiftLeft", "ShiftRight", "AltLeft", "AltRight" -> {
                mods.add(code)
                return
            }
            "Escape", "BrowserBack" -> { performGlobalAction(GLOBAL_ACTION_BACK); return }
        }
        val ctrl = mods.any { it.startsWith("Control") || it.startsWith("Meta") }
        val n = focusedEdit() ?: return
        if (ctrl) {
            when (code) {
                "KeyA" -> setSelection(n, 0, currentText(n).length)
                "KeyC" -> n.performAction(AccessibilityNodeInfo.ACTION_COPY)
                "KeyX" -> n.performAction(AccessibilityNodeInfo.ACTION_CUT)
                "KeyV" -> n.performAction(AccessibilityNodeInfo.ACTION_PASTE)
            }
            return
        }
        val cur = currentText(n)
        val (s, e) = selection(n, cur.length)
        when (code) {
            "Backspace" -> when {
                s != e -> setText(n, cur.removeRange(s, e), s)
                s > 0 -> setText(n, cur.removeRange(s - 1, s), s - 1)
            }
            "Delete" -> when {
                s != e -> setText(n, cur.removeRange(s, e), s)
                s < cur.length -> setText(n, cur.removeRange(s, s + 1), s)
            }
            "ArrowLeft" -> (if (s != e) s else maxOf(0, s - 1)).let { setSelection(n, it, it) }
            "ArrowRight" -> (if (s != e) e else minOf(cur.length, e + 1)).let { setSelection(n, it, it) }
            "Home", "ArrowUp" -> setSelection(n, 0, 0)
            "End", "ArrowDown" -> setSelection(n, cur.length, cur.length)
            "Enter", "NumpadEnter" -> when {
                n.isMultiLine -> insert("\n")
                android.os.Build.VERSION.SDK_INT >= 30 ->
                    n.performAction(AccessibilityNodeInfo.AccessibilityAction.ACTION_IME_ENTER.id)
            }
        }
    }

    /** After a tap: tell viewers whether a text field has the focus (phones open their keyboard). */
    private fun reportEdit() {
        val n = focusedEdit()
        val m = JSONObject().put("t", "edit").put("on", n != null).put("click", true)
        if (n != null) {
            val r = Rect()
            n.getBoundsInScreen(r)
            val (w, h) = screen()
            m.put("x", r.centerX().toDouble() / w).put("y", r.centerY().toDouble() / h)
        }
        AgentService.instance?.send(m)
    }

    private fun clipboard() = getSystemService(CLIPBOARD_SERVICE) as ClipboardManager

    private fun nav(k: String) {
        when (k) {
            "back" -> performGlobalAction(GLOBAL_ACTION_BACK)
            "home" -> performGlobalAction(GLOBAL_ACTION_HOME)
            "recents" -> performGlobalAction(GLOBAL_ACTION_RECENTS)
            "notifications" -> performGlobalAction(GLOBAL_ACTION_NOTIFICATIONS)
            "quick" -> performGlobalAction(GLOBAL_ACTION_QUICK_SETTINGS)
            "power" -> performGlobalAction(GLOBAL_ACTION_POWER_DIALOG)
            "wake" -> AgentService.instance?.let { a -> a.handler.post { a.wakeScreen() } }
        }
    }

    // ------------------------------------------------------------ our own dialogs

    override fun onAccessibilityEvent(event: AccessibilityEvent?) {
        val pkg = event?.packageName?.toString() ?: return
        val now = SystemClock.uptimeMillis()
        when {
            pkg == "com.android.systemui" && now < consentUntil -> {
                val root = rootInActiveWindow ?: return
                // "Don't show again" first, so later requests are granted silently.
                root.findAccessibilityNodeInfosByViewId("com.android.systemui:id/remember")
                    .firstOrNull()?.let { if (!it.isChecked) it.performAction(AccessibilityNodeInfo.ACTION_CLICK) }
                val ok = root.findAccessibilityNodeInfosByViewId("android:id/button1").firstOrNull()
                if (ok != null && ok.performAction(AccessibilityNodeInfo.ACTION_CLICK)) consentUntil = 0
            }
            pkg.contains("packageinstaller") && now < installUntil -> {
                val root = rootInActiveWindow ?: return
                for (id in listOf("$pkg:id/ok_button", "android:id/button1")) {
                    val b = root.findAccessibilityNodeInfosByViewId(id).firstOrNull() ?: continue
                    if (b.isEnabled) b.performAction(AccessibilityNodeInfo.ACTION_CLICK)
                    break
                }
            }
        }
    }

    companion object {
        private const val LONG_PRESS = 450L
        private const val TOUCH_SLOP = 12f

        @Volatile var instance: InputService? = null; private set
        @Volatile private var consentUntil = 0L
        @Volatile private var installUntil = 0L

        /** Our screen-capture request is about to show: tap "Start now" for 20 s. */
        fun expectConsent() { consentUntil = SystemClock.uptimeMillis() + 20_000 }
        fun consentDone() { consentUntil = 0 }

        /** Our own update is installing: tap "Install" for 2 minutes. */
        fun expectInstall() { installUntil = SystemClock.uptimeMillis() + 120_000 }
    }
}
