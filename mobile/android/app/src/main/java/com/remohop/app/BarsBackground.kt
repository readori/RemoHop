package com.remohop.app

import android.graphics.Canvas
import android.graphics.ColorFilter
import android.graphics.Paint
import android.graphics.PixelFormat
import android.graphics.drawable.Drawable

/**
 * The main window's background behind the system bars: the page's top bar
 * colour under the status bar ([top] pixels), the page colour elsewhere
 * (under the navigation bar), so the bars blend into the page.
 */
class BarsBackground(private val topColor: Int, private val bottomColor: Int) : Drawable() {
    var top = 0
        set(v) { if (field != v) { field = v; invalidateSelf() } }
    private val paint = Paint()

    override fun draw(canvas: Canvas) {
        val b = bounds
        paint.color = bottomColor
        canvas.drawRect(b, paint)
        if (top > 0) {
            paint.color = topColor
            canvas.drawRect(b.left.toFloat(), b.top.toFloat(), b.right.toFloat(), (b.top + top).toFloat(), paint)
        }
    }

    override fun setAlpha(alpha: Int) {}
    override fun setColorFilter(colorFilter: ColorFilter?) {}
    @Deprecated("Deprecated in Java")
    override fun getOpacity(): Int = PixelFormat.OPAQUE
}
