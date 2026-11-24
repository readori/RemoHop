package com.remohop.app

import android.annotation.SuppressLint
import android.app.Activity
import android.app.AlertDialog
import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.os.Build
import android.os.Bundle
import android.text.InputType
import android.view.Menu
import android.view.MenuItem
import android.view.View
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.ProgressBar
import android.widget.Switch
import android.widget.TextView
import android.widget.Toast
import android.window.OnBackInvokedDispatcher
import androidx.core.view.ViewCompat
import androidx.core.view.WindowInsetsCompat
import com.remohop.app.core.remohopcore.Remohopcore
import org.json.JSONArray
import org.json.JSONObject
import java.util.concurrent.Executors

/**
 * Native settings, laid out like the system's (Material 3 lists): grouped
 * sections of rows with a value or summary and a chevron into a detail
 * page. No paragraphs: every explanation is behind the ⓘ in the app bar of
 * the page it is about. Fields save themselves when they are left (no Save
 * buttons) and say "Saved". Connection settings take effect on the next
 * connection (the running gateway reads them per dial); the language at
 * once (see [Lang]).
 *
 * The pages live in this one activity, with their own back stack.
 */
class SettingsActivity : Activity() {
    private val bg = Executors.newSingleThreadExecutor()
    private lateinit var ui: SettingsUi
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
        super.onCreate(savedInstanceState)
        Core.init(this)
        ui = SettingsUi(this)
        actionBar?.setDisplayHomeAsUpEnabled(true)
        actionBar?.elevation = 0f
        if (Build.VERSION.SDK_INT >= 33) {
            onBackInvokedDispatcher.registerOnBackInvokedCallback(OnBackInvokedDispatcher.PRIORITY_DEFAULT) { back() }
        }
        savedInstanceState?.getStringArrayList(STATE_STACK)?.let { stack.addAll(it) }
        if (stack.isEmpty()) intent.getStringExtra(EXTRA_PAGE)?.takeIf { it in DIRECT_PAGES }?.let { stack.add(it) }
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

    // Only Android 12L and older call this (13+ uses the OnBackInvokedCallback registered in onCreate).
    @Deprecated("Android 12L and older; 13+ uses OnBackInvokedCallback")
    @SuppressLint("GestureBackNavigation")
    override fun onBackPressed() = back()

    override fun onOptionsItemSelected(item: MenuItem): Boolean {
        when (item.itemId) {
            android.R.id.home -> { back(); return true }
            MENU_INFO -> { showInfo(); return true }
            MENU_REFRESH -> { refreshHosts(true); return true }
        }
        return super.onOptionsItemSelected(item)
    }

    override fun onCreateOptionsMenu(menu: Menu): Boolean {
        if (stack.lastOrNull() == PAGE_HOSTS) {
            menu.add(Menu.NONE, MENU_REFRESH, Menu.NONE, R.string.hosts_check_online).setIcon(R.drawable.ic_refresh)
                .setShowAsAction(MenuItem.SHOW_AS_ACTION_IF_ROOM)
        }
        if (info != null) {
            menu.add(Menu.NONE, MENU_INFO, Menu.NONE, R.string.info_about_screen).setIcon(R.drawable.ic_info)
                .setShowAsAction(MenuItem.SHOW_AS_ACTION_ALWAYS)
        }
        return true
    }

    private fun showInfo() {
        val (title, items) = info ?: return
        val text = items.joinToString("\n\n") { (h, t) -> if (h.isEmpty()) t else "$h\n$t" }
        AlertDialog.Builder(this).setTitle(title).setMessage(text).setPositiveButton(R.string.done, null).show()
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
        setContentView(scroll)
        // Edge to edge (Android 15+): keep the last rows above the navigation bar.
        ViewCompat.setOnApplyWindowInsetsListener(scroll) { v, insets ->
            val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars() or WindowInsetsCompat.Type.ime())
            v.setPadding(bars.left, 0, bars.right, bars.bottom)
            insets
        }
        invalidateOptionsMenu()
    }

    private fun pageTitle(id: Int) { title = getString(id) }

    private fun saved() = Toast.makeText(this, R.string.saved_short, Toast.LENGTH_SHORT).show()

    // ---- pages ----

    private fun main(col: LinearLayout) {
        pageTitle(R.string.settings)
        var c = ui.section(col, getString(R.string.sec_connection))
        ui.row(c, getString(R.string.rendezvous_heading), value = rendezvousSummary(), icon = R.drawable.ic_s_network, chevron = true) { open(PAGE_RENDEZVOUS) }
        c = ui.section(col, getString(R.string.device_heading))
        ui.row(c, getString(R.string.device_name), value = name.ifEmpty { Diag.device() }, icon = R.drawable.ic_s_device, chevron = true) { open(PAGE_NAME) }
        ui.row(c, getString(R.string.identity_heading), value = AppInfo.shortFingerprint(fingerprint), icon = R.drawable.ic_s_key, chevron = true) { open(PAGE_IDENTITY) }
        c = ui.section(col, getString(R.string.sec_hosts))
        ui.row(c, getString(R.string.hosts_heading), value = hosts.length().toString(), icon = R.drawable.ic_s_host, chevron = true) { open(PAGE_HOSTS) }
        ui.row(c, getString(R.string.pair_heading), icon = R.drawable.ic_s_link, chevron = true) { open(PAGE_PAIR) }
        c = ui.section(col, getString(R.string.sec_general))
        ui.row(c, getString(R.string.language_heading), value = languageName(Lang.choice(this)), icon = R.drawable.ic_s_language, chevron = true) { chooseLanguage() }
        c = ui.section(col, getString(R.string.sec_support))
        ui.row(c, getString(R.string.diag_title), value = getString(if (Diag.enabled) R.string.on else R.string.off), icon = R.drawable.ic_s_diag, chevron = true) { open(PAGE_DIAGNOSTICS) }
        ui.row(c, getString(R.string.privacy_title), icon = R.drawable.ic_s_privacy, chevron = true) { open(PAGE_PRIVACY) }
        ui.row(c, getString(R.string.about_title), value = BuildConfig.VERSION_NAME, icon = R.drawable.ic_s_info, chevron = true) { open(PAGE_ABOUT) }
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

    private fun chooseLanguage() {
        val choices = arrayOf(Lang.SYSTEM, Lang.ZH, Lang.EN)
        val names = choices.map { languageName(it) }.toTypedArray()
        AlertDialog.Builder(this)
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

    /** Saves one setting if it changed; the core validates it. */
    private fun commit(key: String, value: String, errorView: TextView?, after: () -> Unit = {}) {
        val v = value.trim()
        val old = when (key) { "signal" -> signal; "signal-pin" -> pin; "relay" -> if (relay) "on" else "off"; else -> name }
        if (v == old) return
        bg.execute {
            val err = runCatching { Remohopcore.setSetting(key, v) }.exceptionOrNull()?.message
            val s = runCatching { JSONObject(Remohopcore.settingsJSON()) }.getOrNull()
            ui {
                errorView?.let { it.text = err ?: ""; it.visibility = if (err == null) View.GONE else View.VISIBLE }
                if (err != null) return@ui
                if (s != null) {
                    signal = s.optString("signal"); pin = s.optString("signal-pin")
                    relay = s.optString("relay", "on") != "off"; name = s.optString("name")
                }
                if (key == "signal" || key == "signal-pin") rendezvousStatus = null
                Diag.log("setting $key changed")
                saved()
                after()
            }
        }
    }

    private fun errorLine(col: LinearLayout): TextView {
        val c = ui.section(col, null)
        val t = ui.text(c, "", colorId = R.color.rh_err)
        c.visibility = View.GONE
        // The card shows only while there is an error.
        t.addTextChangedListener(object : android.text.TextWatcher {
            override fun beforeTextChanged(s: CharSequence?, a: Int, b: Int, d: Int) {}
            override fun onTextChanged(s: CharSequence?, a: Int, b: Int, d: Int) {}
            override fun afterTextChanged(s: android.text.Editable?) { c.visibility = if (s.isNullOrEmpty()) View.GONE else View.VISIBLE }
        })
        return t
    }

    private fun rendezvous(col: LinearLayout) {
        pageTitle(R.string.rendezvous_heading)
        info = getString(R.string.rendezvous_heading) to listOf(
            getString(R.string.rendezvous_heading) to getString(R.string.info_rendezvous),
            getString(R.string.rendezvous_key) to getString(R.string.info_rendezvous_key),
            getString(R.string.relay_short) to getString(R.string.info_relay),
            getString(R.string.info_admin_title) to getString(R.string.info_rendezvous_admin),
        )
        val c = ui.section(col, getString(R.string.server))
        lateinit var err: TextView
        var testRow: SettingsUi.Row? = null
        val check = {
            val row = testRow
            if (RendezvousCheck.AVAILABLE && row != null && signal.isNotEmpty()) {
                row.value(getString(R.string.checking))
                val s = signal
                val p = pin
                bg.execute {
                    val r = RendezvousCheck.run(s, p)
                    ui { rendezvousStatus = r; row.value(statusText(r)) }
                }
            }
        }
        lateinit var key: EditText
        // A new server address clears the key of the old one (the core does that).
        ui.field(c, getString(R.string.server_address), signal, getString(R.string.rendezvous_example), InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_URI) {
            commit("signal", it, err) { key.setText(pin); check() }
        }
        key = ui.field(c, getString(R.string.rendezvous_key), pin, getString(R.string.optional), InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_FLAG_NO_SUGGESTIONS) {
            commit("signal-pin", it, err) { check() }
        }
        err = errorLine(col)
        if (RendezvousCheck.AVAILABLE) {
            val t = ui.section(col, null)
            testRow = ui.row(t, getString(R.string.test_connection), value = rendezvousStatus?.let { statusText(it) } ?: "") { check() }
        }
        val r = ui.section(col, null)
        ui.switchRow(r, getString(R.string.relay_short), relay) { on -> commit("relay", if (on) "on" else "off", err) }
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
        lateinit var err: TextView
        val e = ui.field(c, getString(R.string.device_name), name, Diag.device()) { commit("name", it, err) }
        e.filters = arrayOf(android.text.InputFilter.LengthFilter(64))
        err = errorLine(col)
        e.requestFocus()
    }

    private fun identity(col: LinearLayout) {
        pageTitle(R.string.identity_heading)
        info = getString(R.string.identity_heading) to listOf(
            getString(R.string.identity_heading) to getString(R.string.info_identity),
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
                Toast.makeText(this, R.string.copied, Toast.LENGTH_SHORT).show()
            }.onFailure { Toast.makeText(this, it.message, Toast.LENGTH_LONG).show() }
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
        if (hosts.length() == 0) ui.text(c, getString(R.string.no_hosts), colorId = R.color.rh_muted)
        for (i in 0 until hosts.length()) {
            val d = hosts.getJSONObject(i)
            val target = d.optString("target")
            val alias = d.optString("alias")
            val label = alias.ifEmpty { target }
            val p = d.optString("presence")
            val sum = listOfNotNull(target.takeIf { alias.isNotEmpty() }, login(d.optString("login")),
                presence(p).takeIf { p != "not_checked" && p.isNotEmpty() }).joinToString(" · ")
            val row = ui.row(c, label, summary = sum, icon = R.drawable.ic_s_host) { forget(target, label) }
            row.view.contentDescription = getString(R.string.forget_host, label)
        }
        ui.row(ui.section(col, null), getString(R.string.pair_heading), icon = R.drawable.ic_s_link, chevron = true) { open(PAGE_PAIR) }
    }

    private fun refreshHosts(online: Boolean) {
        if (online) Toast.makeText(this, R.string.checking, Toast.LENGTH_SHORT).show()
        bg.execute {
            val list = runCatching { JSONArray(Remohopcore.devicesJSON(online)) }
            ui {
                list.onSuccess { hosts = it }.onFailure { Toast.makeText(this, it.message, Toast.LENGTH_LONG).show() }
                if (stack.lastOrNull() == PAGE_HOSTS || stack.isEmpty()) show()
            }
        }
    }

    private fun forget(target: String, label: String) {
        AlertDialog.Builder(this)
            .setTitle(label)
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
        val alias = ui.field(c, getString(R.string.pair_alias), "", getString(R.string.optional), InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_FLAG_NO_SUGGESTIONS) {}
        val actions = ui.section(col, null)
        val status = ui.section(col, null).also { it.visibility = View.GONE }
        val statusText = ui.text(status, "")
        lateinit var pairRow: SettingsUi.Row
        lateinit var cancelRow: SettingsUi.Row
        val progress = ProgressBar(this).apply { isIndeterminate = true }
        pairRow = ui.row(actions, getString(R.string.pair), icon = R.drawable.ic_s_link) {
            val t = target.text.toString().trim()
            if (t.isEmpty()) { target.requestFocus(); return@row }
            val pw = password.text.toString()
            password.text.clear()
            status.visibility = View.VISIBLE
            statusText.setTextColor(getColor(R.color.rh_muted))
            statusText.text = getString(R.string.pairing)
            pairRow.view.isEnabled = false
            cancelRow.view.visibility = View.VISIBLE
            progress.visibility = View.VISIBLE
            // Not on bg: a pairing may wait minutes for approval.
            Thread {
                val out = runCatching { JSONObject(Remohopcore.pairWithPassword(t, pw, alias.text.toString())) }
                ui {
                    pairRow.view.isEnabled = true
                    cancelRow.view.visibility = View.GONE
                    progress.visibility = View.GONE
                    out.onSuccess { r ->
                        val ok = r.optBoolean("ok")
                        statusText.setTextColor(getColor(if (ok) R.color.rh_ok else R.color.rh_err))
                        statusText.text = if (ok) getString(R.string.paired_short, r.optString("target")) else pairError(r)
                        Diag.log(if (ok) "paired with a host" else "pairing failed: ${r.optString("code")} ${r.optString("error")}")
                        if (ok) bg.execute {
                            val list = runCatching { JSONArray(Remohopcore.devicesJSON(false)) }.getOrNull()
                            ui { if (list != null) hosts = list }
                        }
                    }.onFailure { statusText.setTextColor(getColor(R.color.rh_err)); statusText.text = it.message }
                }
            }.start()
        }
        progress.visibility = View.GONE
        pairRow.view.addView(progress, LinearLayout.LayoutParams(ui.dp(24), ui.dp(24)))
        cancelRow = ui.row(actions, getString(R.string.cancel)) { Thread { Remohopcore.cancelPair() }.start() }
        cancelRow.view.visibility = View.GONE
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
        lateinit var sw: Switch
        sw = ui.switchRow(c, getString(R.string.diag_title), on, R.drawable.ic_s_diag) { v ->
            runCatching { Diag.setEnabled(v) }.onFailure { Toast.makeText(this, it.message, Toast.LENGTH_LONG).show() }
            SettingsUi.setQuietly(sw, Diag.enabled)
            show()
        }
        if (on) ui.row(c, getString(R.string.diag_size), value = AppInfo.size(Diag.size))
        if (!on && Diag.size == 0L) return
        val s = ui.section(col, null)
        val fmt = ui.row(s, getString(R.string.diag_format), value = diagFormat.uppercase(), chevron = true) {}
        fmt.view.setOnClickListener {
            val opts = arrayOf("txt", "json")
            AlertDialog.Builder(this).setTitle(R.string.diag_format)
                .setSingleChoiceItems(arrayOf("TXT", "JSON"), opts.indexOf(diagFormat)) { d, i -> diagFormat = opts[i]; fmt.value(diagFormat.uppercase()); d.dismiss() }
                .show()
        }
        ui.row(s, getString(R.string.diag_share), icon = R.drawable.ic_share) {
            report()?.let { Diag.share(this, it, getString(R.string.diag_share)) }
        }
        ui.row(s, getString(R.string.diag_email), icon = R.drawable.ic_mail) {
            report()?.let {
                val body = getString(R.string.diag_email_body) + "\n\n\nRemoHop ${BuildConfig.VERSION_NAME} (${BuildConfig.VERSION_CODE}), Android ${Build.VERSION.RELEASE}"
                Diag.email(this, it, getString(R.string.diag_email_subject), body, getString(R.string.diag_email))
            }
        }
        ui.row(ui.section(col, null), getString(R.string.diag_clear), icon = R.drawable.ic_delete, danger = true) {
            AlertDialog.Builder(this).setMessage(R.string.diag_clear_confirm)
                .setNegativeButton(R.string.cancel, null)
                .setPositiveButton(R.string.diag_clear) { _, _ -> runCatching { Diag.clear() }; show() }
                .show()
        }
    }

    private fun report() = runCatching { Diag.export(this, diagFormat) }
        .onFailure { Toast.makeText(this, it.message, Toast.LENGTH_LONG).show() }.getOrNull()

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
        ui.row(ui.section(col, null), getString(R.string.privacy_policy), icon = R.drawable.ic_open, chevron = true) {
            Diag.open(this, AppInfo.privacyPolicy(Lang.effective(this)))
        }
    }

    private fun about(col: LinearLayout) {
        pageTitle(R.string.about_title)
        ui.header(col, getString(R.string.app_name), getString(R.string.about_version, BuildConfig.VERSION_NAME, BuildConfig.VERSION_CODE))
        var c = ui.section(col, null)
        ui.row(c, getString(R.string.about_version_label), value = BuildConfig.VERSION_NAME)
        ui.row(c, getString(R.string.about_build), value = BuildConfig.VERSION_CODE.toString())
        ui.row(c, getString(R.string.about_core), value = Diag.coreVersion)
        c = ui.section(col, null)
        val lang = Lang.effective(this)
        ui.row(c, getString(R.string.about_website), icon = R.drawable.ic_s_language, chevron = true) { Diag.open(this, AppInfo.WEBSITE) }
        ui.row(c, getString(R.string.about_source), icon = R.drawable.ic_code, chevron = true) { Diag.open(this, AppInfo.SOURCE_CODE) }
        ui.row(c, getString(R.string.about_license), icon = R.drawable.ic_doc, chevron = true) { Diag.open(this, AppInfo.LICENSING) }
        ui.row(c, getString(R.string.about_notices), icon = R.drawable.ic_doc, chevron = true) {
            AlertDialog.Builder(this).setTitle(R.string.about_notices).setMessage(AppInfo.NOTICE)
                .setPositiveButton(R.string.done, null)
                .setNeutralButton(R.string.about_third_party) { _, _ -> Diag.open(this, AppInfo.THIRD_PARTY) }
                .show()
        }
        c = ui.section(col, getString(R.string.about_contact))
        ui.row(c, AppInfo.SUPPORT_EMAIL, icon = R.drawable.ic_mail, chevron = true) { Diag.open(this, "mailto:" + AppInfo.SUPPORT_EMAIL) }
        ui.row(c, getString(R.string.about_telegram), icon = R.drawable.ic_send, chevron = true) { Diag.open(this, AppInfo.TELEGRAM) }
        c = ui.section(col, null)
        ui.row(c, getString(R.string.about_terms), icon = R.drawable.ic_doc, chevron = true) { Diag.open(this, AppInfo.terms(lang)) }
        ui.row(c, getString(R.string.privacy_policy), icon = R.drawable.ic_s_privacy, chevron = true) { Diag.open(this, AppInfo.privacyPolicy(lang)) }
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
