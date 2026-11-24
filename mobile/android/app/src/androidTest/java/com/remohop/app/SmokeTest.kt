package com.remohop.app

import android.webkit.WebView
import androidx.test.core.app.ActivityScenario
import androidx.test.ext.junit.runners.AndroidJUnit4
import com.remohop.app.core.remohopcore.Remohopcore
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit

/**
 * On a device or emulator: the app starts its gateway, the WebView loads the
 * connect form, the injected script is active with its bridge, and the
 * back button on the connect screen is left to the app.
 */
@RunWith(AndroidJUnit4::class)
class SmokeTest {
    private fun eval(s: ActivityScenario<MainActivity>, js: String): String? {
        var out: String? = null
        val done = CountDownLatch(1)
        s.onActivity { a ->
            val web = findWebView(a.window.decorView)
            web!!.evaluateJavascript(js) { out = it; done.countDown() }
        }
        done.await(10, TimeUnit.SECONDS)
        return out
    }

    private fun findWebView(v: android.view.View): WebView? {
        if (v is WebView) return v
        if (v is android.view.ViewGroup) for (i in 0 until v.childCount) findWebView(v.getChildAt(i))?.let { return it }
        return null
    }

    private fun waitFor(s: ActivityScenario<MainActivity>, js: String, seconds: Int = 30): Boolean {
        repeat(seconds * 4) {
            if (eval(s, js) == "true") return true
            Thread.sleep(250)
        }
        return false
    }

    @Test fun connectFormLoadsWithBridge() {
        // A fresh gateway: another test in this process may have left a connected page session.
        Remohopcore.stopGateway()
        ActivityScenario.launch(MainActivity::class.java).use { s ->
            assertTrue("connect form", waitFor(s, "!!document.getElementById('connect') && !document.getElementById('connect').hidden"))
            assertTrue("injected script", waitFor(s, "typeof window.remohopMobile === 'object'"))
            assertTrue("native bridge", waitFor(s, "typeof window.remohopNative === 'object'"))
            assertTrue("settings button", waitFor(s, "!!document.getElementById('sdm-settings')"))
            assertEquals("\"unhandled\"", eval(s, "remohopMobile.back()"))
            assertTrue(Remohopcore.gatewayRunning())
            // The clipboard bridge answers.
            assertTrue(waitFor(s, "(window.__clip = window.__clip || navigator.clipboard.readText().then(() => window.__clipOK = true), window.__clipOK === true)"))
        }
    }
}
