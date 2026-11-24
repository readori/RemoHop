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

    /**
     * The URL (with its token) of a gateway that has just answered a request
     * from this process: started if needed, its listening socket reopened
     * if it failed (see EnsureGateway in internal/mobile; Android does not
     * take sockets from a frozen app the way iOS does, but the check is
     * cheap). Blocks for at most a couple of seconds: call it off the UI
     * thread. [generation] changes whenever the URL does, so a caller
     * reloads its page only then.
     */
    @Synchronized
    fun gatewayUrl(context: Context): String {
        init(context)
        val fresh = Remohopcore.ensureGateway()
        if (fresh != url) {
            url = fresh
            generation++
        }
        return fresh
    }

    /** Changes with the gateway's URL (a new gateway, or a new port). */
    @Volatile var generation = 0
        private set
}
