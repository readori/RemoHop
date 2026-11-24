package com.remohop.app

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class SavedHostTest {
    @Test fun kindsAndLabels() {
        val paired = SavedHost.of(JSONObject("""{"target":"123456789","alias":"Office PC","login":"pairing","presence":"not_checked","last_seen":"2026-11-24T10:00:00Z"}"""))
        assertEquals(SavedHost.Kind.PAIRED, paired.kind)
        assertEquals("Office PC", paired.label)
        assertTrue(paired.isId)
        assertEquals(1795514400L, paired.lastSeen)
        val recent = SavedHost.of(JSONObject("""{"target":"10.0.0.2:21118","login":"password","presence":"not_checked"}"""))
        assertEquals(SavedHost.Kind.RECENT, recent.kind)
        assertEquals("10.0.0.2:21118", recent.label)
        assertFalse(recent.isId)
        assertEquals(0L, recent.lastSeen)
        assertEquals(SavedHost.Kind.KEY, SavedHost.of(JSONObject("""{"target":"x:1","login":"key"}""")).kind)
        assertEquals(SavedHost.Kind.RECENT, SavedHost.of(JSONObject("""{"target":"x:1","login":"account"}""")).kind)
        assertEquals(0L, SavedHost.parseTime("yesterday"))
    }

    @Test fun rendezvousKeyOffer() {
        val ok = RendezvousCheck.parse(JSONObject("""{"ok":true,"code":"ok","server":"s:21116","key":"AAAA"}"""))
        assertEquals(RendezvousStatus.OK, ok.status)
        assertTrue(ok.offerKey(""))
        assertFalse("a key is already saved", ok.offerKey("BBBB"))
        val bad = RendezvousCheck.parse(JSONObject("""{"ok":false,"code":"unreachable","error":"no answer"}"""))
        assertEquals(RendezvousStatus.UNREACHABLE, bad.status)
        assertFalse(bad.offerKey(""))
    }
}
