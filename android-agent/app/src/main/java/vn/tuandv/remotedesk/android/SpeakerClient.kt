package vn.tuandv.remotedesk.android

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.media.AudioAttributes
import android.media.MediaPlayer
import android.net.Uri
import android.os.Build
import android.os.Handler
import android.os.HandlerThread
import android.util.Log
import okhttp3.Call
import okhttp3.Callback
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import org.json.JSONObject
import java.io.File
import java.io.IOException
import java.util.concurrent.TimeUnit

/**
 * BirdMonitor's speaker, replacing the /speaker page in Chrome: the server
 * sends {"type":"play","id","url","volume"} / {"type":"stop"} over
 * wss://…/ws?role=speaker; we download the whole file (streaming a 17-minute
 * track drops after ~10 minutes), play it on the loudspeaker and answer
 * started / ended / error, plus the battery every 60 s (the server switches
 * the charger plug with it). Media playback only: the microphone, call mode
 * and mute stay untouched (IP Webcam listens on the mic).
 */
class SpeakerClient(private val ctx: Context, private val cfg: Config) {
    private val thread = HandlerThread("speaker").also { it.start() }
    private val h = Handler(thread.looper)
    private val http = OkHttpClient.Builder()
        .pingInterval(25, TimeUnit.SECONDS)
        .connectTimeout(15, TimeUnit.SECONDS)
        .readTimeout(60, TimeUnit.SECONDS)
        .build()

    private var ws: WebSocket? = null
    private var gen = 0
    private var stopped = false
    private var url = ""
    private var token = ""

    private var current: String? = null // id that should be playing
    private var download: Call? = null
    private var player: MediaPlayer? = null
    private var file: File? = null

    @Volatile var state = "Đang kết nối…"; private set

    private val powerReceiver = object : BroadcastReceiver() {
        override fun onReceive(c: Context?, i: Intent?) {
            h.postDelayed(::reportBattery, 1_000) // let the battery status settle
        }
    }

    fun start() {
        ctx.registerReceiver(powerReceiver, IntentFilter().apply {
            addAction(Intent.ACTION_POWER_CONNECTED)
            addAction(Intent.ACTION_POWER_DISCONNECTED)
        })
        h.post { connect() }
        h.postDelayed(::batteryTick, 60_000)
    }

    fun stop() {
        try { ctx.unregisterReceiver(powerReceiver) } catch (_: Exception) {}
        h.post {
            stopped = true
            gen++
            ws?.close(1000, null)
            stopPlayback()
            thread.quitSafely()
        }
    }

    fun restartIfChanged() = h.post {
        if (cfg.speakerUrl != url || cfg.speakerToken != token) {
            gen++
            ws?.close(1000, null)
            connect()
        }
    }

    private fun connect() {
        if (stopped) return
        url = cfg.speakerUrl
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
                    send(JSONObject().put("type", "hello").put("ua", "RemoteDesk Android ${BuildConfig.VERSION_NAME} (${Build.MANUFACTURER} ${Build.MODEL}; Android ${Build.VERSION.RELEASE})"))
                    reportBattery()
                }
            }

            override fun onMessage(webSocket: WebSocket, text: String) {
                h.post { if (my == gen) onServer(text) }
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

    /** Reconnect after 3 s, forever. */
    private fun down(my: Int, why: String) = h.post {
        if (my != gen) return@post
        gen++
        ws = null
        state = why
        h.postDelayed({ if (!stopped) connect() }, 3_000)
    }

    private fun send(o: JSONObject) {
        ws?.send(o.toString())
    }

    private fun onServer(text: String) {
        val m = try { JSONObject(text) } catch (e: Exception) { return }
        when (m.optString("type")) {
            "play" -> play(m.optString("id"), m.optString("url"), m.optDouble("volume", 1.0).toFloat().coerceIn(0f, 1f))
            "stop" -> {
                current = null
                stopPlayback()
                state = "Đã kết nối"
            }
        }
    }

    private fun play(id: String, path: String, volume: Float) {
        if (id.isEmpty() || path.isEmpty()) return
        stopPlayback() // a new item replaces the one playing
        current = id
        // The url is relative to the server the WebSocket is on.
        val base = Uri.parse(url)
        val fileUrl = Uri.Builder()
            .scheme(if (base.scheme == "ws") "http" else "https")
            .encodedAuthority(base.encodedAuthority)
            .encodedPath(path.substringBefore('?'))
            .encodedQuery(path.substringAfter('?', "").ifEmpty { null })
            .build().toString()
        val out = File(ctx.cacheDir, "speaker-$id")
        file = out
        state = "Đang tải…"
        val req = Request.Builder().url(fileUrl).apply {
            if (token.isNotEmpty()) header("Authorization", "Bearer $token")
        }.build()
        val call = http.newCall(req)
        download = call
        call.enqueue(object : Callback {
            override fun onFailure(call: Call, e: IOException) {
                h.post { if (current == id) fail(id, "không tải được âm thanh: ${e.message}") }
            }

            override fun onResponse(call: Call, response: Response) {
                val ok = try {
                    response.use { r ->
                        if (!r.isSuccessful) throw IOException("HTTP ${r.code}")
                        out.outputStream().use { r.body!!.byteStream().copyTo(it) }
                    }
                    true
                } catch (e: IOException) {
                    h.post { if (current == id) fail(id, "không tải được âm thanh: ${e.message}") }
                    false
                }
                if (ok) h.post { if (current == id) startPlayer(id, out, volume) else out.delete() }
            }
        })
    }

    private fun startPlayer(id: String, f: File, volume: Float) {
        val mp = MediaPlayer()
        player = mp
        try {
            mp.setAudioAttributes(AudioAttributes.Builder()
                .setUsage(AudioAttributes.USAGE_MEDIA)
                .setContentType(AudioAttributes.CONTENT_TYPE_MUSIC)
                .build())
            mp.setDataSource(f.path)
            mp.setVolume(volume, volume)
            mp.setOnCompletionListener {
                if (current == id) {
                    send(JSONObject().put("type", "ended").put("id", id))
                    current = null
                    state = "Đã kết nối"
                }
                stopPlayback()
            }
            mp.setOnErrorListener { _, what, extra ->
                if (current == id) fail(id, "không phát được âm thanh ($what/$extra)")
                true
            }
            mp.prepare()
            mp.start()
            send(JSONObject().put("type", "started").put("id", id))
            state = "Đang phát"
            Log.i(TAG, "playing $id (${f.length() / 1024} KB, volume $volume)")
        } catch (e: Exception) {
            fail(id, "không phát được âm thanh: ${e.message}")
        }
    }

    private fun fail(id: String, msg: String) {
        Log.w(TAG, msg)
        send(JSONObject().put("type", "error").put("id", id).put("message", msg))
        current = null
        state = "Đã kết nối"
        stopPlayback()
    }

    private fun stopPlayback() {
        download?.cancel()
        download = null
        player?.let {
            try { it.stop() } catch (_: Exception) {}
            it.release()
        }
        player = null
        file?.delete()
        file = null
    }

    private fun reportBattery() {
        val (level, charging) = AgentService.instance?.battery() ?: return
        if (level >= 0) send(JSONObject().put("type", "battery").put("level", level).put("charging", charging))
    }

    private fun batteryTick() {
        if (stopped) return
        reportBattery()
        h.postDelayed(::batteryTick, 60_000)
    }

    companion object {
        private const val TAG = "Speaker"
    }
}
