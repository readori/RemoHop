package com.remohop.app

import android.annotation.SuppressLint
import android.app.Activity
import android.content.ClipData
import android.content.ClipboardManager
import android.content.Intent
import android.content.pm.PackageManager
import android.content.pm.ApplicationInfo
import android.content.res.Configuration
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.view.HapticFeedbackConstants
import android.view.ViewGroup
import android.view.WindowManager
import android.view.inputmethod.InputMethodManager
import android.webkit.ConsoleMessage
import android.webkit.CookieManager
import android.webkit.GeolocationPermissions
import android.webkit.PermissionRequest
import android.webkit.RenderProcessGoneDetail
import android.webkit.URLUtil
import android.webkit.ValueCallback
import android.webkit.WebChromeClient
import android.webkit.WebResourceRequest
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.FrameLayout
import android.widget.TextView
import android.window.OnBackInvokedDispatcher
import androidx.core.view.ViewCompat
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.WindowInsetsControllerCompat
import androidx.webkit.ScriptHandler
import androidx.webkit.WebViewCompat
import androidx.webkit.WebViewFeature
import org.json.JSONObject
import java.util.concurrent.Executors
import kotlin.math.max

/**
 * The controller: a WebView on the in-process gateway's loopback page, plus
 * the native pieces a WebView does badly (see remohop-mobile.js for the page
 * side): clipboard, downloads, file picking, keep-screen-on, full screen,
 * the soft keyboard and the back button.
 *
 * The gateway URL carries its access token. It is passed to loadUrl and to
 * nothing else: not logged, not put in an Intent, not saved. Only its origin
 * (http://127.0.0.1:PORT) is kept, to scope the bridge and navigation.
 *
 * The page shows the app's language (see [Lang]): it is loaded with
 * ?lang=zh or ?lang=en, and told when the setting changes. This activity
 * is not recreated for a language change (it would drop the page), so its
 * own strings come from [Lang.localized].
 */
class MainActivity : Activity(), BridgeHost {
    private lateinit var root: FrameLayout
    private lateinit var web: WebView
    private var origin: String? = null
    private var bridge: Bridge? = null
    private var scriptHandler: ScriptHandler? = null
    private var listenerInstalled = false
    private var fileCallback: ValueCallback<Array<Uri>>? = null
    private var fullscreen = false
    private var pageLang: String? = null
    /** [Core.generation] of the URL the WebView has. */
    private var loadedGen = -1
    private lateinit var saver: DownloadSaver
    private val bg = Executors.newSingleThreadExecutor()
    private val main = Handler(Looper.getMainLooper())
    private var askedNotifications = false
    /** While resumed: start the foreground service once a host session exists (see SessionService). */
    private val watch = object : Runnable {
        override fun run() {
            watchSessions()
            main.postDelayed(this, WATCH_MS)
        }
    }
    private val script by lazy { assets.open("remohop-mobile.js").bufferedReader().use { it.readText() } }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        saver = DownloadSaver(this, REQ_SAVE)
        WindowCompat.setDecorFitsSystemWindows(window, false)
        root = FrameLayout(this)
        setContentView(root)
        applyBarColors()
        // Edge to edge (enforced from Android 15): pad for the system bars,
        // and for the keyboard so the page resizes above it.
        ViewCompat.setOnApplyWindowInsetsListener(root) { v, insets ->
            val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars() or WindowInsetsCompat.Type.displayCutout())
            val ime = insets.getInsets(WindowInsetsCompat.Type.ime())
            if (fullscreen) v.setPadding(0, 0, 0, ime.bottom)
            else v.setPadding(bars.left, bars.top, bars.right, max(bars.bottom, ime.bottom))
            barsBackground?.top = if (fullscreen) 0 else bars.top
            WindowInsetsCompat.CONSUMED
        }
        newWebView()
        if (Build.VERSION.SDK_INT >= 33) {
            onBackInvokedDispatcher.registerOnBackInvokedCallback(OnBackInvokedDispatcher.PRIORITY_DEFAULT) { handleBack() }
        }
        startGateway()
    }

    private fun startGateway() {
        bg.execute {
            val url = runCatching { Core.gatewayUrl(this) }
            val gen = Core.generation
            runOnUiThread {
                if (isDestroyed) return@runOnUiThread
                url.onSuccess { loadedGen = gen; load(it) }.onFailure { showError(str(R.string.gateway_failed, it.message ?: "")) }
            }
        }
    }

    /**
     * Makes sure the gateway answers (reopening its listener if it failed)
     * and loads the page again only if the gateway's URL changed. Runs on
     * every return to the foreground and when the page asks (bridge
     * "gateway.ensure"); [done] gets the outcome on the UI thread.
     */
    override fun ensureGateway(done: (Boolean) -> Unit) {
        if (bg.isShutdown) return done(false)
        bg.execute {
            val url = runCatching { Core.gatewayUrl(this) }
            val gen = Core.generation
            runOnUiThread {
                if (isDestroyed) return@runOnUiThread
                val u = url.getOrNull()
                if (u != null && (gen != loadedGen || origin == null)) {
                    if (!::web.isInitialized || web.parent == null) newWebView()
                    loadedGen = gen
                    load(u)
                }
                done(u != null)
            }
        }
    }

    private fun load(url: String) {
        val o = GatewayUrls.origin(url) ?: return showError(str(R.string.gateway_failed, "unexpected address"))
        installBridge(o)
        val lang = Lang.effective(this)
        pageLang = lang
        web.loadUrl(GatewayUrls.withLang(url, lang))
    }

    private fun str(id: Int, vararg args: Any): String = Lang.localized(this).getString(id, *args)

    /** Tells the page when the app's language changed (Settings, or the system's). */
    private fun syncLang() {
        val lang = Lang.effective(this)
        if (!::web.isInitialized || origin == null || pageLang == null || lang == pageLang) return
        pageLang = lang
        web.evaluateJavascript("window.remohopMobile&&remohopMobile.setLang(${JSONObject.quote(lang)})", null)
    }

    /** Passes on what Settings did to the saved hosts: list them again, or connect to one. */
    private fun tellPage() {
        if (!::web.isInitialized || origin == null) return
        val (changed, target) = PendingPage.take()
        when {
            target != null -> web.evaluateJavascript("window.remohopMobile&&remohopMobile.connect&&remohopMobile.connect(${JSONObject.quote(target)})", null)
            changed -> web.evaluateJavascript("window.remohopMobile&&remohopMobile.peersChanged&&remohopMobile.peersChanged()", null)
        }
    }

    private var barsBackground: BarsBackground? = null

    /**
     * The status bar area shows the page's top bar colour and the rest the
     * page colour, with status bar icons that read on it, in light and dark.
     */
    private fun applyBarColors() {
        val top = barsBackground?.top ?: 0
        val page = AppTheme.pageColor(this) // the page's colour (dynamic where the device has it)
        barsBackground = BarsBackground(page, page).also { it.top = top; root.background = it }
        val night = (resources.configuration.uiMode and Configuration.UI_MODE_NIGHT_MASK) == Configuration.UI_MODE_NIGHT_YES
        WindowCompat.getInsetsController(window, root).apply {
            isAppearanceLightStatusBars = !night
            isAppearanceLightNavigationBars = !night
        }
    }

    override fun onConfigurationChanged(newConfig: Configuration) {
        super.onConfigurationChanged(newConfig)
        applyBarColors()
        syncLang()
    }

    @SuppressLint("SetJavaScriptEnabled")
    private fun newWebView() {
        web = WebView(this)
        root.removeAllViews()
        root.addView(web, FrameLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT))
        listenerInstalled = false
        scriptHandler = null
        origin = null
        val debuggable = (applicationInfo.flags and ApplicationInfo.FLAG_DEBUGGABLE) != 0
        WebView.setWebContentsDebuggingEnabled(debuggable)
        CookieManager.getInstance().setAcceptCookie(false) // the loopback gateway uses none
        web.settings.apply {
            javaScriptEnabled = true
            domStorageEnabled = true // the page keeps its session id in sessionStorage
            allowFileAccess = false
            allowContentAccess = false
            javaScriptCanOpenWindowsAutomatically = false
            setSupportMultipleWindows(false)
            setGeolocationEnabled(false)
            mixedContentMode = WebSettings.MIXED_CONTENT_NEVER_ALLOW
            cacheMode = WebSettings.LOAD_NO_CACHE
            safeBrowsingEnabled = false // would send URLs to Google; the page is local
            mediaPlaybackRequiresUserGesture = true
            textZoom = 100
            // No page zoom: the page is laid out for the phone, and the
            // remote desktop zooms itself (pinch in remohop-mobile.js).
            setSupportZoom(false)
            builtInZoomControls = false
            displayZoomControls = false
        }
        web.webViewClient = object : WebViewClient() {
            override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean {
                val o = origin ?: return true
                return !GatewayUrls.sameOrigin(request.url.toString(), o) // block everything else
            }

            override fun onPageFinished(view: WebView, url: String?) {
                // Without document-start injection, inject late: the page
                // works, but its first desktop socket may be missed.
                if (scriptHandler == null) view.evaluateJavascript(script, null)
            }

            override fun onRenderProcessGone(view: WebView, detail: RenderProcessGoneDetail): Boolean {
                Diag.log("web renderer gone (crashed: ${detail.didCrash()})")
                // The renderer died (or was killed for memory): start over
                // with a fresh WebView on the same gateway.
                view.destroy()
                newWebView()
                startGateway()
                return true
            }
        }
        web.webChromeClient = object : WebChromeClient() {
            override fun onShowFileChooser(view: WebView, cb: ValueCallback<Array<Uri>>, params: FileChooserParams): Boolean {
                fileCallback?.onReceiveValue(null)
                fileCallback = cb
                val pick = Intent(Intent.ACTION_GET_CONTENT).addCategory(Intent.CATEGORY_OPENABLE).setType("*/*")
                    .putExtra(Intent.EXTRA_ALLOW_MULTIPLE, params.mode == FileChooserParams.MODE_OPEN_MULTIPLE)
                return try {
                    startActivityForResult(Intent.createChooser(pick, str(R.string.choose_files)), REQ_FILES)
                    true
                } catch (e: Exception) {
                    fileCallback = null
                    false
                }
            }

            // Console messages name their source URL, which carries the token:
            // only an error's text goes to the diagnostic log (the core redacts it).
            override fun onConsoleMessage(message: ConsoleMessage): Boolean {
                if (message.messageLevel() == ConsoleMessage.MessageLevel.ERROR) Diag.logPage("console: " + message.message())
                return true
            }
            override fun onPermissionRequest(request: PermissionRequest) = request.deny()
            override fun onGeolocationPermissionsShowPrompt(o: String?, cb: GeolocationPermissions.Callback) = cb.invoke(o, false, false)
        }
        // Fallback for a download the page did not hand over itself.
        web.setDownloadListener { url, _, disposition, mime, _ ->
            val o = origin ?: return@setDownloadListener
            if (GatewayUrls.isDownload(url, o)) download(url, GatewayUrls.safeFileName(URLUtil.guessFileName(url, disposition, mime)))
        }
    }

    private fun installBridge(o: String) {
        if (origin == o && listenerInstalled) return
        origin = o
        val b = Bridge(this, o)
        bridge = b
        if (WebViewFeature.isFeatureSupported(WebViewFeature.WEB_MESSAGE_LISTENER)) {
            if (listenerInstalled) WebViewCompat.removeWebMessageListener(web, BRIDGE_NAME)
            // Exposed to the gateway's origin only.
            WebViewCompat.addWebMessageListener(web, BRIDGE_NAME, setOf(o)) { _, message, sourceOrigin, isMainFrame, reply ->
                val data = message.data ?: return@addWebMessageListener
                b.handle(data, GatewayUrls.origin(sourceOrigin.toString()), isMainFrame) { reply.postMessage(it) }
            }
            listenerInstalled = true
        }
        scriptHandler = if (WebViewFeature.isFeatureSupported(WebViewFeature.DOCUMENT_START_SCRIPT)) {
            scriptHandler?.remove()
            WebViewCompat.addDocumentStartJavaScript(web, script, setOf(o))
        } else null
    }

    private fun showError(text: String) {
        root.removeAllViews()
        root.addView(TextView(this).apply { this.text = text; setPadding(48, 48, 48, 48); textSize = 16f })
    }

    private fun handleBack() {
        if (!::web.isInitialized || origin == null) { moveTaskToBack(true); return }
        web.evaluateJavascript(Bridge.BACK_SCRIPT) { r ->
            // Unhandled: go to the background, keeping the sessions.
            if (!Bridge.backHandled(r)) moveTaskToBack(true)
        }
    }

    // Only Android 12L and older call this (no predictive back gestures there): from 13 on the
    // manifest's enableOnBackInvokedCallback routes back to the OnBackInvokedCallback registered
    // in onCreate. Lint's GestureBackNavigation (an error from targetSdk 36) is about 13+.
    @Deprecated("Android 12L and older; 13+ uses OnBackInvokedCallback")
    @SuppressLint("GestureBackNavigation")
    override fun onBackPressed() = handleBack()

    @Deprecated("Activity result API needs androidx.activity, which this app does not use")
    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        when (requestCode) {
            REQ_FILES -> {
                val cb = fileCallback ?: return
                fileCallback = null
                val uris = if (resultCode != RESULT_OK || data == null) null else {
                    val clip = data.clipData
                    if (clip != null) Array(clip.itemCount) { clip.getItemAt(it).uri } else data.data?.let { arrayOf(it) }
                }
                cb.onReceiveValue(uris)
            }
            REQ_SAVE -> saver.onResult(resultCode == RESULT_OK, data?.data)
            else -> @Suppress("DEPRECATION") super.onActivityResult(requestCode, resultCode, data)
        }
    }

    override fun onResume() {
        super.onResume()
        if (::web.isInitialized) web.onResume()
        syncLang()
        // The gateway is process wide: check that it answers (it reopens a
        // failed listener, or starts again) before the page needs it. It
        // also re-establishes host sessions that dropped meanwhile.
        if (origin != null) ensureGateway {}
        tellPage()
        main.removeCallbacks(watch)
        main.post(watch)
    }

    override fun onPause() {
        main.removeCallbacks(watch)
        // Still visible, so still allowed to start a foreground service:
        // the last chance before the background.
        watchSessions()
        if (::web.isInitialized) web.onPause()
        super.onPause()
    }

    private fun watchSessions() {
        if (bg.isShutdown) return
        bg.execute {
            val has = SessionWatch.sessions().isNotEmpty()
            if (has) SessionWatch.check(this)
            runOnUiThread { if (has && !isDestroyed) askNotifications() }
        }
    }

    /** Android 13+: the session notification needs the user's permission; asked once, when it is first needed. */
    private fun askNotifications() {
        if (Build.VERSION.SDK_INT < 33 || askedNotifications) return
        askedNotifications = true
        if (checkSelfPermission(android.Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) {
            requestPermissions(arrayOf(android.Manifest.permission.POST_NOTIFICATIONS), REQ_NOTIFICATIONS)
        }
    }

    override fun onDestroy() {
        main.removeCallbacks(watch)
        if (::web.isInitialized) {
            root.removeAllViews()
            web.destroy()
        }
        bg.shutdown()
        super.onDestroy()
    }

    // ---- BridgeHost ----

    override fun clipboardRead(): String {
        val cm = getSystemService(ClipboardManager::class.java) ?: return ""
        return cm.primaryClip?.takeIf { it.itemCount > 0 }?.getItemAt(0)?.coerceToText(this)?.toString() ?: ""
    }

    override fun clipboardWrite(text: String) {
        getSystemService(ClipboardManager::class.java)?.setPrimaryClip(ClipData.newPlainText("RemoHop", text))
    }

    override fun download(url: String, name: String) = saver.start(url, name)

    override fun setState(keepScreenOn: Boolean, fullscreen: Boolean) {
        if (keepScreenOn) window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
        else window.clearFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
        if (fullscreen != this.fullscreen) {
            this.fullscreen = fullscreen
            val c = WindowCompat.getInsetsController(window, root)
            if (fullscreen) {
                c.systemBarsBehavior = WindowInsetsControllerCompat.BEHAVIOR_SHOW_TRANSIENT_BARS_BY_SWIPE
                c.hide(WindowInsetsCompat.Type.systemBars())
            } else c.show(WindowInsetsCompat.Type.systemBars())
            ViewCompat.requestApplyInsets(root)
        }
    }

    override fun keyboard(show: Boolean) {
        val imm = getSystemService(InputMethodManager::class.java) ?: return
        if (show) imm.showSoftInput(web, 0) else imm.hideSoftInputFromWindow(web.windowToken, 0)
    }

    override fun haptic() {
        web.performHapticFeedback(HapticFeedbackConstants.LONG_PRESS)
    }

    override fun openSettings() = startActivity(Intent(this, SettingsActivity::class.java))

    override fun openSettings(page: String) =
        startActivity(Intent(this, SettingsActivity::class.java).putExtra(SettingsActivity.EXTRA_PAGE, page))

    override fun diagLog(text: String) = Diag.logPage(text)

    override fun theme(): JSONObject = AppTheme.webRoles(this)

    override fun showDialog(request: DialogRequest, done: (DialogAnswer) -> Unit) {
        if (isFinishing || isDestroyed) return done(DialogAnswer.Cancelled)
        NativeDialog.show(this, request, done)
    }

    companion object {
        private const val REQ_FILES = 1
        private const val REQ_SAVE = 2
        private const val REQ_NOTIFICATIONS = 3
        private const val WATCH_MS = 3_000L
        private const val BRIDGE_NAME = "remohopNative"
    }
}
