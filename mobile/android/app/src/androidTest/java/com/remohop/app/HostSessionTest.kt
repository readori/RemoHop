package com.remohop.app

import android.app.Activity
import android.app.Instrumentation
import android.content.ClipData
import android.content.ClipboardManager
import android.content.Intent
import android.content.IntentFilter
import android.content.pm.ActivityInfo
import android.net.Uri
import android.util.Log
import android.view.View
import android.view.ViewGroup
import android.webkit.WebView
import androidx.test.core.app.ActivityScenario
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.remohop.app.core.remohopcore.Remohopcore
import org.json.JSONObject
import org.json.JSONTokener
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.After
import org.junit.Assume.assumeTrue
import org.junit.Test
import org.junit.runner.RunWith
import java.io.File
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit

/**
 * The app's real job, end to end, on an emulator: inside the real
 * MainActivity (real WebView, in-process gateway, Go core) it connects to a
 * real `remohop host` running on the CI runner, the way a user does through the
 * connect form, and uses the host.
 *
 * Instrumentation arguments (never hard-coded; see sodesk-android.yml):
 *   remohop_host      host address as the emulator sees it, e.g. 10.0.2.2:21118
 *   remohop_password  the host's password
 *   remohop_dir       a directory on the host holding remohop_file
 *   remohop_file      optional, default [DEFAULT_FILE]; its content must be [FILE_TEXT]
 *   remohop_background_seconds  optional, default 10
 *   remohop_require   "true": missing arguments fail instead of skipping
 *
 * Nothing here may print the gateway URL (it carries the access token) or
 * the password: failure messages carry labels and page status text only.
 * Progress goes to logcat under the tag [TAG].
 */
@RunWith(AndroidJUnit4::class)
class HostSessionTest {
    private val inst: Instrumentation = InstrumentationRegistry.getInstrumentation()

    /** The app asks for the notification permission once a session exists (SessionService); a test has no one to answer. */
    @org.junit.Before
    fun grantNotifications() {
        if (android.os.Build.VERSION.SDK_INT >= 33) {
            inst.uiAutomation.grantRuntimePermission(inst.targetContext.packageName, android.Manifest.permission.POST_NOTIFICATIONS)
        }
    }
    private val args = InstrumentationRegistry.getArguments()

    private class Config(val host: String, val password: String, val dir: String, val file: String, val backgroundSeconds: Int)

    private fun arg(name: String): String? = args.getString(name)?.trim()?.takeIf { it.isNotEmpty() }

    private fun config(): Config? {
        val host = arg("remohop_host")
        val password = arg("remohop_password")
        val dir = arg("remohop_dir")
        if (host == null || password == null || dir == null) {
            if (arg("remohop_require") == "true") fail("remohop_host, remohop_password and remohop_dir instrumentation arguments are required")
            assumeTrue("no remohop_host/remohop_password/remohop_dir arguments: host test skipped", false)
            return null
        }
        return Config(host, password, dir, arg("remohop_file") ?: DEFAULT_FILE, arg("remohop_background_seconds")?.toIntOrNull() ?: 10)
    }

    // ---- WebView helpers ----

    private fun findWebView(v: View): WebView? {
        if (v is WebView) return v
        if (v is ViewGroup) for (i in 0 until v.childCount) findWebView(v.getChildAt(i))?.let { return it }
        return null
    }

    /** Runs JS in the page; the raw evaluateJavascript result (JSON text), or null if it did not answer. */
    private fun eval(s: ActivityScenario<MainActivity>, js: String): String? {
        var out: String? = null
        val done = CountDownLatch(1)
        try {
            s.onActivity { a ->
                val web = findWebView(a.window.decorView)
                if (web == null) done.countDown() else web.evaluateJavascript(js) { out = it; done.countDown() }
            }
        } catch (e: Exception) {
            return null // the activity is between states (recreating)
        }
        done.await(10, TimeUnit.SECONDS)
        return out
    }

    /** A string result of [eval] without the JSON quoting. */
    private fun text(s: ActivityScenario<MainActivity>, js: String): String {
        val raw = eval(s, js) ?: return ""
        val v = try { JSONTokener(raw).nextValue() } catch (e: Exception) { raw }
        return if (v == null || v == JSONObject.NULL) "" else v.toString()
    }

    private fun ok(s: ActivityScenario<MainActivity>, js: String): Boolean = eval(s, js) == "true"

    private fun fails(label: String, s: ActivityScenario<MainActivity>, diagJs: String?): Nothing {
        val d = if (diagJs == null) "" else text(s, diagJs).take(200)
        fail(label + (if (d.isNotEmpty()) " [page says: $d]" else ""))
        throw IllegalStateException() // unreachable, fail() throws
    }

    private fun waitFor(s: ActivityScenario<MainActivity>, js: String, label: String, seconds: Int = 30, diagJs: String? = null) {
        if (pollFocused(s, js, label, seconds)) return
        fails("timed out: $label", s, diagJs)
    }

    /**
     * Polls js until it is true. A WebView in the background runs no script,
     * and the emulator's launcher sometimes takes the foreground mid-test, so
     * whenever the app has lost focus it is brought back (see foreground) and
     * the wait starts over once, rather than timing out on an idle page.
     */
    private fun pollFocused(s: ActivityScenario<MainActivity>, js: String, label: String, seconds: Int): Boolean {
        var refocused = false
        var end = System.currentTimeMillis() + seconds * 1000L
        var n = 0
        while (System.currentTimeMillis() < end) {
            if (ok(s, js)) return true
            if (++n % 8 == 0 && !hasFocus(s)) { // every ~2 s
                foreground(s, label)
                if (!refocused) { refocused = true; end = System.currentTimeMillis() + seconds * 1000L }
            }
            Thread.sleep(250)
        }
        return false
    }

    private fun tryWait(s: ActivityScenario<MainActivity>, js: String, seconds: Int): Boolean =
        pollFocused(s, js, "wait", seconds)

    private fun click(s: ActivityScenario<MainActivity>, id: String) {
        val js = "(function(){var e=document.getElementById(${JSONObject.quote(id)});if(!e)return 'missing';e.click();return 'clicked'})()"
        var last: String? = null
        repeat(20) {
            last = eval(s, js)
            if (last == "\"clicked\"") return
            Thread.sleep(500)
        }
        fail("could not click #$id (page answered: $last)")
    }

    private fun openTab(s: ActivityScenario<MainActivity>, name: String) {
        val selected = "document.getElementById('tab-$name').getAttribute('aria-selected')==='true'"
        // The emulator's launcher sometimes takes the foreground; a WebView
        // in the background runs no script, so the click never lands.
        // Bring the app back and click again before giving up.
        repeat(2) {
            foreground(s, "$name tab")
            click(s, "tab-$name")
            if (tryWait(s, selected, 10)) return
        }
        waitFor(s, selected, "$name tab selected", 10)
    }

    private fun stage(name: String) {
        Log.i(TAG, "stage ok: $name")
    }

    /** True if MainActivity currently has window focus (it is the foreground, resumed app). */
    private fun hasFocus(s: ActivityScenario<MainActivity>): Boolean {
        var f = false
        try { s.onActivity { a -> f = a.hasWindowFocus() } } catch (e: Exception) { /* between states */ }
        return f
    }

    /**
     * Page-dependent steps need the app in front (WebView timers, clipboard
     * access and focus all depend on it). If something else took the
     * foreground (the launcher, a system dialog), bring MainActivity back with
     * an intent and say so in logcat: it tells a test-environment hiccup from
     * the app going away.
     */
    private fun foreground(s: ActivityScenario<MainActivity>, label: String) {
        if (hasFocus(s)) return
        Log.w(TAG, "$label: MainActivity was not focused; bringing it to the front")
        val end = System.currentTimeMillis() + 30_000
        var tries = 0
        while (System.currentTimeMillis() < end) {
            if (tries >= 6 && tries % 6 == 0) { // first 3 s: just wait for focus to arrive
                inst.targetContext.startActivity(
                    Intent(inst.targetContext, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
                )
            }
            tries++
            Thread.sleep(500)
            if (hasFocus(s)) { Log.w(TAG, "$label: focus back after ${tries * 500} ms"); return }
        }
        fail("$label: MainActivity could not be brought back to the foreground")
    }

    // ---- steps ----

    /** The connect form as a user fills it. */
    private fun connect(s: ActivityScenario<MainActivity>, target: String, password: String, pair: Boolean) {
        assertTrue("the connect form is visible", ok(s, "!document.getElementById('connect').hidden"))
        assertTrue(
            "filled the connect form",
            ok(
                s,
                "(function(){document.getElementById('target').value=${JSONObject.quote(target)};" +
                    "document.getElementById('hostpass').value=${JSONObject.quote(password)};" +
                    "document.getElementById('pair').checked=$pair;" +
                    "document.getElementById('connectbtn').click();return true})()"
            )
        )
        waitFor(
            s, "!document.getElementById('workspace').hidden", "connected to the host (workspace shown)", 90,
            "document.getElementById('connerr').textContent"
        )
    }

    private fun disconnect(s: ActivityScenario<MainActivity>) {
        click(s, "disconnect")
        waitFor(s, "!document.getElementById('connect').hidden", "disconnect returns to the connect form", 15)
    }

    /** One try: type the command into the terminal like keyboard input and wait for its output. */
    private fun typeAndWait(s: ActivityScenario<MainActivity>, command: String, expected: String, seconds: Int): Boolean {
        val typed = ok(
            s,
            "(function(){var i=document.querySelector('#t-host textarea');if(!i)return false;i.focus();" +
                "i.value=${JSONObject.quote(command)};" +
                "i.dispatchEvent(new InputEvent('input',{inputType:'insertText',data:i.value,bubbles:true}));" +
                "i.value='\\n';" +
                "i.dispatchEvent(new InputEvent('input',{inputType:'insertLineBreak',bubbles:true}));return true})()"
        )
        if (!typed) return false
        return tryWait(s, "document.getElementById('t-host').textContent.indexOf(${JSONObject.quote(expected)})>=0", seconds)
    }

    /**
     * Opens the terminal tab (from another tab, so its socket is new) and runs
     * `echo remohop-$((a*b))`. The command text on screen holds "$((a*b))",
     * only the shell's output holds the product, so a match is the host's answer.
     */
    private fun terminalCommand(s: ActivityScenario<MainActivity>, a: Int, b: Int, label: String) {
        foreground(s, label)
        openTab(s, "files")
        openTab(s, "terminal")
        waitFor(s, "!!document.querySelector('#t-host textarea')", "$label: terminal opens", 20)
        Thread.sleep(1500) // the socket attaches to the host's shell session
        val command = "echo remohop-\$(($a*$b))"
        val expected = "remohop-" + (a * b)
        repeat(3) { if (typeAndWait(s, command, expected, 10)) return }
        fails(
            "timed out: $label: terminal shows the host's command output", s,
            "document.getElementById('t-status').textContent+' '+document.getElementById('t-err').textContent+" +
                "' | term '+document.getElementById('t-host').clientWidth+'x'+document.getElementById('t-host').clientHeight+" +
                "' panel '+(document.getElementById('panel-terminal').hidden?'hidden':'shown')+' tail: '+document.getElementById('t-host').textContent.trim().slice(-80)"
        )
    }

    private fun listDirectory(s: ActivityScenario<MainActivity>, cfg: Config, label: String) {
        foreground(s, label)
        openTab(s, "files")
        waitFor(s, "document.getElementById('f-path').value!==''", "$label: files tab loads", 30, "document.getElementById('f-err').textContent")
        assertTrue(
            "$label: path set",
            ok(
                s,
                "(function(){document.getElementById('f-path').value=${JSONObject.quote(cfg.dir)};" +
                    "document.getElementById('f-go').click();return true})()"
            )
        )
        waitFor(
            s, "document.getElementById('f-body').textContent.indexOf(${JSONObject.quote(cfg.file)})>=0",
            "$label: files tab lists the host's known file", 30, "document.getElementById('f-err').textContent"
        )
    }

    /** The app's download hand-over: page -> native bridge -> DownloadSaver -> the system save dialog (answered here). */
    private fun downloadFile(s: ActivityScenario<MainActivity>, cfg: Config) {
        val target = File(inst.targetContext.cacheDir, "e2e-saved.txt")
        target.delete()
        target.createNewFile()
        val filter = IntentFilter(Intent.ACTION_CREATE_DOCUMENT)
        filter.addCategory(Intent.CATEGORY_OPENABLE)
        filter.addDataType("*/*")
        val answer = Instrumentation.ActivityResult(Activity.RESULT_OK, Intent().setData(Uri.fromFile(target)))
        // Blocking monitor: the save dialog is not shown, MainActivity gets this result.
        val monitor = inst.addMonitor(filter, answer, true)
        try {
            val name = JSONObject.quote(cfg.file)
            assertTrue(
                "download button present",
                ok(
                    s,
                    "(function(){var b=[].slice.call(document.querySelectorAll('#f-body button')).filter(function(x){return (x.getAttribute('aria-label')||'').indexOf('Download '+$name)===0})[0];" +
                        "if(!b)return false;b.click();return true})()"
                )
            )
            val end = System.currentTimeMillis() + 60_000
            while (System.currentTimeMillis() < end && target.length() == 0L) Thread.sleep(250)
            assertTrue("the app asked the system to create a document (save dialog) for the download", monitor.hits > 0)
            // The copy into the chosen document may still be running.
            Thread.sleep(500)
            assertEquals("downloaded file content", FILE_TEXT, target.readText())
        } finally {
            inst.removeMonitor(monitor)
            target.delete()
        }
    }

    private fun clipboard(): ClipboardManager = inst.targetContext.getSystemService(ClipboardManager::class.java)

    private fun setDeviceClipboard(value: String) {
        inst.runOnMainSync { clipboard().setPrimaryClip(ClipData.newPlainText("test", value)) }
    }

    private fun deviceClipboard(): String {
        var out = ""
        inst.runOnMainSync {
            val c = clipboard().primaryClip
            out = if (c != null && c.itemCount > 0) c.getItemAt(0).coerceToText(inst.targetContext).toString() else ""
        }
        return out
    }

    /** device clipboard -> page (bridge read) -> host clipboard -> page -> device clipboard (bridge write). */
    private fun clipboardRoundTrip(s: ActivityScenario<MainActivity>) {
        foreground(s, "clipboard")
        val value = "remohop-clip-" + System.currentTimeMillis()
        // Android lets only the focused app (or the IME) read the clipboard: make sure the window has focus.
        var focused = false
        val focusEnd = System.currentTimeMillis() + 20_000
        while (System.currentTimeMillis() < focusEnd && !focused) {
            try { s.onActivity { a -> focused = a.hasWindowFocus() } } catch (e: Exception) { /* between states */ }
            if (!focused) Thread.sleep(500)
        }
        setDeviceClipboard(value)
        val readBack = deviceClipboard() == value
        Log.i(TAG, "clipboard: window focused=$focused, test can read back what it set=$readBack")
        openTab(s, "clipboard")
        click(s, "c-paste")
        waitFor(
            s, "document.getElementById('c-local').value===${JSONObject.quote(value)}",
            "Paste from this device reads the Android clipboard through the bridge (window focused=$focused, readable by test=$readBack)", 15,
            "document.getElementById('c-status').textContent+' local-length='+document.getElementById('c-local').value.length"
        )
        click(s, "c-send")
        waitFor(s, "document.getElementById('c-status').textContent.indexOf('Sent')===0", "Send to remote sets the host clipboard", 20, "document.getElementById('c-err').textContent")
        assertTrue("cleared the remote text box", ok(s, "(function(){document.getElementById('c-remote').value='';return true})()"))
        click(s, "c-fetch")
        waitFor(s, "document.getElementById('c-remote').value===${JSONObject.quote(value)}", "Fetch remote clipboard reads back what was set", 20, "document.getElementById('c-err').textContent")
        setDeviceClipboard("overwritten")
        click(s, "c-copy")
        val end = System.currentTimeMillis() + 15_000
        while (System.currentTimeMillis() < end && deviceClipboard() != value) Thread.sleep(250)
        assertEquals("Copy to this device writes the Android clipboard through the bridge", value, deviceClipboard())
    }

    /** The desktop tab as far as a headless host goes: key bar, then either a picture or a handled error. */
    private fun desktopTab(s: ActivityScenario<MainActivity>): String {
        foreground(s, "desktop")
        openTab(s, "desktop")
        waitFor(s, "!document.getElementById('sdm-bar').hidden", "the touch key bar shows on the desktop tab", 15)
        assertTrue(
            "the key bar has its keys",
            ok(s, "document.querySelectorAll('#sdm-bar button').length>=4 && [].slice.call(document.querySelectorAll('#sdm-bar button')).some(function(b){return b.textContent==='Ctrl'})")
        )
        // Settled: the host sent its hello (a display), or the page shows why not.
        val settled = "(window.remohopMobile&&remohopMobile._desk&&remohopMobile._desk.hello)?true:" +
            "(function(){var m=document.getElementById('d-msg').textContent;return m!==''&&m.indexOf('Connecting')!==0})()"
        waitFor(s, settled, "the desktop tab settles (picture or an error message)", 30, "document.getElementById('d-msg').textContent")
        val outcome = if (ok(s, "!!(window.remohopMobile&&remohopMobile._desk&&remohopMobile._desk.hello)")) "desktop streaming" else text(s, "document.getElementById('d-msg').textContent").take(120)
        assertTrue("the page still fits the screen width", ok(s, "document.documentElement.scrollWidth<=innerWidth+1"))
        fullscreen(s)
        return outcome
    }

    /** Full screen on the desktop tab can always be left: by the floating button, and by the system back key. */
    private fun fullscreen(s: ActivityScenario<MainActivity>) {
        val isFs = "!!(window.remohopMobile&&remohopMobile._fullscreen())"
        val restored = "!remohopMobile._fullscreen()&&document.getElementById('d-stage').style.position===''&&!document.getElementById('workspace').hidden"
        click(s, "d-fs")
        waitFor(s, isFs, "the toolbar button enters full screen", 10)
        assertTrue(
            "full screen shows the floating exit button",
            ok(s, "(function(){var b=document.getElementById('d-exitfs');return !!b&&getComputedStyle(b).display!=='none'&&b.getBoundingClientRect().width>=40})()")
        )
        click(s, "d-exitfs")
        waitFor(s, restored, "the floating button leaves full screen and restores the layout", 10)
        click(s, "d-fs")
        waitFor(s, isFs, "full screen again", 10)
        // The app's own back handling (what the back gesture and key reach,
        // through the OnBackInvokedCallback on 13+). Not a key event: on the
        // first full screen the system shows its "viewing full screen" hint,
        // which would take that key and the window focus.
        @Suppress("DEPRECATION")
        s.onActivity { it.onBackPressed() }
        waitFor(s, restored, "back leaves full screen and keeps the session on screen", 10)
    }

    private fun sessionUsable(s: ActivityScenario<MainActivity>, cfg: Config, a: Int, b: Int, label: String) {
        foreground(s, label)
        waitFor(s, "!document.getElementById('workspace').hidden", "$label: the host session is still shown", 40, "document.getElementById('connerr').textContent")
        assertTrue(
            "$label: the page did not report the session as ended",
            !text(s, "document.getElementById('banner').textContent").contains("ended")
        )
        listDirectory(s, cfg, label)
        terminalCommand(s, a, b, label)
    }

    /** Leave a fresh gateway (new port, token and page storage) for whatever test runs next in this process. */
    @After
    fun freshGateway() {
        Remohopcore.stopGateway()
    }

    @Test
    fun hostSessionEndToEnd() {
        val cfg = config() ?: return
        ActivityScenario.launch(MainActivity::class.java).use { s ->
            waitFor(s, "!!document.getElementById('connect') && !document.getElementById('connect').hidden", "connect form loads", 60)
            waitFor(s, "typeof window.remohopNative==='object' && typeof window.remohopMobile==='object'", "injected script and native bridge present", 30)
            stage("connect form")

            // 1. Pair: address + password + the pair box, then log in with the saved pairing alone.
            connect(s, cfg.host, cfg.password, true)
            assertTrue("the connection is paired", text(s, "document.getElementById('pathbadge').textContent").contains("paired"))
            stage("connected and paired")
            disconnect(s)
            connect(s, cfg.host, "", false)
            // (The host log shows auth=token for this login; the badge only says "paired" for the pairing login itself.)
            stage("reconnected with the saved pairing, no password")

            // 2. Terminal.
            terminalCommand(s, 6, 7, "first run")
            stage("terminal command output from the host")

            // 3. Files: list, download through the bridge.
            listDirectory(s, cfg, "first run")
            stage("files lists the host directory")
            downloadFile(s, cfg)
            stage("download hand-over saved the file")

            // 4. Clipboard through the bridge, both ways.
            clipboardRoundTrip(s)
            stage("clipboard round trip")

            // 5. Desktop tab: key bar and a handled outcome.
            val outcome = desktopTab(s)
            Log.i(TAG, "desktop outcome: $outcome")
            stage("desktop tab with key bar")

            // 6. Rotation: the activity handles it (configChanges) and the session stays.
            var before: Activity? = null
            s.onActivity { a -> before = a; a.requestedOrientation = ActivityInfo.SCREEN_ORIENTATION_LANDSCAPE }
            waitFor(s, "innerWidth>innerHeight", "the page is landscape after rotating", 20)
            var same = false
            s.onActivity { a -> same = a === before }
            Log.i(TAG, "rotation kept the same activity instance: $same")
            sessionUsable(s, cfg, 3, 11, "landscape")
            s.onActivity { a -> a.requestedOrientation = ActivityInfo.SCREEN_ORIENTATION_PORTRAIT }
            waitFor(s, "innerHeight>innerWidth", "the page is portrait after rotating back", 20)
            sessionUsable(s, cfg, 4, 13, "portrait again")
            stage("rotation (same activity: $same)")

            // 7. Recreation: a new Activity and WebView on the same in-process gateway.
            s.recreate()
            Log.i(TAG, "after recreate: focused=${hasFocus(s)}")
            waitFor(s, "!!document.getElementById('connect')", "the page reloads after recreate", 60)
            sessionUsable(s, cfg, 5, 9, "after recreate")
            assertEquals("one host connection after recreate", "1", text(s, "String(document.getElementById('hostsel').options.length)"))
            assertTrue("gateway still running", Remohopcore.gatewayRunning())
            stage("recreate: session resumed")

            // 8. Background briefly: the terminal tab stays selected while the activity is stopped.
            openTab(s, "terminal")
            // A real trip to the home screen and back (no Lifecycle types needed).
            inst.targetContext.startActivity(
                Intent(Intent.ACTION_MAIN).addCategory(Intent.CATEGORY_HOME).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
            )
            Thread.sleep(cfg.backgroundSeconds * 1000L)
            assertTrue("the session service keeps the sessions in the background", SessionService.running)
            // What iOS does to a suspended app (and what broke the iOS app's
            // "Connect" after a trip to another app): the gateway's listening
            // socket goes away under it, unnoticed by the server. On return,
            // onResume's EnsureGateway must find and reopen it on the same port.
            Remohopcore.breakGatewayListenerForTest(true)
            inst.targetContext.startActivity(
                Intent(inst.targetContext, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
            )
            waitFor(s, "document.visibilityState==='visible'", "the page is visible again after returning from the home screen", 30)
            assertTrue("gateway still running after background", Remohopcore.gatewayRunning())
            waitFor(s, "!!document.getElementById('tab-terminal')", "page answers after background", 30)
            // What a user does on return: type into the terminal that was open.
            val direct = typeAndWait(s, "echo remohop-\$((7*8))", "remohop-56", 10)
            Log.i(TAG, "terminal socket survived ${cfg.backgroundSeconds}s in background: $direct")
            sessionUsable(s, cfg, 8, 8, "after background")
            stage("background ${cfg.backgroundSeconds}s: session usable (open terminal worked directly: $direct)")

            // 9. The owner's sequence: back from another app, then Connect.
            disconnect(s)
            connect(s, cfg.host, "", false)
            sessionUsable(s, cfg, 9, 9, "connect after a lost listener")
            stage("connect after the listener was lost in the background")

            // 10. The connection drops under the session (iOS suspension, a network
            // change): the gateway re-establishes it and the page re-opens its tab,
            // with no action from the user.
            openTab(s, "terminal")
            Remohopcore.dropSessionsForTest()
            sessionUsable(s, cfg, 10, 10, "after a dropped connection")
            assertEquals("one host connection after reconnecting", "1", text(s, "String(document.getElementById('hostsel').options.length)"))
            stage("dropped connection re-established without the user")
        }
    }

    companion object {
        const val TAG = "RemoHopE2E"
        const val DEFAULT_FILE = "remohop-e2e.txt"

        /** What the CI workflow writes to DEFAULT_FILE on the host. */
        const val FILE_TEXT = "remohop android e2e payload 7f3a\n"
    }
}
