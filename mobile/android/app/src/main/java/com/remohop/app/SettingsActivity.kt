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
import com.google.android.material.appbar.MaterialToolbar
import com.google.android.material.bottomsheet.BottomSheetDialog
import com.google.android.material.button.MaterialButton
import com.google.android.material.color.DynamicColors
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
 * Native settings in Material 3 (Material Components): a small top app bar
 * that lifts on scroll, list items grouped by task under subheaders with
 * dividers, M3 switches, outlined text fields, dialogs, bottom sheets and
 * snackbars; dynamic colour on Android 12+ and the cobalt scheme otherwise.
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
    private val bg = Executors.newSingleThreadExecutor()
    private lateinit var ui: SettingsUi
    private lateinit var root: CoordinatorLayout
    private lateinit var bar: AppBarLayout
    private lateinit var toolbar: MaterialToolbar
    private var content: View? = null
    private val stack = ArrayList<String>()
    private var info: Pair<String, List<Pair<String, String>>>? = null

    // What the core has, to save only changes.
    private var signal = ""
    private var pin = ""
    private var relay = true
    private var name = ""
    private var fingerprint = ""
    private var hosts = JSONArray()
    private var rendezvousStatus: RendezvousStatus? = null
    private var diagFormat = "txt"

    override fun attachBaseContext(newBase: Context) {
        super.attachBaseContext(Lang.wrap(newBase))
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        DynamicColors.applyToActivityIfAvailable(this)
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
        root = CoordinatorLayout(this)
        bar = AppBarLayout(this).apply {
            fitsSystemWindows = true
            isLiftOnScroll = true
            statusBarForeground = com.google.android.material.shape.MaterialShapeDrawable.createWithElevationOverlay(this@SettingsActivity)
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
        bar.addView(toolbar, AppBarLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
        root.addView(bar, CoordinatorLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
        setContentView(root)
        onBackPressedDispatcher.addCallback(this, object : OnBackPressedCallback(true) {
            override fun handleOnBackPressed() = back()
        })
        savedInstanceState?.getStringArrayList(STATE_STACK)?.let { stack.addAll(it) }
        if (stack.isEmpty()) intent.getStringExtra(EXTRA_PAGE)?.takeIf { it in DIRECT_PAGES }?.let { stack.add(it) }
        show()
        load { show() }
    }

    override fun onSaveInstanceState(out: Bundle) {
        super.onSaveInstanceState(out)
        out.putStringArrayList(STATE_STACK, stack)
    }

    override fun onDestroy() {
        bg.shutdownNow()
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
        currentFocus?.clearFocus()
        if (stack.isEmpty()) finish() else { stack.removeAt(stack.size - 1); show() }
    }

    private fun showInfo() {
        val (title, items) = info ?: return
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

    private fun resolve(attr: Int): Int {
        val tv = android.util.TypedValue()
        theme.resolveAttribute(attr, tv, true)
        return tv.resourceId
    }

    private fun show() {
        info = null
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
            else -> main(col)
        }
        content?.let { root.removeView(it) }
        content = scroll
        root.addView(scroll, CoordinatorLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT).apply {
            behavior = AppBarLayout.ScrollingViewBehavior()
        })
        bar.isLifted = false
        bar.liftOnScrollTargetViewId = View.generateViewId().also { scroll.id = it }
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

    private fun pageTitle(id: Int) { toolbar.title = getString(id); title = toolbar.title }

    private fun snack(text: CharSequence) = Snackbar.make(root, text, Snackbar.LENGTH_SHORT).show()
    private fun snack(id: Int) = snack(getString(id))

    // ---- pages ----

    private fun main(col: LinearLayout) {
        pageTitle(R.string.settings)
        var c = ui.section(col, getString(R.string.sec_connection))
        ui.row(c, getString(R.string.rendezvous_heading), summary = rendezvousSummary(), icon = R.drawable.ic_s_network, chevron = true) { open(PAGE_RENDEZVOUS) }
        c = ui.section(col, getString(R.string.device_heading))
        ui.row(c, getString(R.string.device_name), summary = name.ifEmpty { Diag.device() }, icon = R.drawable.ic_s_device, chevron = true) { open(PAGE_NAME) }
        ui.row(c, getString(R.string.identity_heading), summary = AppInfo.shortFingerprint(fingerprint), icon = R.drawable.ic_s_key, chevron = true) { open(PAGE_IDENTITY) }
        c = ui.section(col, getString(R.string.sec_hosts))
        ui.row(c, getString(R.string.hosts_heading), value = hosts.length().toString(), icon = R.drawable.ic_s_host, chevron = true) { open(PAGE_HOSTS) }
        ui.row(c, getString(R.string.pair_heading), icon = R.drawable.ic_s_link, chevron = true) { open(PAGE_PAIR) }
        c = ui.section(col, getString(R.string.sec_general))
        ui.row(c, getString(R.string.language_heading), summary = languageName(Lang.choice(this)), icon = R.drawable.ic_s_language) { chooseLanguage() }
        c = ui.section(col, getString(R.string.sec_support))
        ui.row(c, getString(R.string.diag_title), summary = getString(if (Diag.enabled) R.string.on else R.string.off), icon = R.drawable.ic_s_diag, chevron = true) { open(PAGE_DIAGNOSTICS) }
        ui.row(c, getString(R.string.privacy_title), icon = R.drawable.ic_s_privacy, chevron = true) { open(PAGE_PRIVACY) }
        ui.row(c, getString(R.string.about_title), summary = getString(R.string.about_version, BuildConfig.VERSION_NAME, BuildConfig.VERSION_CODE), icon = R.drawable.ic_s_info, chevron = true) { open(PAGE_ABOUT) }
    }

    private fun rendezvousSummary(): String = when {
        signal.isEmpty() -> getString(R.string.not_set)
        rendezvousStatus == RendezvousStatus.OK -> getString(R.string.value_ok, signal)
        rendezvousStatus != null -> getString(R.string.value_problem, signal)
        else -> signal
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

    /** Saves one setting if it changed; the core validates it, and an error stays on the field. */
    private fun commit(key: String, value: String, field: TextInputLayout?, after: () -> Unit = {}) {
        val v = value.trim()
        val old = when (key) { "signal" -> signal; "signal-pin" -> pin; "relay" -> if (relay) "on" else "off"; else -> name }
        if (v == old) { field?.error = null; return }
        bg.execute {
            val err = runCatching { Remohopcore.setSetting(key, v) }.exceptionOrNull()?.message
            val s = runCatching { JSONObject(Remohopcore.settingsJSON()) }.getOrNull()
            ui {
                if (err != null) {
                    if (field != null) field.error = err else snack(err)
                    return@ui
                }
                field?.error = null
                if (s != null) {
                    signal = s.optString("signal"); pin = s.optString("signal-pin")
                    relay = s.optString("relay", "on") != "off"; name = s.optString("name")
                }
                if (key == "signal" || key == "signal-pin") rendezvousStatus = null
                Diag.log("setting $key changed")
                snack(R.string.saved_short)
                after()
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
        val c = ui.section(col, getString(R.string.server))
        var testRow: SettingsUi.Row? = null
        val check = {
            val row = testRow
            if (RendezvousCheck.AVAILABLE && row != null && signal.isNotEmpty()) {
                row.summary(getString(R.string.checking))
                val s = signal
                val p = pin
                bg.execute {
                    val r = RendezvousCheck.run(s, p)
                    ui { rendezvousStatus = r; row.summary(statusText(r)) }
                }
            }
        }
        lateinit var addr: TextInputLayout
        lateinit var key: TextInputLayout
        // A new server address clears the key of the old one (the core does that).
        addr = ui.field(c, getString(R.string.server_address), signal, getString(R.string.rendezvous_example), InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_URI) {
            commit("signal", it, addr) { key.editText?.setText(pin); check() }
        }
        key = ui.field(c, getString(R.string.rendezvous_key), pin, getString(R.string.optional), InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_FLAG_NO_SUGGESTIONS) {
            commit("signal-pin", it, key) { check() }
        }
        if (RendezvousCheck.AVAILABLE) {
            testRow = ui.row(c, getString(R.string.test_connection), summary = rendezvousStatus?.let { statusText(it) } ?: "", icon = R.drawable.ic_s_network) { check() }
        }
        val r = ui.section(col, null)
        ui.switchRow(r, getString(R.string.relay_short), relay) { on -> commit("relay", if (on) "on" else "off", null) }
    }

    private fun statusText(s: RendezvousStatus?) = getString(when (s) {
        RendezvousStatus.OK -> R.string.rv_ok
        RendezvousStatus.WRONG_PROTOCOL -> R.string.rv_wrong_protocol
        RendezvousStatus.UNREACHABLE -> R.string.rv_unreachable
        RendezvousStatus.KEY_MISMATCH -> R.string.rv_key_mismatch
        else -> R.string.rv_failed
    })

    private fun deviceName(col: LinearLayout) {
        pageTitle(R.string.device_name)
        info = getString(R.string.device_name) to listOf("" to getString(R.string.info_device_name))
        val c = ui.section(col, null)
        lateinit var f: TextInputLayout
        f = ui.field(c, getString(R.string.device_name), name, Diag.device()) { commit("name", it, f) }
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
        else -> l
    }

    private fun savedHosts(col: LinearLayout) {
        pageTitle(R.string.hosts_heading)
        info = getString(R.string.hosts_heading) to listOf("" to getString(R.string.info_hosts))
        val c = ui.section(col, null)
        if (hosts.length() == 0) {
            // Empty state: what it means, and the next step.
            ui.text(c, getString(R.string.no_hosts))
        }
        for (i in 0 until hosts.length()) {
            val d = hosts.getJSONObject(i)
            val target = d.optString("target")
            val alias = d.optString("alias")
            val label = alias.ifEmpty { target }
            val p = d.optString("presence")
            val sum = listOfNotNull(target.takeIf { alias.isNotEmpty() }, login(d.optString("login")),
                presence(p).takeIf { p != "not_checked" && p.isNotEmpty() }).joinToString(" · ")
            val row = ui.row(c, label, summary = sum, icon = R.drawable.ic_s_host) { forget(target, label) }
            row.view.contentDescription = "$label, $sum. " + getString(R.string.forget_host, label)
        }
        ui.row(ui.section(col, null), getString(R.string.pair_heading), icon = R.drawable.ic_s_link, chevron = true) { open(PAGE_PAIR) }
    }

    private fun refreshHosts(online: Boolean) {
        if (online) snack(R.string.checking)
        bg.execute {
            val list = runCatching { JSONArray(Remohopcore.devicesJSON(online)) }
            ui {
                list.onSuccess { hosts = it }.onFailure { snack(it.message ?: "") }
                if (stack.lastOrNull() == PAGE_HOSTS || stack.isEmpty()) show()
            }
        }
    }

    private fun forget(target: String, label: String) {
        MaterialAlertDialogBuilder(this)
            .setTitle(getString(R.string.forget_host, label))
            .setMessage(getString(R.string.forget_confirm, label))
            .setNegativeButton(R.string.cancel, null)
            .setPositiveButton(R.string.forget) { _, _ ->
                bg.execute { runCatching { Remohopcore.forget(target) }; ui { refreshHosts(false) } }
            }.show()
    }

    /** A pairing failure in the app's words where its code is known (Chinese); else the core's message. */
    private fun pairError(r: JSONObject): String {
        val raw = r.optString("error")
        if (Lang.effective(this) != Lang.ZH) return raw
        val id = when (r.optString("code")) {
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
            else -> return raw
        }
        return getString(id)
    }

    private fun pair(col: LinearLayout) {
        pageTitle(R.string.pair_heading)
        info = getString(R.string.pair_heading) to listOf(
            "" to getString(R.string.info_pair),
            getString(R.string.pair_target) to getString(R.string.info_pair_target),
        )
        val c = ui.section(col, null)
        val target = ui.field(c, getString(R.string.pair_target), "", "", InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_URI) {}
        val password = ui.field(c, getString(R.string.pair_password), "", "", InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_PASSWORD) {}
        password.endIconMode = TextInputLayout.END_ICON_PASSWORD_TOGGLE
        val alias = ui.field(c, getString(R.string.pair_alias), "", getString(R.string.optional), InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_FLAG_NO_SUGGESTIONS) {}
        alias.helperText = getString(R.string.optional)
        val actions = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = android.view.Gravity.CENTER_VERTICAL
            setPadding(ui.dp(16), ui.dp(16), ui.dp(16), ui.dp(8))
        }
        val pairBtn = MaterialButton(this).apply { text = getString(R.string.pair) }
        val cancelBtn = MaterialButton(this, null, M.attr.materialButtonOutlinedStyle).apply { text = getString(R.string.cancel); visibility = View.GONE }
        val progress = CircularProgressIndicator(this).apply { isIndeterminate = true; visibility = View.GONE; indicatorSize = ui.dp(24) }
        actions.addView(pairBtn)
        actions.addView(cancelBtn, LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply { marginStart = ui.dp(8) })
        actions.addView(progress, LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply { marginStart = ui.dp(16) })
        c.addView(actions)
        val status = ui.text(c, "")
        status.visibility = View.GONE
        cancelBtn.setOnClickListener { Thread { Remohopcore.cancelPair() }.start() }
        pairBtn.setOnClickListener {
            val t = target.editText?.text.toString().trim()
            if (t.isEmpty()) { target.error = getString(R.string.pair_target_required); target.editText?.requestFocus(); return@setOnClickListener }
            target.error = null
            val pw = password.editText?.text.toString()
            password.editText?.text?.clear()
            status.visibility = View.VISIBLE
            status.setTextColor(ui.color(M.attr.colorOnSurfaceVariant))
            status.text = getString(R.string.pairing)
            pairBtn.isEnabled = false
            cancelBtn.visibility = View.VISIBLE
            progress.visibility = View.VISIBLE
            // Not on bg: a pairing may wait minutes for approval.
            Thread {
                val out = runCatching { JSONObject(Remohopcore.pairWithPassword(t, pw, alias.editText?.text.toString())) }
                ui {
                    pairBtn.isEnabled = true
                    cancelBtn.visibility = View.GONE
                    progress.visibility = View.GONE
                    out.onSuccess { r ->
                        val ok = r.optBoolean("ok")
                        // Words first; the colour only repeats them.
                        status.setTextColor(ui.color(if (ok) M.attr.colorPrimary else M.attr.colorError))
                        status.text = if (ok) getString(R.string.paired_short, r.optString("target")) else pairError(r)
                        Diag.log(if (ok) "paired with a host" else "pairing failed: ${r.optString("code")} ${r.optString("error")}")
                        if (ok) bg.execute {
                            val list = runCatching { JSONArray(Remohopcore.devicesJSON(false)) }.getOrNull()
                            ui { if (list != null) hosts = list }
                        }
                    }.onFailure { status.setTextColor(ui.color(M.attr.colorError)); status.text = it.message }
                }
            }.start()
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
        val fmt = ui.row(s, getString(R.string.diag_format), summary = diagFormat.uppercase(), icon = R.drawable.ic_doc) {}
        fmt.view.setOnClickListener {
            val opts = arrayOf("txt", "json")
            MaterialAlertDialogBuilder(this).setTitle(R.string.diag_format)
                .setSingleChoiceItems(arrayOf("TXT", "JSON"), opts.indexOf(diagFormat)) { d, i -> diagFormat = opts[i]; fmt.summary(diagFormat.uppercase()); d.dismiss() }
                .setNegativeButton(R.string.cancel, null)
                .show()
        }
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
        for (r in listOf(fmt, share, email, clear)) r.enabled(has)
    }

    private fun report() = runCatching { Diag.export(this, diagFormat) }
        .onFailure { snack(it.message ?: "") }.getOrNull()

    private fun privacy(col: LinearLayout) {
        pageTitle(R.string.privacy_title)
        info = getString(R.string.privacy_title) to listOf(
            getString(R.string.privacy_talks_title) to getString(R.string.info_privacy_talks),
            getString(R.string.privacy_gateway_title) to getString(R.string.info_privacy_gateway),
            getString(R.string.diag_title) to getString(R.string.info_privacy_diag),
        )
        val c = ui.section(col, null)
        ui.row(c, getString(R.string.privacy_no_tracking), icon = R.drawable.ic_s_privacy)
        ui.row(c, getString(R.string.privacy_on_device), icon = R.drawable.ic_s_device)
        ui.row(c, getString(R.string.privacy_e2e), icon = R.drawable.ic_s_key)
        ui.row(c, getString(R.string.privacy_diag), icon = R.drawable.ic_s_diag)
        ui.row(ui.section(col, null), getString(R.string.privacy_policy), icon = R.drawable.ic_open) {
            Diag.open(this, AppInfo.privacyPolicy(Lang.effective(this)))
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
        c = ui.section(col, getString(R.string.about_contact))
        ui.row(c, AppInfo.SUPPORT_EMAIL, icon = R.drawable.ic_mail) { Diag.open(this, "mailto:" + AppInfo.SUPPORT_EMAIL) }
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
        const val PAGE_RENDEZVOUS = "rendezvous"
        const val PAGE_NAME = "deviceName"
        const val PAGE_IDENTITY = "identity"
        const val PAGE_HOSTS = "hosts"
        const val PAGE_PAIR = "pair"
        const val PAGE_DIAGNOSTICS = "diagnostics"
        const val PAGE_PRIVACY = "privacy"
        const val PAGE_ABOUT = "about"

        /** Pages the web page may open directly. */
        val DIRECT_PAGES = setOf(PAGE_PRIVACY, PAGE_ABOUT, PAGE_DIAGNOSTICS, PAGE_RENDEZVOUS)
    }
}
