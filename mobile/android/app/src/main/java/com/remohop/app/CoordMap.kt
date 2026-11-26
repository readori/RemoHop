package com.remohop.app

import kotlin.math.abs
import kotlin.math.hypot
import kotlin.math.max
import kotlin.math.min

/**
 * Where a controller's pointer lands on the phone's display. Pure Kotlin:
 * unit-tested.
 *
 * Viewers send the pointer as fractions of the shared picture (0..1 from
 * its top-left corner, whatever size they show it at). The picture is the
 * virtual display that mirrors the screen, and the mirror keeps the
 * screen's aspect: when the phone turns, the screen's content rotates and,
 * until the encoder has been restarted at the new orientation, shows
 * letterboxed in the old picture. So the mapping finds the screen inside
 * the picture (scaled to fit, centred) and maps from there onto the display
 * in its current rotation, the coordinates gestures are dispatched in.
 */
object CoordMap {
    /**
     * Maps ([nx], [ny]) in a [pictureW] x [pictureH] picture onto a
     * [displayW] x [displayH] display (its size in its current rotation).
     * Points outside the screen's area (in the letterbox bars) go to its
     * nearest edge.
     */
    fun map(nx: Double, ny: Double, pictureW: Int, pictureH: Int, displayW: Int, displayH: Int): Pair<Float, Float> {
        if (displayW <= 0 || displayH <= 0) return 0f to 0f
        val pw = if (pictureW > 0) pictureW.toDouble() else displayW.toDouble()
        val ph = if (pictureH > 0) pictureH.toDouble() else displayH.toDouble()
        val s = min(pw / displayW, ph / displayH)
        val ox = (pw - displayW * s) / 2
        val oy = (ph - displayH * s) / 2
        val x = (clamp01(nx) * pw - ox) / s
        val y = (clamp01(ny) * ph - oy) / s
        return x.coerceIn(0.0, (displayW - 1).toDouble()).toFloat() to y.coerceIn(0.0, (displayH - 1).toDouble()).toFloat()
    }

    private fun clamp01(v: Double) = if (v.isNaN()) 0.0 else v.coerceIn(0.0, 1.0)

    /**
     * The encoder's picture for a [displayW] x [displayH] screen: the long
     * edge at most [maxSize] (0: no limit), both sides multiples of 16
     * (what hardware encoders handle everywhere), the aspect kept as well
     * as that allows.
     */
    fun pictureSize(displayW: Int, displayH: Int, maxSize: Int): Pair<Int, Int> {
        if (displayW <= 0 || displayH <= 0) return 0 to 0
        val long = max(displayW, displayH)
        val k = if (maxSize in 1 until long) maxSize.toDouble() / long else 1.0
        fun align(v: Double) = max(16, (v / 16).toInt() * 16)
        return align(displayW * k) to align(displayH * k)
    }
}

/**
 * Turns a controller's mouse into touch gestures, the way the phone's own
 * screen would take a finger. Pure Kotlin: unit-tested; [ControlService]
 * dispatches what it returns.
 *
 * Left button down starts a stroke, moves while it is down add to it, and
 * the release ends it. A stroke that stayed in place is a tap, or a long
 * press when held for [LONG_PRESS_MS]; one that moved is a swipe or drag
 * along the path it took, with its timing. (Android dispatches a gesture
 * as a whole, so a drag plays on release, at the speed it was made.)
 */
class StrokeTracker {
    data class Point(val x: Float, val y: Float, val t: Long)

    sealed class Gesture {
        data class Tap(val x: Float, val y: Float) : Gesture()
        data class LongPress(val x: Float, val y: Float, val durationMs: Long) : Gesture()
        data class Swipe(val points: List<Point>, val durationMs: Long) : Gesture()
    }

    private val points = ArrayList<Point>()
    val active: Boolean get() = points.isNotEmpty()

    fun down(x: Float, y: Float, t: Long) {
        points.clear()
        points += Point(x, y, t)
    }

    fun move(x: Float, y: Float, t: Long) {
        if (points.isEmpty()) return
        val last = points.last()
        if (x == last.x && y == last.y) return
        if (points.size >= MAX_POINTS) points.removeAt(points.size / 2) // keep both ends
        points += Point(x, y, max(t, last.t))
    }

    /** Ends the stroke at ([x], [y]); null when no stroke was under way. */
    fun up(x: Float, y: Float, t: Long, slop: Float): Gesture? {
        if (points.isEmpty()) return null
        move(x, y, t)
        val first = points.first()
        val duration = (max(t, points.last().t) - first.t).coerceAtLeast(1)
        val moved = points.any { hypot(it.x - first.x, it.y - first.y) > slop }
        val g = when {
            !moved && duration >= LONG_PRESS_MS -> Gesture.LongPress(first.x, first.y, min(duration, MAX_DURATION_MS))
            !moved -> Gesture.Tap(first.x, first.y)
            else -> Gesture.Swipe(points.toList(), duration.coerceIn(MIN_SWIPE_MS, MAX_DURATION_MS))
        }
        points.clear()
        return g
    }

    fun cancel() = points.clear()

    companion object {
        const val LONG_PRESS_MS = 500L
        const val MIN_SWIPE_MS = 50L
        /** Android caps a gesture at a minute; drags longer than this are compressed. */
        const val MAX_DURATION_MS = 10_000L
        const val MAX_POINTS = 200

        /**
         * A scroll of [dy] wheel notches (positive: down) as a swipe from
         * ([x], [y]) on a display [displayH] pixels high: the content moves
         * the way the wheel says, so the finger goes the other way, a
         * tenth of the screen per notch, at most half of it.
         */
        fun scroll(x: Float, y: Float, dx: Double, dy: Double, displayW: Int, displayH: Int): Pair<Point, Point>? {
            if (abs(dx) < 0.01 && abs(dy) < 0.01) return null
            val stepY = displayH / 10f
            val stepX = displayW / 10f
            val my = (-dy * stepY).toFloat().coerceIn(-displayH / 2f, displayH / 2f)
            val mx = (-dx * stepX).toFloat().coerceIn(-displayW / 2f, displayW / 2f)
            val ex = (x + mx).coerceIn(0f, (displayW - 1).toFloat())
            val ey = (y + my).coerceIn(0f, (displayH - 1).toFloat())
            return Point(x, y, 0) to Point(ex, ey, 0)
        }
    }
}
