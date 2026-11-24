package com.remohop.app

import android.accessibilityservice.AccessibilityService
import android.accessibilityservice.AccessibilityServiceInfo
import android.app.Activity
import android.app.Instrumentation
import android.app.Notification
import android.app.NotificationManager
import android.app.UiAutomation
import android.content.Intent
import android.graphics.Rect
import android.os.Build
import android.os.ParcelFileDescriptor
import android.os.SystemClock
import android.util.Log
import android.view.View
import android.view.ViewGroup
import android.view.WindowInsets
import android.view.accessibility.AccessibilityNodeInfo
import android.view.inputmethod.InputMethodManager
import android.webkit.WebView
import androidx.test.core.app.ActivityScenario
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.remohop.app.core.remohopcore.Remohopcore
import org.json.JSONObject
import org.json.JSONTokener
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Assume.assumeTrue
import org.junit.Test
import org.junit.runner.RunWith
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import kotlin.math.roundToInt

/**
 * The Google Play demo of the FOREGROUND_SERVICE_SPECIAL_USE declaration: what
 * SessionService is for, played slowly enough to watch while CI records the
 * screen (request key FGS_VIDEO of sodesk-android.yml). Skipped unless the
 * instrumentation argument remohop_fgs_demo is "1".
 *
 * The flow: open the app -> connect to a real `remohop host` (password,
 * "Remember this computer" off) -> a command's output in the terminal ->
 * Home -> the notification shade with the ongoing "Connected to <host>"
 * notification -> its Disconnect action -> back in the app, the session has
 * ended (the connect screen, "The session with ... ended.").
 *
 * Instrumentation arguments:
 *   remohop_fgs_demo   "1" to run
 *   remohop_host       host address as the emulator sees it, e.g. 10.0.2.2:21120
 *   remohop_password   the host's password
 *   remohop_dir        optional: a directory on the host, listed in the terminal
 *   remohop_file       optional: a file name in remohop_dir, awaited in that listing
 *
 * Taps on the screen are real touches (`input tap`, so "show touches" can
 * draw them in the recording); the form fields are filled by script. Each
 * step is logged under [TAG] as "stage <name> uptime=<ms>"
 * (SystemClock.elapsedRealtime), which the workflow maps onto the recording's
 * timeline. As in the other tests, nothing here logs the gateway URL (it
 * carries the access token) or the password.
 */
@RunWith(AndroidJUnit4::class)
class FgsDemoTest {
    private val inst: Instrumentation = InstrumentationRegistry.getInstrumentation()
    private val args = InstrumentationRegistry.getArguments()

    private fun arg(name: String): String? = args.getString(name)?.trim()?.takeIf { it.isNotEmpty() }

    private val enabled get() = arg("remohop_fgs_demo") == "1"

    /** The app asks for the notification permission once a session exists (SessionService); a test has no one to answer. */
    @org.junit.Before
    fun grantNotifications() {
        if (Build.VERSION.SDK_INT >= 33) {
            inst.uiAutomation.grantRuntimePermission(inst.targetContext.packageName, android.Manifest.permission.POST_NOTIFICATIONS)
        }
    }

    @After
    fun cleanUp() {
        if (!enabled) return
        try { sh("cmd statusbar collapse") } catch (e: Exception) { /* best effort */ }
        Remohopcore.stopGateway()
        Lang.setChoice(inst.targetContext, Lang.SYSTEM)
    }

    // ---- WebView helpers (the same patterns as HostSessionTest) ----

    private fun findWebView(v: View): WebView? {
        if (v is WebView) return v
        if (v is ViewGroup) for (i in 0 until v.childCount) findWebView(v.getChildAt(i))?.let { return it }
        return null
    }

    private fun eval(s: ActivityScenario<MainActivity>, js: String): String? {
        var out: String? = null
        val done = CountDownLatch(1)
        try {
            s.onActivity { a ->
                val web = findWebView(a.window.decorView)
                if (web == null) done.countDown() else web.evaluateJavascript(js) { out = it; done.countDown() }
            }
        } catch (e: Exception) {
            return null
        }
        done.await(10, TimeUnit.SECONDS)
        return out
    }

    private fun text(s: ActivityScenario<MainActivity>, js: String): String {
        val raw = eval(s, js) ?: return ""
        val v = try { JSONTokener(raw).nextValue() } catch (e: Exception) { raw }
        return if (v == null || v == JSONObject.NULL) "" else v.toString()
    }

    private fun ok(s: ActivityScenario<MainActivity>, js: String): Boolean = eval(s, js) == "true"

    private fun hasFocus(s: ActivityScenario<MainActivity>): Boolean {
        var f = false
        try { s.onActivity { a -> f = a.hasWindowFocus() } } catch (e: Exception) { /* between states */ }
        return f
    }

    /** Brings MainActivity back if the emulator's launcher or a system dialog took the foreground. */
    private fun foreground(s: ActivityScenario<MainActivity>, label: String) {
        if (hasFocus(s)) return
        Log.w(TAG, "$label: MainActivity was not focused; bringing it to the front")
        val end = System.currentTimeMillis() + 30_000
        var tries = 0
        while (System.currentTimeMillis() < end) {
            if (tries >= 6 && tries % 6 == 0) launchApp()
            tries++
            Thread.sleep(500)
            if (hasFocus(s)) return
        }
        fail("$label: MainActivity could not be brought back to the foreground")
    }

    private fun poll(s: ActivityScenario<MainActivity>, js: String, label: String, seconds: Int): Boolean {
        var refocused = false
        var end = System.currentTimeMillis() + seconds * 1000L
        var n = 0
        while (System.currentTimeMillis() < end) {
            if (ok(s, js)) return true
            if (++n % 8 == 0 && !hasFocus(s)) {
                foreground(s, label)
                if (!refocused) { refocused = true; end = System.currentTimeMillis() + seconds * 1000L }
            }
            Thread.sleep(250)
        }
        return false
    }

    private fun waitFor(s: ActivityScenario<MainActivity>, js: String, label: String, seconds: Int = 30, diagJs: String? = null) {
        if (poll(s, js, label, seconds)) return
        val d = if (diagJs == null) "" else text(s, diagJs).take(200)
        fail("timed out: $label" + (if (d.isNotEmpty()) " [page says: $d]" else ""))
    }

    /** Types a line into the terminal the way the keyboard does (see HostSessionTest.typeAndWait). */
    private fun type(s: ActivityScenario<MainActivity>, line: String): Boolean = ok(
        s,
        "(function(){var i=document.querySelector('#t-host textarea');if(!i)return false;i.focus();" +
            "i.value=${JSONObject.quote(line)};" +
            "i.dispatchEvent(new InputEvent('input',{inputType:'insertText',data:i.value,bubbles:true}));" +
            "i.value='\\n';" +
            "i.dispatchEvent(new InputEvent('input',{inputType:'insertLineBreak',bubbles:true}));return true})()"
    )

    private fun imeVisible(a: Activity): Boolean {
        if (Build.VERSION.SDK_INT < 30) return false
        return a.window.decorView.rootWindowInsets?.isVisible(WindowInsets.Type.ime()) == true
    }

    /** No soft keyboard over the page: blur the page's focus and ask the IME to hide, then wait for it. */
    private fun hideKeyboard(s: ActivityScenario<MainActivity>) {
        eval(s, "(function(){var e=document.activeElement;if(e&&e.blur)e.blur();return true})()")
        val end = System.currentTimeMillis() + 8_000
        while (System.currentTimeMillis() < end) {
            var shown = false
            try {
                s.onActivity { a ->
                    shown = imeVisible(a)
                    if (shown) a.getSystemService(InputMethodManager::class.java)
                        ?.hideSoftInputFromWindow(a.window.decorView.windowToken, 0)
                }
            } catch (e: Exception) { /* between states */ }
            if (!shown) return
            Thread.sleep(300)
        }
        Log.w(TAG, "the soft keyboard did not hide in 8 s")
    }

    // ---- device helpers ----

    private val ua: UiAutomation get() = inst.uiAutomation

    /** A shell command as the shell user (input, cmd statusbar); waits for it to finish. */
    private fun sh(cmd: String): String {
        val pfd = ua.executeShellCommand(cmd)
        return ParcelFileDescriptor.AutoCloseInputStream(pfd).use { it.readBytes().toString(Charsets.UTF_8) }
    }

    private fun tap(x: Int, y: Int) {
        sh("input tap $x $y")
    }

    /**
     * A real touch on a page element (its centre, scrolled into view), so the
     * recording shows a tap; false if the element is missing or not laid out.
     */
    private fun tapElement(s: ActivityScenario<MainActivity>, id: String): Boolean {
        val q = JSONObject.quote(id)
        eval(s, "(function(){var e=document.getElementById($q);if(e)e.scrollIntoView({block:'center'});return true})()")
        Thread.sleep(300)
        val r = text(
            s,
            "(function(){var e=document.getElementById($q);if(!e)return '';var r=e.getBoundingClientRect();" +
                "if(r.width<1||r.height<1)return '';return [r.left+r.width/2,r.top+r.height/2,devicePixelRatio].join(',')})()"
        ).split(',').mapNotNull { it.toDoubleOrNull() }
        if (r.size != 3) return false
        val loc = IntArray(2)
        s.onActivity { a -> findWebView(a.window.decorView)?.getLocationOnScreen(loc) }
        tap(loc[0] + (r[0] * r[2]).roundToInt(), loc[1] + (r[1] * r[2]).roundToInt())
        return true
    }

    private fun launchApp() {
        inst.targetContext.startActivity(
            Intent(inst.targetContext, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        )
    }

    private fun stage(name: String) {
        Log.i(TAG, "stage $name uptime=${SystemClock.elapsedRealtime()}")
    }

    /** The text of this app's own ongoing notification (SessionService's), or "" if none is posted. */
    private fun ownNotificationText(): String {
        val nm = inst.targetContext.getSystemService(NotificationManager::class.java) ?: return ""
        return nm.activeNotifications
            .firstOrNull { (it.notification.flags and Notification.FLAG_ONGOING_EVENT) != 0 }
            ?.notification?.extras?.getCharSequence(Notification.EXTRA_TEXT)?.toString() ?: ""
    }

    // ---- the notification shade, through accessibility (what UiAutomator does) ----

    /** Lets [roots] see every window (the shade, not only the app's). */
    private fun seeAllWindows() {
        val info = ua.serviceInfo ?: return
        info.flags = info.flags or AccessibilityServiceInfo.FLAG_RETRIEVE_INTERACTIVE_WINDOWS
        ua.serviceInfo = info
    }

    private fun roots(): List<AccessibilityNodeInfo> {
        val out = mutableListOf<AccessibilityNodeInfo>()
        try { for (w in ua.windows) w.root?.let { out += it } } catch (e: Exception) { /* windows changing */ }
        if (out.isEmpty()) ua.rootInActiveWindow?.let { out += it }
        return out
    }

    private fun walk(n: AccessibilityNodeInfo?, out: MutableList<AccessibilityNodeInfo>) {
        if (n == null) return
        out += n
        for (i in 0 until n.childCount) walk(n.getChild(i), out)
    }

    private fun allNodes(): List<AccessibilityNodeInfo> {
        val out = mutableListOf<AccessibilityNodeInfo>()
        for (r in roots()) walk(r, out)
        return out
    }

    private fun label(n: AccessibilityNodeInfo): String = (n.text ?: n.contentDescription ?: "").toString().trim()

    /** The notification's "Connected to <host>" line (English or Chinese). */
    private fun connectedLine(host: String): AccessibilityNodeInfo? = allNodes().firstOrNull { n ->
        val t = label(n)
        n.isVisibleToUser && (t.startsWith("Connected to") || t.startsWith("已连接到")) && t.contains(host)
    }

    /** The notification's Disconnect action button (English or Chinese, any case). */
    private fun disconnectButton(): AccessibilityNodeInfo? = allNodes().firstOrNull { n ->
        val t = label(n)
        n.isVisibleToUser && (t.equals("Disconnect", ignoreCase = true) || t == "断开连接")
    }

    private fun centre(n: AccessibilityNodeInfo): Pair<Int, Int> {
        val r = Rect()
        n.getBoundsInScreen(r)
        return r.centerX() to r.centerY()
    }

    /** What the shade shows, for the log when a step there fails (notification texts only). */
    private fun shadeTexts(): String =
        allNodes().filter { it.isVisibleToUser }.map { label(it) }.filter { it.isNotEmpty() }.distinct().take(40).joinToString(" | ").take(800)

    private fun waitNode(seconds: Int, find: () -> AccessibilityNodeInfo?): AccessibilityNodeInfo? {
        val end = System.currentTimeMillis() + seconds * 1000L
        while (System.currentTimeMillis() < end) {
            find()?.let { return it }
            Thread.sleep(250)
        }
        return null
    }

    /** A low-importance notification may show collapsed (no actions): expand it like a user would. */
    private fun expandNotification(line: AccessibilityNodeInfo) {
        // 1. The row's expand button (the small chevron).
        val expand = allNodes().firstOrNull { it.isVisibleToUser && it.viewIdResourceName == "android:id/expand_button" }
        if (expand != null) {
            val (x, y) = centre(expand)
            Log.i(TAG, "shade: tapping the notification's expand button")
            tap(x, y)
            if (waitNode(3) { disconnectButton() } != null) return
        }
        // 2. The row's own expand action.
        var p: AccessibilityNodeInfo? = line
        while (p != null) {
            if (p.actionList.any { it.id == AccessibilityNodeInfo.ACTION_EXPAND }) {
                Log.i(TAG, "shade: expanding the notification row (accessibility action)")
                p.performAction(AccessibilityNodeInfo.ACTION_EXPAND)
                if (waitNode(3) { disconnectButton() } != null) return
                break
            }
            p = p.parent
        }
        // 3. A downward drag on the notification.
        val (x, y) = centre(line)
        Log.i(TAG, "shade: dragging the notification down to expand it")
        sh("input swipe $x $y $x ${y + 400} 400")
    }

    // ---- the demo ----

    @Test
    fun foregroundServiceDemo() {
        assumeTrue("remohop_fgs_demo is not 1: FGS demo skipped", enabled)
        val missing = "remohop_host and remohop_password are required with remohop_fgs_demo=1"
        val host = arg("remohop_host") ?: throw AssertionError(missing)
        val password = arg("remohop_password") ?: throw AssertionError(missing)
        val dir = arg("remohop_dir")
        val file = arg("remohop_file")
        val hostName = host.substringBeforeLast(':')

        Lang.setChoice(inst.targetContext, Lang.EN)
        assertEquals("the app's effective language", Lang.EN, Lang.effective(inst.targetContext))
        Remohopcore.stopGateway() // a fresh page session
        seeAllWindows()

        // The recording starts on the home screen.
        ua.performGlobalAction(AccessibilityService.GLOBAL_ACTION_HOME)
        Thread.sleep(1500)
        stage("home-start")

        ActivityScenario.launch(MainActivity::class.java).use { s ->
            waitFor(s, "!!document.getElementById('connect') && !document.getElementById('connect').hidden", "connect form loads", 60)
            waitFor(s, "typeof window.remohopMobile==='object'", "injected script present", 30)
            waitFor(s, "document.documentElement.lang.indexOf('en')===0", "the page shows English", 20, "document.documentElement.lang")
            foreground(s, "connect form")
            stage("app-open")
            Thread.sleep(1800)

            // The address, typed out; the password in one go (it shows as dots).
            for (i in 1..host.length) {
                eval(s, "(function(){var t=document.getElementById('target');t.value=${JSONObject.quote(host.substring(0, i))};" +
                    "t.dispatchEvent(new Event('input',{bubbles:true}));return true})()")
                Thread.sleep(70)
            }
            eval(s, "(function(){var t=document.getElementById('target');t.dispatchEvent(new Event('change',{bubbles:true}));" +
                "var p=document.getElementById('hostpass');p.value=${JSONObject.quote(password)};" +
                "p.dispatchEvent(new Event('input',{bubbles:true}));return true})()")
            Thread.sleep(800)
            // "Remember this computer" off (it is on by default): a tap on the switch.
            if (ok(s, "document.getElementById('pair').checked")) {
                tapElement(s, "pair")
                if (!poll(s, "!document.getElementById('pair').checked", "remember off", 3)) {
                    eval(s, "(function(){document.getElementById('pair').checked=false;return true})()")
                }
            }
            assertTrue("Remember this computer is off", ok(s, "!document.getElementById('pair').checked"))
            hideKeyboard(s)
            Thread.sleep(1000)
            stage("form-filled")

            if (!tapElement(s, "connectbtn")) eval(s, "(function(){document.getElementById('connectbtn').click();return true})()")
            if (!poll(s, "!document.getElementById('workspace').hidden||document.getElementById('connectbtn').disabled||document.getElementById('connerr').textContent!==''", "connecting", 4)) {
                Log.w(TAG, "the tap on Connect did not take; clicking it by script")
                eval(s, "(function(){document.getElementById('connectbtn').click();return true})()")
            }
            waitFor(s, "!document.getElementById('workspace').hidden", "connected to the host", 90, "document.getElementById('connerr').textContent")
            // The first connection shows a one-time notice (the host's key fingerprint): not part of this demo.
            eval(s, "(function(){var b=document.getElementById('banner');b.hidden=true;return true})()")
            stage("connected")

            // The session at work: the terminal with a command's output.
            if (!ok(s, "document.getElementById('tab-terminal').getAttribute('aria-selected')==='true'")) {
                tapElement(s, "tab-terminal")
                if (!poll(s, "document.getElementById('tab-terminal').getAttribute('aria-selected')==='true'", "terminal tab", 5)) {
                    eval(s, "(function(){document.getElementById('tab-terminal').click();return true})()")
                }
            }
            waitFor(s, "document.getElementById('tab-terminal').getAttribute('aria-selected')==='true'", "terminal tab selected", 10)
            waitFor(s, "!!document.querySelector('#t-host textarea')", "terminal opens", 20)
            poll(s, "document.getElementById('t-host').textContent.trim()!==''", "terminal prompt", 15)
            Thread.sleep(1000)
            val q = dir?.let { if (Regex("^[A-Za-z0-9._/~-]+$").matches(it)) it else "'" + it.replace("'", "'\\''") + "'" }
            val command = if (q != null) "cd $q && ls -l" else "echo \"Hello from RemoHop\" && uname -sr"
            val expected = if (q != null) (file ?: "total") else "Hello from RemoHop"
            var ran = false
            for (i in 0 until 3) {
                if (!type(s, command)) { Thread.sleep(1000); continue }
                ran = poll(s, "document.getElementById('t-host').textContent.indexOf(${JSONObject.quote(expected)})>=0", "terminal output", 10)
                if (ran) break
            }
            assertTrue("the terminal shows the host's output", ran)
            hideKeyboard(s)
            stage("terminal-output")

            // The app starts SessionService once a session exists; wait for its notification to name the host.
            val end = System.currentTimeMillis() + 30_000
            while (System.currentTimeMillis() < end && !(SessionService.running && ownNotificationText().contains(hostName))) Thread.sleep(250)
            assertTrue("SessionService runs while connected", SessionService.running)
            val nText = ownNotificationText()
            Log.i(TAG, "notification text: '$nText'")
            assertTrue("the ongoing notification names the host: '$nText'", nText.startsWith("Connected to") && nText.contains(hostName))
            Thread.sleep(3500) // a moment to read the output

            // Home: the app leaves the screen, the session stays (the status bar shows the notification icon).
            stage("press-home")
            ua.performGlobalAction(AccessibilityService.GLOBAL_ACTION_HOME)
            Thread.sleep(3500)
            assertTrue("SessionService keeps running in the background", SessionService.running)

            // The notification shade.
            stage("open-shade")
            ua.performGlobalAction(AccessibilityService.GLOBAL_ACTION_NOTIFICATIONS)
            val line = waitNode(10) { connectedLine(hostName) }
            if (line == null) {
                Log.w(TAG, "shade shows: ${shadeTexts()}")
                fail("the notification shade shows no \"Connected to $hostName\" notification")
            }
            Thread.sleep(1500)
            if (disconnectButton() == null) expandNotification(line!!)
            var button = waitNode(5) { disconnectButton() }
            if (button == null) {
                Log.w(TAG, "shade shows: ${shadeTexts()}")
                fail("the notification has no visible Disconnect action")
            }
            stage("notification-visible")
            Thread.sleep(4500) // the notification and its Disconnect action, clearly on screen

            // Disconnect from the notification.
            button = disconnectButton() ?: button!!
            val (bx, by) = centre(button)
            stage("tap-disconnect")
            tap(bx, by)
            val stopEnd = System.currentTimeMillis() + 10_000
            while (System.currentTimeMillis() < stopEnd && SessionService.running) Thread.sleep(200)
            if (SessionService.running) {
                Log.w(TAG, "the tap on Disconnect did not stop the service; clicking it through accessibility")
                disconnectButton()?.performAction(AccessibilityNodeInfo.ACTION_CLICK)
                val e2 = System.currentTimeMillis() + 10_000
                while (System.currentTimeMillis() < e2 && SessionService.running) Thread.sleep(200)
            }
            assertTrue("Disconnect stopped SessionService", !SessionService.running)
            assertTrue("Disconnect ended the host sessions", SessionWatch.sessions().isEmpty())
            Thread.sleep(2500) // the notification is gone from the shade
            stage("notification-gone")

            // Back to the app: the session has ended.
            sh("cmd statusbar collapse")
            Thread.sleep(1500)
            stage("open-app")
            launchApp()
            foreground(s, "back in the app")
            waitFor(
                s, "!document.getElementById('connect').hidden && document.getElementById('workspace').hidden",
                "back in the app, the session has ended (the connect screen)", 30
            )
            val banner = text(s, "(function(){var b=document.getElementById('banner');return b.hidden?'':b.textContent})()")
            Log.i(TAG, "back in the app: connect screen; banner '${banner.take(80)}'")
            stage("app-disconnected")
            Thread.sleep(5000)
            stage("end")
        }
    }

    companion object {
        const val TAG = "RemoHopFgs"
    }
}
