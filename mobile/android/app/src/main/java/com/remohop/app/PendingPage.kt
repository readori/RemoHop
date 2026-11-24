package com.remohop.app

/**
 * What Settings did that the page must hear about when it is back in front
 * (MainActivity.onResume): saved hosts renamed or forgotten (the page lists
 * them again), or Connect on a saved host. Kept in the process, never in an
 * Intent: MainActivity is exported, and no other app may make it connect.
 */
object PendingPage {
    private var changed = false
    private var target: String? = null

    @Synchronized fun peersChanged() { changed = true }

    @Synchronized fun connect(t: String) { changed = true; target = t }

    /** What is pending (hosts changed, a target to connect to), cleared. */
    @Synchronized fun take(): Pair<Boolean, String?> {
        val r = changed to target
        changed = false
        target = null
        return r
    }
}
