package vn.tuandv.remotedesk.android

import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.pm.PackageInstaller
import android.net.Uri
import android.provider.Settings
import android.util.Log
import okhttp3.OkHttpClient
import okhttp3.Request
import java.util.concurrent.TimeUnit

/**
 * Self-update from the relay ({server}/dl/RemoteDesk-Android.apk): the APK goes
 * into a PackageInstaller session; Android then shows its "Install" prompt,
 * which [InputService] taps (or the remote user does). The new version comes
 * back up through [BootReceiver] (MY_PACKAGE_REPLACED).
 */
object Updater {
    private const val TAG = "Updater"
    const val APK = "RemoteDesk-Android.apk"

    fun start(agent: AgentService, cfg: Config) {
        if (!agent.packageManager.canRequestPackageInstalls()) {
            // Android 8+: each app needs "Install unknown apps" once. Open that
            // screen so the remote user can switch it on, then update again.
            agent.wakeScreen()
            agent.startActivity(
                Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES, Uri.parse("package:${agent.packageName}"))
                    .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK),
            )
            agent.sendNotice("Điện thoại chưa cho RemoteDesk tự cài bản mới. Đã mở trang cài đặt: bật \"Cho phép từ nguồn này\", rồi bấm Cập nhật lại.")
            return
        }
        agent.sendNotice("Đang tải bản cập nhật app Android…")
        Thread {
            try {
                install(agent, cfg)
                agent.sendNotice("Đang cài bản mới — app sẽ tự nối lại sau khoảng 10–30 giây.")
            } catch (e: Exception) {
                Log.w(TAG, "update", e)
                agent.sendNotice("Không cập nhật được: ${e.message}")
            }
        }.start()
    }

    private fun install(ctx: Context, cfg: Config) {
        val http = OkHttpClient.Builder().readTimeout(60, TimeUnit.SECONDS).build()
        val req = Request.Builder().url("${cfg.server}/dl/$APK")
            .header("Authorization", "Bearer ${cfg.token}").build()
        val pi = ctx.packageManager.packageInstaller
        http.newCall(req).execute().use { r ->
            if (!r.isSuccessful) throw IllegalStateException("HTTP ${r.code}")
            val params = PackageInstaller.SessionParams(PackageInstaller.SessionParams.MODE_FULL_INSTALL)
                .apply { setAppPackageName(ctx.packageName) }
            val id = pi.createSession(params)
            pi.openSession(id).use { s ->
                s.openWrite("app.apk", 0, r.body!!.contentLength()).use { out ->
                    r.body!!.byteStream().copyTo(out)
                    s.fsync(out)
                }
                InputService.expectInstall()
                val done = PendingIntent.getBroadcast(
                    ctx, id, Intent(ctx, UpdateReceiver::class.java),
                    PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_MUTABLE,
                )
                s.commit(done.intentSender)
            }
        }
    }
}

/** PackageInstaller's answer: show Android's confirmation, or report the failure. */
class UpdateReceiver : BroadcastReceiver() {
    override fun onReceive(ctx: Context, intent: Intent) {
        when (val status = intent.getIntExtra(PackageInstaller.EXTRA_STATUS, -1)) {
            PackageInstaller.STATUS_PENDING_USER_ACTION -> {
                @Suppress("DEPRECATION")
                val confirm = intent.getParcelableExtra<Intent>(Intent.EXTRA_INTENT) ?: return
                AgentService.instance?.wakeScreen()
                ctx.startActivity(confirm.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
            }
            PackageInstaller.STATUS_SUCCESS -> {}
            else -> AgentService.instance?.sendNotice(
                "Không cài được bản mới ($status): ${intent.getStringExtra(PackageInstaller.EXTRA_STATUS_MESSAGE) ?: ""}",
            )
        }
    }
}
