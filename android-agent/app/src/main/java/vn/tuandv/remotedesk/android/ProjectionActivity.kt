package vn.tuandv.remotedesk.android

import android.app.Activity
import android.app.KeyguardManager
import android.content.Intent
import android.media.projection.MediaProjectionManager
import android.os.Bundle
import android.view.WindowManager

/**
 * Invisible activity, shown over a swipe lock screen. It asks for screen
 * capture (Android 8–9 grants it silently once "Don't show again" was ticked,
 * and [InputService] taps "Start now" otherwise), or with [EXTRA_UNLOCK_ONLY]
 * just dismisses the swipe lock so the remote user lands on the home screen.
 */
class ProjectionActivity : Activity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        @Suppress("DEPRECATION")
        window.addFlags(
            WindowManager.LayoutParams.FLAG_SHOW_WHEN_LOCKED or
                WindowManager.LayoutParams.FLAG_TURN_SCREEN_ON or
                WindowManager.LayoutParams.FLAG_DISMISS_KEYGUARD,
        )
        val km = getSystemService(KEYGUARD_SERVICE) as KeyguardManager
        if (km.isKeyguardLocked && !km.isKeyguardSecure) km.requestDismissKeyguard(this, null)
        if (intent.getBooleanExtra(EXTRA_UNLOCK_ONLY, false)) {
            finish()
            return
        }
        if (savedInstanceState == null) {
            val mpm = getSystemService(MEDIA_PROJECTION_SERVICE) as MediaProjectionManager
            startActivityForResult(mpm.createScreenCaptureIntent(), REQ)
        }
    }

    @Deprecated("Activity result API needs AndroidX")
    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        if (requestCode == REQ) {
            val agent = AgentService.instance
            if (resultCode == RESULT_OK && data != null) agent?.onProjection(resultCode, data)
            else agent?.onProjectionDenied()
            InputService.consentDone()
        }
        finish()
    }

    companion object {
        const val EXTRA_UNLOCK_ONLY = "unlockOnly"
        private const val REQ = 1
    }
}
