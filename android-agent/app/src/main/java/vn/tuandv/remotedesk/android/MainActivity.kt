package vn.tuandv.remotedesk.android

import android.Manifest
import android.app.Activity
import android.content.Intent
import android.content.pm.PackageManager
import android.graphics.Typeface
import android.net.Uri
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.os.PowerManager
import android.provider.Settings
import android.text.InputType
import android.view.View
import android.widget.Button
import android.widget.CheckBox
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView

/** Setup: server, access code, the three permissions, the BirdMonitor speaker. */
class MainActivity : Activity() {
    private lateinit var cfg: Config
    private lateinit var status: TextView
    private val ui = Handler(Looper.getMainLooper())

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        cfg = Config(this)
        val pad = (16 * resources.displayMetrics.density).toInt()
        val col = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(pad, pad, pad, pad)
        }
        fun label(t: String, bold: Boolean = false) = TextView(this).apply {
            text = t
            if (bold) setTypeface(typeface, Typeface.BOLD)
            setPadding(0, pad / 2, 0, pad / 4)
        }.also { col.addView(it) }
        fun field(v: String, hint: String, secret: Boolean = false) = EditText(this).apply {
            setText(v)
            this.hint = hint
            isSingleLine = true
            inputType = if (secret) InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_PASSWORD
            else InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_URI
        }.also { col.addView(it) }
        fun button(t: String, on: () -> Unit) = Button(this).apply {
            text = t
            isAllCaps = false
            setOnClickListener { on() }
        }.also { col.addView(it) }

        status = label("").apply { setTypeface(Typeface.MONOSPACE) }

        label("Máy chủ RemoteDesk", true)
        val server = field(cfg.server, DEFAULT_SERVER)
        label("Mã truy cập", true)
        val token = field(cfg.token, "mã giống khi cài agent máy tính", secret = true)
        label("Tên điện thoại (hiện trong danh sách máy)", true)
        val name = field(cfg.name, "VD: Điện thoại lồng vẹt").apply {
            inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_FLAG_CAP_SENTENCES
        }

        label("BirdMonitor (lồng vẹt)", true)
        val speakerOn = CheckBox(this).apply {
            text = "Loa (thay trang Loa trên Chrome)"
            isChecked = cfg.speakerOn
        }.also { col.addView(it) }
        val cameraOn = CheckBox(this).apply {
            text = "Camera + micro (thay IP Webcam, không cần Tailscale/ADB)"
            isChecked = cfg.cameraOn
        }.also { col.addView(it) }
        val speakerBox = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }.also { col.addView(it) }
        val speakerUrl = EditText(this).apply {
            setText(cfg.speakerUrl); hint = DEFAULT_SPEAKER_URL; isSingleLine = true
            inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_URI
        }
        val speakerToken = EditText(this).apply {
            setText(cfg.speakerToken); hint = "API token (để trống nếu server không bật)"; isSingleLine = true
            inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_PASSWORD
        }
        speakerBox.addView(speakerUrl)
        speakerBox.addView(speakerToken)
        fun showBird() {
            speakerBox.visibility = if (speakerOn.isChecked || cameraOn.isChecked) View.VISIBLE else View.GONE
        }
        showBird()
        speakerOn.setOnCheckedChangeListener { _, _ -> showBird() }
        cameraOn.setOnCheckedChangeListener { _, on ->
            showBird()
            if (on) askCameraMic()
        }

        button("Lưu và kết nối") {
            cfg.server = server.text.toString().ifBlank { DEFAULT_SERVER }
            cfg.token = token.text.toString()
            cfg.name = name.text.toString()
            cfg.speakerOn = speakerOn.isChecked
            cfg.cameraOn = cameraOn.isChecked
            cfg.speakerUrl = speakerUrl.text.toString().ifBlank { DEFAULT_SPEAKER_URL }
            cfg.speakerToken = speakerToken.text.toString()
            AgentService.start(this, reload = true)
            refresh()
        }

        label("Cấp quyền (một lần)", true)
        button("1. Cho phép chụp màn hình") {
            AgentService.start(this)
            startActivity(Intent(this, ProjectionActivity::class.java))
        }
        label("Tích \"Không hiển thị lại\" rồi bấm \"Bắt đầu ngay\": sau này máy khởi động lại cũng không phải bấm nữa.")
        button("2. Bật Trợ năng → RemoteDesk") {
            startActivity(Intent(Settings.ACTION_ACCESSIBILITY_SETTINGS))
        }
        label("Để chạm, vuốt, gõ chữ và bấm Back / Home từ xa. Trên Samsung: Trợ năng → Dịch vụ đã cài đặt → RemoteDesk → Bật.")
        button("3. Cho chạy nền liên tục") {
            startActivity(Intent(Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS, Uri.parse("package:$packageName")))
        }
        label("Thêm trên Samsung: Cài đặt → Bảo trì thiết bị → Pin → không cho RemoteDesk vào \"ứng dụng ngủ\". Khoá màn hình nên để \"Vuốt\" hoặc \"Không\" (có mã PIN thì từ xa không mở khoá được).")
        button("4. Cho phép tự cập nhật") {
            startActivity(Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES, Uri.parse("package:$packageName")))
        }
        label("Bật \"Cho phép từ nguồn này\": nút Cập nhật trên trang web sẽ cài được bản mới mà không cần cầm máy.")
        button("5. Cho phép Camera và Micro (BirdMonitor)") { askCameraMic() }
        label("Chỉ cần khi bật \"Camera + micro\". Tắt hẳn IP Webcam để app dùng được camera.")
        label("Phiên bản ${BuildConfig.VERSION_NAME} · mã máy ${cfg.id}")

        setContentView(ScrollView(this).apply { addView(col) })
        if (cfg.ready) AgentService.start(this)
    }

    private fun camMicGranted() = PERMS.all { checkSelfPermission(it) == PackageManager.PERMISSION_GRANTED }

    private fun askCameraMic() {
        if (!camMicGranted()) requestPermissions(PERMS, 1)
    }

    override fun onRequestPermissionsResult(requestCode: Int, permissions: Array<out String>, grantResults: IntArray) {
        super.onRequestPermissionsResult(requestCode, permissions, grantResults)
        // The camera client retries on its own; make it pick the grant up now.
        if (camMicGranted() && cfg.cameraOn) AgentService.start(this, reload = true)
        refresh()
    }

    companion object {
        private val PERMS = arrayOf(Manifest.permission.CAMERA, Manifest.permission.RECORD_AUDIO)
    }

    override fun onResume() {
        super.onResume()
        tick()
    }

    override fun onPause() {
        ui.removeCallbacksAndMessages(null)
        super.onPause()
    }

    private fun tick() {
        refresh()
        ui.postDelayed(::tick, 1_000)
    }

    private fun refresh() {
        val s = AgentService.instance?.status()
        val pm = getSystemService(POWER_SERVICE) as PowerManager
        fun ok(b: Boolean) = if (b) "✓" else "✗"
        status.text = buildString {
            if (s == null) {
                append(if (cfg.ready) "Đang khởi động…" else "Nhập mã truy cập rồi bấm \"Lưu và kết nối\".")
                return@buildString
            }
            append("${ok(s.relayUp)} Máy chủ: ${s.relay}\n")
            append("${ok(s.capture)} Chụp màn hình\n")
            append("${ok(s.input)} Trợ năng (điều khiển)\n")
            append("${ok(pm.isIgnoringBatteryOptimizations(packageName))} Chạy nền liên tục\n")
            append("${ok(packageManager.canRequestPackageInstalls())} Tự cập nhật\n")
            if (cfg.cameraOn) append("${ok(camMicGranted())} Quyền Camera + Micro\n")
            s.speaker?.let { append("• Loa BirdMonitor: $it\n") }
            s.camera?.let { append("• Camera BirdMonitor: $it\n") }
            if (s.viewing) append("• Đang có người xem / điều khiển\n")
        }
    }
}
