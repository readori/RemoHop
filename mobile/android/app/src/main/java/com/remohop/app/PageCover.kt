package com.remohop.app

import android.animation.ObjectAnimator
import android.animation.PropertyValuesHolder
import android.animation.ValueAnimator
import android.annotation.SuppressLint
import android.content.Context
import android.content.res.Configuration
import android.provider.Settings
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.view.animation.AccelerateDecelerateInterpolator
import android.widget.Button
import android.widget.FrameLayout
import android.widget.ImageView
import android.widget.LinearLayout
import android.widget.TextView

/**
 * The native cover over the WebView (see [PageRecovery]), so the user never
 * sees a blank page: the RemoHop logo on the page colour while the page
 * loads, gently breathing (scale 0.94 to 1 and opacity 0.7 to 1, 1.6 s each
 * way; still when the system's animations are off), and "Try again" under
 * it when loading failed. It fades out (200 ms) when the page is ready.
 *
 * [text] gives the activity's strings in the app's language (MainActivity
 * is not recreated for a language change).
 */
@SuppressLint("ViewConstructor")
class PageCover(context: Context, private val text: (Int) -> String, onRetry: () -> Unit) : FrameLayout(context) {
    enum class State { HIDDEN, LOADING, FAILED }

    var state = State.LOADING
        private set
    private val logo = ImageView(context)
    private val message = TextView(context)
    private val retry = Button(context)
    private var breath: ValueAnimator? = null

    init {
        isClickable = true // the WebView under it gets no touches
        isFocusable = true
        val dp = resources.displayMetrics.density
        val col = LinearLayout(context).apply {
            orientation = LinearLayout.VERTICAL
            gravity = Gravity.CENTER_HORIZONTAL
            setPadding((32 * dp).toInt(), 0, (32 * dp).toInt(), 0)
        }
        logo.setImageResource(R.drawable.ic_remohop_logo)
        col.addView(logo, LinearLayout.LayoutParams((88 * dp).toInt(), (88 * dp).toInt()))
        message.apply { textSize = 16f; gravity = Gravity.CENTER; visibility = View.GONE }
        col.addView(message, LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT)
            .apply { topMargin = (24 * dp).toInt() })
        retry.apply { visibility = View.GONE; setOnClickListener { onRetry() } }
        col.addView(retry, LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT)
            .apply { topMargin = (16 * dp).toInt() })
        addView(col, LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT, Gravity.CENTER))
        applyColors()
        applyTexts()
    }

    fun show(next: State) {
        if (next == state) return
        state = next
        animate().cancel()
        if (next == State.HIDDEN) {
            animate().alpha(0f).setDuration(FADE_MS).withEndAction {
                if (state == State.HIDDEN) { visibility = View.GONE; breathe(false) }
            }
            return
        }
        alpha = 1f
        visibility = View.VISIBLE
        val failed = next == State.FAILED
        message.visibility = if (failed) View.VISIBLE else View.GONE
        retry.visibility = if (failed) View.VISIBLE else View.GONE
        applyTexts()
        breathe(!failed)
    }

    private fun applyTexts() {
        message.text = text(R.string.page_load_failed)
        retry.text = text(R.string.gateway_retry)
        contentDescription = if (state == State.LOADING) text(R.string.page_loading) else null
    }

    /** Starts or stops the breathing (only while shown, and never with animations off). */
    private fun breathe(on: Boolean) {
        if (on && breath == null && !animationsOff()) {
            breath = ObjectAnimator.ofPropertyValuesHolder(logo,
                PropertyValuesHolder.ofFloat(View.SCALE_X, 0.94f, 1f),
                PropertyValuesHolder.ofFloat(View.SCALE_Y, 0.94f, 1f),
                PropertyValuesHolder.ofFloat(View.ALPHA, 0.7f, 1f),
            ).apply {
                duration = BREATH_MS
                interpolator = AccelerateDecelerateInterpolator()
                repeatCount = ValueAnimator.INFINITE
                repeatMode = ValueAnimator.REVERSE
                start()
            }
        } else if (!on) {
            breath?.cancel()
            breath = null
            logo.scaleX = 1f
            logo.scaleY = 1f
            logo.alpha = 1f
        }
    }

    private fun animationsOff() =
        Settings.Global.getFloat(context.contentResolver, Settings.Global.ANIMATOR_DURATION_SCALE, 1f) == 0f

    // No breathing while the window is hidden (the app in the background).
    override fun onWindowVisibilityChanged(visibility: Int) {
        super.onWindowVisibilityChanged(visibility)
        breathe(visibility == View.VISIBLE && state == State.LOADING)
    }

    override fun onDetachedFromWindow() {
        breathe(false)
        super.onDetachedFromWindow()
    }

    override fun onConfigurationChanged(newConfig: Configuration?) {
        super.onConfigurationChanged(newConfig)
        applyColors()
        applyTexts() // the app's language may have changed
    }

    private fun applyColors() {
        setBackgroundColor(context.getColor(R.color.rh_bg))
        message.setTextColor(context.getColor(R.color.md_on_surface_variant))
    }

    private companion object {
        const val FADE_MS = 200L
        const val BREATH_MS = 1_600L
    }
}
