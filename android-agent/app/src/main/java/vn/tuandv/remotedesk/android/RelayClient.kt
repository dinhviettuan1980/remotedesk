package vn.tuandv.remotedesk.android

import android.net.Uri
import android.os.Handler
import android.util.Log
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okio.ByteString
import okio.ByteString.Companion.toByteString
import java.util.concurrent.TimeUnit

/**
 * The outbound connection to the relay (/ws/agent), reconnecting forever with
 * backoff. Callbacks run on [handler]'s thread.
 */
class RelayClient(
    private val cfg: Config,
    private val handler: Handler,
    private val onOpen: () -> Unit,
    private val onText: (String) -> Unit,
    private val onDown: () -> Unit,
) {
    private val http = OkHttpClient.Builder()
        .pingInterval(20, TimeUnit.SECONDS) // a dead link shows up within ~40 s
        .connectTimeout(15, TimeUnit.SECONDS)
        .readTimeout(0, TimeUnit.MILLISECONDS)
        .build()
    private var ws: WebSocket? = null
    private var backoff = 2_000L
    private var stopped = true
    private var gen = 0 // connection attempt; stale callbacks are ignored

    @Volatile var state = "Chưa kết nối"; private set
    @Volatile var connected = false; private set

    fun start() {
        if (!stopped) return
        stopped = false
        connect()
    }

    fun stop() {
        stopped = true
        gen++
        ws?.close(1000, "stop")
        ws = null
        connected = false
        state = "Đã dừng"
    }

    /** Drop the current link and dial again now (settings changed). */
    fun reconnect() {
        stop()
        start()
    }

    private fun connect() {
        if (stopped) return
        if (!cfg.ready) {
            state = "Chưa nhập mã truy cập"
            return
        }
        val my = ++gen
        // OkHttp dials https:// and upgrades to a WebSocket itself.
        val base = Uri.parse(cfg.server)
        val url = base.buildUpon()
            .path((base.path ?: "").trimEnd('/') + "/ws/agent")
            .clearQuery()
            .appendQueryParameter("id", cfg.id)
            .appendQueryParameter("name", cfg.name)
            .appendQueryParameter("os", "android")
            .appendQueryParameter("ver", BuildConfig.VERSION_NAME)
            .build().toString()
        state = "Đang kết nối…"
        val req = Request.Builder().url(url).header("Authorization", "Bearer ${cfg.token}").build()
        ws = http.newWebSocket(req, object : WebSocketListener() {
            override fun onOpen(webSocket: WebSocket, response: Response) = handler.post {
                if (my != gen) return@post
                connected = true
                backoff = 2_000L
                state = "Đã kết nối"
                Log.i(TAG, "connected to ${cfg.server} as ${cfg.id}")
                onOpen()
            }.let {}

            override fun onMessage(webSocket: WebSocket, text: String) = handler.post {
                if (my == gen) onText(text)
            }.let {}

            override fun onMessage(webSocket: WebSocket, bytes: ByteString) {} // file data: unsupported

            override fun onClosing(webSocket: WebSocket, code: Int, reason: String) {
                webSocket.close(1000, null)
                down(my, if (code == 4000) "Máy khác đã kết nối bằng cùng tên máy" else "Mất kết nối ($code)",
                    if (code == 4000) 60_000L else null)
            }

            override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
                val code = response?.code
                down(my, when (code) {
                    401, 403 -> "Sai mã truy cập"
                    null -> "Không kết nối được: ${t.message ?: t.javaClass.simpleName}"
                    else -> "Máy chủ trả lỗi HTTP $code"
                }, if (code == 401 || code == 403) 60_000L else null)
            }
        })
    }

    private fun down(my: Int, why: String, wait: Long?) = handler.post {
        if (my != gen) return@post
        gen++
        ws = null
        val was = connected
        connected = false
        state = why
        Log.w(TAG, why)
        if (was) onDown()
        val delay = wait ?: backoff
        backoff = minOf(backoff * 2, 30_000L)
        handler.postDelayed({ connect() }, delay)
    }

    fun send(text: String): Boolean = ws?.send(text) ?: false

    fun send(bytes: ByteArray): Boolean = ws?.send(bytes.toByteString()) ?: false

    /** Bytes waiting to go out: the uplink is backed up when this grows. */
    fun queued(): Long = ws?.queueSize() ?: 0

    companion object {
        private const val TAG = "Relay"
    }
}
