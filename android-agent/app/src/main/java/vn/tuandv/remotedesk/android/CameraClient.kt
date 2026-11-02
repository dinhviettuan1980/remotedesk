package vn.tuandv.remotedesk.android

import android.Manifest
import android.annotation.SuppressLint
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.content.pm.PackageManager
import android.graphics.ImageFormat
import android.graphics.Rect
import android.graphics.YuvImage
import android.hardware.camera2.CameraAccessException
import android.hardware.camera2.CameraCaptureSession
import android.hardware.camera2.CameraCharacteristics
import android.hardware.camera2.CameraDevice
import android.hardware.camera2.CameraManager
import android.hardware.camera2.CaptureRequest
import android.media.AudioFormat
import android.media.AudioManager
import android.media.AudioRecord
import android.media.Image
import android.media.ImageReader
import android.media.MediaRecorder
import android.net.ConnectivityManager
import android.os.BatteryManager
import android.os.Build
import android.os.Handler
import android.os.HandlerThread
import android.os.PowerManager
import android.os.SystemClock
import android.util.Log
import android.util.Range
import android.util.Size
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okio.ByteString
import okio.ByteString.Companion.toByteString
import org.json.JSONArray
import org.json.JSONObject
import java.io.ByteArrayOutputStream
import java.net.Inet4Address
import java.net.InetSocketAddress
import java.net.Socket
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import kotlin.math.abs
import kotlin.math.roundToInt

/**
 * BirdMonitor's camera and microphone, replacing IP Webcam + Tailscale + ADB:
 * the app connects out to wss://…/ws?role=camera and the server asks for what
 * it needs.
 *  - Microphone: 16-bit mono PCM while {"type":"audio","on":true} (the server's
 *    sound detection keeps it on all the time).
 *  - Camera: JPEG frames at the asked size/fps/quality only while
 *    {"type":"video","on":true} (someone watches), or one JPEG per {"type":"shot"}.
 *    The camera closes when neither is wanted.
 *  - Battery, temperature, screen and volume every 30 s.
 *  - Home LAN: the server switches the Tapo plugs (charger, light) through a TCP
 *    tunnel the app opens, only to port 80 on its own Wi‑Fi subnet.
 * Binary frames: [1][jpeg] live, [2][pcm] microphone, [3][u32 id][jpeg] shot,
 * [4][u32 id][bytes] tunnel.
 */
class CameraClient(private val ctx: Context, private val cfg: Config) {
    private val thread = HandlerThread("birdcam").also { it.start() }
    private val h = Handler(thread.looper)
    private val camThread = HandlerThread("birdcam-img").also { it.start() }
    private val camH = Handler(camThread.looper)
    private val http = OkHttpClient.Builder()
        .pingInterval(20, TimeUnit.SECONDS)
        .connectTimeout(15, TimeUnit.SECONDS)
        .readTimeout(60, TimeUnit.SECONDS)
        .build()

    private var ws: WebSocket? = null
    private var gen = 0
    private var stopped = false
    private var url = ""
    private var token = ""

    // What the server wants (set on [h]; the frame thread reads the volatile ones).
    @Volatile private var videoOn = false
    private var size = Size(1280, 720)
    @Volatile private var fps = 10
    @Volatile private var quality = 70
    private var facing = "front"
    private var zoom = 100
    private var audioOn = false
    /** Pending shot ids; shared with the frame thread, always under its lock. */
    private val shots = ArrayList<Long>()

    // Camera state (opened/closed on [h], frames on [camH]).
    private var device: CameraDevice? = null
    private var session: CameraCaptureSession? = null
    private var reader: ImageReader? = null
    private var request: CaptureRequest.Builder? = null
    private var openFacing = ""
    private var opening = false
    private var cameraErr: String? = null
    private var framesSinceOpen = 0
    @Volatile private var lastFrameAt = 0L
    private var idleClose: Runnable? = null

    private var audio: Thread? = null
    @Volatile private var audioRun = false

    @Volatile var state = "Đang kết nối…"; private set

    private val tcpPool = Executors.newCachedThreadPool()
    private val tunnels = ConcurrentHashMap<Long, Socket>()

    fun start() {
        h.post { connect() }
        h.postDelayed(::statusTick, 30_000)
    }

    fun stop() = h.post {
        stopped = true
        gen++
        ws?.close(1000, null)
        closeCamera()
        stopAudio()
        closeTunnels()
        tcpPool.shutdownNow()
        thread.quitSafely()
        camThread.quitSafely()
    }

    fun restartIfChanged() = h.post {
        if (cameraUrl() != url || cfg.speakerToken != token) {
            gen++
            ws?.close(1000, null)
            connect()
            return@post
        }
        // Permissions may have just been granted: retry what is wanted.
        if (audioOn && audio == null) startAudio()
        if (wanted() && device == null && !opening) openCamera()
    }

    /** Same BirdMonitor server and token as the speaker, role=camera. */
    private fun cameraUrl() = cfg.speakerUrl.replace("role=speaker", "role=camera")
        .let { if ("role=camera" in it) it else it + (if ('?' in it) "&" else "?") + "role=camera" }

    // ------------------------------------------------------------ connection

    private fun connect() {
        if (stopped) return
        url = cameraUrl()
        token = cfg.speakerToken
        val my = ++gen
        val req = Request.Builder().url(url).apply {
            if (token.isNotEmpty()) header("Authorization", "Bearer $token")
        }.build()
        state = "Đang kết nối…"
        ws = http.newWebSocket(req, object : WebSocketListener() {
            override fun onOpen(webSocket: WebSocket, response: Response) {
                h.post {
                    if (my != gen) return@post
                    state = "Đã kết nối"
                    Log.i(TAG, "connected to $url")
                    send(JSONObject().put("type", "hello")
                        .put("app", "RemoteDesk").put("ver", BuildConfig.VERSION_NAME)
                        .put("model", "${Build.MANUFACTURER} ${Build.MODEL}")
                        .put("android", Build.VERSION.RELEASE)
                        .put("cameras", JSONArray(cameras().keys.toList())))
                    sendStatus()
                    cameraErr?.let { send(JSONObject().put("type", "camera").put("open", false).put("error", it)) }
                }
            }

            override fun onMessage(webSocket: WebSocket, text: String) {
                h.post { if (my == gen) onServer(text) }
            }

            override fun onMessage(webSocket: WebSocket, bytes: ByteString) {
                if (my != gen || bytes.size < 6 || bytes[0] != FRAME_TCP) return
                val id = ((bytes[1].toLong() and 0xff) shl 24) or ((bytes[2].toLong() and 0xff) shl 16) or
                    ((bytes[3].toLong() and 0xff) shl 8) or (bytes[4].toLong() and 0xff)
                val sock = tunnels[id] ?: return
                try {
                    sock.getOutputStream().write(bytes.substring(5).toByteArray())
                } catch (e: Exception) {
                    closeTunnel(id, notify = true)
                }
            }

            override fun onClosing(webSocket: WebSocket, code: Int, reason: String) {
                webSocket.close(1000, null)
                down(my, "Mất kết nối ($code)")
            }

            override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
                down(my, if (response != null) "Lỗi HTTP ${response.code}" else "Không kết nối được")
            }
        })
    }

    /** Server gone: stop the camera and mic (nobody to send to), reconnect in 3 s. */
    private fun down(my: Int, why: String) = h.post {
        if (my != gen) return@post
        gen++
        ws = null
        state = why
        videoOn = false
        audioOn = false
        synchronized(shots) { shots.clear() }
        stopAudio()
        closeTunnels()
        updateCamera()
        h.postDelayed({ if (!stopped) connect() }, 3_000)
    }

    private fun send(o: JSONObject) {
        ws?.send(o.toString())
    }

    private fun onServer(text: String) {
        val m = try { JSONObject(text) } catch (e: Exception) { return }
        when (m.optString("type")) {
            "video" -> {
                videoOn = m.optBoolean("on")
                if (videoOn) {
                    parseSize(m.optString("size"))?.let { if (it != size) { size = it; closeCamera() } }
                    fps = m.optInt("fps", fps).coerceIn(1, 30)
                    quality = m.optInt("quality", quality).coerceIn(20, 95)
                }
                updateCamera()
            }
            "audio" -> if (m.optBoolean("on")) startAudio() else stopAudio()
            "shot" -> {
                synchronized(shots) { shots.add(m.optLong("id")) }
                updateCamera()
            }
            "camera" -> {
                val f = m.optString("facing", facing)
                zoom = m.optInt("zoom", zoom).coerceIn(100, 800)
                if (f != facing) {
                    facing = f
                    closeCamera()
                    updateCamera()
                } else applyRequest()
            }
            "focus" -> focus()
            "restart" -> {
                Log.i(TAG, "restart asked by the server")
                closeCamera()
                if (audioOn) { stopAudio(); startAudio() }
                updateCamera()
            }
            "screen" -> screen(m.optBoolean("on"))
            "volume" -> volume(m.optInt("percent", -1))
            "tcp_open" -> tcpOpen(m.optLong("id"), m.optString("host"), m.optInt("port"))
            "tcp_close" -> closeTunnel(m.optLong("id"), notify = false)
            "lan_scan" -> lanScan(m.optLong("id"), m.optInt("port", 80))
        }
    }

    // ------------------------------------------------------------ home LAN (Tapo plugs)

    /** This phone's Wi‑Fi IPv4 subnet as (network, prefix), at most a /22. */
    private fun lanSubnet(): Pair<Int, Int>? {
        val cm = ctx.getSystemService(Context.CONNECTIVITY_SERVICE) as ConnectivityManager
        val lp = cm.getLinkProperties(cm.activeNetwork ?: return null) ?: return null
        val la = lp.linkAddresses.firstOrNull { it.address is Inet4Address && !it.address.isLoopbackAddress } ?: return null
        val ip = la.address.address.fold(0) { acc, b -> (acc shl 8) or (b.toInt() and 0xff) }
        val prefix = la.prefixLength.coerceIn(22, 30)
        val mask = -1 shl (32 - prefix)
        return Pair(ip and mask, prefix)
    }

    private fun ipToInt(host: String): Int? {
        val p = host.split('.')
        if (p.size != 4) return null
        return p.fold(0) { acc, s -> (acc shl 8) or (s.toIntOrNull()?.takeIf { it in 0..255 } ?: return null) }
    }

    private fun intToIp(v: Int) = "${(v ushr 24) and 255}.${(v ushr 16) and 255}.${(v ushr 8) and 255}.${v and 255}"

    /** Only port 80 on the phone's own subnet: the server cannot use the phone to reach anything else. */
    private fun allowed(host: String, port: Int): Boolean {
        if (port != 80) return false
        val (net, prefix) = lanSubnet() ?: return false
        val ip = ipToInt(host) ?: return false
        val mask = -1 shl (32 - prefix)
        return ip and mask == net && ip != net && ip != (net or mask.inv())
    }

    private fun tcpOpen(id: Long, host: String, port: Int) = tcpPool.execute {
        if (!allowed(host, port)) {
            send(JSONObject().put("type", "tcp_error").put("id", id).put("message", "$host:$port không thuộc mạng Wi‑Fi nhà"))
            return@execute
        }
        val sock = Socket()
        try {
            sock.connect(InetSocketAddress(host, port), 4_000)
            sock.tcpNoDelay = true
            tunnels[id] = sock
            send(JSONObject().put("type", "tcp_opened").put("id", id))
            val buf = ByteArray(16 * 1024 + 5)
            buf[0] = FRAME_TCP
            buf[1] = (id ushr 24).toByte(); buf[2] = (id ushr 16).toByte(); buf[3] = (id ushr 8).toByte(); buf[4] = id.toByte()
            val input = sock.getInputStream()
            while (true) {
                val n = input.read(buf, 5, buf.size - 5)
                if (n < 0) break
                ws?.send(buf.toByteString(0, 5 + n))
            }
            closeTunnel(id, notify = true)
        } catch (e: Exception) {
            if (tunnels.containsKey(id)) closeTunnel(id, notify = true)
            else {
                try { sock.close() } catch (_: Exception) {}
                send(JSONObject().put("type", "tcp_error").put("id", id).put("message", "không nối được $host:$port: ${e.message}"))
            }
        }
    }

    private fun closeTunnel(id: Long, notify: Boolean) {
        val s = tunnels.remove(id) ?: return
        try { s.close() } catch (_: Exception) {}
        if (notify) send(JSONObject().put("type", "tcp_close").put("id", id))
    }

    private fun closeTunnels() {
        for (id in tunnels.keys.toList()) closeTunnel(id, notify = false)
    }

    /** Hosts on the Wi‑Fi subnet with the port open (64 at a time, 800 ms each). */
    private fun lanScan(id: Long, port: Int) = tcpPool.execute {
        val sub = lanSubnet()
        if (sub == null || port != 80) {
            send(JSONObject().put("type", "lan_scan").put("id", id).put("error", "không có mạng Wi‑Fi"))
            return@execute
        }
        val (net, prefix) = sub
        val count = (1 shl (32 - prefix)) - 2
        val pool = Executors.newFixedThreadPool(64)
        val open = java.util.Collections.synchronizedList(ArrayList<String>())
        try {
            val jobs = (1..count).map { i ->
                val host = intToIp(net + i)
                pool.submit {
                    try {
                        Socket().use { it.connect(InetSocketAddress(host, port), 800) }
                        open.add(host)
                    } catch (_: Exception) {}
                }
            }
            jobs.forEach { try { it.get() } catch (_: Exception) {} }
        } finally {
            pool.shutdownNow()
        }
        Log.i(TAG, "LAN scan ${intToIp(net)}/$prefix: ${open.size} hosts with port $port")
        send(JSONObject().put("type", "lan_scan").put("id", id).put("hosts", JSONArray(open.sorted())))
    }

    // ------------------------------------------------------------ camera

    private fun wanted() = videoOn || synchronized(shots) { shots.isNotEmpty() }

    /** Open when wanted, close 10 s after the last use (night snapshots every 2 s keep it open). */
    private fun updateCamera() {
        idleClose?.let { h.removeCallbacks(it) }
        if (wanted()) {
            if (device == null && !opening) openCamera()
        } else if (device != null || opening) {
            idleClose = Runnable { if (!wanted()) closeCamera() }.also { h.postDelayed(it, 10_000) }
        }
    }

    private fun manager() = ctx.getSystemService(Context.CAMERA_SERVICE) as CameraManager

    /** "front"/"back" → camera id. */
    private fun cameras(): Map<String, String> {
        val out = LinkedHashMap<String, String>()
        try {
            val m = manager()
            for (id in m.cameraIdList) {
                when (m.getCameraCharacteristics(id).get(CameraCharacteristics.LENS_FACING)) {
                    CameraCharacteristics.LENS_FACING_FRONT -> out.putIfAbsent("front", id)
                    CameraCharacteristics.LENS_FACING_BACK -> out.putIfAbsent("back", id)
                }
            }
        } catch (e: Exception) {
            Log.w(TAG, "camera list", e)
        }
        return out
    }

    @SuppressLint("MissingPermission")
    private fun openCamera() {
        if (ctx.checkSelfPermission(Manifest.permission.CAMERA) != PackageManager.PERMISSION_GRANTED) {
            cameraFailed("App chưa được cấp quyền Camera (mở app RemoteDesk → bước 5)")
            return
        }
        val all = cameras()
        val id = all[facing] ?: all.values.firstOrNull()
        if (id == null) {
            cameraFailed("Điện thoại không có camera")
            return
        }
        opening = true
        openFacing = facing
        framesSinceOpen = 0
        try {
            manager().openCamera(id, object : CameraDevice.StateCallback() {
                override fun onOpened(d: CameraDevice) {
                    h.post {
                        opening = false
                        if (!wanted() || stopped) { d.close(); return@post }
                        device = d
                        startSession(d, id)
                    }
                }

                override fun onDisconnected(d: CameraDevice) {
                    h.post {
                        d.close()
                        if (device === d) device = null
                        opening = false
                        cameraFailed("Camera bị app khác lấy mất (IP Webcam?)")
                    }
                }

                override fun onError(d: CameraDevice, error: Int) {
                    h.post {
                        d.close()
                        if (device === d) device = null
                        opening = false
                        cameraFailed(when (error) {
                            ERROR_CAMERA_IN_USE, ERROR_MAX_CAMERAS_IN_USE -> "Camera đang bị app khác dùng (IP Webcam?)"
                            ERROR_CAMERA_DISABLED -> "Camera bị tắt bởi chính sách thiết bị"
                            else -> "Lỗi camera ($error)"
                        })
                    }
                }
            }, h)
        } catch (e: CameraAccessException) {
            opening = false
            cameraFailed("Không mở được camera: ${e.message}")
        } catch (e: SecurityException) {
            opening = false
            cameraFailed("App chưa được cấp quyền Camera")
        }
    }

    private fun startSession(d: CameraDevice, id: String) {
        try {
            val ch = manager().getCameraCharacteristics(id)
            val map = ch.get(CameraCharacteristics.SCALER_STREAM_CONFIGURATION_MAP)!!
            val out = pickSize(map.getOutputSizes(ImageFormat.YUV_420_888), size)
            val r = ImageReader.newInstance(out.width, out.height, ImageFormat.YUV_420_888, 3)
            r.setOnImageAvailableListener({ onImage(it) }, camH)
            reader = r
            val b = d.createCaptureRequest(CameraDevice.TEMPLATE_RECORD)
            b.addTarget(r.surface)
            b.set(CaptureRequest.CONTROL_MODE, CaptureRequest.CONTROL_MODE_AUTO)
            val afModes = ch.get(CameraCharacteristics.CONTROL_AF_AVAILABLE_MODES) ?: IntArray(0)
            if (CaptureRequest.CONTROL_AF_MODE_CONTINUOUS_VIDEO in afModes)
                b.set(CaptureRequest.CONTROL_AF_MODE, CaptureRequest.CONTROL_AF_MODE_CONTINUOUS_VIDEO)
            pickFpsRange(ch.get(CameraCharacteristics.CONTROL_AE_AVAILABLE_TARGET_FPS_RANGES), fps)
                ?.let { b.set(CaptureRequest.CONTROL_AE_TARGET_FPS_RANGE, it) }
            request = b
            @Suppress("DEPRECATION")
            d.createCaptureSession(listOf(r.surface), object : CameraCaptureSession.StateCallback() {
                override fun onConfigured(s: CameraCaptureSession) {
                    h.post {
                        if (device !== d) { s.close(); return@post }
                        session = s
                        applyRequest()
                        if (cameraErr != null) {
                            cameraErr = null
                            send(JSONObject().put("type", "camera").put("open", true))
                        }
                        Log.i(TAG, "camera $openFacing open at ${out.width}x${out.height}")
                    }
                }

                override fun onConfigureFailed(s: CameraCaptureSession) {
                    h.post {
                        closeCamera()
                        cameraFailed("Không cấu hình được camera ở ${out.width}x${out.height}")
                    }
                }
            }, h)
        } catch (e: Exception) {
            Log.w(TAG, "session", e)
            closeCamera()
            cameraFailed("Lỗi camera: ${e.message}")
        }
    }

    /** Zoom as a centred crop of the sensor, limited by the camera's digital zoom. */
    private fun applyRequest() {
        val s = session ?: return
        val b = request ?: return
        val d = device ?: return
        try {
            val ch = manager().getCameraCharacteristics(d.id)
            val active = ch.get(CameraCharacteristics.SENSOR_INFO_ACTIVE_ARRAY_SIZE)
            val maxZoom = ch.get(CameraCharacteristics.SCALER_AVAILABLE_MAX_DIGITAL_ZOOM) ?: 1f
            if (active != null) {
                val z = (zoom / 100f).coerceIn(1f, maxZoom.coerceAtLeast(1f))
                val w = (active.width() / z).roundToInt()
                val hh = (active.height() / z).roundToInt()
                val l = active.left + (active.width() - w) / 2
                val t = active.top + (active.height() - hh) / 2
                b.set(CaptureRequest.SCALER_CROP_REGION, Rect(l, t, l + w, t + hh))
            }
            s.setRepeatingRequest(b.build(), null, h)
        } catch (e: Exception) {
            Log.w(TAG, "repeating request", e)
        }
    }

    private fun focus() {
        val s = session ?: return
        val b = request ?: return
        try {
            b.set(CaptureRequest.CONTROL_AF_TRIGGER, CaptureRequest.CONTROL_AF_TRIGGER_START)
            s.capture(b.build(), null, h)
            b.set(CaptureRequest.CONTROL_AF_TRIGGER, CaptureRequest.CONTROL_AF_TRIGGER_IDLE)
        } catch (e: Exception) {
            Log.w(TAG, "focus", e)
        }
    }

    private fun closeCamera() {
        try { session?.close() } catch (_: Exception) {}
        try { device?.close() } catch (_: Exception) {}
        val r = reader
        // Close the reader on its own thread: a frame may be in flight there.
        camH.post { try { r?.close() } catch (_: Exception) {} }
        session = null
        device = null
        reader = null
        request = null
    }

    private fun cameraFailed(msg: String) {
        Log.w(TAG, msg)
        cameraErr = msg
        send(JSONObject().put("type", "camera").put("open", false).put("error", msg))
        // Shots cannot wait for a camera that may never open.
        val failed = synchronized(shots) { ArrayList(shots).also { shots.clear() } }
        for (id in failed) send(JSONObject().put("type", "shot_error").put("id", id).put("message", msg))
        // Try again while it is still wanted (IP Webcam may let go of it).
        h.postDelayed({ if (wanted() && device == null && !opening) openCamera() }, 10_000)
    }

    /** On [camH]: throttle to the asked fps, JPEG-encode, send. */
    private fun onImage(r: ImageReader) {
        val img = try { r.acquireLatestImage() } catch (e: Exception) { null } ?: return
        try {
            framesSinceOpen++
            val now = SystemClock.elapsedRealtime()
            val pendingShots = synchronized(shots) { if (shots.isEmpty()) null else ArrayList(shots) }
            // Let exposure settle before answering a shot from a cold camera.
            val shotReady = pendingShots != null && framesSinceOpen > 6
            val videoDue = videoOn && now - lastFrameAt >= 1000L / fps - 8
            if (!shotReady && !videoDue) return
            val socket = ws ?: return
            // Viewer on a slow link: skip frames rather than queue seconds of video.
            if (videoDue && !shotReady && socket.queueSize() > 512 * 1024) return
            val jpeg = toJpeg(img, quality)
            if (videoDue) {
                lastFrameAt = now
                socket.send(ByteString.of(FRAME_VIDEO, *jpeg))
            }
            if (shotReady) {
                for (id in pendingShots!!) {
                    val head = byteArrayOf(FRAME_SHOT, (id ushr 24).toByte(), (id ushr 16).toByte(), (id ushr 8).toByte(), id.toByte())
                    socket.send((head + jpeg).toByteString())
                }
                synchronized(shots) { shots.removeAll(pendingShots.toSet()) }
                h.post { updateCamera() }
            }
        } catch (e: Exception) {
            Log.w(TAG, "frame", e)
        } finally {
            img.close()
        }
    }

    // ------------------------------------------------------------ microphone

    @SuppressLint("MissingPermission")
    private fun startAudio() {
        audioOn = true
        if (audio != null) return
        if (ctx.checkSelfPermission(Manifest.permission.RECORD_AUDIO) != PackageManager.PERMISSION_GRANTED) {
            send(JSONObject().put("type", "camera").put("open", false)
                .put("error", "App chưa được cấp quyền Micro (mở app RemoteDesk → bước 5)"))
            return
        }
        audioRun = true
        audio = Thread({ audioLoop() }, "birdcam-mic").also { it.start() }
    }

    private fun stopAudio() {
        audioOn = false
        audioRun = false
        audio?.interrupt()
        audio = null
    }

    @SuppressLint("MissingPermission")
    private fun audioLoop() {
        var rec: AudioRecord? = null
        try {
            val rate = intArrayOf(22050, 16000, 44100).first {
                AudioRecord.getMinBufferSize(it, AudioFormat.CHANNEL_IN_MONO, AudioFormat.ENCODING_PCM_16BIT) > 0
            }
            val min = AudioRecord.getMinBufferSize(rate, AudioFormat.CHANNEL_IN_MONO, AudioFormat.ENCODING_PCM_16BIT)
            val chunk = rate / 10 * 2 // 100 ms
            rec = AudioRecord(MediaRecorder.AudioSource.MIC, rate, AudioFormat.CHANNEL_IN_MONO,
                AudioFormat.ENCODING_PCM_16BIT, maxOf(min, chunk * 4))
            if (rec.state != AudioRecord.STATE_INITIALIZED) throw IllegalStateException("micro đang bận")
            rec.startRecording()
            ws?.send(JSONObject().put("type", "audio_format").put("sampleRate", rate)
                .put("channels", 1).put("bits", 16).toString())
            Log.i(TAG, "microphone on at $rate Hz")
            val buf = ByteArray(chunk + 1)
            buf[0] = FRAME_AUDIO
            while (audioRun) {
                var n = 0
                while (n < chunk && audioRun) {
                    val got = rec.read(buf, 1 + n, chunk - n)
                    if (got < 0) throw IllegalStateException("đọc micro lỗi ($got)")
                    n += got
                }
                val socket = ws ?: continue
                // Never let audio pile up behind a stalled connection.
                if (socket.queueSize() < 1024 * 1024) socket.send(buf.toByteString(0, 1 + n))
            }
        } catch (e: Exception) {
            if (audioRun) {
                Log.w(TAG, "microphone", e)
                h.post {
                    send(JSONObject().put("type", "camera").put("open", false).put("error", "Micro lỗi: ${e.message}"))
                    audio = null
                    // Retry while the server still wants sound.
                    h.postDelayed({ if (audioOn && audio == null) startAudio() }, 10_000)
                }
            }
        } finally {
            try { rec?.stop() } catch (_: Exception) {}
            rec?.release()
            Log.i(TAG, "microphone off")
        }
    }

    // ------------------------------------------------------------ phone status / commands

    private fun statusTick() {
        if (stopped) return
        sendStatus()
        h.postDelayed(::statusTick, 30_000)
    }

    private fun sendStatus() {
        val b = ctx.registerReceiver(null, IntentFilter(Intent.ACTION_BATTERY_CHANGED))
        val (level, charging) = AgentService.instance?.battery() ?: Pair(-1, false)
        val temp = b?.getIntExtra(BatteryManager.EXTRA_TEMPERATURE, Int.MIN_VALUE) ?: Int.MIN_VALUE
        val pm = ctx.getSystemService(Context.POWER_SERVICE) as PowerManager
        val am = ctx.getSystemService(Context.AUDIO_SERVICE) as AudioManager
        val max = am.getStreamMaxVolume(AudioManager.STREAM_MUSIC).coerceAtLeast(1)
        send(JSONObject().put("type", "status").apply {
            if (level >= 0) put("level", level).put("charging", charging)
            if (temp != Int.MIN_VALUE) put("tempC", temp / 10.0)
        }.put("screenOn", pm.isInteractive)
            .put("volume", am.getStreamVolume(AudioManager.STREAM_MUSIC) * 100 / max))
    }

    private fun screen(on: Boolean) {
        val agent = AgentService.instance
        if (on) agent?.handler?.post { agent.wakeScreen() }
        else {
            agent?.handler?.post { agent.releaseScreen() }
            InputService.instance?.lockScreen()
        }
        h.postDelayed(::sendStatus, 1_500)
    }

    private fun volume(percent: Int) {
        if (percent !in 0..100) return
        val am = ctx.getSystemService(Context.AUDIO_SERVICE) as AudioManager
        val max = am.getStreamMaxVolume(AudioManager.STREAM_MUSIC)
        try {
            am.setStreamVolume(AudioManager.STREAM_MUSIC, (max * percent / 100f).roundToInt(), 0)
        } catch (e: SecurityException) {
            Log.w(TAG, "volume (do not disturb?)", e) // never mute or change modes otherwise
        }
        h.postDelayed(::sendStatus, 500)
    }

    companion object {
        private const val TAG = "BirdCam"
        private const val FRAME_VIDEO: Byte = 1
        private const val FRAME_AUDIO: Byte = 2
        private const val FRAME_SHOT: Byte = 3
        private const val FRAME_TCP: Byte = 4

        fun parseSize(s: String): Size? {
            val m = Regex("""^(\d{2,4})x(\d{2,4})$""").find(s) ?: return null
            return Size(m.groupValues[1].toInt(), m.groupValues[2].toInt())
        }

        /** Same aspect ratio and no larger than asked if possible, else the closest area. */
        fun pickSize(sizes: Array<Size>, want: Size): Size {
            val ratio = want.width.toFloat() / want.height
            val sameRatio = sizes.filter { abs(it.width.toFloat() / it.height - ratio) < 0.05f }
            val pool = sameRatio.ifEmpty { sizes.toList() }
            val area = want.width * want.height
            return pool.filter { it.width * it.height <= area }.maxByOrNull { it.width * it.height }
                ?: pool.minByOrNull { abs(it.width * it.height - area) }
                ?: want
        }

        /** A range that reaches the asked fps with the lowest ceiling (less heat, more light). */
        fun pickFpsRange(ranges: Array<Range<Int>>?, fps: Int): Range<Int>? {
            if (ranges.isNullOrEmpty()) return null
            return ranges.filter { it.upper >= fps }.minWithOrNull(compareBy({ it.upper }, { -it.lower }))
                ?: ranges.maxByOrNull { it.upper }
        }

        /** YUV_420_888 → NV21 (any strides) → JPEG. */
        fun toJpeg(img: Image, quality: Int): ByteArray {
            val w = img.width
            val h = img.height
            val nv21 = ByteArray(w * h * 3 / 2)
            val y = img.planes[0]
            var pos = 0
            val yBuf = y.buffer
            for (row in 0 until h) {
                yBuf.position(row * y.rowStride)
                yBuf.get(nv21, pos, w)
                pos += w
            }
            val u = img.planes[1]
            val v = img.planes[2]
            val uBuf = u.buffer
            val vBuf = v.buffer
            val ps = u.pixelStride
            for (row in 0 until h / 2) {
                for (col in 0 until w / 2) {
                    val i = row * u.rowStride + col * ps
                    nv21[pos++] = vBuf.get(row * v.rowStride + col * v.pixelStride)
                    nv21[pos++] = uBuf.get(i)
                }
            }
            val out = ByteArrayOutputStream(w * h / 4)
            YuvImage(nv21, ImageFormat.NV21, w, h, null).compressToJpeg(Rect(0, 0, w, h), quality, out)
            return out.toByteArray()
        }
    }
}
