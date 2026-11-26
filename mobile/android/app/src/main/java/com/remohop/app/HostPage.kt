package com.remohop.app

import android.content.ClipData
import android.content.ClipboardManager
import android.content.res.ColorStateList
import android.os.Build
import android.os.PersistableBundle
import android.view.View
import android.widget.ImageButton
import android.widget.LinearLayout
import androidx.appcompat.app.AppCompatActivity
import com.google.android.material.color.MaterialColors
import com.google.android.material.dialog.MaterialAlertDialogBuilder
import com.google.android.material.materialswitch.MaterialSwitch
import com.google.android.material.R as M

/**
 * The "This device" page of Settings: host mode's switch, the ID (or the
 * addresses, without a rendezvous server) and the temporary password to
 * tell the other side, the status, whether control is possible, and who is
 * connected. Only the essentials show; the explanations are behind the
 * page's ⓘ and the ⓘ of the control row (the owner's UI rule).
 *
 * It follows [HostMode]'s state: values change in place, and the page is
 * built again ([rebuild]) only when what it lists changes.
 */
class HostPage(
    private val a: AppCompatActivity,
    private val ui: SettingsUi,
    private val showInfo: (String, List<Pair<String, String>>) -> Unit,
    private val snack: (String) -> Unit,
    private val rebuild: () -> Unit,
) {
    private var sw: MaterialSwitch? = null
    private var status: SettingsUi.Row? = null
    private var idRow: SettingsUi.Row? = null
    private var pwRow: SettingsUi.Row? = null
    private var controlRow: SettingsUi.Row? = null
    private val sessionRows = HashMap<Int, SettingsUi.Row>()
    private var shape: String? = null
    private var current: HostState = HostState.OFF
    private val listener: (HostState) -> Unit = { s -> update(s) }

    private fun str(id: Int, vararg args: Any) = a.getString(id, *args)

    /** The page's explanations (its ⓘ). */
    fun info(): Pair<String, List<Pair<String, String>>> = str(R.string.hm_this_device) to listOf(
        str(R.string.hm_info_what_t) to str(R.string.hm_info_what),
        str(R.string.hm_info_access_t) to str(R.string.hm_info_access),
        str(R.string.hm_info_start_t) to str(R.string.hm_info_start),
        str(R.string.hm_info_end_t) to str(R.string.hm_info_end),
        str(R.string.hm_info_control_t) to str(R.string.hm_info_control),
    )

    fun build(col: LinearLayout) {
        val s = HostMode.state
        current = s
        shape = shapeOf(s)
        var c = ui.section(col, null, SettingsUi.Tone.PRIMARY)
        sw = ui.switchRow(c, str(R.string.hm_allow), s.on, R.drawable.ic_s_device) { on -> HostMode.setEnabled(a, on) }
        status = ui.row(c, str(R.string.hm_status), summary = statusText(s), icon = R.drawable.ic_s_network)
        if (s.on) {
            c = ui.section(col, str(R.string.hm_connect_with), SettingsUi.Tone.PRIMARY)
            val byId = s.id.isNotEmpty() || s.addresses.isEmpty()
            idRow = ui.row(c, str(if (byId) R.string.hm_id else R.string.hm_address), value = s.reachableAs.ifEmpty { "…" },
                icon = R.drawable.ic_s_host, trailing = iconButton(R.drawable.ic_copy, str(R.string.hm_copy)) { copy(current.reachableAs, false) })
            pwRow = ui.row(c, str(R.string.hm_password), value = s.password, icon = R.drawable.ic_s_key,
                trailing = iconButton(R.drawable.ic_copy, str(R.string.hm_copy)) { copy(current.password, true) })
            ui.row(c, str(R.string.hm_new_password), icon = R.drawable.ic_refresh) { HostMode.regenerate() }
        }
        c = ui.section(col, str(R.string.hm_control), SettingsUi.Tone.SECONDARY)
        controlRow = ui.row(c, str(R.string.hm_control), summary = controlText(s), icon = R.drawable.ic_eye,
            trailing = iconButton(R.drawable.ic_info, str(R.string.info_about_screen)) {
                showInfo(str(R.string.hm_info_control_t), listOf("" to str(R.string.hm_info_control)))
            }) { if (ControlService.instance == null) ControlDisclosure.show(a) else ControlService.openSettings(a) }
        if (s.sessions.isNotEmpty()) {
            c = ui.section(col, str(R.string.hm_connected), SettingsUi.Tone.TERTIARY)
            for (x in s.sessions) {
                sessionRows[x.id] = ui.row(c, x.label, summary = sessionText(x), leading = ui.avatar(x.label, SettingsUi.Tone.TERTIARY), chevron = true) { sessionActions(x.id) }
            }
            ui.row(c, str(R.string.hm_disconnect_all), icon = R.drawable.ic_delete, danger = true) { HostMode.disconnect(0) }
        }
        HostMode.listen(listener)
    }

    /** Stops following the state (the page is left or built again). */
    fun dispose() = HostMode.unlisten(listener)

    /** What the page lists: when this changes the page is built again. */
    private fun shapeOf(s: HostState) = listOf(s.on, s.id.isNotEmpty() || s.addresses.isEmpty(), s.sessions.map { it.id }).toString()

    private fun update(s: HostState) {
        current = s
        if (shapeOf(s) != shape) { rebuild(); return }
        sw?.let { if (it.isChecked != s.on) SettingsUi.setQuietly(it, s.on) }
        status?.summary(statusText(s))
        idRow?.value(s.reachableAs.ifEmpty { "…" })
        pwRow?.value(s.password)
        controlRow?.summary(controlText(s))
        for (x in s.sessions) sessionRows[x.id]?.summary(sessionText(x))
    }

    private fun statusText(s: HostState) = when (s.status) {
        HostState.Status.OFF -> str(R.string.hm_status_off)
        HostState.Status.STARTING -> str(R.string.hm_status_starting)
        HostState.Status.READY -> str(R.string.hm_status_ready)
        HostState.Status.OFFLINE -> str(R.string.hm_status_offline)
        HostState.Status.NO_SERVER -> str(R.string.hm_status_no_server)
        HostState.Status.ERROR -> str(R.string.hm_status_error, s.error.take(200))
    }

    private fun controlText(s: HostState) = when {
        ControlService.instance != null -> str(R.string.hm_control_on)
        ControlService.enabledInSettings(a) -> str(R.string.hm_control_starting)
        else -> str(R.string.hm_control_off)
    }

    private fun sessionText(x: HostSession): String {
        val t = HostTime.duration(x.seconds)
        return str(if (x.control) R.string.hm_controlling else R.string.hm_viewing, t)
    }

    private fun sessionActions(id: Int) {
        val x = current.sessions.firstOrNull { it.id == id } ?: return
        val items = arrayOf(str(if (x.control) R.string.hm_take_control else R.string.hm_give_control), str(R.string.hm_disconnect))
        MaterialAlertDialogBuilder(a)
            .setTitle(x.label)
            .setItems(items) { _, i ->
                when (i) {
                    0 -> HostMode.setControl(id, !x.control)
                    1 -> HostMode.disconnect(id)
                }
            }
            .show()
    }

    private fun iconButton(icon: Int, label: String, onClick: () -> Unit): ImageButton = ImageButton(a).apply {
        setImageResource(icon)
        imageTintList = ColorStateList.valueOf(MaterialColors.getColor(a, M.attr.colorOnSurfaceVariant, ""))
        contentDescription = label
        val tv = android.util.TypedValue()
        a.theme.resolveAttribute(android.R.attr.selectableItemBackgroundBorderless, tv, true)
        setBackgroundResource(tv.resourceId)
        minimumWidth = ui.dp(48)
        minimumHeight = ui.dp(48)
        setOnClickListener { onClick() }
    }

    private fun copy(text: String, sensitive: Boolean) {
        if (text.isEmpty()) return
        val cm = a.getSystemService(ClipboardManager::class.java) ?: return
        val clip = ClipData.newPlainText("RemoHop", text)
        if (sensitive) clip.description.extras = PersistableBundle().apply { putBoolean(EXTRA_IS_SENSITIVE, true) }
        cm.setPrimaryClip(clip)
        // Android 13+ confirms a copy itself.
        if (Build.VERSION.SDK_INT < 33) snack(str(R.string.hm_copied))
    }

    private companion object {
        const val EXTRA_IS_SENSITIVE = "android.content.extra.IS_SENSITIVE"
    }
}

/** Durations for the session list. Pure Kotlin: unit-tested. */
object HostTime {
    /** "45 s", "3 min", "1 h 05 min". */
    fun duration(seconds: Int): String = when {
        seconds < 60 -> "${maxOf(seconds, 0)} s"
        seconds < 3600 -> "${seconds / 60} min"
        else -> String.format(java.util.Locale.ROOT, "%d h %02d min", seconds / 3600, seconds % 3600 / 60)
    }
}

/**
 * The prominent disclosure Google Play asks of an app before it sends the
 * user to switch on its accessibility service: what the service does, that
 * it acts only during sessions the owner approves with control, and how
 * to switch it off. Shown only when the owner taps to turn control on.
 */
object ControlDisclosure {
    fun show(a: AppCompatActivity, then: () -> Unit = {}) {
        Diag.log("control: disclosure shown")
        MaterialAlertDialogBuilder(a)
            .setTitle(R.string.hm_ax_title)
            .setMessage(R.string.hm_ax_body)
            .setPositiveButton(R.string.hm_ax_agree) { _, _ ->
                Diag.log("control: disclosure accepted; opening the accessibility settings")
                ControlService.openSettings(a)
                then()
            }
            .setNegativeButton(R.string.hm_ax_not_now, null)
            .show()
    }
}

/** View helpers for [View] visibility. */
internal fun View.shown(on: Boolean) { visibility = if (on) View.VISIBLE else View.GONE }
