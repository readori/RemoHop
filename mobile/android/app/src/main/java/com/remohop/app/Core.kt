package com.remohop.app

import android.content.Context
import android.os.Build
import com.remohop.app.core.remohopcore.Remohopcore
import java.io.File

/**
 * The Go core (gomobile-bound internal/mobile), process wide. The gateway
 * lives as long as the process, so host sessions survive the Activity being
 * recreated; each process start gets a new port and token.
 *
 * Never log [gatewayUrl]: it contains the gateway's access token, and any
 * app on the device can reach 127.0.0.1.
 */
object Core {
    @Volatile private var initialized = false
    private var url: String? = null

    @Synchronized
    fun init(context: Context) {
        if (initialized) return
        val app = context.applicationContext
        // App-private storage only: identity key, saved peers, settings.
        val dir = File(app.filesDir, "remohop")
        val tmp = File(app.cacheDir, "go-tmp")
        Remohopcore.init(dir.absolutePath, tmp.absolutePath)
        Remohopcore.setAppVersion("${BuildConfig.VERSION_NAME} (Android)")
        Remohopcore.setDeviceName(listOf(Build.MANUFACTURER, Build.MODEL).filter { it.isNotBlank() }.distinct().joinToString(" ").take(64))
        initialized = true
    }

    /** The gateway's URL with its token, starting the gateway if needed. */
    @Synchronized
    fun gatewayUrl(context: Context): String {
        init(context)
        val u = url
        if (u != null && Remohopcore.gatewayRunning()) return u
        val fresh = Remohopcore.startGateway()
        url = fresh
        return fresh
    }
}
