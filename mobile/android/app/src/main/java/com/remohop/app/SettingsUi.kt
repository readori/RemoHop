package com.remohop.app

import android.app.Activity
import android.content.res.ColorStateList
import android.content.res.Configuration
import android.graphics.drawable.RippleDrawable
import android.text.InputType
import android.text.TextUtils
import android.util.TypedValue
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.view.inputmethod.EditorInfo
import android.widget.FrameLayout
import android.widget.ImageView
import android.widget.LinearLayout
import android.widget.TextView
import androidx.core.graphics.ColorUtils
import androidx.core.view.ViewCompat
import androidx.core.widget.NestedScrollView
import androidx.core.widget.TextViewCompat
import com.google.android.material.button.MaterialButton
import com.google.android.material.color.MaterialColors
import com.google.android.material.materialswitch.MaterialSwitch
import com.google.android.material.shape.MaterialShapeDrawable
import com.google.android.material.shape.ShapeAppearanceModel
import com.google.android.material.textfield.TextInputEditText
import com.google.android.material.textfield.TextInputLayout
import com.google.android.material.R as M

/**
 * Builds the Settings screens in code from Material Components, following
 * Material 3 (with the grouped lists of Android's own settings):
 *
 * - the page on surfaceContainer (surface in the dark theme), each section
 *   a group of list items on a brighter container: 24 dp outer corners,
 *   4 dp between items, 2 dp apart; a titleSmall subheader in primary above;
 * - list items: a leading icon in a 40 dp tonal circle (the section's
 *   container role), headline bodyLarge, supporting text bodyMedium in
 *   onSurfaceVariant, a trailing value or chevron; one line 56 dp, two 72 dp;
 * - M3 switches, outlined text fields with supporting/error text, filled
 *   and tonal buttons, a status card in primaryContainer.
 *
 * Colours come from the theme's roles (dynamic colour on Android 12+, the
 * cobalt scheme otherwise); there are no literals here.
 */
class SettingsUi(private val a: Activity) {
    private val res = a.resources
    fun dp(v: Int) = TypedValue.applyDimension(TypedValue.COMPLEX_UNIT_DIP, v.toFloat(), res.displayMetrics).toInt()
    fun color(attr: Int) = MaterialColors.getColor(a, attr, javaClass.simpleName)

    val night = (res.configuration.uiMode and Configuration.UI_MODE_NIGHT_MASK) == Configuration.UI_MODE_NIGHT_YES
    /** The page behind the groups. */
    val pageColor get() = color(if (night) M.attr.colorSurface else M.attr.colorSurfaceContainer)
    /** The groups' items. */
    val itemColor get() = color(if (night) M.attr.colorSurfaceContainerHigh else M.attr.colorSurfaceBright)

    /** The container role of a leading icon (one per section, as in the system settings). */
    enum class Tone { PRIMARY, SECONDARY, TERTIARY, ERROR, NEUTRAL }

    private fun toneColors(t: Tone): Pair<Int, Int> = when (t) {
        Tone.PRIMARY -> color(M.attr.colorPrimaryContainer) to color(M.attr.colorOnPrimaryContainer)
        Tone.SECONDARY -> color(M.attr.colorSecondaryContainer) to color(M.attr.colorOnSecondaryContainer)
        Tone.TERTIARY -> color(M.attr.colorTertiaryContainer) to color(M.attr.colorOnTertiaryContainer)
        Tone.ERROR -> color(M.attr.colorErrorContainer) to color(M.attr.colorOnErrorContainer)
        Tone.NEUTRAL -> color(M.attr.colorSurfaceContainerHighest) to color(M.attr.colorOnSurfaceVariant)
    }

    /** A scrolling page; add sections to the returned column. */
    fun page(): Pair<NestedScrollView, LinearLayout> {
        val scroll = NestedScrollView(a).apply { isFillViewport = true; clipToPadding = false; setBackgroundColor(pageColor) }
        val col = LinearLayout(a).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(0, dp(4), 0, dp(32))
        }
        scroll.addView(col, ViewGroup.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
        return scroll to col
    }

    /**
     * A group of items that shapes them as one rounded container: 24 dp
     * corners at its ends, 4 dp between items (only visible items count).
     */
    inner class Group(var tone: Tone) : LinearLayout(a) {
        init { orientation = VERTICAL }

        override fun onMeasure(widthMeasureSpec: Int, heightMeasureSpec: Int) {
            reshape()
            super.onMeasure(widthMeasureSpec, heightMeasureSpec)
        }

        private fun reshape() {
            val shown = (0 until childCount).map { getChildAt(it) }.filter { it.visibility != View.GONE }
            val big = dp(24).toFloat()
            val small = dp(4).toFloat()
            shown.forEachIndexed { i, v ->
                val top = if (i == 0) big else small
                val bottom = if (i == shown.size - 1) big else small
                val model = ShapeAppearanceModel.builder()
                    .setTopLeftCornerSize(top).setTopRightCornerSize(top)
                    .setBottomLeftCornerSize(bottom).setBottomRightCornerSize(bottom).build()
                segment(v, model)
                val lp = v.layoutParams as LayoutParams
                val want = if (i == 0) 0 else dp(2)
                if (lp.topMargin != want) lp.topMargin = want
            }
        }
    }

    /** Gives [v] the item container (and a ripple of the same shape when it is clickable). */
    private fun segment(v: View, model: ShapeAppearanceModel) {
        val bg = v.background as? MaterialShapeDrawable ?: MaterialShapeDrawable().also {
            it.fillColor = ColorStateList.valueOf(itemColor)
            v.background = it
        }
        if (bg.shapeAppearanceModel != model) bg.shapeAppearanceModel = model
        if (v.isClickable) {
            val fg = v.foreground as? RippleDrawable
            if (fg == null) {
                val mask = MaterialShapeDrawable(model).apply { fillColor = ColorStateList.valueOf(android.graphics.Color.WHITE) }
                v.foreground = RippleDrawable(ColorStateList.valueOf(ColorUtils.setAlphaComponent(color(M.attr.colorOnSurface), 0x1F)), null, mask)
            } else (fg.findDrawableByLayerId(android.R.id.mask) ?: fg.getDrawable(fg.numberOfLayers - 1)).let { m ->
                (m as? MaterialShapeDrawable)?.let { if (it.shapeAppearanceModel != model) it.shapeAppearanceModel = model }
            }
        }
    }

    /**
     * A section: an optional subheader, then a group for its rows (added
     * to the returned container). [tone] colours the rows' leading icons.
     */
    fun section(parent: LinearLayout, title: String?, tone: Tone = Tone.SECONDARY): Group {
        if (title != null) {
            parent.addView(TextView(a).apply {
                text = title
                TextViewCompat.setTextAppearance(this, textAppearance(M.attr.textAppearanceTitleSmall))
                setTextColor(color(M.attr.colorPrimary))
                setPadding(dp(32), dp(20), dp(32), dp(8))
                ViewCompat.setAccessibilityHeading(this, true)
            })
        }
        val box = Group(tone)
        parent.addView(box, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply {
            marginStart = dp(16); marginEnd = dp(16); topMargin = if (title == null) dp(16) else 0
        })
        return box
    }

    fun textAppearance(attr: Int): Int {
        val tv = TypedValue()
        a.theme.resolveAttribute(attr, tv, true)
        return tv.resourceId
    }

    private fun clickable(v: View, onClick: () -> Unit) {
        v.isClickable = true
        v.isFocusable = true
        v.setOnClickListener { onClick() }
    }

    /** A 40 dp circle in [tone]'s container role holding [icon] in its on-container role. */
    fun iconCircle(icon: Int, tone: Tone, size: Int = 40, iconSize: Int = 22): FrameLayout {
        val (bg, fg) = toneColors(tone)
        val box = FrameLayout(a).apply {
            background = MaterialShapeDrawable(ShapeAppearanceModel.builder().setAllCornerSizes(dp(size) / 2f).build()).apply {
                fillColor = ColorStateList.valueOf(bg)
            }
            importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO_HIDE_DESCENDANTS
        }
        box.addView(ImageView(a).apply {
            setImageResource(icon)
            imageTintList = ColorStateList.valueOf(fg)
        }, FrameLayout.LayoutParams(dp(iconSize), dp(iconSize), Gravity.CENTER))
        return box
    }

    /** A monogram avatar: the first letter of [label] (a desktop icon for an ID or address) in [tone]. */
    fun avatar(label: String, tone: Tone, size: Int = 40): FrameLayout {
        val letter = label.trim().firstOrNull { it.isLetter() }?.takeIf { label.trim().first().isLetter() }
        if (letter == null) return iconCircle(R.drawable.ic_s_host, tone, size, size * 11 / 20)
        val (bg, fg) = toneColors(tone)
        val box = FrameLayout(a).apply {
            background = MaterialShapeDrawable(ShapeAppearanceModel.builder().setAllCornerSizes(dp(size) / 2f).build()).apply {
                fillColor = ColorStateList.valueOf(bg)
            }
            importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO_HIDE_DESCENDANTS
        }
        box.addView(TextView(a).apply {
            text = letter.uppercaseChar().toString()
            TextViewCompat.setTextAppearance(this, textAppearance(if (size >= 64) M.attr.textAppearanceHeadlineMedium else M.attr.textAppearanceTitleMedium))
            setTextColor(fg)
            gravity = Gravity.CENTER
        }, FrameLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT))
        return box
    }

    /**
     * A small status label (not interactive): labelMedium in a rounded
     * container; [tone] picks the role, and the words carry the meaning.
     */
    fun badge(text: String, tone: Tone): TextView {
        val (bg, fg) = when (tone) {
            Tone.NEUTRAL -> color(M.attr.colorSurfaceContainerHighest) to color(M.attr.colorOnSurfaceVariant)
            else -> toneColors(tone)
        }
        return pill(text, bg, fg)
    }

    /**
     * A status on a coloured card: a dot in the state's role ([tone]: primary
     * working, error a problem, outline unknown) and the words, on the surface.
     */
    fun statusBadge(text: String, tone: Tone): TextView =
        pill(text, color(M.attr.colorSurfaceContainerLowest), color(M.attr.colorOnSurface)).also { setStatus(it, text, tone) }

    /** Changes a [statusBadge]'s words and state. */
    fun setStatus(tv: TextView, text: String, tone: Tone) {
        val dot = color(when (tone) {
            Tone.ERROR -> M.attr.colorError
            Tone.NEUTRAL -> M.attr.colorOutline
            else -> M.attr.colorPrimary
        })
        val d = MaterialShapeDrawable(ShapeAppearanceModel.builder().setAllCornerSizes(dp(4).toFloat()).build()).apply {
            fillColor = ColorStateList.valueOf(dot)
            setBounds(0, 0, dp(8), dp(8))
        }
        tv.text = text
        tv.visibility = if (text.isEmpty()) View.GONE else View.VISIBLE
        tv.setCompoundDrawablesRelative(d, null, null, null)
        tv.compoundDrawablePadding = dp(6)
        tv.setPadding(dp(10), dp(4), dp(12), dp(4))
    }

    private fun pill(text: String, bg: Int, fg: Int): TextView {
        return TextView(a).apply {
            this.text = text
            TextViewCompat.setTextAppearance(this, textAppearance(M.attr.textAppearanceLabelMedium))
            setTextColor(fg)
            maxLines = 1
            ellipsize = TextUtils.TruncateAt.END
            setPadding(dp(8), dp(3), dp(8), dp(3))
            background = MaterialShapeDrawable(ShapeAppearanceModel.builder().setAllCornerSizes(dp(8).toFloat()).build()).apply {
                fillColor = ColorStateList.valueOf(bg)
            }
        }
    }

    /**
     * A list item: leading icon, headline, supporting text under it or a
     * trailing value, and a chevron when it opens a page. [danger] uses the
     * error role (with the text saying what happens, not the colour alone).
     */
    fun row(box: LinearLayout, title: String, value: String? = null, summary: String? = null, icon: Int = 0,
            chevron: Boolean = false, danger: Boolean = false, tone: Tone? = null, leading: View? = null, trailing: View? = null,
            onClick: (() -> Unit)? = null): Row {
        val row = LinearLayout(a).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            minimumHeight = dp(if (summary != null) 72 else 56)
            setPadding(dp(16), dp(10), dp(16), dp(10))
        }
        val fg = color(if (danger) M.attr.colorError else M.attr.colorOnSurface)
        val t0 = tone ?: if (danger) Tone.ERROR else (box as? Group)?.tone ?: Tone.SECONDARY
        val lead = leading ?: if (icon != 0) iconCircle(icon, t0) else null
        if (lead != null) row.addView(lead, LinearLayout.LayoutParams(dp(40), dp(40)).apply { marginEnd = dp(16) })
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
            TextViewCompat.setTextAppearance(this, textAppearance(M.attr.textAppearanceLabelLarge))
            setTextColor(color(M.attr.colorOnSurfaceVariant))
            maxLines = 1
            ellipsize = TextUtils.TruncateAt.MIDDLE
            maxWidth = dp(180)
            text = value ?: ""
            visibility = if (value.isNullOrEmpty()) View.GONE else View.VISIBLE
        }
        row.addView(v, LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply { marginStart = dp(16) })
        if (trailing != null) row.addView(trailing, LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply { marginStart = dp(12) })
        if (chevron) row.addView(ImageView(a).apply {
            setImageResource(R.drawable.ic_chevron)
            imageTintList = ColorStateList.valueOf(color(M.attr.colorOnSurfaceVariant))
            importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
        }, LinearLayout.LayoutParams(dp(20), dp(20)).apply { marginStart = dp(8) })
        if (onClick != null) clickable(row, onClick)
        box.addView(row, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
        return Row(row, t, sum, v)
    }

    class Row(val view: LinearLayout, val title: TextView, val summary: TextView, val value: TextView) {
        fun value(s: String) { value.text = s; value.visibility = if (s.isEmpty()) View.GONE else View.VISIBLE }
        fun summary(s: String) { summary.text = s; summary.visibility = if (s.isEmpty()) View.GONE else View.VISIBLE }
        fun enabled(on: Boolean) {
            view.isEnabled = on
            // The content fades, the container stays (M3 disabled list item).
            for (i in 0 until view.childCount) view.getChildAt(i).alpha = if (on) 1f else 0.38f
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
        clickable(r.view) { sw.toggle() }
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

    /** The group's last item when it is a form (fields, then buttons), else a new one. */
    private fun formBlock(box: Group): LinearLayout {
        val last = box.getChildAt(box.childCount - 1)
        if (last is LinearLayout && last.tag == FORM) return last
        return block(box, 16, 12).also { it.tag = FORM }
    }

    /** An item of the group holding other views, padded like a row. */
    fun block(box: LinearLayout, top: Int = 12, bottom: Int = 12): LinearLayout {
        val b = LinearLayout(a).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(16), dp(top), dp(16), dp(bottom))
        }
        box.addView(b, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
        return b
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
            // The outline's notch is cut in the item's colour.
            boxBackgroundColor = itemColor
        }
        val e = TextInputEditText(layout.context).apply {
            setText(value)
            inputType = type
            // A password field keeps the app's font (the variation sets monospace).
            if (type and InputType.TYPE_MASK_VARIATION == InputType.TYPE_TEXT_VARIATION_PASSWORD) typeface = android.graphics.Typeface.DEFAULT
            imeOptions = EditorInfo.IME_ACTION_DONE
            maxLines = 1
            importantForAutofill = View.IMPORTANT_FOR_AUTOFILL_NO
            setOnEditorActionListener { v, id, _ ->
                if (id == EditorInfo.IME_ACTION_DONE) { v.clearFocus(); onCommit(text.toString()); false } else false
            }
            setOnFocusChangeListener { _, has -> if (!has) onCommit(text.toString()) }
        }
        layout.addView(e, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
        // In a group, consecutive fields share one item (a form).
        val holder = if (box is Group) formBlock(box) else box
        holder.addView(layout, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply {
            if (box !is Group) { marginStart = dp(16); marginEnd = dp(16); topMargin = dp(8); bottomMargin = dp(8) }
            else if (holder.childCount > 0) topMargin = dp(8)
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
            setPadding(dp(16), dp(16), dp(16), dp(16))
            minHeight = dp(56)
            gravity = Gravity.CENTER_VERTICAL
        }
        box.addView(t, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
        return t
    }

    /** A row of buttons under a form, start aligned. */
    fun buttons(box: LinearLayout): LinearLayout {
        val row = LinearLayout(a).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
        }
        val holder = if (box is Group) formBlock(box) else box
        holder.addView(row, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply {
            topMargin = dp(8); if (box is Group) bottomMargin = dp(4)
        })
        return row
    }

    /** A filled (or tonal, or outlined) M3 button. */
    fun button(text: String, style: ButtonStyle = ButtonStyle.FILLED, icon: Int = 0): MaterialButton {
        val attr = when (style) {
            ButtonStyle.FILLED -> M.attr.materialButtonStyle
            ButtonStyle.TONAL -> 0
            ButtonStyle.OUTLINED -> M.attr.materialButtonOutlinedStyle
        }
        val b = if (style == ButtonStyle.TONAL)
            MaterialButton(android.view.ContextThemeWrapper(a, M.style.Widget_Material3_Button_TonalButton), null, 0)
        else MaterialButton(a, null, attr)
        b.text = text
        b.minHeight = dp(48)
        if (icon != 0) { b.setIconResource(icon); b.iconSize = dp(18); b.iconPadding = dp(8) }
        return b
    }

    enum class ButtonStyle { FILLED, TONAL, OUTLINED }

    /**
     * A notice card (in a tonal container): an icon, a title and short text,
     * and optional content under it (e.g. a button).
     */
    fun notice(parent: LinearLayout, icon: Int, title: String, text: String, tone: Tone = Tone.SECONDARY): LinearLayout {
        val (bg, fg) = toneColors(tone)
        val card = LinearLayout(a).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(16), dp(16), dp(16), dp(16))
            background = MaterialShapeDrawable(ShapeAppearanceModel.builder().setAllCornerSizes(dp(24).toFloat()).build()).apply {
                fillColor = ColorStateList.valueOf(bg)
            }
        }
        val head = LinearLayout(a).apply { orientation = LinearLayout.HORIZONTAL; gravity = Gravity.TOP }
        head.addView(ImageView(a).apply {
            setImageResource(icon)
            imageTintList = ColorStateList.valueOf(fg)
            importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
        }, LinearLayout.LayoutParams(dp(24), dp(24)).apply { marginEnd = dp(16) })
        val texts = LinearLayout(a).apply { orientation = LinearLayout.VERTICAL }
        texts.addView(TextView(a).apply {
            this.text = title
            TextViewCompat.setTextAppearance(this, textAppearance(M.attr.textAppearanceTitleSmall))
            setTextColor(fg)
        })
        texts.addView(TextView(a).apply {
            this.text = text
            TextViewCompat.setTextAppearance(this, textAppearance(M.attr.textAppearanceBodyMedium))
            setTextColor(fg)
            setPadding(0, dp(4), 0, 0)
        })
        head.addView(texts, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
        card.addView(head)
        parent.addView(card, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply {
            marginStart = dp(16); marginEnd = dp(16); topMargin = dp(16)
        })
        return card
    }

    /**
     * The main page's status card (primaryContainer, 28 dp corners): a large
     * icon, a label, the value in titleLarge and a status badge; an action
     * button when there is something to do. The whole card is one target.
     */
    class Hero(val view: LinearLayout, val value: TextView, val status: TextView, val action: MaterialButton?)

    fun hero(parent: LinearLayout, icon: Int, label: String, value: String, status: String?, statusTone: Tone,
             action: String?, sub: String? = null, onClick: () -> Unit): Hero {
        val bg = color(M.attr.colorPrimaryContainer)
        val fg = color(M.attr.colorOnPrimaryContainer)
        val card = LinearLayout(a).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(20), dp(20), dp(20), dp(20))
        }
        val model = ShapeAppearanceModel.builder().setAllCornerSizes(dp(28).toFloat()).build()
        card.background = MaterialShapeDrawable(model).apply { fillColor = ColorStateList.valueOf(bg) }
        card.foreground = RippleDrawable(ColorStateList.valueOf(ColorUtils.setAlphaComponent(fg, 0x1F)), null,
            MaterialShapeDrawable(model).apply { fillColor = ColorStateList.valueOf(android.graphics.Color.WHITE) })
        val top = LinearLayout(a).apply { orientation = LinearLayout.HORIZONTAL; gravity = Gravity.CENTER_VERTICAL }
        val circle = FrameLayout(a).apply {
            background = MaterialShapeDrawable(ShapeAppearanceModel.builder().setAllCornerSizes(dp(28).toFloat()).build()).apply {
                fillColor = ColorStateList.valueOf(color(M.attr.colorPrimary))
            }
            importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO_HIDE_DESCENDANTS
        }
        circle.addView(ImageView(a).apply {
            setImageResource(icon)
            imageTintList = ColorStateList.valueOf(color(M.attr.colorOnPrimary))
        }, FrameLayout.LayoutParams(dp(28), dp(28), Gravity.CENTER))
        top.addView(circle, LinearLayout.LayoutParams(dp(56), dp(56)).apply { marginEnd = dp(16) })
        val texts = LinearLayout(a).apply { orientation = LinearLayout.VERTICAL }
        texts.addView(TextView(a).apply {
            text = label
            TextViewCompat.setTextAppearance(this, textAppearance(M.attr.textAppearanceLabelLarge))
            setTextColor(fg)
            alpha = 0.8f
        })
        val v = TextView(a).apply {
            text = value
            TextViewCompat.setTextAppearance(this, textAppearance(M.attr.textAppearanceTitleLarge))
            setTextColor(fg)
            maxLines = 1
            ellipsize = TextUtils.TruncateAt.MIDDLE
        }
        texts.addView(v, LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply { topMargin = dp(2) })
        if (!sub.isNullOrEmpty()) texts.addView(TextView(a).apply {
            text = sub
            TextViewCompat.setTextAppearance(this, textAppearance(M.attr.textAppearanceBodyMedium))
            setTextColor(fg)
        }, LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply { topMargin = dp(2) })
        val st = statusBadge(status ?: "", statusTone).apply { visibility = if (status.isNullOrEmpty()) View.GONE else View.VISIBLE }
        texts.addView(st, LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply { topMargin = dp(8) })
        top.addView(texts, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
        top.addView(ImageView(a).apply {
            setImageResource(R.drawable.ic_chevron)
            imageTintList = ColorStateList.valueOf(fg)
            importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
        }, LinearLayout.LayoutParams(dp(20), dp(20)).apply { marginStart = dp(8) })
        card.addView(top)
        var btn: MaterialButton? = null
        if (action != null) {
            btn = button(action).apply { setOnClickListener { onClick() } }
            card.addView(btn, LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply {
                topMargin = dp(16); marginStart = dp(72)
            })
        }
        clickable(card, onClick)
        parent.addView(card, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply {
            marginStart = dp(16); marginEnd = dp(16); topMargin = dp(8)
        })
        return Hero(card, v, st, btn)
    }

    /**
     * A page header centred over the list: an avatar or the app icon, a
     * headline and supporting text, and optional badges under them.
     */
    fun header(parent: LinearLayout, title: String, sub: String, image: View? = null, badges: List<TextView> = emptyList()): LinearLayout {
        val box = LinearLayout(a).apply {
            orientation = LinearLayout.VERTICAL
            gravity = Gravity.CENTER_HORIZONTAL
            setPadding(dp(24), dp(16), dp(24), dp(8))
        }
        val img = image ?: ImageView(a).apply {
            setImageResource(R.mipmap.ic_launcher)
            importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
        }
        box.addView(img, LinearLayout.LayoutParams(dp(88), dp(88)))
        if (title.isNotEmpty()) box.addView(TextView(a).apply {
            text = title
            TextViewCompat.setTextAppearance(this, textAppearance(M.attr.textAppearanceHeadlineSmall))
            setTextColor(color(M.attr.colorOnSurface))
            gravity = Gravity.CENTER
            setPadding(0, dp(16), 0, 0)
        })
        if (sub.isNotEmpty()) box.addView(TextView(a).apply {
            text = sub
            TextViewCompat.setTextAppearance(this, textAppearance(M.attr.textAppearanceBodyMedium))
            setTextColor(color(M.attr.colorOnSurfaceVariant))
            gravity = Gravity.CENTER
            setPadding(0, dp(if (title.isEmpty()) 12 else 4), 0, 0)
        })
        if (badges.isNotEmpty()) {
            val row = LinearLayout(a).apply { orientation = LinearLayout.HORIZONTAL; gravity = Gravity.CENTER }
            badges.forEachIndexed { i, b ->
                row.addView(b, LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply { if (i > 0) marginStart = dp(8) })
            }
            box.addView(row, LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply { topMargin = dp(12) })
        }
        parent.addView(box, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
        return box
    }

    companion object {
        private const val QUIET = "quiet"
        private const val FORM = "form"

        /** Sets a switch made by [switchRow] without running its onChange. */
        fun setQuietly(sw: MaterialSwitch, on: Boolean) {
            sw.tag = QUIET
            sw.isChecked = on
            sw.tag = null
        }
    }
}
