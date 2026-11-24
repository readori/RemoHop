package com.remohop.app

import android.app.Activity
import android.content.res.ColorStateList
import android.graphics.Typeface
import android.graphics.drawable.GradientDrawable
import android.text.InputType
import android.util.TypedValue
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.view.inputmethod.EditorInfo
import android.widget.EditText
import android.widget.ImageView
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.Switch
import android.widget.TextView
import androidx.core.view.ViewCompat

/**
 * Builds the Settings screens in code, in the Material 3 list style:
 * section titles in the accent colour over rounded surface cards of rows
 * (icon, title, value or summary, chevron), 56 dp and taller rows, no
 * paragraphs (explanations are behind the ⓘ in the app bar).
 */
class SettingsUi(private val a: Activity) {
    private val res = a.resources
    fun dp(v: Int) = TypedValue.applyDimension(TypedValue.COMPLEX_UNIT_DIP, v.toFloat(), res.displayMetrics).toInt()
    private fun color(id: Int) = a.getColor(id)

    /** A scrolling page; add sections to the returned column. */
    fun page(): Pair<ScrollView, LinearLayout> {
        val scroll = ScrollView(a).apply {
            setBackgroundColor(color(R.color.rh_bg))
            isFillViewport = true
            clipToPadding = false
        }
        val col = LinearLayout(a).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(16), dp(4), dp(16), dp(24))
        }
        scroll.addView(col, ViewGroup.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
        return scroll to col
    }

    /** A section: an optional title over a rounded card; add rows to the card. */
    fun section(parent: LinearLayout, title: String?): LinearLayout {
        if (title != null) {
            parent.addView(TextView(a).also { ViewCompat.setAccessibilityHeading(it, true) }.apply {
                text = title
                setTextColor(color(R.color.rh_accent))
                setTextSize(TypedValue.COMPLEX_UNIT_SP, 14f)
                typeface = Typeface.create("sans-serif-medium", Typeface.NORMAL)
                setPadding(dp(16), dp(20), dp(16), dp(8))
                isFocusable = false
            })
        } else parent.addView(View(a), LinearLayout.LayoutParams(1, dp(16)))
        val card = LinearLayout(a).apply {
            orientation = LinearLayout.VERTICAL
            background = GradientDrawable().apply {
                setColor(color(R.color.rh_surface))
                cornerRadius = dp(16).toFloat()
            }
            clipToOutline = true
        }
        parent.addView(card, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
        return card
    }

    private fun divider(card: LinearLayout, inset: Int) {
        if (card.childCount == 0) return
        card.addView(View(a).apply { setBackgroundColor(color(R.color.rh_divider)) },
            LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, maxOf(1, dp(1) / 2)).apply { marginStart = inset })
    }

    private fun ripple(v: View) {
        val tv = TypedValue()
        a.theme.resolveAttribute(android.R.attr.selectableItemBackground, tv, true)
        v.setBackgroundResource(tv.resourceId)
    }

    /** One row: icon, title, a summary under it or a value on the right, and a chevron when it opens something. */
    fun row(card: LinearLayout, title: String, value: String? = null, summary: String? = null, icon: Int = 0,
            chevron: Boolean = false, danger: Boolean = false, onClick: (() -> Unit)? = null): Row {
        divider(card, if (icon != 0) dp(56) else dp(16))
        val row = LinearLayout(a).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            minimumHeight = dp(if (summary != null) 72 else 56)
            setPadding(dp(16), dp(10), dp(16), dp(10))
        }
        if (icon != 0) row.addView(ImageView(a).apply {
            setImageResource(icon)
            imageTintList = ColorStateList.valueOf(color(if (danger) R.color.rh_err else R.color.rh_accent))
            importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
        }, LinearLayout.LayoutParams(dp(24), dp(24)).apply { marginEnd = dp(16) })
        val texts = LinearLayout(a).apply { orientation = LinearLayout.VERTICAL }
        val t = TextView(a).apply {
            text = title
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 16f)
            setTextColor(color(if (danger) R.color.rh_err else R.color.rh_text))
        }
        texts.addView(t)
        val sum = TextView(a).apply {
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 14f)
            setTextColor(color(R.color.rh_muted))
            text = summary ?: ""
            visibility = if (summary.isNullOrEmpty()) View.GONE else View.VISIBLE
        }
        texts.addView(sum)
        row.addView(texts, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
        val v = TextView(a).apply {
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 14f)
            setTextColor(color(R.color.rh_muted))
            maxLines = 1
            ellipsize = android.text.TextUtils.TruncateAt.MIDDLE
            maxWidth = dp(180)
            text = value ?: ""
            visibility = if (value.isNullOrEmpty()) View.GONE else View.VISIBLE
        }
        row.addView(v, LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply { marginStart = dp(8) })
        if (chevron) row.addView(ImageView(a).apply {
            setImageResource(R.drawable.ic_chevron)
            imageTintList = ColorStateList.valueOf(color(R.color.rh_muted))
            importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
        }, LinearLayout.LayoutParams(dp(20), dp(20)).apply { marginStart = dp(4) })
        if (onClick != null) {
            ripple(row)
            row.isClickable = true
            row.setOnClickListener { onClick() }
        }
        card.addView(row, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
        return Row(row, t, sum, v)
    }

    class Row(val view: LinearLayout, val title: TextView, val summary: TextView, val value: TextView) {
        fun value(s: String) { value.text = s; value.visibility = if (s.isEmpty()) View.GONE else View.VISIBLE }
        fun summary(s: String) { summary.text = s; summary.visibility = if (s.isEmpty()) View.GONE else View.VISIBLE }
    }

    /** A row with a switch; [onChange] runs when the user flips it. */
    fun switchRow(card: LinearLayout, title: String, checked: Boolean, icon: Int = 0, onChange: (Boolean) -> Unit): Switch {
        val r = row(card, title, icon = icon)
        val sw = Switch(a).apply {
            isChecked = checked
            contentDescription = title
            setOnCheckedChangeListener { b, on -> if (b.tag != QUIET) onChange(on) }
        }
        r.view.addView(sw, LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT))
        r.view.isClickable = true
        ripple(r.view)
        r.view.setOnClickListener { sw.toggle() }
        return sw
    }

    companion object {
        private const val QUIET = "quiet"

        /** Sets a switch made by [switchRow] without running its onChange. */
        fun setQuietly(sw: Switch, on: Boolean) {
            sw.tag = QUIET
            sw.isChecked = on
            sw.tag = null
        }
    }

    /**
     * A labelled text field that saves itself: [onCommit] runs when the
     * keyboard's Done is pressed or the field loses focus.
     */
    fun field(card: LinearLayout, label: String, value: String, hint: String = "", type: Int = InputType.TYPE_CLASS_TEXT,
              onCommit: (String) -> Unit): EditText {
        divider(card, dp(16))
        val box = LinearLayout(a).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(16), dp(10), dp(16), dp(6))
        }
        box.addView(TextView(a).apply {
            text = label
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 13f)
            setTextColor(color(R.color.rh_muted))
            importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
        })
        val e = EditText(a).apply {
            setText(value)
            this.hint = hint
            inputType = type
            imeOptions = EditorInfo.IME_ACTION_DONE
            setSingleLine(true)
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 16f)
            minHeight = dp(48)
            contentDescription = label
            importantForAutofill = View.IMPORTANT_FOR_AUTOFILL_NO
            background = null
            setPadding(0, dp(4), 0, dp(4))
            setOnEditorActionListener { v, id, _ ->
                if (id == EditorInfo.IME_ACTION_DONE) { v.clearFocus(); onCommit(text.toString()); false } else false
            }
            setOnFocusChangeListener { _, has -> if (!has) onCommit(text.toString()) }
        }
        box.addView(e, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
        card.addView(box, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
        return e
    }

    /** Plain text in a card (a fingerprint, a status line). */
    fun text(card: LinearLayout, s: String, mono: Boolean = false, colorId: Int = R.color.rh_text): TextView {
        divider(card, dp(16))
        val t = TextView(a).apply {
            text = s
            setTextSize(TypedValue.COMPLEX_UNIT_SP, if (mono) 15f else 15f)
            setTextColor(color(colorId))
            if (mono) typeface = Typeface.MONOSPACE
            setTextIsSelectable(mono)
            setPadding(dp(16), dp(14), dp(16), dp(14))
        }
        card.addView(t, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
        return t
    }

    /** The About header: the app icon, its name and version. */
    fun header(parent: LinearLayout, title: String, sub: String) {
        val box = LinearLayout(a).apply {
            orientation = LinearLayout.VERTICAL
            gravity = Gravity.CENTER_HORIZONTAL
            setPadding(0, dp(20), 0, dp(4))
        }
        box.addView(ImageView(a).apply {
            setImageResource(R.mipmap.ic_launcher)
            importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
        }, LinearLayout.LayoutParams(dp(80), dp(80)))
        box.addView(TextView(a).apply {
            text = title
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 22f)
            typeface = Typeface.create("sans-serif-medium", Typeface.NORMAL)
            setTextColor(color(R.color.rh_text))
            setPadding(0, dp(8), 0, 0)
        })
        box.addView(TextView(a).apply {
            text = sub
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 14f)
            setTextColor(color(R.color.rh_muted))
        })
        parent.addView(box, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
    }
}
