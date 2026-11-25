package com.remohop.app

import android.app.Activity
import android.content.Intent
import android.view.View
import android.view.ViewGroup
import android.widget.EditText
import android.widget.TextView
import androidx.test.core.app.ActivityScenario
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import androidx.test.runner.lifecycle.ActivityLifecycleMonitorRegistry
import androidx.test.runner.lifecycle.Stage
import com.remohop.app.core.remohopcore.Remohopcore
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith

/**
 * On a device or emulator: the Settings screen keeps what the user typed
 * and a pairing in progress when it is recreated, shows a field saved by
 * leaving the page, tests what is typed, and stays open when the app is
 * started again from its icon.
 */
@RunWith(AndroidJUnit4::class)
class SettingsLifecycleTest {
    private val ctx = InstrumentationRegistry.getInstrumentation().targetContext

    @Before fun clean() {
        Core.init(ctx)
        Remohopcore.setSetting("signal", "")
    }

    @After fun after() {
        if (PairJob.running) PairJob.cancel()
        waitUntil(20_000) { !PairJob.running }
        Remohopcore.setSetting("signal", "")
    }

    private fun idle() = InstrumentationRegistry.getInstrumentation().waitForIdleSync()

    /** Polls [cond] on the test thread (it uses onActivity or runOnMainSync for the UI). */
    private fun waitUntil(ms: Long, cond: () -> Boolean): Boolean {
        val end = System.currentTimeMillis() + ms
        while (System.currentTimeMillis() < end) {
            if (cond()) return true
            Thread.sleep(100)
        }
        return false
    }

    private fun rendezvous() = ActivityScenario.launch<SettingsActivity>(
        Intent(ctx, SettingsActivity::class.java).putExtra(SettingsActivity.EXTRA_PAGE, SettingsActivity.PAGE_RENDEZVOUS)
    )

    private fun texts(v: View, out: MutableList<TextView> = mutableListOf()): List<TextView> {
        if (v is TextView && v.isShown) out += v
        if (v is ViewGroup) for (i in 0 until v.childCount) texts(v.getChildAt(i), out)
        return out
    }

    private fun shows(a: Activity, text: String) = texts(a.window.decorView).any { it.text.toString() == text }

    @Test fun typedTextSurvivesRecreation() {
        rendezvous().use { s ->
            idle()
            s.onActivity { a -> a.findViewById<EditText>(R.id.field_signal).setText("typed.example.net:21116") }
            s.recreate()
            idle()
            Thread.sleep(500) // the settings are read again and the page is built again
            idle()
            s.onActivity { a -> assertEquals("typed.example.net:21116", a.findViewById<EditText>(R.id.field_signal).text.toString()) }
        }
    }

    @Test fun leavingTheFieldShowsTheSavedValueOnTheMainPage() {
        rendezvous().use { s ->
            idle()
            s.onActivity { a ->
                val f = a.findViewById<EditText>(R.id.field_signal)
                f.requestFocus()
                f.setText("left.example.net")
                a.onBackPressedDispatcher.onBackPressed()
            }
            assertTrue("main page shows the server saved on the way out", waitUntil(5_000) {
                var ok = false
                s.onActivity { a -> ok = shows(a, "left.example.net") }
                ok
            })
            assertEquals("left.example.net", Remohopcore.getSetting("signal"))
        }
    }

    @Test fun testConnectionSavesWhatIsTyped() {
        rendezvous().use { s ->
            idle()
            s.onActivity { a ->
                a.findViewById<EditText>(R.id.field_signal).setText("typed-test.example.net:21116")
                val row = texts(a.window.decorView).first { it.text.toString() == a.getString(R.string.test_connection) }
                var v: View = row
                while (!v.isClickable && v.parent is View) v = v.parent as View
                v.performClick()
            }
            assertTrue("Test connection saves the typed address first", waitUntil(5_000) {
                Remohopcore.getSetting("signal") == "typed-test.example.net:21116"
            })
        }
    }

    @Test fun pairingInProgressSurvivesRecreation() {
        ActivityScenario.launch(SettingsActivity::class.java).use { s ->
            idle()
            s.onActivity { a ->
                a.openForTest(SettingsActivity.PAGE_PAIR)
                // TEST-NET-1: nothing answers, so the pairing keeps trying for a while.
                a.findViewById<EditText>(R.id.field_pair_target).setText("192.0.2.1:21118")
                a.findViewById<EditText>(R.id.field_pair_password).setText("x")
                val pair = texts(a.window.decorView).first { it.text.toString() == a.getString(R.string.pair) }
                pair.performClick()
            }
            idle()
            assertTrue(PairJob.running)
            s.recreate()
            idle()
            s.onActivity { a ->
                assertTrue("the recreated page shows the pairing", shows(a, a.getString(R.string.pairing)))
                assertTrue("with Cancel", shows(a, a.getString(R.string.cancel)))
                assertEquals("192.0.2.1:21118", a.findViewById<EditText>(R.id.field_pair_target).text.toString())
            }
            PairJob.cancel()
            assertTrue(waitUntil(20_000) { !PairJob.running })
            assertTrue("and then its outcome", waitUntil(5_000) {
                var ok = false
                s.onActivity { a -> ok = shows(a, a.getString(R.string.pair_err_canceled)) }
                ok
            })
        }
    }

    private fun resumed(): Activity? {
        var a: Activity? = null
        InstrumentationRegistry.getInstrumentation().runOnMainSync {
            a = ActivityLifecycleMonitorRegistry.getInstance().getActivitiesInStage(Stage.RESUMED).firstOrNull()
        }
        return a
    }

    @Test fun startingTheAppAgainKeepsSettingsOpen() {
        val launch = ctx.packageManager.getLaunchIntentForPackage(ctx.packageName)!!
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TASK)
        ctx.startActivity(launch)
        assertTrue(waitUntil(10_000) { resumed() is MainActivity })
        val main = resumed() as MainActivity
        InstrumentationRegistry.getInstrumentation().runOnMainSync { main.openSettings() }
        assertTrue(waitUntil(10_000) { resumed() is SettingsActivity })
        val settings = resumed()
        assertNotNull(settings)
        // What the launcher does when its icon is tapped again.
        ctx.startActivity(ctx.packageManager.getLaunchIntentForPackage(ctx.packageName)!!
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_RESET_TASK_IF_NEEDED))
        Thread.sleep(1500)
        idle()
        assertFalse("Settings was closed", settings!!.isDestroyed)
        assertTrue("Settings is in front", resumed() is SettingsActivity)
        InstrumentationRegistry.getInstrumentation().runOnMainSync { settings.finish(); main.finish() }
    }
}
