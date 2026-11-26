package com.remohop.app

import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.content.res.Configuration
import android.os.Build
import android.os.Bundle
import android.text.InputType
import android.view.Menu
import android.view.View
import android.view.ViewGroup
import android.widget.LinearLayout
import android.widget.TextView
import androidx.activity.OnBackPressedCallback
import androidx.appcompat.app.AppCompatActivity
import androidx.coordinatorlayout.widget.CoordinatorLayout
import androidx.core.view.ViewCompat
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.widget.NestedScrollView
import com.google.android.material.appbar.AppBarLayout
import com.google.android.material.appbar.CollapsingToolbarLayout
import com.google.android.material.appbar.MaterialToolbar
import com.google.android.material.bottomsheet.BottomSheetDialog
import com.google.android.material.dialog.MaterialAlertDialogBuilder
import com.google.android.material.materialswitch.MaterialSwitch
import com.google.android.material.progressindicator.CircularProgressIndicator
import com.google.android.material.snackbar.Snackbar
import com.google.android.material.textfield.TextInputLayout
import com.remohop.app.core.remohopcore.Remohopcore
import org.json.JSONArray
import org.json.JSONObject
import java.util.concurrent.Executors
import com.google.android.material.R as M

/**
 * Native settings in Material 3 (Material Components): a large top app bar
 * that collapses on scroll (medium on the pages under it), list items in
 * rounded groups by task under subheaders (see [SettingsUi]), a status card
 * for the rendezvous server, M3 switches, outlined text fields, filled and
 * tonal buttons, dialogs, bottom sheets and snackbars; always in the
 * RemoHop cobalt scheme (no dynamic colour), light and dark.
 *
 * No paragraphs on the screens: each page's explanation is a bottom sheet
 * behind the ⓘ in its app bar. Fields save themselves when they are left
 * (no Save buttons) and confirm with a snackbar; a field's error stays on
 * the field. Connection settings take effect on the next connection (the
 * running gateway reads them per dial); the language at once (see [Lang]).
 *
 * The pages live in this one activity, with their own back stack.
 */
class SettingsActivity : AppCompatActivity() {
    /** Reads and writes of the core's settings and saved hosts: quick. */
    private val bg = Executors.newSingleThreadExecutor()
    /** Network checks (rendezvous server, which hosts are online): up to 10 s, so never ahead of a save. */
    private val net = Executors.newSingleThreadExecutor()
    private lateinit var ui: SettingsUi
    private lateinit var root: CoordinatorLayout
    private lateinit var bar: AppBarLayout
    private lateinit var toolbar: MaterialToolbar
    private lateinit var collapsing: CollapsingToolbarLayout
    private var content: View? = null
    private val stack = ArrayList<String>()
    private var info: Pair<String, List<Pair<String, String>>>? = null
    /** The "This device" page while it shows (it follows host mode's state). */
    private var hostPage: HostPage? = null

    // What the core has, to save only changes.
    private var signal = ""
    private var pin = ""
    private var relay = true
    private var name = ""
    private var fingerprint = ""
    private var hosts = JSONArray()
    /** The settings and saved hosts have been read once. */
    private var loaded = false
    /** Values being saved now, per key: Done and leaving the field must not save twice. */
    private val inflight = HashMap<String, String>()
    private val pairListener: () -> Unit = { if (stack.lastOrNull() == PAGE_PAIR) show() }
    private var rendezvousStatus: RendezvousStatus? = null
    /** The key the saved server presented in the last check (offered when none is saved). */
    private var rendezvousKey = ""
    /** Masks a saved password the host page shows (set while that page is built, cleared by [show]). */
    private var hidePassword: (() -> Unit)? = null

    override fun attachBaseContext(newBase: Context) {
        super.attachBaseContext(Lang.wrap(newBase))
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        Core.init(this)
        ui = SettingsUi(this)
        // Edge to edge: the app bar draws under the status bar, the list
        // scrolls under the navigation bar.
        WindowCompat.setDecorFitsSystemWindows(window, false)
        val night = (resources.configuration.uiMode and Configuration.UI_MODE_NIGHT_MASK) == Configuration.UI_MODE_NIGHT_YES
        WindowCompat.getInsetsController(window, window.decorView).apply {
            isAppearanceLightStatusBars = !night
            isAppearanceLightNavigationBars = !night
        }
        root = CoordinatorLayout(this).apply { setBackgroundColor(ui.pageColor) }
        // A large top app bar on the page colour; collapsed, it takes the
        // surfaceContainerHigh role (the scrim) instead of a shadow.
        bar = AppBarLayout(this).apply {
            fitsSystemWindows = true
            isLiftOnScroll = false
            setBackgroundColor(ui.pageColor)
            stateListAnimator = null
            elevation = 0f
        }
        collapsing = CollapsingToolbarLayout(this, null, M.attr.collapsingToolbarLayoutLargeStyle).apply {
            fitsSystemWindows = true
            setContentScrimColor(ui.color(M.attr.colorSurfaceContainerHigh))
            setStatusBarScrimColor(ui.color(M.attr.colorSurfaceContainerHigh))
            setExpandedTitleColor(ui.color(M.attr.colorOnSurface))
            setCollapsedTitleTextColor(ui.color(M.attr.colorOnSurface))
            expandedTitleMarginStart = ui.dp(16)
            expandedTitleMarginEnd = ui.dp(16)
            setExpandedTitleTextAppearance(ui.textAppearance(M.attr.textAppearanceHeadlineMedium))
            setCollapsedTitleTextAppearance(ui.textAppearance(M.attr.textAppearanceTitleLarge))
        }
        toolbar = MaterialToolbar(this).apply {
            setNavigationIcon(R.drawable.ic_arrow_back)
            setNavigationContentDescription(R.string.back)
            setNavigationOnClickListener { back() }
            setOnMenuItemClickListener {
                when (it.itemId) {
                    MENU_INFO -> showInfo()
                    MENU_REFRESH -> refreshHosts(true)
                }
                true
            }
        }
        collapsing.addView(toolbar, CollapsingToolbarLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, actionBarSize()).apply {
            collapseMode = CollapsingToolbarLayout.LayoutParams.COLLAPSE_MODE_PIN
        })
        bar.addView(collapsing, AppBarLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ui.dp(152)).apply {
            scrollFlags = AppBarLayout.LayoutParams.SCROLL_FLAG_SCROLL or AppBarLayout.LayoutParams.SCROLL_FLAG_EXIT_UNTIL_COLLAPSED or
                AppBarLayout.LayoutParams.SCROLL_FLAG_SNAP
        })
        root.addView(bar, CoordinatorLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
        setContentView(root)
        onBackPressedDispatcher.addCallback(this, object : OnBackPressedCallback(true) {
            override fun handleOnBackPressed() = back()
        })
        savedInstanceState?.getStringArrayList(STATE_STACK)?.let { stack.addAll(it) }
        if (stack.isEmpty()) intent.getStringExtra(EXTRA_PAGE)?.takeIf { it in DIRECT_PAGES }?.let { stack.add(it) }
        PairJob.listen(pairListener)
        show()
        load { show() }
    }

    override fun onSaveInstanceState(out: Bundle) {
        super.onSaveInstanceState(out)
        out.putStringArrayList(STATE_STACK, stack)
    }

    override fun onStop() {
        hidePassword?.invoke() // a shown saved password is masked again when the screen is left
        hostPage?.mask() // and so is a shown own password for host mode
        super.onStop()
    }

    override fun onDestroy() {
        hostPage?.dispose()
        PairJob.unlisten(pairListener)
        bg.shutdownNow()
        net.shutdownNow()
        super.onDestroy()
    }

    private fun ui(f: () -> Unit) = runOnUiThread { if (!isDestroyed) f() }

    private fun load(then: () -> Unit) = bg.execute {
        val s = runCatching { JSONObject(Remohopcore.settingsJSON()) }.getOrNull()
        val fp = runCatching { Remohopcore.identity() }.getOrDefault("")
        val list = runCatching { JSONArray(Remohopcore.devicesJSON(false)) }.getOrDefault(JSONArray())
        ui {
            if (s != null) {
                signal = s.optString("signal")
                pin = s.optString("signal-pin")
                relay = s.optString("relay", "on") != "off"
                name = s.optString("name")
            }
            fingerprint = fp
            hosts = list
            loaded = true
            then()
        }
    }

    // ---- navigation ----

    private fun open(page: String) {
        currentFocus?.clearFocus() // commits a field being edited
        stack.add(page)
        show()
    }

    private fun back() {
        currentFocus?.clearFocus() // commits a field being edited; the page it changes is built again when saved
        if (stack.lastOrNull() == PAGE_PAIR) PairJob.clear()
        if (stack.isEmpty()) finish() else { stack.removeAt(stack.size - 1); show() }
    }

    /** Opens a page (for the instrumented tests). */
    @androidx.annotation.VisibleForTesting
    internal fun openForTest(page: String) = open(page)

    private fun showInfo() {
        val (title, items) = info ?: return
        showInfo(title, items)
    }

    /** An explanation in a bottom sheet: a title, then headed paragraphs. */
    private fun showInfo(title: String, items: List<Pair<String, String>>) {
        val d = BottomSheetDialog(this)
        val (scroll, col) = ui.page()
        col.setPadding(ui.dp(24), ui.dp(8), ui.dp(24), ui.dp(32))
        col.addView(TextView(this).apply {
            text = title
            androidx.core.widget.TextViewCompat.setTextAppearance(this, resolve(M.attr.textAppearanceTitleLarge))
            setTextColor(ui.color(M.attr.colorOnSurface))
            setPadding(0, 0, 0, ui.dp(8))
        })
        for ((h, t) in items) {
            if (h.isNotEmpty()) col.addView(TextView(this).apply {
                text = h
                androidx.core.widget.TextViewCompat.setTextAppearance(this, resolve(M.attr.textAppearanceTitleSmall))
                setTextColor(ui.color(M.attr.colorOnSurface))
                setPadding(0, ui.dp(16), 0, ui.dp(4))
            })
            col.addView(TextView(this).apply {
                text = t
                androidx.core.widget.TextViewCompat.setTextAppearance(this, resolve(M.attr.textAppearanceBodyMedium))
                setTextColor(ui.color(M.attr.colorOnSurfaceVariant))
            })
        }
        d.setContentView(scroll)
        d.show()
    }

    private fun actionBarSize(): Int {
        val tv = android.util.TypedValue()
        return if (theme.resolveAttribute(android.R.attr.actionBarSize, tv, true))
            android.util.TypedValue.complexToDimensionPixelSize(tv.data, resources.displayMetrics) else ui.dp(64)
    }

    private fun resolve(attr: Int): Int {
        val tv = android.util.TypedValue()
        theme.resolveAttribute(attr, tv, true)
        return tv.resourceId
    }

    private var shownPage: String? = "\u0000"

    private fun show() {
        hostPage?.dispose()
        hostPage = null
        info = null
        hidePassword = null
        // What the user typed into the page being built again (the settings
        // were read, a save finished, the activity was recreated) stays.
        val page = stack.lastOrNull()
        val typed = if (page == shownPage) editedFields() else emptyMap()
        shownPage = page
        val (scroll, col) = ui.page()
        when (stack.lastOrNull()) {
            null -> main(col)
            PAGE_RENDEZVOUS -> rendezvous(col)
            PAGE_NAME -> deviceName(col)
            PAGE_IDENTITY -> identity(col)
            PAGE_HOSTS -> savedHosts(col)
            PAGE_PAIR -> pair(col)
            PAGE_DIAGNOSTICS -> diagnostics(col)
            PAGE_PRIVACY -> privacy(col)
            PAGE_ABOUT -> about(col)
            PAGE_THIS_DEVICE -> thisDevice(col)
            else -> stack.last().takeIf { it.startsWith(PAGE_HOST) }?.let { hostDetail(col, it.removePrefix(PAGE_HOST)) } ?: main(col)
        }
        for ((id, text) in typed) (scroll.findViewById<View>(id) as? android.widget.EditText)?.let { if (it.text.toString() != text) it.setText(text) }
        content?.let { root.removeView(it) }
        content = scroll
        root.addView(scroll, CoordinatorLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT).apply {
            behavior = AppBarLayout.ScrollingViewBehavior()
        })
        scroll.id = View.generateViewId()
        // Large on the main page, medium on the ones under it; expanded on arrival.
        val h = ui.dp(if (stack.isEmpty()) 152 else 112)
        (collapsing.layoutParams as AppBarLayout.LayoutParams).let { if (it.height != h) { it.height = h; collapsing.layoutParams = it } }
        collapsing.setExpandedTitleTextAppearance(ui.textAppearance(if (stack.isEmpty()) M.attr.textAppearanceHeadlineMedium else M.attr.textAppearanceHeadlineSmall))
        collapsing.expandedTitleMarginBottom = ui.dp(if (stack.isEmpty()) 28 else 20)
        bar.setExpanded(true, false)
        ViewCompat.setOnApplyWindowInsetsListener(scroll) { v, insets ->
            val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars() or WindowInsetsCompat.Type.displayCutout() or WindowInsetsCompat.Type.ime())
            v.setPadding(bars.left, 0, bars.right, bars.bottom)
            insets
        }
        ViewCompat.requestApplyInsets(scroll)
        // App bar actions: ⓘ where the page has an explanation, refresh on the host list.
        val menu = toolbar.menu
        menu.clear()
        if (stack.lastOrNull() == PAGE_HOSTS) {
            menu.add(Menu.NONE, MENU_REFRESH, Menu.NONE, R.string.hosts_check_online).setIcon(R.drawable.ic_refresh)
                .setShowAsAction(android.view.MenuItem.SHOW_AS_ACTION_IF_ROOM)
        }
        if (info != null) {
            menu.add(Menu.NONE, MENU_INFO, Menu.NONE, R.string.info_about_screen).setIcon(R.drawable.ic_info)
                .setShowAsAction(android.view.MenuItem.SHOW_AS_ACTION_ALWAYS)
        }
        val tint = ui.color(M.attr.colorOnSurfaceVariant)
        for (i in 0 until menu.size()) menu.getItem(i).icon?.setTint(tint)
    }

    /** The text of the page's fields that the user changed since they were built, by view ID. */
    private fun editedFields(): Map<Int, String> {
        val out = HashMap<Int, String>()
        fun walk(v: View) {
            if (v is android.widget.EditText && v.id != View.NO_ID && v.isSaveEnabled) {
                val t = v.text.toString()
                if (t != v.getTag(R.id.field_initial)) out[v.id] = t
            }
            if (v is ViewGroup) for (i in 0 until v.childCount) walk(v.getChildAt(i))
        }
        content?.let { walk(it) }
        return out
    }

    private fun pageTitle(id: Int) = pageTitle(getString(id))
    private fun pageTitle(s: CharSequence) { collapsing.title = s; title = s }

    private fun snack(text: CharSequence) = Snackbar.make(root, text, Snackbar.LENGTH_SHORT).show()
    private fun snack(id: Int) = snack(getString(id))

    // ---- pages ----

    private fun main(col: LinearLayout) {
        pageTitle(R.string.settings)
        // The connection's status card: the rendezvous server and whether it
        // answers, with "Set up" while there is none.
        val set = signal.isNotEmpty()
        val hero = ui.hero(col, R.drawable.ic_s_network, getString(R.string.rendezvous_heading),
            if (set) signal else getString(R.string.not_set),
            if (set) heroStatus() else null, heroTone(),
            if (set) null else getString(R.string.set_up), if (set) null else getString(R.string.rv_card_unset)) { open(PAGE_RENDEZVOUS) }
        hero.view.contentDescription = listOf(getString(R.string.rendezvous_heading), hero.value.text, hero.status.text,
            if (set) "" else getString(R.string.rv_card_unset)).filter { it.isNotEmpty() }.joinToString(", ")
        if (set && rendezvousStatus == null && RendezvousCheck.AVAILABLE && !checkingMain) {
            checkingMain = true
            net.execute {
                val r = RendezvousCheck.check()
                ui {
                    checkingMain = false
                    rendezvousStatus = r?.status ?: RendezvousStatus.of("error")
                    r?.key?.let { rendezvousKey = it }
                    // In place: the page keeps its scroll position.
                    if (stack.isEmpty() && hero.view.isAttachedToWindow) {
                        ui.setStatus(hero.status, heroStatus(), heroTone())
                        hero.view.contentDescription = listOf(getString(R.string.rendezvous_heading), hero.value.text, hero.status.text).joinToString(", ")
                    } else if (stack.isEmpty()) show() // the page was built again meanwhile
                }
            }
        }
        // This phone as a host: view and control it from another device.
        var c = ui.section(col, getString(R.string.hm_this_device), SettingsUi.Tone.PRIMARY)
        ui.row(c, getString(R.string.hm_allow), summary = getString(if (HostMode.state.on) R.string.on else R.string.off),
            icon = R.drawable.ic_s_device, chevron = true) { open(PAGE_THIS_DEVICE) }
        c = ui.section(col, getString(R.string.device_heading), SettingsUi.Tone.SECONDARY)
        ui.row(c, getString(R.string.device_name), summary = name.ifEmpty { Diag.device() }, icon = R.drawable.ic_s_device, chevron = true) { open(PAGE_NAME) }
        ui.row(c, getString(R.string.identity_heading), summary = AppInfo.shortFingerprint(fingerprint), icon = R.drawable.ic_s_key, chevron = true) { open(PAGE_IDENTITY) }
        c = ui.section(col, getString(R.string.sec_hosts), SettingsUi.Tone.PRIMARY)
        ui.row(c, getString(R.string.hosts_heading), value = hosts.length().toString(), icon = R.drawable.ic_s_host, chevron = true) { open(PAGE_HOSTS) }
        ui.row(c, getString(R.string.pair_heading), icon = R.drawable.ic_s_link, chevron = true) { open(PAGE_PAIR) }
        c = ui.section(col, getString(R.string.sec_general), SettingsUi.Tone.SECONDARY)
        ui.row(c, getString(R.string.language_heading), summary = languageName(Lang.choice(this)), icon = R.drawable.ic_s_language) { chooseLanguage() }
        c = ui.section(col, getString(R.string.sec_support), SettingsUi.Tone.TERTIARY)
        ui.row(c, getString(R.string.diag_title), summary = getString(if (Diag.enabled) R.string.on else R.string.off), icon = R.drawable.ic_s_diag, chevron = true) { open(PAGE_DIAGNOSTICS) }
        ui.row(c, getString(R.string.privacy_title), icon = R.drawable.ic_s_privacy, chevron = true) { open(PAGE_PRIVACY) }
        ui.row(c, getString(R.string.about_title), summary = getString(R.string.about_version, BuildConfig.VERSION_NAME, BuildConfig.VERSION_CODE), icon = R.drawable.ic_s_info, chevron = true) { open(PAGE_ABOUT) }
    }

    private var checkingMain = false

    private fun thisDevice(col: LinearLayout) {
        pageTitle(R.string.hm_this_device)
        val page = HostPage(this, ui, { t, items -> showInfo(t, items) }, { snack(it) }) {
            if (stack.lastOrNull() == PAGE_THIS_DEVICE && !isDestroyed) show()
        }
        page.build(col)
        info = page.info()
        hostPage = page
    }

    /** The status card's badge: checking, working, or what is wrong (in words). */
    private fun heroStatus(): String = if (rendezvousStatus == null) getString(R.string.checking) else statusText(rendezvousStatus)

    private fun heroTone() = when {
        signal.isEmpty() || rendezvousStatus == null -> SettingsUi.Tone.NEUTRAL
        rendezvousStatus == RendezvousStatus.OK -> SettingsUi.Tone.SECONDARY
        else -> SettingsUi.Tone.ERROR
    }

    private fun languageName(choice: String) = when (choice) {
        Lang.ZH -> getString(R.string.lang_zh)
        Lang.EN -> getString(R.string.lang_en)
        else -> getString(R.string.lang_system)
    }

    /** One choice of three, visible at once: radio buttons in a dialog. */
    private fun chooseLanguage() {
        val choices = arrayOf(Lang.SYSTEM, Lang.ZH, Lang.EN)
        val names = choices.map { languageName(it) }.toTypedArray()
        MaterialAlertDialogBuilder(this)
            .setTitle(R.string.language_heading)
            .setSingleChoiceItems(names, choices.indexOf(Lang.choice(this))) { d, i ->
                d.dismiss()
                if (choices[i] == Lang.choice(this)) return@setSingleChoiceItems
                Lang.setChoice(this, choices[i])
                // Android 13+ recreates this screen itself for the new locale.
                if (Build.VERSION.SDK_INT < 33) recreate()
            }
            .setNegativeButton(R.string.cancel, null)
            .show()
    }

    /**
     * Saves one setting if it changed; the core validates it, and an error
     * stays on the field (in the app's words; the core's text goes to the
     * diagnostic log). [after] runs once it is saved, or at once when
     * nothing changed and [afterIfUnchanged]. A page left before the save
     * finished, and showing that setting, is built again with it.
     */
    private fun commit(key: String, value: String, field: TextInputLayout?, afterIfUnchanged: Boolean = false, after: () -> Unit = {}) {
        val v = value.trim()
        val old = when (key) { "signal" -> signal; "signal-pin" -> pin; "relay" -> if (relay) "on" else "off"; else -> name }
        if (v == old) { field?.error = null; if (afterIfUnchanged) after(); return }
        if (inflight[key] == v) return
        inflight[key] = v
        bg.execute {
            val out = runCatching { JSONObject(Remohopcore.setSettingJSON(key, v)) }
            val s = runCatching { JSONObject(Remohopcore.settingsJSON()) }.getOrNull()
            ui {
                if (inflight[key] == v) inflight.remove(key)
                val r = out.getOrNull()
                if (r == null || !r.optBoolean("ok")) {
                    Diag.log("setting $key not saved: ${r?.optString("code")} ${r?.optString("error") ?: out.exceptionOrNull()?.message}")
                    val err = getString(when (r?.optString("code")) {
                        "bad_address" -> R.string.set_err_bad_address
                        "bad_key" -> R.string.set_err_bad_key
                        "bad_name", "bad_value" -> R.string.set_err_bad_name
                        else -> R.string.set_err_failed
                    })
                    if (field != null && field.isAttachedToWindow) field.error = err else snack(err)
                    return@ui
                }
                field?.error = null
                if (s != null) {
                    signal = s.optString("signal"); pin = s.optString("signal-pin")
                    relay = s.optString("relay", "on") != "off"; name = s.optString("name")
                }
                if (key == "signal" || key == "signal-pin") { rendezvousStatus = null; rendezvousKey = "" }
                Diag.log("setting $key changed")
                snack(R.string.saved_short)
                after()
                // Saved after its page was left (back, or another page):
                // the page now shown may display the old value.
                if (field != null && !field.isAttachedToWindow) {
                    val top = stack.lastOrNull()
                    if (top == null || top == PAGE_RENDEZVOUS) show()
                }
            }
        }
    }

    private fun rendezvous(col: LinearLayout) {
        pageTitle(R.string.rendezvous_heading)
        info = getString(R.string.rendezvous_heading) to listOf(
            "" to getString(R.string.info_rendezvous),
            getString(R.string.rendezvous_key) to getString(R.string.info_rendezvous_key),
            getString(R.string.relay_short) to getString(R.string.info_relay),
            getString(R.string.info_admin_title) to getString(R.string.info_rendezvous_admin),
        )
        val c = ui.section(col, getString(R.string.server), SettingsUi.Tone.PRIMARY)
        var testRow: SettingsUi.Row? = null
        val check = {
            val row = testRow
            if (RendezvousCheck.AVAILABLE && row != null && signal.isEmpty()) row.summary(getString(R.string.rv_enter_address))
            if (RendezvousCheck.AVAILABLE && row != null && signal.isNotEmpty()) {
                row.summary(getString(R.string.checking))
                net.execute {
                    val r = RendezvousCheck.check()
                    ui {
                        rendezvousStatus = r?.status
                        row.summary(statusText(r?.status))
                        val k = r?.key.orEmpty()
                        // Offer the presented key once it is known (the page is built again to show it).
                        if (k != rendezvousKey) { rendezvousKey = k; if (stack.lastOrNull() == PAGE_RENDEZVOUS && pin.isBlank() && k.isNotEmpty()) show() }
                    }
                }
            }
        }
        lateinit var addr: TextInputLayout
        lateinit var key: TextInputLayout
        // A new server address clears the key of the old one (the core does that).
        addr = ui.field(c, getString(R.string.server_address), signal, getString(R.string.rendezvous_example), InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_URI, R.id.field_signal) {
            commit("signal", it, addr) { key.editText?.setText(pin); check() }
        }
        key = ui.field(c, getString(R.string.rendezvous_key), pin, getString(R.string.optional), InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_FLAG_NO_SUGGESTIONS, R.id.field_signal_pin) {
            commit("signal-pin", it, key) { check() }
        }
        if (RendezvousCheck.AVAILABLE) {
            // Tests what is typed, not what was saved before: a tap on a row
            // does not take the focus, so the fields are saved first.
            testRow = ui.row(c, getString(R.string.test_connection), summary = rendezvousStatus?.let { statusText(it) } ?: "", icon = R.drawable.ic_s_network) {
                val a = addr.editText?.text.toString()
                val k = key.editText?.text.toString()
                if (a.trim() != signal) commit("signal", a, addr) { key.editText?.setText(pin); check() }
                else commit("signal-pin", k, key, afterIfUnchanged = true) { check() }
            }
        }
        // No key: the first connection trusts whichever server answers (trust
        // on first use). Say so, and offer the key the server presented.
        if (signal.isNotEmpty() && pin.isBlank()) {
            // Short here; the whole story is in the ⓘ sheet.
            info = info?.let { (t, items) -> t to items + (getString(R.string.rv_no_key_title) to getString(R.string.rv_no_key_msg)) }
            val w = ui.notice(col, R.drawable.ic_info, getString(R.string.rv_no_key_title), getString(R.string.rv_no_key_short), SettingsUi.Tone.TERTIARY)
            if (rendezvousStatus == RendezvousStatus.OK && rendezvousKey.isNotEmpty()) {
                // The presented key, to compare, and a button to save it.
                w.addView(TextView(this).apply {
                    text = rendezvousKey
                    androidx.core.widget.TextViewCompat.setTextAppearance(this, resolve(M.attr.textAppearanceBodyMedium))
                    setTextColor(ui.color(M.attr.colorOnTertiaryContainer))
                    typeface = android.graphics.Typeface.MONOSPACE
                }, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply { topMargin = ui.dp(12); marginStart = ui.dp(40) })
                w.addView(ui.button(getString(R.string.rv_use_key), SettingsUi.ButtonStyle.FILLED, R.drawable.ic_s_key).apply {
                    setOnClickListener {
                        val k = rendezvousKey
                        key.editText?.setText(k)
                        commit("signal-pin", k, key) { snack(R.string.rv_key_used); show() }
                    }
                }, LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply { topMargin = ui.dp(12); marginStart = ui.dp(40) })
            }
        }
        val r = ui.section(col, null)
        ui.switchRow(r, getString(R.string.relay_short), relay, R.drawable.ic_send) { on -> commit("relay", if (on) "on" else "off", null) }
        if (rendezvousStatus == null) check()
    }

    private fun statusText(s: RendezvousStatus?) = getString(when (s) {
        RendezvousStatus.OK -> R.string.rv_ok
        RendezvousStatus.IS_HOST -> R.string.rv_wrong_protocol
        RendezvousStatus.WRONG_PROTOCOL -> R.string.rv_not_server
        RendezvousStatus.UNREACHABLE -> R.string.rv_unreachable
        RendezvousStatus.KEY_MISMATCH -> R.string.rv_key_mismatch
        else -> R.string.rv_failed
    })

    private fun deviceName(col: LinearLayout) {
        pageTitle(R.string.device_name)
        info = getString(R.string.device_name) to listOf("" to getString(R.string.info_device_name))
        val c = ui.section(col, null)
        lateinit var f: TextInputLayout
        f = ui.field(c, getString(R.string.device_name), name, Diag.device(), id = R.id.field_name) { commit("name", it, f) }
        f.counterMaxLength = 64
        f.isCounterEnabled = true
        f.editText?.filters = arrayOf(android.text.InputFilter.LengthFilter(64))
        f.editText?.requestFocus()
    }

    private fun identity(col: LinearLayout) {
        pageTitle(R.string.identity_heading)
        info = getString(R.string.identity_heading) to listOf(
            "" to getString(R.string.info_identity),
            getString(R.string.key_line) to getString(R.string.info_key_line),
        )
        ui.text(ui.section(col, getString(R.string.fingerprint)), fingerprint, mono = true)
        ui.row(ui.section(col, null), getString(R.string.copy_key), icon = R.drawable.ic_copy) { copyKey() }
    }

    private fun copyKey() = bg.execute {
        val line = runCatching { Remohopcore.publicKeyLine() }
        ui {
            line.onSuccess {
                getSystemService(ClipboardManager::class.java)?.setPrimaryClip(ClipData.newPlainText("RemoHop key", it))
                // Android 13+ shows its own clipboard confirmation.
                if (Build.VERSION.SDK_INT < 33) snack(R.string.copied)
            }.onFailure { snack(it.message ?: "") }
        }
    }

    private fun presence(p: String): String = when (p) {
        "online" -> getString(R.string.presence_online)
        "offline" -> getString(R.string.presence_offline)
        "unknown" -> getString(R.string.presence_unknown)
        "not_checked" -> getString(R.string.presence_not_checked)
        "rate_limited" -> getString(R.string.presence_rate_limited)
        "error" -> getString(R.string.presence_error)
        else -> p.replace('_', ' ')
    }

    private fun login(l: String): String = when (l) {
        "pairing" -> getString(R.string.login_pairing)
        "key" -> getString(R.string.login_key)
        "password" -> getString(R.string.login_password)
        "saved_password" -> getString(R.string.login_saved_password)
        else -> l
    }

    private fun hostList(): List<SavedHost> = (0 until hosts.length()).map { SavedHost.of(hosts.getJSONObject(it)) }

    private fun kindSub(h: SavedHost) = getString(when (h.kind) {
        SavedHost.Kind.PAIRED -> R.string.host_paired_sub
        SavedHost.Kind.KEY -> R.string.host_key_sub
        SavedHost.Kind.SAVED_PASSWORD -> R.string.host_savedpw_sub
        SavedHost.Kind.RECENT -> R.string.host_recent_sub
    })

    /** The host's login as a badge: paired, key or saved password (no password to type), or needs the password. */
    private fun kindBadge(h: SavedHost) = when (h.kind) {
        SavedHost.Kind.PAIRED -> ui.badge(getString(R.string.badge_paired), SettingsUi.Tone.PRIMARY)
        SavedHost.Kind.KEY -> ui.badge(getString(R.string.badge_key), SettingsUi.Tone.PRIMARY)
        SavedHost.Kind.SAVED_PASSWORD -> ui.badge(getString(R.string.badge_saved_password), SettingsUi.Tone.PRIMARY)
        SavedHost.Kind.RECENT -> ui.badge(getString(R.string.badge_needs_password), SettingsUi.Tone.NEUTRAL)
    }

    /** Hosts that connect without a typed password in the primary role, the others neutral. */
    private fun hostTone(h: SavedHost) = if (h.ready) SettingsUi.Tone.PRIMARY else SettingsUi.Tone.NEUTRAL

    /** "5 min. ago", "3 days ago" in the app's language (not the system's); a date after a week. */
    private fun lastSeen(h: SavedHost): String {
        if (h.lastSeen <= 0) return ""
        val loc = resources.configuration.locales[0]
        val mins = (System.currentTimeMillis() / 1000 - h.lastSeen) / 60
        if (mins >= 7 * 24 * 60) return java.text.DateFormat.getDateInstance(java.text.DateFormat.MEDIUM, loc).format(java.util.Date(h.lastSeen * 1000))
        val f = android.icu.text.RelativeDateTimeFormatter.getInstance(android.icu.util.ULocale.forLocale(loc), null,
            android.icu.text.RelativeDateTimeFormatter.Style.SHORT, android.icu.text.DisplayContext.CAPITALIZATION_FOR_STANDALONE)
        val last = android.icu.text.RelativeDateTimeFormatter.Direction.LAST
        return when {
            mins < 1 -> f.format(android.icu.text.RelativeDateTimeFormatter.Direction.PLAIN, android.icu.text.RelativeDateTimeFormatter.AbsoluteUnit.NOW)
            mins < 60 -> f.format(mins.toDouble(), last, android.icu.text.RelativeDateTimeFormatter.RelativeUnit.MINUTES)
            mins < 24 * 60 -> f.format((mins / 60).toDouble(), last, android.icu.text.RelativeDateTimeFormatter.RelativeUnit.HOURS)
            else -> f.format((mins / (24 * 60)).toDouble(), last, android.icu.text.RelativeDateTimeFormatter.RelativeUnit.DAYS)
        }
    }

    private fun savedHosts(col: LinearLayout) {
        pageTitle(R.string.hosts_heading)
        info = getString(R.string.hosts_heading) to listOf("" to getString(R.string.info_hosts))
        val list = hostList()
        if (list.isEmpty()) {
            // Empty state: what it means, and the next step.
            val e = ui.notice(col, R.drawable.ic_s_host, getString(R.string.no_hosts), getString(R.string.no_hosts_next), SettingsUi.Tone.SECONDARY)
            e.addView(ui.button(getString(R.string.pair_heading), SettingsUi.ButtonStyle.FILLED, R.drawable.ic_s_link).apply {
                setOnClickListener { open(PAGE_PAIR) }
            }, LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply { topMargin = ui.dp(12); marginStart = ui.dp(40) })
        } else {
            val c = ui.section(col, null)
            for (h in list) {
                // Every row opens the host's page: name, address, status, Connect, Forget.
                val online = h.presence.takeIf { it != "not_checked" && it.isNotEmpty() }?.let { presence(it) }
                val sum = listOfNotNull(h.target.takeIf { h.alias.isNotEmpty() }, online, lastSeen(h).ifEmpty { null }).joinToString(" · ")
                val row = ui.row(c, h.label, summary = sum.ifEmpty { null }, leading = ui.avatar(h.label, hostTone(h)), trailing = kindBadge(h)) { open(PAGE_HOST + h.target) }
                row.title.maxLines = 1
                row.title.ellipsize = android.text.TextUtils.TruncateAt.END
                row.view.contentDescription = listOf(h.label, kindSub(h), sum).filter { it.isNotEmpty() }.joinToString(", ")
            }
            ui.row(ui.section(col, null), getString(R.string.pair_heading), icon = R.drawable.ic_s_link, chevron = true, tone = SettingsUi.Tone.PRIMARY) { open(PAGE_PAIR) }
        }
    }

    private fun refreshHosts(online: Boolean) {
        if (online) snack(R.string.checking)
        (if (online) net else bg).execute {
            val list = runCatching { JSONArray(Remohopcore.devicesJSON(online)) }
            ui {
                list.onSuccess { hosts = it }.onFailure { Diag.log("saved hosts: ${it.message}"); snack(R.string.rv_failed) }
                val top = stack.lastOrNull()
                if (top == PAGE_HOSTS || top == null || top.startsWith(PAGE_HOST)) show()
            }
        }
    }

    /** One saved host: its name (saved when the field is left), ID or address, status, Connect and Forget. */
    private fun hostDetail(col: LinearLayout, target: String) {
        val h = hostList().firstOrNull { it.target == target }
        if (h == null && !loaded) {
            // Recreated: the saved hosts are still being read (load() builds the page again).
            pageTitle("")
            return
        }
        if (h == null) {
            // Forgotten meanwhile: back to the list.
            stack.removeAt(stack.size - 1)
            savedHosts(col)
            return
        }
        pageTitle(h.label)
        info = h.label to listOfNotNull(getString(R.string.pair_alias) to getString(R.string.info_host_name),
            (getString(R.string.savedpw_heading) to getString(R.string.info_host_savedpw)).takeIf { h.kind == SavedHost.Kind.SAVED_PASSWORD },
            getString(R.string.host_forget) to getString(R.string.info_host_forget))
        // Who it is at a glance, and the main action: Connect.
        val badges = listOfNotNull(kindBadge(h), h.presence.takeIf { it == "online" || it == "offline" }?.let {
            ui.badge(presence(it).replaceFirstChar { c -> c.uppercaseChar() }, if (it == "online") SettingsUi.Tone.SECONDARY else SettingsUi.Tone.NEUTRAL)
        })
        ui.header(col, "", if (h.alias.isNotEmpty()) h.target else "", ui.avatar(h.label, hostTone(h), 88), badges)
        val connectBtn = ui.button(getString(R.string.host_connect), SettingsUi.ButtonStyle.FILLED, R.drawable.ic_s_link).apply {
            // The page connects (a paired host) or asks for the password (a recent one).
            setOnClickListener { PendingPage.connect(target); finish() }
        }
        col.addView(connectBtn, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply {
            marginStart = ui.dp(16); marginEnd = ui.dp(16); topMargin = ui.dp(8)
        })
        val c = ui.section(col, null)
        lateinit var f: TextInputLayout
        f = ui.field(c, getString(R.string.pair_alias), h.alias, h.target, InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_FLAG_NO_SUGGESTIONS, R.id.field_host_alias) { v ->
            val name = v.trim()
            if (name == h.alias) { f.error = null; return@field }
            bg.execute {
                val err = runCatching { Remohopcore.setAlias(target, name) }.exceptionOrNull()
                ui {
                    if (err != null) { f.error = getString(R.string.host_name_invalid); return@ui }
                    f.error = null
                    Diag.log("saved host renamed")
                    PendingPage.peersChanged()
                    snack(R.string.saved_short)
                    bg.execute {
                        val list = runCatching { JSONArray(Remohopcore.devicesJSON(false)) }.getOrNull()
                        ui {
                            if (list != null) hosts = list
                            val top = stack.lastOrNull()
                            if (top == PAGE_HOST + target) pageTitle(name.ifEmpty { target })
                            else if (top == PAGE_HOSTS || top == null) show() // left meanwhile: the list shows the new name
                        }
                    }
                }
            }
        }
        f.counterMaxLength = 63
        f.isCounterEnabled = true
        val d = ui.section(col, null)
        val idLabel = getString(if (h.isId) R.string.host_id else R.string.host_address)
        val idRow = ui.row(d, idLabel, summary = h.target, icon = R.drawable.ic_copy) {
            getSystemService(ClipboardManager::class.java)?.setPrimaryClip(ClipData.newPlainText(idLabel, h.target))
            if (Build.VERSION.SDK_INT < 33) snack(R.string.copied)
        }
        idRow.view.contentDescription = getString(R.string.host_copy, h.target)
        ui.row(d, getString(R.string.host_status), summary = getString(when (h.kind) {
            SavedHost.Kind.PAIRED -> R.string.host_status_paired
            SavedHost.Kind.KEY -> R.string.host_status_key
            SavedHost.Kind.SAVED_PASSWORD -> R.string.host_status_savedpw
            SavedHost.Kind.RECENT -> R.string.host_status_recent
        }), icon = R.drawable.ic_s_key)
        if (h.lastSeen > 0) {
            val whenText = android.text.format.DateUtils.getRelativeDateTimeString(this, h.lastSeen * 1000,
                android.text.format.DateUtils.MINUTE_IN_MILLIS, android.text.format.DateUtils.WEEK_IN_MILLIS, 0).toString()
            ui.row(d, getString(R.string.host_last_seen), summary = whenText, icon = R.drawable.ic_refresh)
        }
        if (h.presence.isNotEmpty() && h.presence !in setOf("not_checked", "online", "offline")) {
            ui.row(d, getString(R.string.host_online), summary = presence(h.presence), icon = R.drawable.ic_s_network)
        }
        if (h.kind == SavedHost.Kind.SAVED_PASSWORD) savedPassword(col, h)
        val a = ui.section(col, null)
        ui.row(a, getString(R.string.host_forget), icon = R.drawable.ic_delete, danger = true) { forget(target, h.label) }
    }

    /**
     * The password saved for a host that does not pair: masked, with an eye
     * to show it (monospaced) and hide it again, Copy, and Delete. It is read
     * from the core only on a tap, lives only in this page's views, is
     * hidden again when the app leaves the screen ([hidePassword]) and is
     * never logged.
     */
    private fun savedPassword(col: LinearLayout, h: SavedHost) {
        val s = ui.section(col, getString(R.string.savedpw_heading))
        val eye = android.widget.ImageButton(this).apply {
            setImageResource(R.drawable.ic_eye)
            imageTintList = android.content.res.ColorStateList.valueOf(ui.color(M.attr.colorOnSurfaceVariant))
            val ripple = android.util.TypedValue()
            theme.resolveAttribute(android.R.attr.selectableItemBackgroundBorderless, ripple, true)
            setBackgroundResource(ripple.resourceId)
            contentDescription = getString(R.string.savedpw_show)
            minimumWidth = ui.dp(48)
            minimumHeight = ui.dp(48)
        }
        val row = ui.row(s, MASKED_PASSWORD, icon = R.drawable.ic_s_key, trailing = eye)
        var shown = false
        val hide = {
            shown = false
            row.title.text = MASKED_PASSWORD
            row.title.contentDescription = getString(R.string.savedpw_heading)
            row.title.typeface = android.graphics.Typeface.DEFAULT
            row.title.setTextIsSelectable(false)
            eye.setImageResource(R.drawable.ic_eye)
            eye.contentDescription = getString(R.string.savedpw_show)
        }
        hide()
        hidePassword = hide
        eye.setOnClickListener {
            if (shown) { hide(); return@setOnClickListener }
            readSavedPassword(h.target) { pw ->
                if (hidePassword !== hide) return@readSavedPassword // the page was built again meanwhile
                shown = true
                row.title.text = pw
                row.title.contentDescription = null
                row.title.typeface = android.graphics.Typeface.MONOSPACE
                row.title.setTextIsSelectable(true)
                eye.setImageResource(R.drawable.ic_eye_off)
                eye.contentDescription = getString(R.string.savedpw_hide)
            }
        }
        ui.row(s, getString(R.string.savedpw_copy), icon = R.drawable.ic_copy) {
            readSavedPassword(h.target) { pw ->
                val clip = ClipData.newPlainText(getString(R.string.savedpw_heading), pw)
                // Android 13+ keeps it out of the clipboard preview.
                clip.description.extras = android.os.PersistableBundle().apply { putBoolean(EXTRA_IS_SENSITIVE, true) }
                getSystemService(ClipboardManager::class.java)?.setPrimaryClip(clip)
                if (Build.VERSION.SDK_INT < 33) snack(R.string.copied)
            }
        }
        ui.row(s, getString(R.string.savedpw_delete), icon = R.drawable.ic_delete, danger = true) { forgetSavedPassword(h.target, h.label) }
    }

    /** Reads the saved password off the UI thread; [use] gets it while this page is still shown. */
    private fun readSavedPassword(target: String, use: (String) -> Unit) = bg.execute {
        val pw = runCatching { Remohopcore.savedPassword(target) }
        ui {
            if (stack.lastOrNull() != PAGE_HOST + target) return@ui
            pw.onSuccess(use).onFailure { Diag.log("saved password not read"); snack(R.string.savedpw_read_failed) }
        }
    }

    private fun forgetSavedPassword(target: String, label: String) {
        val d = MaterialAlertDialogBuilder(this)
            .setTitle(R.string.savedpw_delete_title)
            .setMessage(getString(R.string.savedpw_delete_msg, label))
            .setNegativeButton(R.string.cancel, null)
            .setPositiveButton(R.string.savedpw_delete_button) { _, _ ->
                bg.execute {
                    val ok = runCatching { Remohopcore.forgetSavedPassword(target) }.isSuccess
                    ui {
                        Diag.log(if (ok) "saved password deleted" else "saved password not deleted")
                        snack(if (ok) R.string.savedpw_deleted else R.string.savedpw_delete_failed)
                        PendingPage.peersChanged()
                        refreshHosts(false)
                    }
                }
            }.show()
        // Destructive: the confirming text button in the error colour (M3).
        d.getButton(android.content.DialogInterface.BUTTON_POSITIVE)?.setTextColor(ui.color(M.attr.colorError))
    }

    private fun forget(target: String, label: String) {
        val d = MaterialAlertDialogBuilder(this)
            .setTitle(getString(R.string.host_forget_title, label))
            .setMessage(R.string.host_forget_msg)
            .setNegativeButton(R.string.cancel, null)
            .setPositiveButton(R.string.forget) { _, _ ->
                bg.execute {
                    runCatching { Remohopcore.forget(target) }
                    ui {
                        Diag.log("saved host forgotten")
                        if (stack.lastOrNull() == PAGE_HOST + target) stack.removeAt(stack.size - 1)
                        PendingPage.peersChanged()
                        refreshHosts(false)
                    }
                }
            }.show()
        // Destructive: the confirming text button in the error colour (M3).
        d.getButton(android.content.DialogInterface.BUTTON_POSITIVE)?.setTextColor(ui.color(M.attr.colorError))
    }

    /** The pairing failure's code: the reason of a request turned down, else the code. */
    private fun pairCode(r: JSONObject) = r.optString("reason").ifEmpty { r.optString("code") }

    /**
     * A pairing failure in the app's words, in either language. The core's
     * own text is English and may name CLI commands: it goes to the
     * diagnostic log only.
     */
    private fun pairError(r: JSONObject): String = getString(when (pairCode(r)) {
        "bad_password" -> R.string.pair_err_bad_password
        "unknown_id" -> R.string.pair_err_unknown_id
        "host_offline" -> R.string.pair_err_host_offline
        "no_path" -> R.string.pair_err_no_path
        "no_rendezvous" -> R.string.pair_err_no_rendezvous
        "host_key_changed" -> R.string.pair_err_host_key_changed
        "denied" -> R.string.pair_err_denied
        "locked_out" -> R.string.pair_err_locked_out
        "password_required" -> R.string.pair_err_password_required
        "timeout" -> R.string.pair_err_timeout
        "canceled" -> R.string.pair_err_canceled
        "target_required" -> R.string.pair_err_target_required
        "bad_target" -> R.string.pair_err_bad_target
        "bad_alias" -> R.string.host_name_invalid
        "already_pairing" -> R.string.pair_err_already_pairing
        "host_unreachable" -> R.string.pair_err_host_unreachable
        "cannot_resolve" -> R.string.pair_err_cannot_resolve
        "rendezvous_unreachable" -> R.string.pair_err_rendezvous_unreachable
        "rendezvous_rate_limited" -> R.string.pair_err_rendezvous_rate_limited
        "signal_is_host" -> R.string.pair_err_signal_is_host
        "signal_wrong_protocol" -> R.string.pair_err_signal_wrong_protocol
        "host_busy" -> R.string.pair_err_host_busy
        "key_rejected" -> R.string.pair_err_key_rejected
        "pairing_rejected" -> R.string.pair_err_pairing_rejected
        "password_login_disabled" -> R.string.pair_err_password_login_disabled
        else -> R.string.pair_err_failed
    })

    /** Failures fixed in Settings → Rendezvous server: the pair page offers "Set up". */
    private fun rendezvousProblem(code: String) =
        code in setOf("no_rendezvous", "rendezvous_unreachable", "signal_is_host", "signal_wrong_protocol")

    private fun pair(col: LinearLayout) {
        pageTitle(R.string.pair_heading)
        info = getString(R.string.pair_heading) to listOf(
            "" to getString(R.string.info_pair),
            getString(R.string.pair_target) to getString(R.string.info_pair_target),
        )
        val c = ui.section(col, null)
        val target = ui.field(c, getString(R.string.pair_target), "", "", InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_URI, R.id.field_pair_target) {}
        val password = ui.field(c, getString(R.string.pair_password), "", "", InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_PASSWORD, R.id.field_pair_password) {}
        password.endIconMode = TextInputLayout.END_ICON_PASSWORD_TOGGLE
        val alias = ui.field(c, getString(R.string.pair_alias), "", getString(R.string.optional), InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_FLAG_NO_SUGGESTIONS, R.id.field_pair_alias) {}
        alias.helperText = getString(R.string.optional)
        val actions = ui.buttons(c)
        val pairBtn = ui.button(getString(R.string.pair), SettingsUi.ButtonStyle.FILLED, R.drawable.ic_s_link)
        val cancelBtn = ui.button(getString(R.string.cancel), SettingsUi.ButtonStyle.OUTLINED).apply { visibility = View.GONE }
        val progress = CircularProgressIndicator(this).apply { isIndeterminate = true; visibility = View.GONE; indicatorSize = ui.dp(24) }
        actions.addView(pairBtn)
        actions.addView(cancelBtn, LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply { marginStart = ui.dp(8) })
        actions.addView(progress, LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply { marginStart = ui.dp(16) })
        // "Set up" where the rendezvous server is what is missing or wrong.
        val setUp = ui.button(getString(R.string.set_up), SettingsUi.ButtonStyle.TONAL, R.drawable.ic_s_network).apply {
            visibility = View.GONE
            setOnClickListener { open(PAGE_RENDEZVOUS) }
        }
        actions.addView(setUp, LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply { marginStart = ui.dp(8) })
        val status = ui.text(c, "")
        status.visibility = View.GONE
        cancelBtn.setOnClickListener { PairJob.cancel() }
        // The pairing lives in PairJob: this page shows it running, or its outcome.
        if (PairJob.running) {
            status.visibility = View.VISIBLE
            status.setTextColor(ui.color(M.attr.colorOnSurfaceVariant))
            status.text = getString(R.string.pairing)
            pairBtn.isEnabled = false
            cancelBtn.visibility = View.VISIBLE
            progress.visibility = View.VISIBLE
        } else PairJob.result?.let { out ->
            status.visibility = View.VISIBLE
            out.onSuccess { r ->
                val ok = r.optBoolean("ok")
                // Words first; the colour only repeats them.
                status.setTextColor(ui.color(if (ok) M.attr.colorPrimary else M.attr.colorError))
                status.text = if (ok) getString(R.string.paired_short, r.optString("target")) else pairError(r)
                if (!ok && rendezvousProblem(pairCode(r))) setUp.visibility = View.VISIBLE
                if (!ok && pairCode(r) == "target_required") target.error = getString(R.string.pair_target_required)
                if (ok) bg.execute {
                    val list = runCatching { JSONArray(Remohopcore.devicesJSON(false)) }.getOrNull()
                    ui { if (list != null) hosts = list }
                }
            }.onFailure { status.setTextColor(ui.color(M.attr.colorError)); status.text = getString(R.string.pair_err_failed) }
        }
        pairBtn.setOnClickListener {
            val t = target.editText?.text.toString().trim()
            if (t.isEmpty()) { target.error = getString(R.string.pair_target_required); target.editText?.requestFocus(); return@setOnClickListener }
            target.error = null
            val pw = password.editText?.text.toString()
            password.editText?.text?.clear()
            if (PairJob.start(t, pw, alias.editText?.text.toString())) show()
        }
    }

    private fun diagnostics(col: LinearLayout) {
        pageTitle(R.string.diag_title)
        info = getString(R.string.diag_title) to listOf(
            getString(R.string.diag_what_title) to getString(R.string.info_diag_what),
            getString(R.string.diag_removed_title) to getString(R.string.info_diag_removed),
            getString(R.string.diag_who_title) to getString(R.string.info_diag_who),
        )
        val on = Diag.enabled
        val c = ui.section(col, null)
        lateinit var sw: MaterialSwitch
        sw = ui.switchRow(c, getString(R.string.diag_title), on, R.drawable.ic_s_diag) { v ->
            runCatching { Diag.setEnabled(v) }.onFailure { snack(it.message ?: "") }
            SettingsUi.setQuietly(sw, Diag.enabled)
            show()
        }
        if (on) ui.row(c, getString(R.string.diag_size), value = AppInfo.size(Diag.size))
        val size = Diag.size
        val s = ui.section(col, null)
        val share = ui.row(s, getString(R.string.diag_share), icon = R.drawable.ic_share) {
            report()?.let { Diag.share(this, it, getString(R.string.diag_share)) }
        }
        val email = ui.row(s, getString(R.string.diag_email), icon = R.drawable.ic_mail) {
            report()?.let {
                val body = getString(R.string.diag_email_body) + "\n\n\nRemoHop ${BuildConfig.VERSION_NAME} (${BuildConfig.VERSION_CODE}), Android ${Build.VERSION.RELEASE}"
                Diag.email(this, it, getString(R.string.diag_email_subject), body, getString(R.string.diag_email))
            }
        }
        val clear = ui.row(ui.section(col, null), getString(R.string.diag_clear), icon = R.drawable.ic_delete, danger = true) {
            MaterialAlertDialogBuilder(this).setTitle(R.string.diag_clear).setMessage(R.string.diag_clear_confirm)
                .setNegativeButton(R.string.cancel, null)
                .setPositiveButton(R.string.diag_clear) { _, _ -> runCatching { Diag.clear() }; show() }
                .show()
        }
        // Disabled (not hidden) while there is nothing to share.
        val has = on || size > 0
        for (r in listOf(share, email, clear)) r.enabled(has)
    }

    // Always plain text: readable anywhere, the format support asks for.
    private fun report() = runCatching { Diag.export(this, "txt") }
        .onFailure { snack(it.message ?: "") }.getOrNull()

    private fun privacy(col: LinearLayout) {
        pageTitle(R.string.privacy_title)
        info = getString(R.string.privacy_title) to listOf(
            getString(R.string.privacy_talks_title) to getString(R.string.info_privacy_talks),
            getString(R.string.privacy_gateway_title) to getString(R.string.info_privacy_gateway),
            getString(R.string.diag_title) to getString(R.string.info_privacy_diag),
        )
        val c = ui.section(col, null, SettingsUi.Tone.PRIMARY)
        ui.row(c, getString(R.string.privacy_no_tracking), icon = R.drawable.ic_s_privacy)
        ui.row(c, getString(R.string.privacy_on_device), icon = R.drawable.ic_s_device)
        ui.row(c, getString(R.string.privacy_e2e), icon = R.drawable.ic_s_key)
        ui.row(c, getString(R.string.privacy_diag), icon = R.drawable.ic_s_diag)
        val l = ui.section(col, null)
        ui.row(l, getString(R.string.privacy_policy), icon = R.drawable.ic_open) {
            Diag.open(this, AppInfo.privacyPolicy(Lang.effective(this)))
        }
        ui.row(l, getString(R.string.privacy_questions), summary = AppInfo.PRIVACY_EMAIL, icon = R.drawable.ic_mail) {
            Diag.open(this, "mailto:" + AppInfo.PRIVACY_EMAIL)
        }
    }

    private fun about(col: LinearLayout) {
        pageTitle(R.string.about_title)
        ui.header(col, getString(R.string.app_name), getString(R.string.about_version, BuildConfig.VERSION_NAME, BuildConfig.VERSION_CODE))
        var c = ui.section(col, null)
        ui.row(c, getString(R.string.about_version_label), value = BuildConfig.VERSION_NAME)
        ui.row(c, getString(R.string.about_build), value = BuildConfig.VERSION_CODE.toString())
        ui.row(c, getString(R.string.about_core), summary = Diag.coreVersion)
        c = ui.section(col, null)
        val lang = Lang.effective(this)
        ui.row(c, getString(R.string.about_website), icon = R.drawable.ic_s_language) { Diag.open(this, AppInfo.WEBSITE) }
        ui.row(c, getString(R.string.about_source), icon = R.drawable.ic_code) { Diag.open(this, AppInfo.SOURCE_CODE) }
        ui.row(c, getString(R.string.about_license), icon = R.drawable.ic_doc) { Diag.open(this, AppInfo.LICENSING) }
        ui.row(c, getString(R.string.about_notices), icon = R.drawable.ic_doc) {
            MaterialAlertDialogBuilder(this).setTitle(R.string.about_notices).setMessage(AppInfo.NOTICE)
                .setPositiveButton(R.string.done, null)
                .setNeutralButton(R.string.about_third_party) { _, _ -> Diag.open(this, AppInfo.THIRD_PARTY) }
                .show()
        }
        c = ui.section(col, getString(R.string.about_contact), SettingsUi.Tone.TERTIARY)
        ui.row(c, AppInfo.CONTACT_EMAIL, icon = R.drawable.ic_mail) { Diag.open(this, "mailto:" + AppInfo.CONTACT_EMAIL) }
        ui.row(c, getString(R.string.about_telegram), icon = R.drawable.ic_send) { Diag.open(this, AppInfo.TELEGRAM) }
        c = ui.section(col, null)
        ui.row(c, getString(R.string.about_terms), icon = R.drawable.ic_doc) { Diag.open(this, AppInfo.terms(lang)) }
        ui.row(c, getString(R.string.privacy_policy), icon = R.drawable.ic_s_privacy) { Diag.open(this, AppInfo.privacyPolicy(lang)) }
    }

    companion object {
        const val EXTRA_PAGE = "page"
        private const val STATE_STACK = "pages"
        private const val MENU_INFO = 1
        private const val MENU_REFRESH = 2
        private const val MASKED_PASSWORD = "••••••••"
        /** ClipDescription.EXTRA_IS_SENSITIVE (Android 13+; older versions ignore it). */
        private const val EXTRA_IS_SENSITIVE = "android.content.extra.IS_SENSITIVE"
        const val PAGE_RENDEZVOUS = "rendezvous"
        const val PAGE_NAME = "deviceName"
        const val PAGE_IDENTITY = "identity"
        const val PAGE_HOSTS = "hosts"
        const val PAGE_PAIR = "pair"
        const val PAGE_DIAGNOSTICS = "diagnostics"
        const val PAGE_PRIVACY = "privacy"
        const val PAGE_ABOUT = "about"
        /** Host mode: this phone viewed and controlled from other devices. */
        const val PAGE_THIS_DEVICE = "thisDevice"
        /** A saved host's page: this prefix and its target. */
        const val PAGE_HOST = "host:"

        /** Pages the web page may open directly. */
        val DIRECT_PAGES = setOf(PAGE_PRIVACY, PAGE_ABOUT, PAGE_DIAGNOSTICS, PAGE_RENDEZVOUS, PAGE_THIS_DEVICE)
    }
}
