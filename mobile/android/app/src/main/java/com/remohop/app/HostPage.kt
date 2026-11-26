package com.remohop.app

import android.content.ClipData
import android.content.ClipboardManager
import android.content.DialogInterface
import android.content.res.ColorStateList
import android.os.Build
import android.os.PersistableBundle
import android.text.InputType
import android.text.SpannableStringBuilder
import android.text.Spanned
import android.text.style.AbsoluteSizeSpan
import android.text.style.ForegroundColorSpan
import android.view.View
import android.view.ViewGroup
import android.view.inputmethod.EditorInfo
import android.widget.ImageButton
import android.widget.LinearLayout
import android.widget.RadioGroup
import android.widget.TextView
import androidx.appcompat.app.AppCompatActivity
import androidx.core.widget.NestedScrollView
import androidx.core.widget.TextViewCompat
import androidx.core.widget.doAfterTextChanged
import com.google.android.material.color.MaterialColors
import com.google.android.material.dialog.MaterialAlertDialogBuilder
import com.google.android.material.materialswitch.MaterialSwitch
import com.google.android.material.radiobutton.MaterialRadioButton
import com.google.android.material.textfield.TextInputEditText
import com.google.android.material.textfield.TextInputLayout
import com.google.android.material.R as M

/**
 * The "This device" page of Settings: host mode's switch, the ID (or the
 * addresses, without a rendezvous server) and the password to tell the
 * other side (temporary, or the owner's own: dots until tapped), the
 * password settings, the status, whether control is possible, and who is
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
    private var eye: ImageButton? = null
    private var modeRow: SettingsUi.Row? = null
    /** The owner's own password is shown instead of dots (until the page is built again or left). */
    private var revealed = false
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
        str(R.string.hm_info_password_t) to str(R.string.hm_info_password),
        str(R.string.hm_info_end_t) to str(R.string.hm_info_end),
        str(R.string.hm_info_control_t) to str(R.string.hm_info_control),
    )

    fun build(col: LinearLayout) {
        val s = HostMode.state
        current = s
        shape = shapeOf(s)
        revealed = false
        pwRow = null
        eye = null
        var c = ui.section(col, null, SettingsUi.Tone.PRIMARY)
        sw = ui.switchRow(c, str(R.string.hm_allow), s.on, R.drawable.ic_s_device) { on -> HostMode.setEnabled(a, on) }
        status = ui.row(c, str(R.string.hm_status), summary = statusText(s), icon = R.drawable.ic_s_network)
        if (s.on) {
            c = ui.section(col, str(R.string.hm_connect_with), SettingsUi.Tone.PRIMARY)
            val byId = s.id.isNotEmpty() || s.addresses.isEmpty()
            idRow = ui.row(c, str(if (byId) R.string.hm_id else R.string.hm_address), value = s.reachableAs.ifEmpty { "…" },
                icon = R.drawable.ic_s_host, trailing = iconButton(R.drawable.ic_copy, str(R.string.hm_copy)) { copy(current.reachableAs, false) })
            val copyPw = iconButton(R.drawable.ic_copy, str(R.string.hm_copy)) { copy(current.password, true) }
            if (s.fixedPassword) {
                // The owner's own password: dots until tapped (the row or its eye); copy takes the real one.
                val e = iconButton(R.drawable.ic_eye, str(R.string.savedpw_show)) { reveal(!revealed) }
                eye = e
                val both = LinearLayout(a).apply { orientation = LinearLayout.HORIZONTAL; addView(e); addView(copyPw) }
                pwRow = ui.row(c, str(R.string.hm_password_fixed), value = passwordText(s), icon = R.drawable.ic_s_key, trailing = both) { reveal(!revealed) }
                reveal(false)
            } else {
                pwRow = ui.row(c, str(R.string.hm_password), value = passwordText(s), icon = R.drawable.ic_s_key, trailing = copyPw)
                ui.row(c, str(R.string.hm_new_password), icon = R.drawable.ic_refresh) { HostMode.regenerate() }
            }
        }
        // The password settings: with the password while host mode is on,
        // under the status while it is off (the setting is kept either way).
        modeRow = ui.row(c, str(R.string.hm_pw_settings), value = modeText(s), icon = R.drawable.ic_s_privacy, chevron = true) { passwordDialog() }
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
        // The password mode is known while host mode is off too: ask for it.
        HostMode.refresh()
    }

    /** Stops following the state (the page is left or built again). */
    fun dispose() = HostMode.unlisten(listener)

    /** Masks the owner's own password again (the screen is left). */
    fun mask() { if (revealed) reveal(false) }

    /** What the page lists: when this changes the page is built again. */
    private fun shapeOf(s: HostState) = listOf(s.on, s.id.isNotEmpty() || s.addresses.isEmpty(), s.fixedPassword, s.sessions.map { it.id }).toString()

    private fun update(s: HostState) {
        current = s
        if (shapeOf(s) != shape) { rebuild(); return }
        sw?.let { if (it.isChecked != s.on) SettingsUi.setQuietly(it, s.on) }
        status?.summary(statusText(s))
        idRow?.value(s.reachableAs.ifEmpty { "…" })
        pwRow?.value(passwordText(s))
        modeRow?.value(modeText(s))
        controlRow?.summary(controlText(s))
        for (x in s.sessions) sessionRows[x.id]?.summary(sessionText(x))
    }

    /** The password as the row shows it: the owner's own as dots unless revealed. */
    private fun passwordText(s: HostState) = if (s.fixedPassword && !revealed && s.password.isNotEmpty()) MASKED_PASSWORD else s.password

    private fun modeText(s: HostState) = str(if (s.fixedPassword) R.string.hm_pw_mode_fixed else R.string.hm_pw_mode_temporary)

    /** Shows the owner's own password, or dots. */
    private fun reveal(on: Boolean) {
        revealed = on
        val row = pwRow ?: return
        row.value(passwordText(current))
        // Dots read as "hidden", not as a row of bullets.
        row.value.contentDescription = if (on) null else str(R.string.hm_password_masked)
        eye?.setImageResource(if (on) R.drawable.ic_eye_off else R.drawable.ic_eye)
        eye?.contentDescription = str(if (on) R.string.savedpw_hide else R.string.savedpw_show)
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

    /**
     * The password settings: a new random password at every start, or the
     * owner's own (typed twice), as in the desktop app. A mismatch, an empty
     * password and the core's refusal show in the dialog, which stays open.
     */
    private fun passwordDialog() {
        val wasOwn = current.fixedPassword
        val box = LinearLayout(a).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(ui.dp(24), ui.dp(8), ui.dp(24), 0)
        }
        val group = RadioGroup(a)
        val temp = option(str(R.string.hm_pwm_temp), str(R.string.hm_pwm_temp_sub))
        val own = option(str(R.string.hm_pwm_fixed), str(R.string.hm_pwm_fixed_sub))
        group.addView(temp, RadioGroup.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
        group.addView(own, RadioGroup.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply { topMargin = ui.dp(4) })
        box.addView(group, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
        val first = passwordField(str(R.string.hm_pwm_password), EditorInfo.IME_ACTION_NEXT)
        val second = passwordField(str(R.string.hm_pwm_repeat), EditorInfo.IME_ACTION_DONE)
        if (wasOwn) first.helperText = str(R.string.hm_pwm_keep)
        val fields = LinearLayout(a).apply { orientation = LinearLayout.VERTICAL }
        fields.addView(first, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply { topMargin = ui.dp(8) })
        fields.addView(second, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply { topMargin = ui.dp(8) })
        box.addView(fields, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
        box.addView(TextView(a).apply {
            text = str(R.string.hm_pwm_note)
            TextViewCompat.setTextAppearance(this, ui.textAppearance(M.attr.textAppearanceBodyMedium))
            setTextColor(ui.color(M.attr.colorOnSurfaceVariant))
        }, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply { topMargin = ui.dp(16) })
        // The core's refusal when going back to a random password (the fields are hidden then).
        val failure = TextView(a).apply {
            TextViewCompat.setTextAppearance(this, ui.textAppearance(M.attr.textAppearanceBodyMedium))
            setTextColor(ui.color(M.attr.colorError))
            visibility = View.GONE
        }
        box.addView(failure, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply { topMargin = ui.dp(8) })
        val scroll = NestedScrollView(a).apply { addView(box) }

        val clearErrors = { first.error = null; second.error = null; failure.shown(false) }
        group.check(if (wasOwn) own.id else temp.id)
        fields.shown(wasOwn)
        group.setOnCheckedChangeListener { _, id ->
            clearErrors()
            fields.shown(id == own.id)
            if (id == own.id && !wasOwn) first.editText?.requestFocus()
        }
        first.editText?.doAfterTextChanged { first.error = null }
        second.editText?.doAfterTextChanged { second.error = null }

        val d = MaterialAlertDialogBuilder(a)
            .setTitle(R.string.hm_pwm_title)
            .setView(scroll)
            .setNegativeButton(R.string.cancel, null)
            // Its click is set below, so that an error keeps the dialog open.
            .setPositiveButton(R.string.save, null)
            .show()
        val save = d.getButton(DialogInterface.BUTTON_POSITIVE) ?: return
        second.editText?.setOnEditorActionListener { _, action, _ ->
            if (action == EditorInfo.IME_ACTION_DONE) { save.performClick(); true } else false
        }
        save.setOnClickListener {
            clearErrors()
            val ownChosen = group.checkedRadioButtonId == own.id
            val p1 = first.editText?.text?.toString().orEmpty()
            val p2 = second.editText?.text?.toString().orEmpty()
            when (val o = HostPasswordForm.check(ownChosen, p1, p2, wasOwn)) {
                HostPasswordForm.Outcome.Unchanged -> d.dismiss()
                HostPasswordForm.Outcome.Mismatch -> second.error = str(R.string.hm_pwm_mismatch)
                HostPasswordForm.Outcome.Empty -> first.error = str(R.string.hm_pwm_rule)
                is HostPasswordForm.Outcome.Save -> {
                    save.isEnabled = false
                    HostMode.setOwnPassword(o.password) { err ->
                        if (!d.isShowing || a.isDestroyed) return@setOwnPassword
                        save.isEnabled = true
                        when {
                            err == null -> { d.dismiss(); snack(str(R.string.hm_pwm_saved)) }
                            ownChosen -> first.error = if (HostPasswordForm.isRuleMessage(err)) str(R.string.hm_pwm_rule) else str(R.string.hm_pwm_failed, err.take(200))
                            else -> { failure.text = str(R.string.hm_pwm_failed, err.take(200)); failure.shown(true) }
                        }
                    }
                }
            }
        }
    }

    /** A choice of the password dialog: its name, and a short description under it. */
    private fun option(title: String, sub: String): MaterialRadioButton = MaterialRadioButton(a).apply {
        id = View.generateViewId()
        TextViewCompat.setTextAppearance(this, ui.textAppearance(M.attr.textAppearanceBodyLarge))
        setTextColor(ui.color(M.attr.colorOnSurface))
        val t = SpannableStringBuilder(title).append('\n')
        val start = t.length
        t.append(sub)
        t.setSpan(AbsoluteSizeSpan(14, true), start, t.length, Spanned.SPAN_EXCLUSIVE_EXCLUSIVE)
        t.setSpan(ForegroundColorSpan(ui.color(M.attr.colorOnSurfaceVariant)), start, t.length, Spanned.SPAN_EXCLUSIVE_EXCLUSIVE)
        text = t
        setPadding(ui.dp(12), ui.dp(8), 0, ui.dp(8))
    }

    /** An outlined password field with the eye toggle: never autofilled, never kept in the saved state. */
    private fun passwordField(label: String, ime: Int): TextInputLayout {
        val l = TextInputLayout(a).apply {
            hint = label
            endIconMode = TextInputLayout.END_ICON_PASSWORD_TOGGLE
        }
        val e = TextInputEditText(l.context).apply {
            inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_PASSWORD
            // A password field keeps the app's font (the variation sets monospace).
            typeface = android.graphics.Typeface.DEFAULT
            imeOptions = ime
            maxLines = 1
            importantForAutofill = View.IMPORTANT_FOR_AUTOFILL_NO
            isSaveEnabled = false
        }
        // TextInputLayout moves the field into its inner LinearLayout and casts the params.
        l.addView(e, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
        return l
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
        const val MASKED_PASSWORD = "••••••••"
    }
}

/**
 * The password dialog's own checks, before the core's rule
 * (Remohopcore.setHostFixedPassword). Pure Kotlin: unit-tested.
 */
object HostPasswordForm {
    sealed class Outcome {
        /** Nothing to change: the dialog closes. */
        object Unchanged : Outcome()
        /** The two fields differ. */
        object Mismatch : Outcome()
        /** "My own password" with nothing typed (the core would take it for a random one). */
        object Empty : Outcome()
        /** Hand this to the core ("" = a random password again). */
        data class Save(val password: String) : Outcome()
    }

    /**
     * [own]: "my own password" is chosen, typed as [first] and [second];
     * [wasOwn]: it is in force already (empty fields keep it then).
     */
    fun check(own: Boolean, first: String, second: String, wasOwn: Boolean): Outcome = when {
        !own -> if (wasOwn) Outcome.Save("") else Outcome.Unchanged
        first != second -> Outcome.Mismatch
        // The core drops spaces and dashes: nothing left is no password.
        normalized(first).isEmpty() -> if (wasOwn) Outcome.Unchanged else Outcome.Empty
        else -> Outcome.Save(first)
    }

    /** The core refused the password by its rule (shown in the app's own words then). */
    fun isRuleMessage(msg: String): Boolean = msg.startsWith("weak_password")

    private fun normalized(s: String) = s.filterNot { it == '-' || it.isWhitespace() }
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
