package vn.tuandv.remotedesk.android

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent

/** Back up after a reboot or an update, with nobody touching the phone. */
class BootReceiver : BroadcastReceiver() {
    override fun onReceive(ctx: Context, intent: Intent) {
        if (Config(ctx).ready) AgentService.start(ctx)
    }
}
