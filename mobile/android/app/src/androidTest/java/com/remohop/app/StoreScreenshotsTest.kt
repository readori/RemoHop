package com.remohop.app

import android.app.Activity
import android.app.Instrumentation
import android.content.Intent
import android.graphics.Bitmap
import android.os.Build
import android.util.Log
import android.view.View
import android.view.ViewGroup
import android.view.WindowInsets
import android.view.inputmethod.InputMethodManager
import android.webkit.WebView
import android.widget.TextView
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
import java.io.File
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit

/**
 * Google Play phone screenshots of the real app, connected to a real
 * `remohop host` (see the SCREENSHOTS request key of sodesk-android.yml).
 * Skipped unless the instrumentation argument remohop_shots is "1".
 *
 * Instrumentation arguments:
 *   remohop_shots       "1" to run
 *   remohop_shots_lang  "en" (default) or "zh": the app's language, set through
 *                       the app's own setting (Lang.setChoice), so the native
 *                       screens and the web console both show it
 *   remohop_host        host address as the emulator sees it, e.g. 10.0.2.2:21119
 *   remohop_password    the host's password
 *   remohop_dir         a directory on the host: listed in the terminal and the Files tab
 *   remohop_file        optional: a file name in remohop_dir, awaited in both listings
 *
 * Output: PNG files (opaque, no alpha) of the whole screen, status bar
 * included, in getExternalFilesDir(null)/store-shots/<lang>/:
 * 1-connect, 2-terminal, 3-files, 4-desktop (only when the host streams a
 * picture), 5-settings, plus shots.txt (one "name WxH" line per picture).
 *
 * As in HostSessionTest, nothing here logs the gateway URL (it carries the
 * access token) or the password. Progress goes to logcat under [TAG].
 */
@RunWith(AndroidJUnit4::class)
class StoreScreenshotsTest {
    private val inst: Instrumentation = InstrumentationRegistry.getInstrumentation()

    /** The app asks for the notification permission once a session exists (SessionService); a test has no one to answer. */
    @org.junit.Before
    fun grantNotifications() {
        if (android.os.Build.VERSION.SDK_INT >= 33) {
            inst.uiAutomation.grantRuntimePermission(inst.targetContext.packageName, android.Manifest.permission.POST_NOTIFICATIONS)
        }
    }
    private val args = InstrumentationRegistry.getArguments()

    private fun arg(name: String): String? = args.getString(name)?.trim()?.takeIf { it.isNotEmpty() }

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
            if (tries >= 6 && tries % 6 == 0) {
                inst.targetContext.startActivity(
                    Intent(inst.targetContext, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
                )
            }
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
        repeat(2) {
            foreground(s, "$name tab")
            click(s, "tab-$name")
            if (poll(s, selected, "$name tab", 10)) return
        }
        waitFor(s, selected, "$name tab selected", 10)
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

    // ---- screen state ----

    private fun imeVisible(a: Activity): Boolean {
        if (Build.VERSION.SDK_INT < 30) return false
        return a.window.decorView.rootWindowInsets?.isVisible(WindowInsets.Type.ime()) == true
    }

    /** No soft keyboard in the picture: blur the page's focus and ask the IME to hide, then wait for it. */
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

    // ---- capture ----

    private lateinit var outDir: File
    private val made = mutableListOf<String>()

    private fun grab(): Bitmap {
        inst.waitForIdleSync()
        val raw = inst.uiAutomation.takeScreenshot() ?: throw AssertionError("UiAutomation.takeScreenshot returned nothing")
        // A software copy, marked opaque: the PNG encoder then writes RGB without an alpha channel.
        val bmp = raw.copy(Bitmap.Config.ARGB_8888, true)
        raw.recycle()
        bmp.setHasAlpha(false)
        return bmp
    }

    /** How many distinct colours a coarse grid over [l,t,r,b) of the bitmap has (a blank frame has one or two). */
    private fun colours(bmp: Bitmap, l: Int = 0, t: Int = 0, r: Int = bmp.width, b: Int = bmp.height): Int {
        val seen = HashSet<Int>()
        val w = (r - l).coerceAtLeast(1)
        val h = (b - t).coerceAtLeast(1)
        for (y in 0 until 48) for (x in 0 until 48) {
            val px = (l + x * w / 48).coerceIn(0, bmp.width - 1)
            val py = (t + y * h / 48).coerceIn(0, bmp.height - 1)
            seen.add(bmp.getPixel(px, py) and 0xF8F8F8) // ignore the lowest bits (compression noise)
        }
        return seen.size
    }

    private fun save(name: String, bmp: Bitmap) {
        val f = File(outDir, "$name.png")
        f.outputStream().use { assertTrue("encode $name", bmp.compress(Bitmap.CompressFormat.PNG, 100, it)) }
        made += "$name ${bmp.width}x${bmp.height}"
        Log.i(TAG, "shot $name: ${bmp.width}x${bmp.height}, ${f.length()} bytes")
        bmp.recycle()
    }

    /** Waits for the screen to settle, then captures and saves it; a blank frame fails the test. */
    private fun shot(name: String, s: ActivityScenario<MainActivity>) {
        foreground(s, name)
        hideKeyboard(s)
        Thread.sleep(SETTLE_MS)
        foreground(s, name)
        val bmp = grab()
        val c = colours(bmp)
        assertTrue("$name: the picture is blank ($c colours)", c >= 6)
        save(name, bmp)
    }

    @After
    fun cleanUp() {
        // A fresh gateway for whatever runs next in this process, and the language back to the system's.
        Remohopcore.stopGateway()
        if (arg("remohop_shots") == "1") Lang.setChoice(inst.targetContext, Lang.SYSTEM)
    }

    @Test
    fun storeScreenshots() {
        assumeTrue("remohop_shots is not 1: store screenshots skipped", arg("remohop_shots") == "1")
        val missing = "remohop_host, remohop_password and remohop_dir are required with remohop_shots=1"
        val host = arg("remohop_host") ?: throw AssertionError(missing)
        val password = arg("remohop_password") ?: throw AssertionError(missing)
        val dir = arg("remohop_dir") ?: throw AssertionError(missing)
        val file = arg("remohop_file")
        val lang = if (arg("remohop_shots_lang") == Lang.ZH) Lang.ZH else Lang.EN
        val zh = lang == Lang.ZH

        val base = inst.targetContext.getExternalFilesDir(null) ?: inst.targetContext.filesDir
        outDir = File(base, "store-shots/$lang")
        outDir.deleteRecursively()
        assertTrue("create ${outDir.name}", outDir.mkdirs())

        // The app's own language setting (what Settings -> Language does).
        Lang.setChoice(inst.targetContext, lang)
        assertEquals("the app's effective language", lang, Lang.effective(inst.targetContext))
        Remohopcore.stopGateway() // a fresh page session

        ActivityScenario.launch(MainActivity::class.java).use { s ->
            waitFor(s, "!!document.getElementById('connect') && !document.getElementById('connect').hidden", "connect form loads", 60)
            waitFor(s, "typeof window.remohopMobile==='object'", "injected script present", 30)
            waitFor(s, "document.documentElement.lang.indexOf(${JSONObject.quote(lang)})===0", "the page shows $lang", 20, "document.documentElement.lang")
            // The visible words are in that language (the tab labels and the connect heading).
            val tab = text(s, "document.getElementById('tab-terminal').textContent.trim()")
            assertEquals("terminal tab label", if (zh) "终端" else "Terminal", tab)
            val heading = text(s, "document.getElementById('connect-h').textContent")
            val cjk = Regex("[\\u4e00-\\u9fff]").containsMatchIn(heading)
            assertTrue("connect heading in $lang: '$heading'", if (zh) cjk else !cjk)
            Log.i(TAG, "language $lang: page lang=${text(s, "document.documentElement.lang")}, tab label '$tab', heading '$heading'")
            Thread.sleep(500) // web fonts
            shot("1-connect", s)

            // Connect with the host's password, as a user does in the form.
            assertTrue(
                "filled the connect form",
                ok(
                    s,
                    "(function(){document.getElementById('target').value=${JSONObject.quote(host)};" +
                        "document.getElementById('hostpass').value=${JSONObject.quote(password)};" +
                        "document.getElementById('pair').checked=false;" +
                        "document.getElementById('connectbtn').click();return true})()"
                )
            )
            waitFor(s, "!document.getElementById('workspace').hidden", "connected to the host", 90, "document.getElementById('connerr').textContent")
            // The first connection to a host shows a one-time notice ("identity key pinned", with
            // the key's fingerprint) until the page loads again; later visits do not show it, so
            // the pictures leave it out.
            val notice = text(s, "(function(){var b=document.getElementById('banner');var t=b.hidden?'':b.textContent;b.hidden=true;return t})()")
            Log.i(TAG, "connected" + if (notice.isNotEmpty()) "; one-time notice hidden for the pictures: '${notice.take(60)}'" else "")

            // Terminal: one step smaller text (the app's A- button), a clean screen, then a directory listing.
            openTab(s, "terminal")
            waitFor(s, "!!document.querySelector('#t-host textarea')", "terminal opens", 20)
            poll(s, "document.getElementById('t-host').textContent.trim()!==''", "terminal prompt", 15)
            click(s, "t-smaller")
            Thread.sleep(1500)
            type(s, "clear")
            Thread.sleep(1000)
            val q = if (Regex("^[A-Za-z0-9._/~-]+$").matches(dir)) dir else "'" + dir.replace("'", "'\\''") + "'"
            val expected = file ?: "total" // `ls -l` (any aliases the host's shell has apply)
            var ran = false
            for (i in 0 until 3) {
                if (!type(s, "cd $q && ls -l")) { Thread.sleep(1000); continue }
                ran = poll(s, "document.getElementById('t-host').textContent.indexOf(${JSONObject.quote(expected)})>=0", "terminal output", 10)
                if (ran) break
            }
            assertTrue("the terminal shows the listing of the host directory", ran)
            Thread.sleep(800)
            shot("2-terminal", s)

            // Files: the same directory.
            openTab(s, "files")
            waitFor(s, "document.getElementById('f-path').value!==''", "files tab loads", 30, "document.getElementById('f-err').textContent")
            assertTrue(
                "files path set",
                ok(s, "(function(){document.getElementById('f-path').value=${JSONObject.quote(dir)};document.getElementById('f-go').click();return true})()")
            )
            val listed = if (file != null) "document.getElementById('f-body').textContent.indexOf(${JSONObject.quote(file)})>=0"
            else "document.querySelectorAll('#f-body tr').length>1"
            waitFor(s, listed, "files tab lists the host directory", 30, "document.getElementById('f-err').textContent")
            shot("3-files", s)

            // Desktop: only when the host streams a picture (a CI host may have no display).
            openTab(s, "desktop")
            val settled = "(window.remohopMobile&&remohopMobile._desk&&remohopMobile._desk.hello)?true:" +
                "(function(){var m=document.getElementById('d-msg').textContent;return m!==''&&m.indexOf('Connecting')!==0&&m.indexOf('正在')!==0})()"
            poll(s, settled, "desktop settles", 30)
            val streaming = ok(s, "!!(window.remohopMobile&&remohopMobile._desk&&remohopMobile._desk.hello)")
            if (!streaming) {
                Log.i(TAG, "desktop: no picture (${text(s, "document.getElementById('d-msg').textContent").take(120)}); 4-desktop skipped")
            } else {
                // The first frames: the canvas takes the remote size and the overlay message clears.
                poll(s, "document.getElementById('d-canvas').width>16&&document.getElementById('d-msg').textContent.trim()===''", "desktop picture", 20)
                foreground(s, "desktop")
                hideKeyboard(s)
                Thread.sleep(3000)
                val rect = text(
                    s,
                    "(function(){var r=document.getElementById('d-canvas').getBoundingClientRect();" +
                        "return [r.left,r.top,r.right,r.bottom,devicePixelRatio].join(',')})()"
                ).split(',').mapNotNull { it.toDoubleOrNull() }
                val loc = IntArray(2)
                s.onActivity { a -> findWebView(a.window.decorView)?.getLocationOnScreen(loc) }
                val bmp = grab()
                val c = if (rect.size == 5) {
                    val d = rect[4]
                    colours(bmp, loc[0] + (rect[0] * d).toInt(), loc[1] + (rect[1] * d).toInt(), loc[0] + (rect[2] * d).toInt(), loc[1] + (rect[3] * d).toInt())
                } else 0
                if (c >= 12) save("4-desktop", bmp)
                else { Log.i(TAG, "desktop: the picture is blank or uniform ($c colours); 4-desktop skipped"); bmp.recycle() }
            }
        }

        // Native settings in the same language.
        ActivityScenario.launch(SettingsActivity::class.java).use { s ->
            var title = ""
            var fingerprint = ""
            var checked = false
            var focused = false
            // The settings list: the Language row shows the choice, the
            // Identity key row the start of the fingerprint once it loaded.
            val langName = if (zh) "简体中文" else "English"
            val fp = Regex("^[0-9a-f]{4}:[0-9a-f]{4}…$")
            fun texts(v: android.view.View, out: MutableList<String>) {
                if (v is TextView) out += v.text.toString()
                if (v is android.view.ViewGroup) for (i in 0 until v.childCount) texts(v.getChildAt(i), out)
            }
            val end = System.currentTimeMillis() + 20_000
            while (System.currentTimeMillis() < end) {
                s.onActivity { a ->
                    title = a.title?.toString() ?: ""
                    val all = mutableListOf<String>()
                    texts(a.window.decorView, all)
                    fingerprint = all.firstOrNull { fp.matches(it) } ?: ""
                    checked = all.contains(langName)
                    focused = a.hasWindowFocus()
                }
                if (fingerprint.isNotEmpty() && checked && focused) break
                Thread.sleep(250)
            }
            assertTrue("settings: the language choice shows $lang", checked)
            val cjk = Regex("[\\u4e00-\\u9fff]").containsMatchIn(title)
            assertTrue("settings title in $lang: '$title'", if (zh) cjk else !cjk)
            assertTrue("settings has the window focus", focused)
            Thread.sleep(SETTLE_MS)
            val bmp = grab()
            assertTrue("5-settings: the picture is blank", colours(bmp) >= 4)
            save("5-settings", bmp)
        }

        File(outDir, "shots.txt").writeText(made.joinToString("\n", postfix = "\n"))
        Log.i(TAG, "done: ${made.size} screenshots for $lang")
    }

    companion object {
        const val TAG = "RemoHopShots"
        const val SETTLE_MS = 1500L
    }
}
