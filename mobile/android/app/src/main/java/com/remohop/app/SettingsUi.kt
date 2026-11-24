package com.remohop.app

import android.app.Activity
import android.content.res.ColorStateList
import android.text.InputType
import android.text.TextUtils
import android.util.TypedValue
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.view.inputmethod.EditorInfo
import android.widget.ImageView
import android.widget.LinearLayout
import android.widget.TextView
import androidx.core.view.ViewCompat
import androidx.core.widget.NestedScrollView
import androidx.core.widget.TextViewCompat
import com.google.android.material.color.MaterialColors
import com.google.android.material.divider.MaterialDivider
import com.google.android.material.materialswitch.MaterialSwitch
import com.google.android.material.textfield.TextInputEditText
import com.google.android.material.textfield.TextInputLayout
import com.google.android.material.R as M

/**
 * Builds the Settings screens in code from Material Components, following
 * Material 3: list items on the surface (one line 56 dp, two lines 72 dp;
 * leading icon in onSurfaceVariant, headline bodyLarge in onSurface,
 * supporting text bodyMedium in onSurfaceVariant), section subheaders in
 * titleSmall/primary, dividers between sections instead of cards, M3
 * switches and outlined text fields with supporting/error text. Colours
 * come from the theme's roles (dynamic colour on Android 12+, the cobalt
 * scheme otherwise); nothing is a literal here.
 */
class SettingsUi(private val a: Activity) {
    private val res = a.resources
    fun dp(v: Int) = TypedValue.applyDimension(TypedValue.COMPLEX_UNIT_DIP, v.toFloat(), res.displayMetrics).toInt()
    fun color(attr: Int) = MaterialColors.getColor(a, attr, javaClass.simpleName)

    /** A scrolling page; add sections to the returned column. */
    fun page(): Pair<NestedScrollView, LinearLayout> {
        val scroll = NestedScrollView(a).apply { isFillViewport = true; clipToPadding = false }
        val col = LinearLayout(a).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(0, 0, 0, dp(24))
        }
        scroll.addView(col, ViewGroup.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
        return scroll to col
    }

    /**
     * A section: a divider from the one before, an optional subheader, then
     * the rows (added to the returned container).
     */
    fun section(parent: LinearLayout, title: String?): LinearLayout {
        if (parent.childCount > 0) parent.addView(MaterialDivider(a), LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply { topMargin = dp(8); bottomMargin = dp(8) })
        if (title != null) {
            parent.addView(TextView(a).apply {
                text = title
                TextViewCompat.setTextAppearance(this, textAppearance(M.attr.textAppearanceTitleSmall))
                setTextColor(color(M.attr.colorPrimary))
                setPadding(dp(16), dp(12), dp(16), dp(8))
                ViewCompat.setAccessibilityHeading(this, true)
            })
        }
        val box = LinearLayout(a).apply { orientation = LinearLayout.VERTICAL }
        parent.addView(box, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
        return box
    }

    private fun textAppearance(attr: Int): Int {
        val tv = TypedValue()
        a.theme.resolveAttribute(attr, tv, true)
        return tv.resourceId
    }

    private fun ripple(v: View) {
        val tv = TypedValue()
        a.theme.resolveAttribute(android.R.attr.selectableItemBackground, tv, true)
        v.setBackgroundResource(tv.resourceId)
    }

    /**
     * A list item: leading icon, headline, supporting text under it or a
     * trailing value, and a chevron when it opens a page. [danger] uses the
     * error role (with the text saying what happens, not the colour alone).
     */
    fun row(box: LinearLayout, title: String, value: String? = null, summary: String? = null, icon: Int = 0,
            chevron: Boolean = false, danger: Boolean = false, onClick: (() -> Unit)? = null): Row {
        val row = LinearLayout(a).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            minimumHeight = dp(if (summary != null) 72 else 56)
            setPadding(dp(16), dp(8), dp(24), dp(8))
        }
        val fg = color(if (danger) M.attr.colorError else M.attr.colorOnSurface)
        if (icon != 0) row.addView(ImageView(a).apply {
            setImageResource(icon)
            imageTintList = ColorStateList.valueOf(if (danger) fg else color(M.attr.colorOnSurfaceVariant))
            importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
        }, LinearLayout.LayoutParams(dp(24), dp(24)).apply { marginEnd = dp(16) })
        val texts = LinearLayout(a).apply { orientation = LinearLayout.VERTICAL }
        val t = TextView(a).apply {
            text = title
            TextViewCompat.setTextAppearance(this, textAppearance(M.attr.textAppearanceBodyLarge))
            setTextColor(fg)
        }
        texts.addView(t)
        val sum = TextView(a).apply {
            TextViewCompat.setTextAppearance(this, textAppearance(M.attr.textAppearanceBodyMedium))
            setTextColor(color(M.attr.colorOnSurfaceVariant))
            text = summary ?: ""
            visibility = if (summary.isNullOrEmpty()) View.GONE else View.VISIBLE
        }
        texts.addView(sum)
        row.addView(texts, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
        val v = TextView(a).apply {
            TextViewCompat.setTextAppearance(this, textAppearance(M.attr.textAppearanceBodyMedium))
            setTextColor(color(M.attr.colorOnSurfaceVariant))
            maxLines = 1
            ellipsize = TextUtils.TruncateAt.MIDDLE
            maxWidth = dp(180)
            text = value ?: ""
            visibility = if (value.isNullOrEmpty()) View.GONE else View.VISIBLE
        }
        row.addView(v, LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply { marginStart = dp(16) })
        if (chevron) row.addView(ImageView(a).apply {
            setImageResource(R.drawable.ic_chevron)
            imageTintList = ColorStateList.valueOf(color(M.attr.colorOnSurfaceVariant))
            importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
        }, LinearLayout.LayoutParams(dp(24), dp(24)).apply { marginStart = dp(8); marginEnd = -dp(8) })
        if (onClick != null) {
            ripple(row)
            row.isClickable = true
            row.isFocusable = true
            row.setOnClickListener { onClick() }
        }
        box.addView(row, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
        return Row(row, t, sum, v)
    }

    class Row(val view: LinearLayout, val title: TextView, val summary: TextView, val value: TextView) {
        fun value(s: String) { value.text = s; value.visibility = if (s.isEmpty()) View.GONE else View.VISIBLE }
        fun summary(s: String) { summary.text = s; summary.visibility = if (s.isEmpty()) View.GONE else View.VISIBLE }
        fun enabled(on: Boolean) {
            view.isEnabled = on
            view.alpha = if (on) 1f else 0.38f
        }
    }

    /** A list item with a trailing Material 3 switch; the whole row toggles it. */
    fun switchRow(box: LinearLayout, title: String, checked: Boolean, icon: Int = 0, onChange: (Boolean) -> Unit): MaterialSwitch {
        val r = row(box, title, icon = icon)
        val sw = MaterialSwitch(a).apply {
            isChecked = checked
            // The row carries the label; the switch is its control.
            importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
            setOnCheckedChangeListener { b, on -> if (b.tag != QUIET) onChange(on) }
        }
        r.view.addView(sw, LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply { marginStart = dp(16) })
        ripple(r.view)
        r.view.isClickable = true
        r.view.isFocusable = true
        r.view.setOnClickListener { sw.toggle() }
        ViewCompat.setAccessibilityDelegate(r.view, object : androidx.core.view.AccessibilityDelegateCompat() {
            override fun onInitializeAccessibilityNodeInfo(host: View, info: androidx.core.view.accessibility.AccessibilityNodeInfoCompat) {
                super.onInitializeAccessibilityNodeInfo(host, info)
                info.className = android.widget.Switch::class.java.name
                info.isCheckable = true
                info.isChecked = sw.isChecked
            }
        })
        return sw
    }

    /**
     * An outlined text field with a persistent label that saves itself:
     * [onCommit] runs on the keyboard's Done and when the field loses focus.
     * Errors go to the field's own supporting text ([TextInputLayout.setError]).
     */
    fun field(box: LinearLayout, label: String, value: String, hint: String = "", type: Int = InputType.TYPE_CLASS_TEXT,
              onCommit: (String) -> Unit): TextInputLayout {
        val layout = TextInputLayout(a).apply {
            this.hint = label
            placeholderText = hint.ifEmpty { null }
            isHintAnimationEnabled = true
        }
        val e = TextInputEditText(layout.context).apply {
            setText(value)
            inputType = type
            imeOptions = EditorInfo.IME_ACTION_DONE
            maxLines = 1
            importantForAutofill = View.IMPORTANT_FOR_AUTOFILL_NO
            setOnEditorActionListener { v, id, _ ->
                if (id == EditorInfo.IME_ACTION_DONE) { v.clearFocus(); onCommit(text.toString()); false } else false
            }
            setOnFocusChangeListener { _, has -> if (!has) onCommit(text.toString()) }
        }
        layout.addView(e, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
        box.addView(layout, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply {
            marginStart = dp(16); marginEnd = dp(16); topMargin = dp(8); bottomMargin = dp(8)
        })
        return layout
    }

    /** Text in the list (a fingerprint, an empty state). */
    fun text(box: LinearLayout, s: String, mono: Boolean = false, appearance: Int = M.attr.textAppearanceBodyLarge): TextView {
        val t = TextView(a).apply {
            text = s
            TextViewCompat.setTextAppearance(this, textAppearance(appearance))
            setTextColor(color(if (mono) M.attr.colorOnSurface else M.attr.colorOnSurfaceVariant))
            if (mono) typeface = android.graphics.Typeface.MONOSPACE
            setTextIsSelectable(mono)
            setPadding(dp(16), dp(12), dp(24), dp(12))
        }
        box.addView(t, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
        return t
    }

    /** The About header: the app icon, its name and version. */
    fun header(parent: LinearLayout, title: String, sub: String) {
        val box = LinearLayout(a).apply {
            orientation = LinearLayout.VERTICAL
            gravity = Gravity.CENTER_HORIZONTAL
            setPadding(0, dp(24), 0, dp(16))
        }
        box.addView(ImageView(a).apply {
            setImageResource(R.mipmap.ic_launcher)
            importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
        }, LinearLayout.LayoutParams(dp(72), dp(72)))
        box.addView(TextView(a).apply {
            text = title
            TextViewCompat.setTextAppearance(this, textAppearance(M.attr.textAppearanceHeadlineSmall))
            setTextColor(color(M.attr.colorOnSurface))
            setPadding(0, dp(12), 0, 0)
        })
        box.addView(TextView(a).apply {
            text = sub
            TextViewCompat.setTextAppearance(this, textAppearance(M.attr.textAppearanceBodyMedium))
            setTextColor(color(M.attr.colorOnSurfaceVariant))
        })
        parent.addView(box, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
    }

    companion object {
        private const val QUIET = "quiet"

        /** Sets a switch made by [switchRow] without running its onChange. */
        fun setQuietly(sw: MaterialSwitch, on: Boolean) {
            sw.tag = QUIET
            sw.isChecked = on
            sw.tag = null
        }
    }
}
