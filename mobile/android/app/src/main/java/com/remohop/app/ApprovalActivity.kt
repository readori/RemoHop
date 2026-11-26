package com.remohop.app

import android.content.Context
import android.content.Intent
import android.content.res.ColorStateList
import android.media.projection.MediaProjectionConfig
import android.media.projection.MediaProjectionManager
import android.os.Build
import android.os.Bundle
import android.view.View
import android.view.ViewGroup
import android.widget.LinearLayout
import android.widget.TextView
import android.widget.Toast
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AlertDialog
import androidx.appcompat.app.AppCompatActivity
import androidx.core.widget.TextViewCompat
import com.google.android.material.checkbox.MaterialCheckBox
import com.google.android.material.dialog.MaterialAlertDialogBuilder
import com.google.android.material.shape.MaterialShapeDrawable
import com.google.android.material.shape.ShapeAppearanceModel
import com.google.android.material.R as M

/**
 * The approval dialog for a connection request to this phone (or a
 * connected device's request for control), like the desktop app's: who
 * asks (name, key fingerprint, address, a "new device" mark), the scam
 * warning, what the device may do (it sees the screen; control is a tick,
 * possible only while the accessibility service is on), Allow and Deny,
 * and the countdown after which the core refuses by itself.
 *
 * Allowing a login starts the screen sharing first: Android asks the owner
 * for consent to capture the screen (every sharing, as Android 14
 * requires); refused, the request is denied.
 */
class ApprovalActivity : AppCompatActivity() {
    private var n = 0
    @androidx.annotation.VisibleForTesting internal var dialog: AlertDialog? = null
    private var left: TextView? = null
    private var control: MaterialCheckBox? = null
    private var controlNote: View? = null
    private var answered = false
    private var pendingControl = false
    private lateinit var ui: SettingsUi

    private val listener: (HostState) -> Unit = { s -> onState(s) }

    private val consent = registerForActivityResult(ActivityResultContracts.StartActivityForResult()) { r ->
        val data = r.data
        if (r.resultCode == RESULT_OK && data != null) {
            Diag.log("screen: the owner allowed screen capture")
            HostService.project(this, r.resultCode, data)
            decide(true, pendingControl)
        } else {
            Diag.log("screen: the owner did not allow screen capture")
            Toast.makeText(this, R.string.hm_consent_denied, Toast.LENGTH_LONG).show()
            decide(false, false)
        }
    }

    override fun attachBaseContext(newBase: Context) {
        super.attachBaseContext(Lang.wrap(newBase))
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        Core.init(this)
        ui = SettingsUi(this)
        n = savedInstanceState?.getInt(EXTRA_N) ?: intent.getIntExtra(EXTRA_N, 0)
        val r = HostMode.state.requests.firstOrNull { it.n == n }
        if (r == null) {
            Toast.makeText(this, R.string.hm_req_gone, Toast.LENGTH_SHORT).show()
            finish()
            return
        }
        show(r)
        HostMode.listen(listener)
    }

    override fun onSaveInstanceState(outState: Bundle) {
        super.onSaveInstanceState(outState)
        outState.putInt(EXTRA_N, n)
    }

    override fun onResume() {
        super.onResume()
        // Back from the accessibility settings: control may be possible now.
        refreshControl()
    }

    override fun onDestroy() {
        HostMode.unlisten(listener)
        dialog?.setOnDismissListener(null)
        dialog?.dismiss()
        super.onDestroy()
    }

    private fun show(r: HostRequest) {
        val who = r.label.ifEmpty { getString(R.string.hm_unknown_device) }
        val (scroll, col) = ui.page()
        scroll.setBackgroundColor(android.graphics.Color.TRANSPARENT)
        col.setPadding(ui.dp(24), ui.dp(4), ui.dp(24), ui.dp(8))
        // New or known device.
        col.addView(ui.badge(getString(if (r.firstSeen) R.string.hm_req_new else R.string.hm_req_known),
            if (r.firstSeen) SettingsUi.Tone.ERROR else SettingsUi.Tone.PRIMARY),
            LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT))
        detail(col, R.string.hm_req_device, who)
        detail(col, R.string.hm_req_key, AppInfo.shortFingerprint(r.identity))
        if (r.from.isNotEmpty()) detail(col, R.string.hm_req_from, r.from)
        // The scam warning: always shown.
        col.addView(TextView(this).apply {
            text = getString(R.string.hm_req_scam)
            TextViewCompat.setTextAppearance(this, ui.textAppearance(M.attr.textAppearanceBodyMedium))
            setTextColor(ui.color(M.attr.colorOnErrorContainer))
            setPadding(ui.dp(12), ui.dp(10), ui.dp(12), ui.dp(10))
            background = MaterialShapeDrawable(ShapeAppearanceModel.builder().setAllCornerSizes(ui.dp(12).toFloat()).build()).apply {
                fillColor = ColorStateList.valueOf(ui.color(M.attr.colorErrorContainer))
            }
            id = R.id.host_scam_warning
        }, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply { topMargin = ui.dp(12) })
        val offered = HostMode.state.controlOffered
        if (!r.isAccess) {
            col.addView(body(getString(R.string.hm_req_may)), LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply { topMargin = ui.dp(12) })
            if (offered) {
                control = MaterialCheckBox(this).apply {
                    text = getString(R.string.hm_req_control)
                    id = R.id.host_req_control
                }
                col.addView(control)
            }
        }
        // Control needs the accessibility service: a short note and a way to turn it on.
        val note = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }
        note.addView(body(getString(R.string.hm_req_control_off)))
        note.addView(ui.button(getString(R.string.hm_req_enable_control), SettingsUi.ButtonStyle.OUTLINED).apply {
            setOnClickListener { ControlDisclosure.show(this@ApprovalActivity) }
        }, LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply { topMargin = ui.dp(4) })
        if (offered) {
            col.addView(note)
            controlNote = note
        }
        left = body(getString(R.string.hm_req_left, r.left)).also {
            col.addView(it, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply { topMargin = ui.dp(8) })
        }
        refreshControl()
        dialog = MaterialAlertDialogBuilder(this)
            .setTitle(getString(if (r.isAccess) R.string.hm_req_title_access else R.string.hm_req_title, who))
            .setView(scroll)
            .setCancelable(false)
            .setNegativeButton(R.string.hm_deny) { _, _ -> decide(false, false) }
            .setPositiveButton(R.string.hm_allow_btn) { _, _ -> allow(r) }
            .setOnDismissListener { if (!answered && !consentShowing) finish() }
            .show()
    }

    private var consentShowing = false

    private fun detail(col: LinearLayout, label: Int, value: String) {
        val row = LinearLayout(this).apply { orientation = LinearLayout.HORIZONTAL }
        row.addView(body(getString(label)).apply { minWidth = ui.dp(72) })
        row.addView(TextView(this).apply {
            text = value
            TextViewCompat.setTextAppearance(this, ui.textAppearance(M.attr.textAppearanceBodyMedium))
            setTextColor(ui.color(M.attr.colorOnSurface))
            setTextIsSelectable(true)
        }, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
        col.addView(row, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply { topMargin = ui.dp(8) })
    }

    private fun body(s: String) = TextView(this).apply {
        text = s
        TextViewCompat.setTextAppearance(this, ui.textAppearance(M.attr.textAppearanceBodyMedium))
        setTextColor(ui.color(M.attr.colorOnSurfaceVariant))
    }

    private fun refreshControl() {
        val on = ControlService.instance != null
        control?.isEnabled = on
        if (!on) control?.isChecked = false
        controlNote?.shown(!on)
    }

    private fun onState(s: HostState) {
        if (answered) return
        val r = s.requests.firstOrNull { it.n == n }
        if (r == null) {
            // Refused on timeout, or answered elsewhere (the notification's Deny).
            if (!consentShowing) finish()
            return
        }
        left?.text = getString(R.string.hm_req_left, r.left)
        refreshControl()
    }

    private fun allow(r: HostRequest) {
        val withControl = control?.isChecked == true && ControlService.instance != null
        if (r.isAccess || ScreenCapture.active) {
            decide(true, withControl)
            return
        }
        pendingControl = withControl
        val mpm = getSystemService(MediaProjectionManager::class.java)
        if (mpm == null) { decide(false, false); return }
        // The whole screen (Android 14 otherwise offers a single app).
        val i = if (Build.VERSION.SDK_INT >= 34) mpm.createScreenCaptureIntent(MediaProjectionConfig.createConfigForDefaultDisplay())
        else mpm.createScreenCaptureIntent()
        consentShowing = true
        consent.launch(i)
    }

    private fun decide(allow: Boolean, withControl: Boolean) {
        answered = true
        consentShowing = false
        HostMode.decide(n, allow, withControl) { err ->
            if (err != null && allow) Toast.makeText(applicationContext, R.string.hm_req_gone, Toast.LENGTH_SHORT).show()
            next()
        }
    }

    /** The next request still waiting, if any, else done. */
    private fun next() {
        val other = HostMode.state.requests.firstOrNull { it.n != n }
        if (other != null && !isFinishing) startActivity(intent(this, other.n))
        finish()
    }

    companion object {
        const val EXTRA_N = "request"

        fun intent(context: Context, n: Int): Intent = Intent(context, ApprovalActivity::class.java)
            .putExtra(EXTRA_N, n)
            .addFlags(Intent.FLAG_ACTIVITY_CLEAR_TOP or Intent.FLAG_ACTIVITY_SINGLE_TOP)
            // One dialog per request; the data makes the pending intents distinct.
            .setData(android.net.Uri.parse("remohop-request:$n"))
    }
}
