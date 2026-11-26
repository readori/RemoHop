package com.remohop.app

import android.accessibilityservice.AccessibilityService
import android.accessibilityservice.GestureDescription
import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.content.Intent
import android.graphics.Path
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.os.SystemClock
import android.provider.Settings
import android.view.ViewConfiguration
import android.view.accessibility.AccessibilityEvent
import android.view.accessibility.AccessibilityNodeInfo
import com.remohop.app.core.remohopcore.InputInjector
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit

/**
 * The accessibility service that lets an approved controller operate the
 * phone: taps, long presses, swipes and drags, scrolling and pinching as
 * gestures (dispatchGesture), Back, Home, Recents and the notification
 * shade as global actions, and text into the focused field.
 *
 * The phone's owner switches it on in the system's accessibility settings,
 * after the in-app disclosure ([ControlDisclosure]). It does nothing by
 * itself: it reads no screen content and reacts to no events, and acts only
 * on input the core passes on, which the core does only for a session the
 * owner approved with control ticked (internal/mobile/host.go), while host
 * mode runs. [ControlInjector] checks that too before anything is
 * dispatched.
 */
class ControlService : AccessibilityService() {
    override fun onServiceConnected() {
        super.onServiceConnected()
        instance = this
        Diag.log("control: accessibility service connected")
        HostMode.refresh()
    }

    override fun onAccessibilityEvent(event: AccessibilityEvent?) {}

    override fun onInterrupt() {}

    override fun onUnbind(intent: Intent?): Boolean {
        if (instance === this) instance = null
        Diag.log("control: accessibility service disconnected")
        HostMode.refresh()
        return super.onUnbind(intent)
    }

    override fun onDestroy() {
        if (instance === this) instance = null
        super.onDestroy()
    }

    /**
     * Dispatches [g] and waits up to [waitMs] for it to finish: a gesture
     * dispatched while another plays cancels that one, so input waits its
     * turn (on the core's thread for the session, never the main thread).
     */
    fun dispatch(g: GestureDescription, waitMs: Long): Boolean {
        val started = CountDownLatch(1)
        val ok = dispatchGesture(g, object : AccessibilityService.GestureResultCallback() {
            override fun onCompleted(gestureDescription: GestureDescription?) = started.countDown()
            override fun onCancelled(gestureDescription: GestureDescription?) = started.countDown()
        }, main)
        if (ok) started.await(waitMs, TimeUnit.MILLISECONDS)
        return ok
    }

    /** The node with the input focus, if any. */
    fun focused(): AccessibilityNodeInfo? = try {
        rootInActiveWindow?.findFocus(AccessibilityNodeInfo.FOCUS_INPUT)
    } catch (e: Exception) {
        null
    }

    companion object {
        @Volatile var instance: ControlService? = null
            private set

        private val main = Handler(Looper.getMainLooper())

        /** Whether the owner has switched the service on (it may still be connecting). */
        fun enabledInSettings(context: Context): Boolean {
            val list = Settings.Secure.getString(context.contentResolver, Settings.Secure.ENABLED_ACCESSIBILITY_SERVICES) ?: return false
            val me = "${context.packageName}/${ControlService::class.java.name}"
            val short = "${context.packageName}/.${ControlService::class.java.simpleName}"
            return list.split(':').any { it.equals(me, ignoreCase = true) || it.equals(short, ignoreCase = true) }
        }

        /** The system's accessibility settings, where the owner switches the service on and off. */
        fun openSettings(context: Context) {
            val i = Intent(Settings.ACTION_ACCESSIBILITY_SETTINGS).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
            try {
                context.startActivity(i)
            } catch (e: Exception) {
                Diag.log("control: accessibility settings did not open: ${e.javaClass.simpleName}")
            }
        }
    }
}

/**
 * The core's InputInjector (internal/mobile/host.go): a controller's mouse,
 * wheel and keys become gestures, global actions and text on the phone,
 * through [ControlService]. Called on core threads, never the main thread.
 */
class ControlInjector(private val context: Context) : InputInjector {
    private val strokes = StrokeTracker()
    private var lastX = 0.5
    private var lastY = 0.5
    private var shift = false
    private var ctrl = false

    private fun service(): ControlService? = ControlService.instance

    override fun unavailable(): String = when {
        service() != null -> ""
        ControlService.enabledInSettings(context) -> "the phone's control service is starting"
        else -> "the phone's owner has not switched on RemoHop's accessibility service, so it can only be viewed"
    }

    /** The display's size in its current rotation, in pixels. */
    private fun display(): Pair<Int, Int> = ScreenCapture.displaySize(context)

    private fun toDisplay(x: Double, y: Double): Pair<Float, Float> {
        val (dw, dh) = display()
        val (pw, ph) = ScreenCapture.pictureSize()
        return CoordMap.map(x, y, pw, ph, dw, dh)
    }

    private fun need(): ControlService {
        check(HostMode.state.controlled) { "no session may control the phone" }
        return service() ?: error(unavailable())
    }

    @Synchronized
    override fun move(x: Double, y: Double) {
        lastX = x; lastY = y
        if (!strokes.active) return
        val (px, py) = toDisplay(x, y)
        strokes.move(px, py, SystemClock.uptimeMillis())
    }

    @Synchronized
    override fun button(button: Long, down: Boolean, x: Double, y: Double) {
        lastX = x; lastY = y
        val svc = need()
        val (px, py) = toDisplay(x, y)
        val now = SystemClock.uptimeMillis()
        when (button.toInt()) {
            0 -> if (down) strokes.down(px, py, now) else strokes.up(px, py, now, slop())?.let { play(svc, it) }
            // The other buttons are the phone's navigation keys: right (and
            // the mouse's back button) is Back, middle is Home, forward is
            // Recents. They act on release, as a click does.
            1 -> if (!down) svc.performGlobalAction(AccessibilityService.GLOBAL_ACTION_HOME)
            2, 3 -> if (!down) svc.performGlobalAction(AccessibilityService.GLOBAL_ACTION_BACK)
            4 -> if (!down) svc.performGlobalAction(AccessibilityService.GLOBAL_ACTION_RECENTS)
        }
    }

    private fun slop(): Float = ViewConfiguration.get(context).scaledTouchSlop.toFloat()

    private fun play(svc: ControlService, g: StrokeTracker.Gesture) {
        val b = GestureDescription.Builder()
        val ms = when (g) {
            is StrokeTracker.Gesture.Tap -> TAP_MS.also {
                b.addStroke(GestureDescription.StrokeDescription(Path().apply { moveTo(g.x, g.y) }, 0, it))
            }
            is StrokeTracker.Gesture.LongPress -> g.durationMs.also {
                b.addStroke(GestureDescription.StrokeDescription(Path().apply { moveTo(g.x, g.y) }, 0, it))
            }
            is StrokeTracker.Gesture.Swipe -> g.durationMs.also {
                val p = Path()
                g.points.forEachIndexed { i, pt -> if (i == 0) p.moveTo(pt.x, pt.y) else p.lineTo(pt.x, pt.y) }
                b.addStroke(GestureDescription.StrokeDescription(p, 0, it))
            }
        }
        svc.dispatch(b.build(), ms + 100)
    }

    @Synchronized
    override fun wheel(dx: Double, dy: Double) {
        val svc = need()
        val (dw, dh) = display()
        val (px, py) = toDisplay(lastX, lastY)
        if (ctrl) {
            pinch(svc, px, py, dy, dw, dh)
            return
        }
        val (a, b) = StrokeTracker.scroll(px, py, dx, dy, dw, dh) ?: return
        val p = Path().apply { moveTo(a.x, a.y); lineTo(b.x, b.y) }
        svc.dispatch(GestureDescription.Builder().addStroke(GestureDescription.StrokeDescription(p, 0, 120)).build(), 250)
    }

    /** Ctrl + wheel: two fingers apart (zoom in, wheel up) or together around the pointer. */
    private fun pinch(svc: ControlService, x: Float, y: Float, dy: Double, dw: Int, dh: Int) {
        val r0 = minOf(dw, dh) / 10f
        val r1 = if (dy < 0) r0 * 2.5f else r0 / 2.5f
        fun finger(sign: Float, r: Float) = Pair((x + sign * r).coerceIn(0f, dw - 1f), y.coerceIn(0f, dh - 1f))
        val b = GestureDescription.Builder()
        for (sign in listOf(-1f, 1f)) {
            val (sx, sy) = finger(sign, if (dy < 0) r0 else r0 * 2.5f)
            val (ex, ey) = finger(sign, if (dy < 0) r1 else r0)
            b.addStroke(GestureDescription.StrokeDescription(Path().apply { moveTo(sx, sy); lineTo(ex, ey) }, 0, 250))
        }
        svc.dispatch(b.build(), 350)
    }

    @Synchronized
    override fun key(code: String, down: Boolean) {
        when (code) {
            "ShiftLeft", "ShiftRight" -> { shift = down; return }
            "ControlLeft", "ControlRight" -> { ctrl = down; return }
        }
        if (!down) return
        val svc = need()
        Keys.action(code)?.let { svc.performGlobalAction(it); return }
        when (code) {
            "Backspace" -> editFocused(svc) { it.dropLast(1) }
            "Enter", "NumpadEnter" -> enter(svc)
            else -> Keys.char(code, shift)?.let { c -> editFocused(svc) { it + c } }
        }
    }

    private fun enter(svc: ControlService) {
        val node = svc.focused() ?: return
        if (Build.VERSION.SDK_INT >= 30 && node.actionList.any { it.id == android.R.id.accessibilityActionImeEnter }) {
            node.performAction(android.R.id.accessibilityActionImeEnter)
        } else if (node.isMultiLine) {
            editFocused(svc) { it + "\n" }
        }
    }

    /** Replaces the focused field's text with [f] of it. */
    private fun editFocused(svc: ControlService, f: (String) -> String): Boolean {
        val node = svc.focused() ?: return false
        if (!node.isEditable) return false
        // A field showing its hint reports the hint as its text.
        val current = if (node.isShowingHintText) "" else node.text?.toString().orEmpty()
        val args = Bundle().apply { putCharSequence(AccessibilityNodeInfo.ACTION_ARGUMENT_SET_TEXT_CHARSEQUENCE, f(current)) }
        return node.performAction(AccessibilityNodeInfo.ACTION_SET_TEXT, args)
    }

    override fun typeText(text: String) {
        val svc = need()
        if (editFocused(svc) { it + text }) return
        // No editable field takes text directly (a web page, a custom
        // view): paste it from the clipboard into whatever has the focus.
        val node = svc.focused() ?: error("no text field has the focus on the phone")
        val clip = context.getSystemService(ClipboardManager::class.java) ?: error("no clipboard")
        val latch = CountDownLatch(1)
        Handler(Looper.getMainLooper()).post {
            clip.setPrimaryClip(ClipData.newPlainText("RemoHop", text))
            latch.countDown()
        }
        latch.await(1, TimeUnit.SECONDS)
        if (!node.performAction(AccessibilityNodeInfo.ACTION_PASTE)) error("the focused field on the phone does not take text")
    }

    private companion object {
        const val TAP_MS = 50L
    }

    @Synchronized
    override fun releaseAll() {
        strokes.cancel()
        shift = false
        ctrl = false
    }
}

/** The keys a controller's keyboard has on the phone. Pure Kotlin: unit-tested. */
object Keys {
    /**
     * The global action of [code], if it is one of the phone's navigation
     * keys: Escape (and the browser's Back key) is Back, Home is Home, the
     * Menu key is Recents, F1 opens the notifications and F2 the quick
     * settings.
     */
    fun action(code: String): Int? = when (code) {
        "Escape", "BrowserBack" -> AccessibilityService.GLOBAL_ACTION_BACK
        "Home", "BrowserHome", "MetaLeft", "MetaRight" -> AccessibilityService.GLOBAL_ACTION_HOME
        "ContextMenu" -> AccessibilityService.GLOBAL_ACTION_RECENTS
        "F1" -> AccessibilityService.GLOBAL_ACTION_NOTIFICATIONS
        "F2" -> AccessibilityService.GLOBAL_ACTION_QUICK_SETTINGS
        else -> null
    }

    /** The character a key types on a US layout, for the keys that type one. */
    fun char(code: String, shift: Boolean): String? {
        if (code.length == 4 && code.startsWith("Key")) {
            val c = code[3]
            if (c in 'A'..'Z') return if (shift) c.toString() else c.lowercaseChar().toString()
        }
        if (code.length == 6 && code.startsWith("Digit")) {
            val c = code[5]
            if (c in '0'..'9') return if (shift) ")!@#$%^&*("[c - '0'].toString() else c.toString()
        }
        if (code.startsWith("Numpad") && code.length == 7 && code[6] in '0'..'9') return code[6].toString()
        val (plain, shifted) = when (code) {
            "Space" -> " " to " "
            "Minus" -> "-" to "_"
            "Equal" -> "=" to "+"
            "BracketLeft" -> "[" to "{"
            "BracketRight" -> "]" to "}"
            "Backslash" -> "\\" to "|"
            "Semicolon" -> ";" to ":"
            "Quote" -> "'" to "\""
            "Backquote" -> "`" to "~"
            "Comma" -> "," to "<"
            "Period" -> "." to ">"
            "Slash" -> "/" to "?"
            "Tab" -> "\t" to "\t"
            else -> return null
        }
        return if (shift) shifted else plain
    }
}
