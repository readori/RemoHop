package com.remohop.app

import android.app.Activity
import android.app.AlertDialog
import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.os.Build
import android.os.Bundle
import android.view.MenuItem
import android.view.View
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.RadioGroup
import android.widget.Switch
import android.widget.TextView
import android.widget.Toast
import com.remohop.app.core.remohopcore.Remohopcore
import org.json.JSONArray
import org.json.JSONObject
import java.util.concurrent.Executors

/**
 * Native settings: the app's language, the rendezvous server (no default:
 * the user's own), the relay switch, this device's name and identity,
 * pairing with a host by password, and the saved hosts. Settings take
 * effect on the next connection; the running gateway reads them per dial.
 * The language applies at once (see [Lang]).
 */
class SettingsActivity : Activity() {
    private val bg = Executors.newSingleThreadExecutor()
    private lateinit var signal: EditText
    private lateinit var pin: EditText
    private lateinit var relay: Switch
    private lateinit var name: EditText

    override fun attachBaseContext(newBase: Context) {
        super.attachBaseContext(Lang.wrap(newBase))
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        Core.init(this)
        setContentView(R.layout.activity_settings)
        // The manifest label is resolved in the system's language.
        title = getString(R.string.settings)
        actionBar?.setDisplayHomeAsUpEnabled(true)
        setupLanguage()
        signal = findViewById(R.id.signal)
        pin = findViewById(R.id.signal_pin)
        relay = findViewById(R.id.relay)
        name = findViewById(R.id.name)
        findViewById<Button>(R.id.save).setOnClickListener { save() }
        findViewById<Button>(R.id.copy_key).setOnClickListener { copyKey() }
        findViewById<Button>(R.id.pair).setOnClickListener { pair() }
        findViewById<Button>(R.id.pair_cancel).setOnClickListener { Thread { Remohopcore.cancelPair() }.start() }
        findViewById<Button>(R.id.refresh_hosts).setOnClickListener { loadHosts(true) }
        loadSettings()
        loadHosts(false)
    }

    override fun onOptionsItemSelected(item: MenuItem): Boolean {
        if (item.itemId == android.R.id.home) { finish(); return true }
        return super.onOptionsItemSelected(item)
    }

    override fun onDestroy() {
        bg.shutdownNow()
        super.onDestroy()
    }

    private fun ui(f: () -> Unit) = runOnUiThread { if (!isDestroyed) f() }

    private fun setupLanguage() {
        val group = findViewById<RadioGroup>(R.id.language)
        group.check(when (Lang.choice(this)) {
            Lang.ZH -> R.id.lang_zh
            Lang.EN -> R.id.lang_en
            else -> R.id.lang_system
        })
        group.setOnCheckedChangeListener { _, id ->
            val choice = when (id) {
                R.id.lang_zh -> Lang.ZH
                R.id.lang_en -> Lang.EN
                else -> Lang.SYSTEM
            }
            if (choice == Lang.choice(this)) return@setOnCheckedChangeListener
            Lang.setChoice(this, choice)
            // Android 13+ recreates this screen itself for the new locale.
            if (Build.VERSION.SDK_INT < 33) recreate()
        }
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

    private fun loadSettings() = bg.execute {
        val s = runCatching { JSONObject(Remohopcore.settingsJSON()) }.getOrNull()
        val fp = runCatching { Remohopcore.identity() }.getOrDefault("")
        ui {
            if (s != null) {
                signal.setText(s.optString("signal"))
                pin.setText(s.optString("signal-pin"))
                relay.isChecked = s.optString("relay", "on") != "off"
                name.setText(s.optString("name"))
            }
            findViewById<TextView>(R.id.fingerprint).text = fp
        }
    }

    private fun save() {
        val values = listOf("signal" to signal.text.toString(), "signal-pin" to pin.text.toString(),
            "relay" to if (relay.isChecked) "on" else "off", "name" to name.text.toString())
        val status = findViewById<TextView>(R.id.save_status)
        bg.execute {
            // Address first: changing it clears a pin for the old server.
            val err = values.firstNotNullOfOrNull { (k, v) -> runCatching { Remohopcore.setSetting(k, v.trim()) }.exceptionOrNull()?.message }
            ui { status.text = err ?: getString(R.string.saved_settings) }
            if (err == null) loadSettings()
        }
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

    private fun pair() {
        val target = findViewById<EditText>(R.id.pair_target)
        val password = findViewById<EditText>(R.id.pair_password)
        val alias = findViewById<EditText>(R.id.pair_alias)
        val status = findViewById<TextView>(R.id.pair_status)
        val pairBtn = findViewById<Button>(R.id.pair)
        val cancel = findViewById<Button>(R.id.pair_cancel)
        val t = target.text.toString()
        val pw = password.text.toString()
        val a = alias.text.toString()
        password.text.clear()
        status.text = getString(R.string.pairing)
        pairBtn.isEnabled = false
        cancel.visibility = View.VISIBLE
        // Not on bg: a pairing may wait minutes for approval; the other
        // actions should stay usable.
        Thread {
            val out = runCatching { JSONObject(Remohopcore.pairWithPassword(t, pw, a)) }
            ui {
                pairBtn.isEnabled = true
                cancel.visibility = View.GONE
                out.onSuccess { r ->
                    status.text = if (r.optBoolean("ok")) getString(R.string.paired, r.optString("target"), r.optString("host_identity"))
                    else pairError(r)
                    if (r.optBoolean("ok")) loadHosts(false)
                }.onFailure { status.text = it.message }
            }
        }.start()
    }

    private fun loadHosts(online: Boolean) = bg.execute {
        val list = runCatching { JSONArray(Remohopcore.devicesJSON(online)) }
        ui {
            val box = findViewById<LinearLayout>(R.id.hosts)
            box.removeAllViews()
            val arr = list.getOrElse { e -> box.addView(text(e.message ?: "")); return@ui }
            if (arr.length() == 0) box.addView(text(getString(R.string.no_hosts)))
            for (i in 0 until arr.length()) {
                val d = arr.getJSONObject(i)
                val target = d.optString("target")
                val label = listOf(d.optString("alias"), target).filter { it.isNotEmpty() }.joinToString(" · ")
                val presence = presence(d.optString("presence"))
                val row = LinearLayout(this).apply { orientation = LinearLayout.HORIZONTAL; setPadding(0, 8, 0, 8) }
                row.addView(text("$label\n${login(d.optString("login"))} · $presence"), LinearLayout.LayoutParams(0, LinearLayout.LayoutParams.WRAP_CONTENT, 1f))
                row.addView(Button(this).apply {
                    text = getString(R.string.forget)
                    contentDescription = getString(R.string.forget_host, label)
                    setOnClickListener { forget(target, label) }
                })
                box.addView(row)
            }
        }
    }

    private fun forget(target: String, label: String) {
        AlertDialog.Builder(this)
            .setMessage(getString(R.string.forget_confirm, label))
            .setNegativeButton(R.string.cancel, null)
            .setPositiveButton(R.string.forget) { _, _ ->
                bg.execute { runCatching { Remohopcore.forget(target) }; ui { loadHosts(false) } }
            }.show()
    }

    private fun text(s: String) = TextView(this).apply { text = s; textSize = 15f }
}
