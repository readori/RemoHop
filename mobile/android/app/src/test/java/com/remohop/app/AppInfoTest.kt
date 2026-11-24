package com.remohop.app

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class AppInfoTest {
    @Test fun linksFollowTheLanguage() {
        assertEquals("https://remohop.com/privacy/", AppInfo.privacyPolicy(Lang.ZH))
        assertEquals("https://remohop.com/en/privacy/", AppInfo.privacyPolicy(Lang.EN))
        assertEquals("https://remohop.com/terms/", AppInfo.terms(Lang.ZH))
        assertEquals("https://remohop.com/en/terms/", AppInfo.terms(Lang.EN))
        assertEquals("admin@remohop.com", AppInfo.SUPPORT_EMAIL)
        assertTrue(AppInfo.NOTICE.contains("AGPL-3.0-only") && AppInfo.NOTICE.contains("LICENSING.md"))
    }

    @Test fun reportInfoHasTheHeaderFieldsAndNoSecrets() {
        val j = JSONObject(AppInfo.reportInfo("1.2.3", 45, "15", 35, "Google Pixel 9", "zh"))
        assertEquals("1.2.3", j.getString("app_version"))
        assertEquals("45", j.getString("build"))
        assertEquals("Android", j.getString("platform"))
        assertEquals("15 (API 35)", j.getString("os"))
        assertEquals("Google Pixel 9", j.getString("device"))
        assertEquals("zh", j.getString("language"))
        assertEquals(6, j.length())
    }

    @Test fun sizesAndFingerprints() {
        assertEquals("0 KB", AppInfo.size(0))
        assertEquals("1 KB", AppInfo.size(1))
        assertEquals("12 KB", AppInfo.size(12 * 1024))
        assertEquals("1.5 MB", AppInfo.size(1536L * 1024))
        assertEquals("8ec2:4de8…", AppInfo.shortFingerprint("8ec2:4de8:0bd0:ccca:2efb"))
        assertEquals("ab:cd", AppInfo.shortFingerprint("ab:cd"))
    }

    @Test fun rendezvousStatusCodes() {
        assertEquals(RendezvousStatus.OK, RendezvousStatus.of("ok"))
        assertEquals(RendezvousStatus.WRONG_PROTOCOL, RendezvousStatus.of("signal_wrong_protocol"))
        assertEquals(RendezvousStatus.WRONG_PROTOCOL, RendezvousStatus.of("signal_is_host"))
        assertEquals(RendezvousStatus.UNREACHABLE, RendezvousStatus.of("unreachable"))
        assertEquals(RendezvousStatus.KEY_MISMATCH, RendezvousStatus.of("key_mismatch"))
        assertEquals(RendezvousStatus.FAILED, RendezvousStatus.of("something else"))
        assertFalse(RendezvousCheck.AVAILABLE && RendezvousCheck.run("a", "b") == null)
    }
}
