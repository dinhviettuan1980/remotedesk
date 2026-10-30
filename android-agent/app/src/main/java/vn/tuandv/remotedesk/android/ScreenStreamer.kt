package vn.tuandv.remotedesk.android

import android.hardware.display.DisplayManager
import android.hardware.display.VirtualDisplay
import android.media.MediaCodec
import android.media.MediaCodecInfo
import android.media.MediaFormat
import android.media.projection.MediaProjection
import android.os.Bundle
import android.util.Log
import android.view.Surface
import kotlin.math.max
import kotlin.math.roundToInt

/**
 * Screen → H.264 (Annex B) with the hardware encoder. Each access unit goes to
 * [sink]; keyframes carry SPS/PPS in front so a viewer can start on any of them,
 * the same as the desktop agent's ffmpeg output.
 */
class ScreenStreamer(
    private val projection: MediaProjection,
    private val screenW: Int,
    private val screenH: Int,
    private val dpi: Int,
    private val sink: (key: Boolean, data: ByteArray) -> Unit,
) {
    private var codec: MediaCodec? = null
    private var surface: Surface? = null
    private var display: VirtualDisplay? = null
    private var thread: Thread? = null
    @Volatile private var running = false

    var width = 0; private set
    var height = 0; private set

    /** Starts encoding; throws when no size the encoder accepts works. */
    fun start(fps: Int, maxLong: Int, kbps: Int) {
        var lastErr: Exception? = null
        // Old encoders reject some sizes: step down until one is accepted.
        for (long in listOf(maxLong, 960, 640).filter { it <= maxLong }.distinct()) {
            try {
                open(fps, long, kbps)
                return
            } catch (e: Exception) {
                Log.w(TAG, "encoder ${long}p failed", e)
                lastErr = e
                release()
            }
        }
        throw lastErr ?: IllegalStateException("no encoder")
    }

    private fun open(fps: Int, maxLong: Int, kbps: Int) {
        val scale = minOf(1f, maxLong.toFloat() / max(screenW, screenH))
        width = round16(screenW * scale)
        height = round16(screenH * scale)
        val fmt = MediaFormat.createVideoFormat(MediaFormat.MIMETYPE_VIDEO_AVC, width, height).apply {
            setInteger(MediaFormat.KEY_COLOR_FORMAT, MediaCodecInfo.CodecCapabilities.COLOR_FormatSurface)
            setInteger(MediaFormat.KEY_BIT_RATE, kbps * 1000)
            setInteger(MediaFormat.KEY_FRAME_RATE, fps)
            setInteger(MediaFormat.KEY_I_FRAME_INTERVAL, GOP_SECONDS)
            // A still screen produces no frames; repeat the last one so new
            // viewers and keyframe requests are answered within 250 ms.
            setLong(MediaFormat.KEY_REPEAT_PREVIOUS_FRAME_AFTER, 250_000L)
        }
        val c = MediaCodec.createEncoderByType(MediaFormat.MIMETYPE_VIDEO_AVC)
        codec = c
        c.configure(fmt, null, null, MediaCodec.CONFIGURE_FLAG_ENCODE)
        surface = c.createInputSurface()
        c.start()
        display = projection.createVirtualDisplay(
            "remotedesk", width, height, dpi,
            DisplayManager.VIRTUAL_DISPLAY_FLAG_AUTO_MIRROR, surface, null, null,
        )
        running = true
        thread = Thread({ drain(c) }, "encoder").also { it.start() }
        Log.i(TAG, "streaming ${width}x$height ${fps}fps ${kbps}kbps (screen ${screenW}x$screenH)")
    }

    private fun drain(c: MediaCodec) {
        val info = MediaCodec.BufferInfo()
        var config: ByteArray? = null
        while (running) {
            val i = try {
                c.dequeueOutputBuffer(info, 100_000)
            } catch (e: IllegalStateException) {
                break
            }
            if (i < 0) continue
            val buf = c.getOutputBuffer(i)
            if (buf != null && info.size > 0) {
                buf.position(info.offset)
                buf.limit(info.offset + info.size)
                val bytes = ByteArray(info.size)
                buf.get(bytes)
                if (info.flags and MediaCodec.BUFFER_FLAG_CODEC_CONFIG != 0) {
                    config = bytes
                } else {
                    val key = info.flags and MediaCodec.BUFFER_FLAG_KEY_FRAME != 0
                    val cfg = config
                    sink(key, if (key && cfg != null) cfg + bytes else bytes)
                }
            }
            try {
                c.releaseOutputBuffer(i, false)
            } catch (e: IllegalStateException) {
                break
            }
        }
    }

    fun requestKeyframe() {
        try {
            codec?.setParameters(Bundle().apply { putInt(MediaCodec.PARAMETER_KEY_REQUEST_SYNC_FRAME, 0) })
        } catch (e: IllegalStateException) {
            // stopping
        }
    }

    fun stop() {
        running = false
        thread?.join(1000)
        thread = null
        release()
    }

    private fun release() {
        display?.release()
        display = null
        codec?.let {
            try { it.stop() } catch (_: Exception) {}
            it.release()
        }
        codec = null
        surface?.release()
        surface = null
    }

    companion object {
        private const val TAG = "ScreenStreamer"
        const val GOP_SECONDS = 2
        private fun round16(v: Float) = max(16, (v / 16f).roundToInt() * 16)
    }
}
