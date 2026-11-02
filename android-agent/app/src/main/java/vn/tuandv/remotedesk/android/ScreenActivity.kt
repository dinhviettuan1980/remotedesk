package vn.tuandv.remotedesk.android

import android.app.Activity
import android.content.Context
import android.content.Intent
import android.graphics.Bitmap
import android.graphics.Color
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.util.TypedValue
import android.view.Gravity
import android.view.SurfaceHolder
import android.view.SurfaceView
import android.view.View
import android.view.WindowManager
import android.widget.FrameLayout
import android.widget.ImageView
import android.widget.TextView

/**
 * What BirdMonitor shows the bird on this phone's screen (its front faces the
 * cage): a nature video, a big emoji while a lesson phrase plays, or the
 * owner's face during a video call. [SpeakerClient] drives it; the activity
 * closes itself when there is nothing to show, so the screen goes back to
 * whatever was there.
 */
object BirdScreen {
    enum class Mode { NONE, VIDEO, PICTURE, CALL }

    private val main = Handler(Looper.getMainLooper())
    @Volatile var mode = Mode.NONE; private set
    // Main thread only.
    @Volatile var picture: String? = null; private set
    internal var activity: ScreenActivity? = null
    @Volatile internal var surface: SurfaceHolder? = null
    private val waiting = mutableListOf<(SurfaceHolder?) -> Unit>()
    /** Called when the video surface goes away (activity closed by hand). */
    @Volatile var onSurfaceGone: (() -> Unit)? = null

    private val hideNow = Runnable {
        mode = Mode.NONE
        picture = null
        surface = null
        activity?.finish()
    }

    fun show(ctx: Context, mode: Mode, picture: String? = null) = main.post {
        main.removeCallbacks(hideNow)
        this.mode = mode
        this.picture = picture
        val a = activity
        if (a != null && !a.isFinishing) a.render()
        else ctx.startActivity(Intent(ctx, ScreenActivity::class.java)
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_NO_ANIMATION))
    }

    /** Close after a moment, so the next piece of a video does not flash the home screen. */
    fun hide() = main.post {
        main.removeCallbacks(hideNow)
        main.postDelayed(hideNow, 1_500)
    }

    /** The video surface, once the activity has one (null after `timeoutMs`). */
    fun withSurface(timeoutMs: Long, cb: (SurfaceHolder?) -> Unit) = main.post {
        val s = surface
        if (s != null) { cb(s); return@post }
        waiting.add(cb)
        main.postDelayed({ if (waiting.remove(cb)) cb(null) }, timeoutMs)
    }

    fun videoSize(w: Int, h: Int) = main.post { activity?.fitVideo(w, h) }

    fun frame(bmp: Bitmap) = main.post { activity?.showFrame(bmp) }

    internal fun surfaceReady(holder: SurfaceHolder) {
        surface = holder
        val cbs = waiting.toList()
        waiting.clear()
        cbs.forEach { it(holder) }
    }

    internal fun surfaceGone(holder: SurfaceHolder) {
        if (surface !== holder) return
        surface = null
        onSurfaceGone?.invoke()
    }
}

class ScreenActivity : Activity(), SurfaceHolder.Callback {
    private lateinit var video: SurfaceView
    private lateinit var emoji: TextView
    private lateinit var face: ImageView
    private var videoW = 0
    private var videoH = 0

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        @Suppress("DEPRECATION")
        window.addFlags(
            WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON or
                WindowManager.LayoutParams.FLAG_SHOW_WHEN_LOCKED or
                WindowManager.LayoutParams.FLAG_TURN_SCREEN_ON or
                WindowManager.LayoutParams.FLAG_DISMISS_KEYGUARD or
                WindowManager.LayoutParams.FLAG_FULLSCREEN,
        )
        val root = FrameLayout(this).apply { setBackgroundColor(Color.BLACK) }
        video = SurfaceView(this).also { it.holder.addCallback(this) }
        emoji = TextView(this).apply {
            gravity = Gravity.CENTER
            setTextSize(TypedValue.COMPLEX_UNIT_PX, resources.displayMetrics.widthPixels * 0.55f)
            setTextColor(Color.WHITE)
        }
        face = ImageView(this).apply { scaleType = ImageView.ScaleType.CENTER_CROP }
        val fill = FrameLayout.LayoutParams.MATCH_PARENT
        root.addView(video, FrameLayout.LayoutParams(fill, fill, Gravity.CENTER))
        root.addView(emoji, FrameLayout.LayoutParams(fill, fill))
        root.addView(face, FrameLayout.LayoutParams(fill, fill))
        setContentView(root)
        BirdScreen.activity = this
        render()
    }

    override fun onResume() {
        super.onResume()
        @Suppress("DEPRECATION")
        window.decorView.systemUiVisibility = View.SYSTEM_UI_FLAG_FULLSCREEN or
            View.SYSTEM_UI_FLAG_HIDE_NAVIGATION or View.SYSTEM_UI_FLAG_IMMERSIVE_STICKY
    }

    override fun onDestroy() {
        if (BirdScreen.activity === this) BirdScreen.activity = null
        super.onDestroy()
    }

    internal fun render() {
        when (BirdScreen.mode) {
            BirdScreen.Mode.NONE -> finish()
            BirdScreen.Mode.VIDEO -> {
                video.visibility = View.VISIBLE; emoji.visibility = View.GONE; face.visibility = View.GONE
            }
            BirdScreen.Mode.PICTURE -> {
                emoji.text = BirdScreen.picture ?: ""
                video.visibility = View.VISIBLE; emoji.visibility = View.VISIBLE; face.visibility = View.GONE
            }
            BirdScreen.Mode.CALL -> {
                face.setImageDrawable(null)
                video.visibility = View.VISIBLE; emoji.visibility = View.GONE; face.visibility = View.VISIBLE
            }
        }
    }

    /** Keep the video's shape: letterbox inside the screen. */
    internal fun fitVideo(w: Int, h: Int) {
        if (w <= 0 || h <= 0) return
        videoW = w; videoH = h
        val root = video.parent as View
        val rw = root.width.takeIf { it > 0 } ?: resources.displayMetrics.widthPixels
        val rh = root.height.takeIf { it > 0 } ?: resources.displayMetrics.heightPixels
        val scale = minOf(rw.toFloat() / w, rh.toFloat() / h)
        video.layoutParams = FrameLayout.LayoutParams((w * scale).toInt(), (h * scale).toInt(), Gravity.CENTER)
    }

    internal fun showFrame(bmp: Bitmap) {
        if (BirdScreen.mode == BirdScreen.Mode.CALL) face.setImageBitmap(bmp)
    }

    override fun surfaceCreated(holder: SurfaceHolder) = BirdScreen.surfaceReady(holder)
    override fun surfaceChanged(holder: SurfaceHolder, format: Int, width: Int, height: Int) {
        if (videoW > 0) fitVideo(videoW, videoH)
    }
    override fun surfaceDestroyed(holder: SurfaceHolder) = BirdScreen.surfaceGone(holder)
}
