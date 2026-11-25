package com.remohop.app

import android.os.Handler
import android.os.Looper
import com.remohop.app.core.remohopcore.Remohopcore
import org.json.JSONObject

/**
 * The pairing in progress (Settings → Pair with a host), kept in the
 * process rather than in the activity: a pairing may wait minutes for the
 * host's owner to approve, and the Settings screen can be recreated
 * meanwhile (the theme changes, the window is resized) or left and opened
 * again. The new screen shows the pairing still running, with Cancel, and
 * then its outcome. State is touched on the main thread only.
 */
object PairJob {
    private val main = Handler(Looper.getMainLooper())
    private val listeners = LinkedHashSet<() -> Unit>()

    /** Whether a pairing runs now. */
    @Volatile var running = false
        private set

    /** The outcome of the last pairing, until it has been shown and left ([clear]). */
    var result: Result<JSONObject>? = null
        private set

    /** Starts a pairing unless one runs; [listeners] hear when it ends. */
    fun start(target: String, password: String, alias: String): Boolean {
        if (running) return false
        running = true
        result = null
        // Not on an executor that other work shares: a pairing may wait minutes.
        Thread {
            val out = runCatching { JSONObject(Remohopcore.pairWithPassword(target, password, alias)) }
            out.onSuccess { r ->
                val ok = r.optBoolean("ok")
                Diag.log(if (ok) "paired with a host" else "pairing failed: ${r.optString("code")} ${r.optString("reason")} ${r.optString("error")}")
                if (ok) PendingPage.peersChanged()
            }.onFailure { Diag.log("pairing failed: ${it.message}") }
            main.post {
                running = false
                result = out
                for (l in listeners.toList()) l()
            }
        }.start()
        return true
    }

    fun cancel() = Thread { Remohopcore.cancelPair() }.start()

    /** The outcome was seen and the page left: the next visit starts clean. */
    fun clear() { if (!running) result = null }

    fun listen(l: () -> Unit) { listeners.add(l) }
    fun unlisten(l: () -> Unit) { listeners.remove(l) }
}
