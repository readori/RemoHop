package com.remohop.app

/**
 * When the gateway page must be loaded again (the same rules as the iOS
 * app's PageRecovery.swift), so that the user never sees a blank WebView:
 * the renderer process died (killed for memory), a load failed, or a load
 * never finished.
 *
 * The controller reports what happened; this answers what to do. It holds
 * no URL (the gateway URL carries its token) and no timer: the controller
 * keeps those and passes [generation] back so that a late timer or probe of
 * an earlier load is ignored.
 */
class PageRecovery {
    enum class Phase {
        /** Nothing loaded yet. */
        IDLE,
        /** A load the app started is under way. */
        LOADING,
        /** The page finished loading and is shown. */
        READY,
        /**
         * The page is gone (renderer gone, load failed or interrupted): load
         * it again, at the next return to the foreground if the app is in
         * the background.
         */
        DEAD,
        /** Loading failed [MAX_RETRIES] times in a row: the "Try again" screen. */
        FAILED,
    }

    /** What to do when the app comes back to the foreground. */
    enum class Foreground {
        NOTHING,
        /** Ask the page whether it is alive ([PROBE_SCRIPT], [probeOk]). */
        PROBE,
        /** Make sure the gateway answers, then load the page again. */
        RELOAD,
    }

    /** What to do after a load failed or timed out. */
    sealed class AfterFailure {
        /** Not about a load the app is waiting for: nothing to do. */
        object Ignored : AfterFailure()
        /** In the background: reload at the next return to the foreground. */
        object WaitForForeground : AfterFailure()
        /** Load again after [afterMs] milliseconds. */
        data class Retry(val afterMs: Long) : AfterFailure()
        /** Show the "Try again" screen. */
        object GiveUp : AfterFailure()
    }

    var phase = Phase.IDLE
        private set
    /** Failed loads in a row. */
    var failures = 0
        private set
    /** Counts the app's loads. */
    var generation = 0
        private set

    /** The native cover is shown (logo, or "Try again" when [phase] is FAILED) whenever the page is not ready. */
    val covered: Boolean get() = phase != Phase.READY

    /** The app starts loading the page. */
    fun loadStarted() {
        phase = Phase.LOADING
        generation++
    }

    /** The main frame finished loading. */
    fun loadFinished() {
        phase = Phase.READY
        failures = 0
    }

    /** The renderer died. Returns whether to reload now (in the foreground) rather than on return to the foreground. */
    fun processTerminated(inBackground: Boolean): Boolean {
        phase = Phase.DEAD
        return !inBackground
    }

    /** The app went to the background: a load under way may be cut off, so it is done again on return. */
    fun enteredBackground() {
        if (phase == Phase.LOADING) phase = Phase.DEAD
    }

    /** The app's load failed (a network error of the main frame). */
    fun loadFailed(inBackground: Boolean): AfterFailure {
        if (phase != Phase.LOADING) return AfterFailure.Ignored
        if (inBackground) {
            phase = Phase.DEAD
            return AfterFailure.WaitForForeground
        }
        failures++
        if (failures > MAX_RETRIES) {
            phase = Phase.FAILED
            return AfterFailure.GiveUp
        }
        phase = Phase.DEAD
        return AfterFailure.Retry(backoffMs(failures))
    }

    /** The load timer of load [generation] fired. */
    fun loadTimedOut(generation: Int, inBackground: Boolean): AfterFailure =
        if (generation != this.generation) AfterFailure.Ignored else loadFailed(inBackground)

    /** Whether the retry timer of load [generation] should still load again (nothing else reloaded meanwhile). */
    fun retryDue(generation: Int): Boolean = generation == this.generation && phase == Phase.DEAD

    /**
     * The check on the return to the foreground found the page of load
     * [generation] dead. Returns whether to reload (no other load started meanwhile).
     */
    fun probeFailed(generation: Int): Boolean {
        if (generation != this.generation || phase != Phase.READY) return false
        phase = Phase.DEAD
        return true
    }

    fun foregroundAction(): Foreground = when (phase) {
        Phase.IDLE, Phase.LOADING -> Foreground.NOTHING
        Phase.READY -> Foreground.PROBE
        Phase.DEAD, Phase.FAILED -> Foreground.RELOAD
    }

    /** The user tapped "Try again", or came back to the app: a fresh round of retries. */
    fun retryNow() {
        failures = 0
    }

    companion object {
        const val MAX_RETRIES = 3
        /** How long a probe of the page may take before it counts as dead. */
        const val PROBE_TIMEOUT_MS = 3_000L
        /** How long a load may take before it counts as failed. */
        const val LOAD_TIMEOUT_MS = 20_000L

        /** The script [probeOk] reads: origin, ready state and how much of the page there is. */
        const val PROBE_SCRIPT = "location.origin+'|'+document.readyState+'|'+(document.body?document.body.childElementCount:0)"

        /** Milliseconds before retry number [failure] (1, 2, 4, then at most 8 seconds). */
        fun backoffMs(failure: Int): Long = minOf(8_000L, 1_000L shl (failure - 1).coerceIn(0, 4))

        /**
         * Whether [PROBE_SCRIPT]'s result (already unquoted) shows a live page
         * of the gateway at [origin]: the right origin, parsed, and with content.
         */
        fun probeOk(result: String?, origin: String): Boolean {
            val parts = result?.split("|") ?: return false
            if (parts.size != 3) return false
            return parts[0] == origin && parts[1] != "loading" && (parts[2].toIntOrNull() ?: 0) > 0
        }
    }
}
