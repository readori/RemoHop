package com.remohop.app

import android.content.DialogInterface
import android.content.Intent
import android.util.Log
import android.view.View
import android.view.ViewGroup
import android.widget.TextView
import androidx.test.core.app.ActivityScenario
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import androidx.test.runner.lifecycle.ActivityLifecycleMonitorRegistry
import androidx.test.runner.lifecycle.Stage
import com.remohop.app.core.remohopcore.Remohopcore
import org.json.JSONObject
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.BeforeClass
import org.junit.Test
import org.junit.runner.RunWith
import java.util.concurrent.Executors
import java.util.concurrent.Future

/**
 * On a device or emulator: host mode starts from the "This device" page,
 * which then shows how to reach the phone (its ID, or without a rendezvous
 * server its address) and the temporary password; a connection request
 * brings up the approval dialog with the scam warning, and Deny refuses
 * it. (Screen capture needs the owner's consent in a system dialog, which
 * a test cannot give: the screen itself is covered by the core's tests.)
 */
@RunWith(AndroidJUnit4::class)
class HostModeTest {
    private val ins = InstrumentationRegistry.getInstrumentation()
    private val ctx = ins.targetContext

    @Before fun clean() {
        Core.init(ctx)
        Remohopcore.setSetting("signal", "")
    }

    @After fun off() {
        HostMode.setEnabled(ctx, false)
        waitUntil(10_000) { !Remohopcore.hostRunning() && !HostService.running }
    }

    private fun waitUntil(ms: Long, cond: () -> Boolean): Boolean {
        val end = System.currentTimeMillis() + ms
        while (System.currentTimeMillis() < end) {
            if (cond()) return true
            Thread.sleep(100)
        }
        return cond()
    }

    private fun texts(v: View, out: MutableList<String> = mutableListOf()): List<String> {
        if (v is TextView && v.isShown) out += v.text.toString()
        if (v is ViewGroup) for (i in 0 until v.childCount) texts(v.getChildAt(i), out)
        return out
    }

    private fun page() = ActivityScenario.launch<SettingsActivity>(
        Intent(ctx, SettingsActivity::class.java).putExtra(SettingsActivity.EXTRA_PAGE, SettingsActivity.PAGE_THIS_DEVICE)
    )

    private fun startHost(): HostState {
        ins.runOnMainSync { HostMode.setEnabled(ctx, true) }
        assertTrue("host mode did not start", waitUntil(30_000) { HostMode.state.on && HostMode.state.password.isNotEmpty() && HostMode.state.reachableAs.isNotEmpty() })
        return HostMode.state
    }

    @Test fun startsAndShowsHowToConnect() {
        page().use { s ->
            ins.waitForIdleSync()
            val st = startHost()
            Log.i(TAG, "host mode: ${st.phase}, reachable as an ${if (st.id.isNotEmpty()) "ID" else "address"}")
            assertEquals(HostState.Status.NO_SERVER, st.status) // no rendezvous server set
            assertTrue("the service is not running", waitUntil(5_000) { HostService.running })
            assertTrue("the page does not show the address and the password", waitUntil(5_000) {
                var ok = false
                s.onActivity { a -> val t = texts(a.window.decorView); ok = st.reachableAs in t && st.password in t }
                ok
            })
            // A new password replaces the old one, and the page follows.
            ins.runOnMainSync { HostMode.regenerate() }
            assertTrue(waitUntil(5_000) { HostMode.state.password.isNotEmpty() && HostMode.state.password != st.password })
            HostMode.setEnabled(ctx, false)
            assertTrue("host mode did not stop", waitUntil(10_000) { !HostMode.state.on && !HostService.running })
        }
    }

    @Test fun requestShowsTheApprovalDialog() {
        page().use { _ ->
            ins.waitForIdleSync()
            val st = startHost()
            // This app's own controller side asks to connect to its host, by
            // address with the temporary password: the owner is asked.
            val target = st.addresses.first()
            val pair: Future<String> = Executors.newSingleThreadExecutor().submit<String> { Remohopcore.pairWithPassword(target, st.password, "") }
            var approval: ApprovalActivity? = null
            assertTrue("no approval dialog", waitUntil(30_000) {
                ins.runOnMainSync {
                    approval = ActivityLifecycleMonitorRegistry.getInstance().getActivitiesInStage(Stage.RESUMED).filterIsInstance<ApprovalActivity>().firstOrNull()
                }
                approval?.dialog?.isShowing == true
            })
            val req = HostMode.state.requests.firstOrNull()
            assertNotNull("the request is not in the state", req)
            ins.runOnMainSync {
                val d = approval!!.dialog!!
                val warning = d.findViewById<TextView>(R.id.host_scam_warning)
                assertNotNull("no scam warning", warning)
                assertTrue(warning!!.isShown && warning.text.isNotEmpty())
                val control = d.findViewById<View>(R.id.host_req_control)
                assertNotNull("no control choice", control)
                // The accessibility service is off on a fresh emulator: control cannot be ticked.
                if (ControlService.instance == null) assertFalse(control!!.isEnabled)
                d.getButton(DialogInterface.BUTTON_NEGATIVE).performClick()
            }
            val out = JSONObject(pair.get())
            Log.i(TAG, "pairing after Deny: ${out.optString("code")}")
            assertFalse(out.optBoolean("ok"))
            assertTrue(waitUntil(5_000) { HostMode.state.requests.isEmpty() && HostMode.state.sessions.isEmpty() })
        }
    }

    companion object {
        const val TAG = "RemoHopE2E"

        /**
         * A crash of the app process later in the run (in any test) is
         * logged under the tag the CI prints, so a "Process crashed" comes
         * with its stack.
         */
        @JvmStatic @BeforeClass fun logCrashes() {
            val previous = Thread.getDefaultUncaughtExceptionHandler()
            Thread.setDefaultUncaughtExceptionHandler { t, e ->
                Log.e(TAG, "process crash on thread ${t.name}: " + Log.getStackTraceString(e).lines().take(40).joinToString(" | "))
                previous?.uncaughtException(t, e)
            }
        }
    }
}
