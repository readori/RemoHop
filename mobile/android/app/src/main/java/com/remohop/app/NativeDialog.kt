package com.remohop.app

import android.app.Activity
import android.content.DialogInterface
import android.text.SpannableString
import android.text.Spanned
import android.text.style.ForegroundColorSpan
import android.view.ContextThemeWrapper
import android.view.ViewGroup
import android.view.WindowManager
import android.view.inputmethod.EditorInfo
import android.widget.FrameLayout
import android.widget.LinearLayout
import androidx.appcompat.app.AlertDialog
import com.google.android.material.color.DynamicColors
import com.google.android.material.color.MaterialColors
import com.google.android.material.dialog.MaterialAlertDialogBuilder
import com.google.android.material.textfield.TextInputEditText
import com.google.android.material.textfield.TextInputLayout
import com.google.android.material.R as M

/**
 * The page's dialogs (see [DialogRequest]) as Material 3 dialogs, built
 * with MaterialAlertDialogBuilder in the Settings theme (dynamic colour on
 * Android 12+): a title, supporting text and text buttons "Cancel" and the
 * confirming one, in the error colour when it ends something; an outlined
 * text field for a prompt; a list for a few actions (destructive ones in
 * the error colour). Each answers exactly once; dismissing is Cancel.
 */
object NativeDialog {
    fun show(a: Activity, r: DialogRequest, done: (DialogAnswer) -> Unit): AlertDialog {
        val ctx = DynamicColors.wrapContextIfAvailable(ContextThemeWrapper(a, R.style.Theme_RemoHop_Settings))
        var answered = false
        val finish = { ans: DialogAnswer -> if (!answered) { answered = true; done(ans) } }
        val error = MaterialColors.getColor(ctx, M.attr.colorError, "NativeDialog")
        val b = MaterialAlertDialogBuilder(ctx)
        var field: TextInputEditText? = null
        when (r.kind) {
            "actions" -> {
                // A list and a message do not go together in a dialog: the message joins the title.
                val title = listOf(r.title, r.message).filter { it.isNotEmpty() }.joinToString(" · ")
                if (title.isNotEmpty()) b.setTitle(title)
                val items = r.actions.map { act ->
                    SpannableString(act.label).apply {
                        if (act.destructive) setSpan(ForegroundColorSpan(error), 0, length, Spanned.SPAN_EXCLUSIVE_EXCLUSIVE)
                    }
                }.toTypedArray<CharSequence>()
                b.setItems(items) { _, i -> finish(DialogAnswer.Picked(i)) }
                b.setNegativeButton(r.cancel) { _, _ -> finish(DialogAnswer.Cancelled) }
            }
            else -> {
                if (r.title.isNotEmpty()) b.setTitle(r.title)
                if (r.message.isNotEmpty()) b.setMessage(r.message)
                if (r.kind == "prompt") {
                    val ui = SettingsUi(a)
                    val layout = TextInputLayout(ctx).apply { hint = r.placeholder.ifEmpty { null } }
                    val e = TextInputEditText(layout.context).apply {
                        setText(r.value)
                        setSelection(text?.length ?: 0)
                        maxLines = 1
                        setSingleLine()
                        imeOptions = EditorInfo.IME_ACTION_DONE
                    }
                    // TextInputLayout moves the field into its inner LinearLayout and casts
                    // the params: plain ViewGroup.LayoutParams crash there.
                    layout.addView(e, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
                    val box = FrameLayout(ctx).apply { setPadding(ui.dp(24), ui.dp(8), ui.dp(24), 0) }
                    box.addView(layout)
                    b.setView(box)
                    field = e
                }
                b.setNegativeButton(r.cancel) { _, _ -> finish(DialogAnswer.Cancelled) }
                b.setPositiveButton(r.confirm) { _, _ -> finish(DialogAnswer.Confirmed(field?.text?.toString() ?: "")) }
            }
        }
        b.setOnCancelListener { finish(DialogAnswer.Cancelled) }
        b.setOnDismissListener { finish(DialogAnswer.Cancelled) }
        val d = b.show()
        if (r.kind == "confirm" && r.destructive) d.getButton(DialogInterface.BUTTON_POSITIVE)?.setTextColor(error)
        field?.let { e ->
            d.window?.setSoftInputMode(WindowManager.LayoutParams.SOFT_INPUT_STATE_ALWAYS_VISIBLE)
            e.requestFocus()
            e.setOnEditorActionListener { _, id, _ ->
                if (id == EditorInfo.IME_ACTION_DONE) { d.getButton(DialogInterface.BUTTON_POSITIVE)?.performClick(); true } else false
            }
        }
        return d
    }
}
