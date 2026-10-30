package vn.tuandv.remotedesk.android

import android.app.KeyguardManager
import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.hardware.display.DisplayManager
import android.media.projection.MediaProjection
import android.media.projection.MediaProjectionManager
import android.net.wifi.WifiManager
import android.os.BatteryManager
import android.os.Build
import android.os.Handler
import android.os.HandlerThread
import android.os.IBinder
import android.os.PowerManager
import android.os.SystemClock
import android.util.DisplayMetrics
import android.util.Log
import android.view.WindowManager
import org.json.JSONArray
import org.json.JSONObject

/**
 * The agent: holds the relay link, streams the screen while someone watches,
 * passes their input to [InputService], and runs the BirdMonitor speaker.
 * Everything here runs on one thread ([handler]).
 */
class AgentService : Service() {
    private lateinit var cfg: Config
    private lateinit var thread: HandlerThread
    lateinit var handler: Handler; private set
    private lateinit var relay: RelayClient
    private var speaker: SpeakerClient? = null

    private var projection: MediaProjection? = null
    private var askedAt = 0L
    private var streamer: ScreenStreamer? = null
    private var streamSize = Pair(0, 0)

    private var viewing = false
    private var fps = 15
    private var maxw = 1920
    private var kbps = 2000
    @Volatile private var wsVideo = true
    @Volatile private var dropping = false
    private val t0 = SystemClock.elapsedRealtime()

    private var cpuLock: PowerManager.WakeLock? = null
    private var screenLock: PowerManager.WakeLock? = null
    private var wifiLock: WifiManager.WifiLock? = null

    private val displayListener = object : DisplayManager.DisplayListener {
        override fun onDisplayAdded(id: Int) {}
        override fun onDisplayRemoved(id: Int) {}
        override fun onDisplayChanged(id: Int) {
            // Rotation: stream the new shape (and tell viewers the new size).
            if (viewing && streamer != null && realSize() != streamSize) restartStream()
        }
    }

    override fun onCreate() {
        super.onCreate()
        cfg = Config(this)
        thread = HandlerThread("agent").also { it.start() }
        handler = Handler(thread.looper)
        createChannel()
        startForeground(NOTIF_ID, notification("Đang khởi động…"))
        val pm = getSystemService(POWER_SERVICE) as PowerManager
        // The phone sits on a charger 24/7: keep the CPU and Wi-Fi awake so the
        // relay and speaker links survive the screen being off.
        cpuLock = pm.newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "remotedesk:cpu").apply { acquire() }
        @Suppress("DEPRECATION")
        wifiLock = (applicationContext.getSystemService(WIFI_SERVICE) as WifiManager)
            .createWifiLock(WifiManager.WIFI_MODE_FULL_HIGH_PERF, "remotedesk:wifi").apply { acquire() }
        relay = RelayClient(cfg, handler, ::onRelayUp, ::onText, ::onRelayDown)
        (getSystemService(DISPLAY_SERVICE) as DisplayManager).registerDisplayListener(displayListener, handler)
        instance = this
        handler.post { applyConfig() }
        handler.postDelayed(::tick, 5_000)
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_RELOAD) handler.post { applyConfig(); relay.reconnect() }
        return START_STICKY
    }

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onDestroy() {
        instance = null
        (getSystemService(DISPLAY_SERVICE) as DisplayManager).unregisterDisplayListener(displayListener)
        handler.post {
            stopStream()
            relay.stop()
            speaker?.stop()
            projection?.stop()
        }
        thread.quitSafely()
        screenLock?.takeIf { it.isHeld }?.release()
        cpuLock?.takeIf { it.isHeld }?.release()
        wifiLock?.takeIf { it.isHeld }?.release()
        super.onDestroy()
    }

    private fun applyConfig() {
        relay.start()
        if (cfg.speakerOn) {
            if (speaker == null) speaker = SpeakerClient(this, cfg).also { it.start() }
            else speaker!!.restartIfChanged()
        } else {
            speaker?.stop()
            speaker = null
        }
        // Ask for screen capture now, while nobody waits on it.
        if (projection == null && cfg.ready) askProjection()
        updateNotification()
    }

    /**
     * Every 5 s: refresh the notification, retry a missing permission (soon
     * while someone waits for the picture, else every 10 minutes: the request
     * puts a dialog on the phone's screen).
     */
    private fun tick() {
        updateNotification()
        val since = SystemClock.elapsedRealtime() - askedAt
        if (projection == null && cfg.ready && since > (if (viewing) 15_000 else 600_000)) askProjection()
        handler.postDelayed(::tick, 5_000)
    }

    // ------------------------------------------------------------ screen capture

    private fun askProjection() {
        askedAt = SystemClock.elapsedRealtime()
        InputService.expectConsent()
        startActivity(Intent(this, ProjectionActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
    }

    /** From [ProjectionActivity]: the user (or "Don't show again") allowed capture. */
    fun onProjection(resultCode: Int, data: Intent) = handler.post {
        val mpm = getSystemService(MEDIA_PROJECTION_SERVICE) as MediaProjectionManager
        val p = try {
            mpm.getMediaProjection(resultCode, data)
        } catch (e: Exception) {
            Log.w(TAG, "getMediaProjection", e)
            null
        } ?: return@post
        projection?.stop()
        projection = p
        p.registerCallback(object : MediaProjection.Callback() {
            override fun onStop() {
                handler.post {
                    if (projection !== p) return@post
                    Log.w(TAG, "screen capture stopped by the system")
                    projection = null
                    stopStream()
                    if (viewing) {
                        sendError("Điện thoại đã dừng quyền chụp màn hình — đang xin lại…")
                        askProjection()
                    }
                }
            }
        }, handler)
        Log.i(TAG, "screen capture allowed")
        if (viewing) startStream()
        updateNotification()
    }

    fun onProjectionDenied() = handler.post {
        if (viewing) sendError("Chưa có quyền chụp màn hình. Trên điện thoại: mở RemoteDesk → \"Cấp quyền chụp màn hình\", tích \"Không hiển thị lại\" rồi \"Bắt đầu ngay\".")
    }

    private fun realSize(): Pair<Int, Int> {
        val dm = DisplayMetrics()
        @Suppress("DEPRECATION")
        (getSystemService(WINDOW_SERVICE) as WindowManager).defaultDisplay.getRealMetrics(dm)
        return Pair(dm.widthPixels, dm.heightPixels)
    }

    private fun startStream() {
        if (streamer != null || !viewing) return
        val p = projection ?: run {
            sendError("Đang xin quyền chụp màn hình trên điện thoại…")
            if (SystemClock.elapsedRealtime() - askedAt > 10_000) askProjection()
            return
        }
        val (w, h) = realSize()
        streamSize = Pair(w, h)
        val s = ScreenStreamer(p, w, h, resources.displayMetrics.densityDpi, ::onFrame)
        try {
            // Portrait 720p, ≤15 fps: the phone also runs the camera all day.
            s.start(minOf(fps, 15), minOf(maxw, 1280), minOf(kbps, 2500))
        } catch (e: Exception) {
            Log.e(TAG, "encoder", e)
            sendError("Không mở được bộ nén video: ${e.message}")
            return
        }
        streamer = s
        dropping = true // start at a keyframe
        sendInfo(s)
    }

    private fun stopStream() {
        streamer?.stop()
        streamer = null
    }

    private fun restartStream() {
        stopStream()
        startStream()
    }

    /** Encoder thread. [1][flags][ts u32 BE][Annex B] — the relay's video frame. */
    private fun onFrame(key: Boolean, data: ByteArray) {
        if (!wsVideo) return
        if (dropping && !key) return
        if (relay.queued() > 1_500_000) { // uplink backed up: skip to the next keyframe
            if (!dropping) Log.w(TAG, "uplink congested: dropping until next keyframe")
            dropping = true
            streamer?.requestKeyframe()
            return
        }
        dropping = false
        val ts = (SystemClock.elapsedRealtime() - t0).toInt()
        val msg = ByteArray(6 + data.size)
        msg[0] = 1
        msg[1] = if (key) 1 else 0
        msg[2] = (ts ushr 24).toByte(); msg[3] = (ts ushr 16).toByte()
        msg[4] = (ts ushr 8).toByte(); msg[5] = ts.toByte()
        System.arraycopy(data, 0, msg, 6, data.size)
        relay.send(msg)
    }

    private fun sendInfo(s: ScreenStreamer?) {
        val (w, h) = realSize()
        val (level, charging) = battery()
        send(JSONObject().apply {
            put("t", "info")
            put("os", "android")
            put("screens", JSONArray().put(JSONObject().put("name", "Màn hình điện thoại").put("w", w).put("h", h).put("primary", true)))
            put("screen", 0)
            put("audio", false)
            put("notify", false)
            put("reboot", false)
            put("clipimg", false)
            put("battery", level)
            put("charging", charging)
            put("input", InputService.instance != null)
            if (s != null) {
                put("enc", "mediacodec ${s.width}x${s.height}")
                put("capture", "mediaprojection")
            }
        })
    }

    // ------------------------------------------------------------ relay messages

    private fun onRelayUp() {
        updateNotification()
    }

    private fun onRelayDown() {
        // The relay replays "join" when we come back with viewers still there.
        viewing = false
        stopStream()
        releaseScreen()
        updateNotification()
    }

    private fun onText(text: String) {
        val m = try { JSONObject(text) } catch (e: Exception) { return }
        when (val t = m.optString("t")) {
            "join" -> {
                readParams(m)
                viewing = true
                wakeScreen()
                sendInfo(null)
                startStream()
                updateNotification()
            }
            "leave" -> {
                viewing = false
                stopStream()
                releaseScreen()
                InputService.instance?.releaseAll()
                updateNotification()
            }
            "cfg" -> {
                readParams(m)
                if (viewing) restartStream()
            }
            "kf", "congested" -> streamer?.requestKeyframe()
            "wsvideo" -> {
                val on = m.optBoolean("on")
                if (on && !wsVideo) { dropping = true; streamer?.requestKeyframe() }
                wsVideo = on
            }
            // No WebRTC here: say so once per offer, so the viewer stays on the relay.
            "rtc" -> if (m.has("sdp")) send(JSONObject().put("t", "rtc").put("to", m.optInt("vid")).put("err", "agent Android chỉ dùng relay"))
            "mm", "md", "mu", "wh", "kd", "ku", "txt", "ra", "clip", "nav" -> {
                val input = InputService.instance
                if (input == null) {
                    if (t == "md" || t == "nav") sendNotice("Chưa bật Trợ năng cho RemoteDesk trên điện thoại nên chưa điều khiển được (chỉ xem).")
                } else input.onMessage(m)
            }
            "launch" -> launchApp(m.optString("pkg"))
            "open" -> openUrl(m.optString("url"))
            "restart" -> {
                sendNotice("Đang kết nối lại…")
                handler.postDelayed({ relay.reconnect() }, 500)
            }
            "update" -> Updater.start(this, cfg)
            "fls", "fput", "fget" -> sendNotice("Điện thoại chưa hỗ trợ chuyển file.")
        }
    }

    private fun readParams(m: JSONObject) {
        fun lim(v: Int, lo: Int, hi: Int, def: Int) = if (v == 0) def else v.coerceIn(lo, hi)
        fps = lim(m.optInt("fps"), 5, 60, 15)
        maxw = lim(m.optInt("maxw"), 640, 3840, 1280)
        kbps = lim(m.optInt("kbps"), 300, 20000, 2000)
    }

    fun send(o: JSONObject) {
        relay.send(o.toString())
    }

    fun sendNotice(msg: String) = send(JSONObject().put("t", "notice").put("msg", msg))
    private fun sendError(msg: String) = send(JSONObject().put("t", "error").put("msg", msg))

    private fun launchApp(pkg: String) {
        if (!Regex("^[A-Za-z0-9_.]{3,100}$").matches(pkg)) return
        val i = packageManager.getLaunchIntentForPackage(pkg)
        if (i == null) {
            sendNotice("Điện thoại chưa cài app $pkg")
            return
        }
        wakeScreen()
        startActivity(i.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
    }

    private fun openUrl(url: String) {
        if (!url.startsWith("https://") && !url.startsWith("http://")) return
        wakeScreen()
        try {
            startActivity(Intent(Intent.ACTION_VIEW, android.net.Uri.parse(url)).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
        } catch (e: Exception) {
            sendNotice("Không mở được $url")
        }
    }

    // ------------------------------------------------------------ screen on / off

    /** Screen on (dimmed) while someone watches; a swipe lock is dismissed. */
    @Suppress("DEPRECATION")
    fun wakeScreen() {
        val pm = getSystemService(POWER_SERVICE) as PowerManager
        if (screenLock == null) {
            screenLock = pm.newWakeLock(
                PowerManager.SCREEN_DIM_WAKE_LOCK or PowerManager.ACQUIRE_CAUSES_WAKEUP or PowerManager.ON_AFTER_RELEASE,
                "remotedesk:screen",
            )
        }
        if (!screenLock!!.isHeld) screenLock!!.acquire(4 * 60 * 60 * 1000L)
        val km = getSystemService(KEYGUARD_SERVICE) as KeyguardManager
        if (km.isKeyguardLocked && !km.isKeyguardSecure) {
            startActivity(Intent(this, ProjectionActivity::class.java)
                .putExtra(ProjectionActivity.EXTRA_UNLOCK_ONLY, true)
                .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
        }
    }

    private fun releaseScreen() {
        screenLock?.takeIf { it.isHeld }?.release()
    }

    // ------------------------------------------------------------ status

    fun battery(): Pair<Int, Boolean> {
        val b = registerReceiver(null, IntentFilter(Intent.ACTION_BATTERY_CHANGED))
        val level = b?.getIntExtra(BatteryManager.EXTRA_LEVEL, -1) ?: -1
        val scale = b?.getIntExtra(BatteryManager.EXTRA_SCALE, 100) ?: 100
        val status = b?.getIntExtra(BatteryManager.EXTRA_STATUS, -1) ?: -1
        val charging = status == BatteryManager.BATTERY_STATUS_CHARGING || status == BatteryManager.BATTERY_STATUS_FULL
        return Pair(if (level < 0) -1 else level * 100 / scale, charging)
    }

    /** One line per part, for the app screen and the notification. */
    fun status(): Status = Status(
        relay = relay.state,
        relayUp = relay.connected,
        capture = projection != null,
        input = InputService.instance != null,
        viewing = viewing,
        speaker = speaker?.state,
    )

    data class Status(
        val relay: String, val relayUp: Boolean, val capture: Boolean, val input: Boolean,
        val viewing: Boolean, val speaker: String?,
    )

    private fun createChannel() {
        val nm = getSystemService(NOTIFICATION_SERVICE) as NotificationManager
        nm.createNotificationChannel(NotificationChannel(CHANNEL, "RemoteDesk", NotificationManager.IMPORTANCE_LOW))
    }

    private fun notification(text: String): Notification {
        val open = PendingIntent.getActivity(this, 0, Intent(this, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE)
        return Notification.Builder(this, CHANNEL)
            .setSmallIcon(R.drawable.ic_launcher)
            .setContentTitle(if (viewing) "RemoteDesk · đang được điều khiển" else "RemoteDesk")
            .setContentText(text)
            .setContentIntent(open)
            .setOngoing(true)
            .build()
    }

    private var lastNotif = ""
    private fun updateNotification() {
        val s = status()
        val text = buildString {
            append(if (s.relayUp) "Online" else s.relay)
            if (!s.capture) append(" · chưa có quyền chụp màn hình")
            if (!s.input) append(" · chưa bật Trợ năng")
            s.speaker?.let { append(" · Loa: $it") }
        }
        if (text + viewing == lastNotif) return
        lastNotif = text + viewing
        (getSystemService(NOTIFICATION_SERVICE) as NotificationManager).notify(NOTIF_ID, notification(text))
    }

    companion object {
        private const val TAG = "Agent"
        private const val CHANNEL = "agent"
        private const val NOTIF_ID = 1
        const val ACTION_RELOAD = "reload"

        @Volatile var instance: AgentService? = null; private set

        fun start(ctx: Context, reload: Boolean = false) {
            val i = Intent(ctx, AgentService::class.java)
            if (reload) i.action = ACTION_RELOAD
            if (Build.VERSION.SDK_INT >= 26) ctx.startForegroundService(i) else ctx.startService(i)
        }
    }
}
