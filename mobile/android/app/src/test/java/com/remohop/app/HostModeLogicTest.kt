package com.remohop.app

import android.accessibilityservice.AccessibilityService
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/** Host mode's pure logic: coordinates, gestures, keys and the core's state. */
class HostModeLogicTest {
    private fun near(want: Float, got: Float) = assertEquals(want, got, 0.51f)

    @Test fun coordinatesSameOrientation() {
        // An 864x1920 picture of a 1080x2400 portrait screen.
        val (x, y) = CoordMap.map(0.5, 0.5, 864, 1920, 1080, 2400)
        near(540f, x); near(1200f, y)
        val (x0, y0) = CoordMap.map(0.0, 0.0, 864, 1920, 1080, 2400)
        near(0f, x0); near(0f, y0)
        val (x1, y1) = CoordMap.map(1.0, 1.0, 864, 1920, 1080, 2400)
        near(1079f, x1); near(2399f, y1)
        // Out of range and NaN are clamped.
        val (xn, yn) = CoordMap.map(Double.NaN, 7.0, 864, 1920, 1080, 2400)
        near(0f, xn); near(2399f, yn)
    }

    @Test fun coordinatesRotatedBeforeTheEncoderFollows() {
        // The phone turned to landscape (2400x1080) while the picture is
        // still the portrait one: the mirror letterboxes the screen into
        // it (scale 864/2400 = 0.36, 388.8 px high, centred at y 765.6).
        val (cx, cy) = CoordMap.map(0.5, 0.5, 864, 1920, 2400, 1080)
        near(1200f, cx); near(540f, cy)
        val (lx, ly) = CoordMap.map(0.0, 765.6 / 1920, 864, 1920, 2400, 1080)
        near(0f, lx); near(0f, ly)
        // A point in the letterbox bar goes to the screen's nearest edge.
        val (bx, by) = CoordMap.map(0.25, 0.05, 864, 1920, 2400, 1080)
        near(600f, bx); near(0f, by)
        // And the encoder restarted at the new orientation: direct again.
        val (rx, ry) = CoordMap.map(0.25, 0.75, 1920, 864, 2400, 1080)
        near(600f, rx); near(810f, ry)
    }

    @Test fun coordinatesWithoutAPicture() {
        val (x, y) = CoordMap.map(0.5, 0.25, 0, 0, 1080, 2400)
        near(540f, x); near(600f, y)
        assertEquals(0f to 0f, CoordMap.map(0.5, 0.5, 100, 100, 0, 0))
    }

    @Test fun pictureSize() {
        assertEquals(864 to 1920, CoordMap.pictureSize(1080, 2400, 1920))
        assertEquals(1920 to 864, CoordMap.pictureSize(2400, 1080, 1920))
        assertEquals(720 to 1280, CoordMap.pictureSize(720, 1280, 1920)) // smaller than the cap: as is
        assertEquals(1072 to 2400, CoordMap.pictureSize(1080, 2400, 0)) // no cap: aligned to 16
        assertEquals(0 to 0, CoordMap.pictureSize(0, 0, 1920))
    }

    @Test fun strokes() {
        val s = StrokeTracker()
        assertNull(s.up(1f, 1f, 0, 8f))
        s.down(100f, 200f, 1000)
        s.move(102f, 201f, 1050)
        assertEquals(StrokeTracker.Gesture.Tap(100f, 200f), s.up(102f, 201f, 1080, 8f))
        assertFalse(s.active)
        s.down(100f, 200f, 0)
        val lp = s.up(101f, 200f, 800, 8f)
        assertTrue(lp is StrokeTracker.Gesture.LongPress && lp.durationMs == 800L)
        s.down(100f, 200f, 0)
        s.move(100f, 400f, 100)
        s.move(100f, 900f, 200)
        val sw = s.up(100f, 1000f, 300, 8f) as StrokeTracker.Gesture.Swipe
        assertEquals(4, sw.points.size)
        assertEquals(300L, sw.durationMs)
        assertEquals(1000f, sw.points.last().y)
        // A very long drag is bounded; points stay bounded too.
        s.down(0f, 0f, 0)
        for (i in 1..1000) s.move(i.toFloat(), 0f, i * 100L)
        val long = s.up(1001f, 0f, 100_100, 8f) as StrokeTracker.Gesture.Swipe
        assertEquals(StrokeTracker.MAX_DURATION_MS, long.durationMs)
        assertTrue(long.points.size <= StrokeTracker.MAX_POINTS + 1)
        assertEquals(0f, long.points.first().x)
        assertEquals(1001f, long.points.last().x)
    }

    @Test fun scroll() {
        // Wheel down: content moves up, the finger goes up a tenth of the screen.
        val (a, b) = StrokeTracker.scroll(540f, 1200f, 0.0, 1.0, 1080, 2400)!!
        assertEquals(1200f, a.y)
        assertEquals(960f, b.y)
        val (_, c) = StrokeTracker.scroll(540f, 100f, 0.0, -20.0, 1080, 2400)!!
        assertEquals(1300f, c.y) // at most half the screen
        assertNull(StrokeTracker.scroll(1f, 1f, 0.0, 0.0, 1080, 2400))
    }

    @Test fun keys() {
        assertEquals(AccessibilityService.GLOBAL_ACTION_BACK, Keys.action("Escape"))
        assertEquals(AccessibilityService.GLOBAL_ACTION_HOME, Keys.action("Home"))
        assertEquals(AccessibilityService.GLOBAL_ACTION_RECENTS, Keys.action("ContextMenu"))
        assertEquals(AccessibilityService.GLOBAL_ACTION_NOTIFICATIONS, Keys.action("F1"))
        assertNull(Keys.action("KeyA"))
        assertEquals("a", Keys.char("KeyA", false))
        assertEquals("A", Keys.char("KeyA", true))
        assertEquals("1", Keys.char("Digit1", false))
        assertEquals("!", Keys.char("Digit1", true))
        assertEquals(" ", Keys.char("Space", false))
        assertEquals("?", Keys.char("Slash", true))
        assertEquals("7", Keys.char("Numpad7", false))
        assertNull(Keys.char("ArrowLeft", false))
        assertNull(Keys.char("Key", false))
    }

    @Test fun state() {
        val s = HostState.parse("""{"on":true,"phase":"ready","id":"123 456 789","password":"ab12-cd34","online":true,
            "server":"signal.remohop.com","device":"Pixel 8","viewers":1,"control_available":false,
            "control_unavailable":"the accessibility service is off","ask_timeout":60,
            "requests":[{"n":3,"device":"Office PC","identity":"SHA256:ab:cd:ef","from":"192.0.2.7","auth":"password","left":42,"first_seen":true}],
            "sessions":[{"id":1,"label":"Laptop","identity":"SHA256:12","from":"192.0.2.8","path":"punched","seconds":75,"control":true}]}""")
        assertEquals(HostState.Status.READY, s.status)
        assertEquals("123 456 789", s.reachableAs)
        assertTrue(s.connected && s.controlled)
        assertFalse(s.controlAvailable)
        assertEquals(1, s.requests.size)
        val r = s.requests[0]
        assertEquals(3, r.n); assertEquals("Office PC", r.label); assertTrue(r.firstSeen); assertFalse(r.isAccess); assertEquals(42, r.left)
        assertEquals("Laptop", s.sessions[0].label)

        assertEquals(HostState.Status.OFFLINE, s.copy(online = false).status)
        assertEquals(HostState.Status.STARTING, s.copy(phase = "starting").status)
        assertEquals(HostState.Status.OFF, HostState.parse("""{"on":false,"phase":"off"}""").status)
        val err = HostState.parse("""{"on":false,"phase":"error","error":"bind: address in use"}""")
        assertEquals(HostState.Status.ERROR, err.status)
        assertEquals("bind: address in use", err.error)
        val lan = HostState.parse("""{"on":true,"phase":"no_server","password":"x","addresses":["192.168.1.5:40000","10.0.0.2:40000"]}""")
        assertEquals(HostState.Status.NO_SERVER, lan.status)
        assertEquals("192.168.1.5:40000", lan.reachableAs)
        assertFalse(lan.connected)
        // Garbage is "off", not a crash.
        assertEquals(HostState.OFF, HostState.parse("not json"))
        assertEquals(HostState.OFF, HostState.parse(null))
    }

    @Test fun passwordMode() {
        val own = HostState.parse("""{"on":true,"phase":"ready","id":"1","password":"Garden-42-Ok","password_mode":"fixed"}""")
        assertTrue(own.fixedPassword)
        assertEquals("Garden-42-Ok", own.password)
        // Reported while host mode is off too (no password then).
        val off = HostState.parse("""{"on":false,"phase":"off","password_mode":"fixed"}""")
        assertTrue(off.fixedPassword)
        assertEquals("", off.password)
        assertEquals(HostState.Status.OFF, off.status)
        // Temporary, missing (an older core) or unknown: the temporary password.
        assertFalse(HostState.parse("""{"on":true,"phase":"ready","password":"ab12-cd34","password_mode":"temporary"}""").fixedPassword)
        assertFalse(HostState.parse("""{"on":true,"phase":"ready","password":"ab12-cd34"}""").fixedPassword)
        assertEquals(HostState.PASSWORD_TEMPORARY, HostState.parse("""{"password_mode":"other"}""").passwordMode)
        assertFalse(HostState.OFF.fixedPassword)
    }

    @Test fun passwordForm() {
        val f = HostPasswordForm
        // Random: saved only when it changes something.
        assertEquals(HostPasswordForm.Outcome.Unchanged, f.check(false, "", "", wasOwn = false))
        assertEquals(HostPasswordForm.Outcome.Save(""), f.check(false, "typed", "", wasOwn = true))
        // Own: typed twice.
        assertEquals(HostPasswordForm.Outcome.Save("Garden-42-Ok"), f.check(true, "Garden-42-Ok", "Garden-42-Ok", wasOwn = false))
        assertEquals(HostPasswordForm.Outcome.Mismatch, f.check(true, "Garden-42-Ok", "Garden-42-ok", wasOwn = false))
        assertEquals(HostPasswordForm.Outcome.Mismatch, f.check(true, "", "x", wasOwn = true))
        // Nothing typed: keeps the own password in force, else it is missing.
        assertEquals(HostPasswordForm.Outcome.Unchanged, f.check(true, "", "", wasOwn = true))
        assertEquals(HostPasswordForm.Outcome.Empty, f.check(true, "", "", wasOwn = false))
        // Spaces and dashes alone are no password (the core drops them).
        assertEquals(HostPasswordForm.Outcome.Empty, f.check(true, " - ", " - ", wasOwn = false))
        // The core's rule is told in the app's words; anything else as it is.
        assertTrue(f.isRuleMessage("weak_password: use at least two of: letters, digits, other characters"))
        assertTrue(f.isRuleMessage("weak_password: password too short: need at least 8 characters (not counting spaces and dashes)"))
        assertFalse(f.isRuleMessage("password too short"))
        assertFalse(f.isRuleMessage("open /data/host-password: permission denied"))
    }

    @Test fun request() {
        val r = HostRequest.parse("""{"n":5,"name":"pairing name","identity":"SHA256:x","access":["input"],"session":2,"left":10}""")!!
        assertTrue(r.isAccess)
        assertEquals("pairing name", r.label)
        assertEquals(2, r.session)
        assertNull(HostRequest.parse("""{"n":0}"""))
        assertNull(HostRequest.parse("{"))
    }

    @Test fun durations() {
        assertEquals("45 s", HostTime.duration(45))
        assertEquals("3 min", HostTime.duration(200))
        assertEquals("1 h 05 min", HostTime.duration(3900))
        assertEquals("0 s", HostTime.duration(-3))
    }
}
