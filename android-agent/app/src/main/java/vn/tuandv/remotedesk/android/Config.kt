package vn.tuandv.remotedesk.android

import android.content.Context
import android.os.Build
import android.provider.Settings

const val DEFAULT_SERVER = "https://remote.tuandv.id.vn"
const val DEFAULT_SPEAKER_URL = "wss://birdmonitor.tuandv.id.vn/ws?role=speaker"

/** Settings, kept in the app's private SharedPreferences. */
class Config(ctx: Context) {
    private val p = ctx.getSharedPreferences("config", Context.MODE_PRIVATE)

    var server: String
        get() = p.getString("server", DEFAULT_SERVER)!!
        set(v) = p.edit().putString("server", v.trim().trimEnd('/')).apply()
    var token: String
        get() = p.getString("token", "")!!
        set(v) = p.edit().putString("token", v.trim()).apply()
    var name: String
        get() = p.getString("name", null) ?: defaultName()
        set(v) = p.edit().putString("name", v.trim()).apply()

    /** Stable machine id on the relay: model + a piece of ANDROID_ID. */
    val id: String
    var speakerOn: Boolean
        get() = p.getBoolean("speakerOn", false)
        set(v) = p.edit().putBoolean("speakerOn", v).apply()
    var speakerUrl: String
        get() = p.getString("speakerUrl", DEFAULT_SPEAKER_URL)!!
        set(v) = p.edit().putString("speakerUrl", v.trim()).apply()
    var speakerToken: String
        get() = p.getString("speakerToken", "")!!
        set(v) = p.edit().putString("speakerToken", v.trim()).apply()

    val ready get() = server.isNotEmpty() && token.isNotEmpty()

    init {
        id = p.getString("id", null) ?: run {
            @Suppress("HardwareIds")
            val aid = Settings.Secure.getString(ctx.contentResolver, Settings.Secure.ANDROID_ID) ?: ""
            val v = slug("${Build.MANUFACTURER} ${Build.MODEL}") + "-" + aid.take(6).ifEmpty { "x" }
            p.edit().putString("id", v).apply()
            v
        }
    }

    private fun defaultName(): String {
        val m = Build.MODEL ?: "Android"
        return if (m.startsWith(Build.MANUFACTURER ?: "", ignoreCase = true)) m
        else "${Build.MANUFACTURER?.replaceFirstChar { it.uppercase() } ?: ""} $m".trim()
    }

    companion object {
        fun slug(s: String) = s.lowercase().replace(Regex("[^a-z0-9]+"), "-").trim('-').take(32).ifEmpty { "android" }
    }
}
