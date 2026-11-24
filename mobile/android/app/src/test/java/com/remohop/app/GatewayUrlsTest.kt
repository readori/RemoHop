package com.remohop.app

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class GatewayUrlsTest {
    private val o = "http://127.0.0.1:41234"

    @Test fun originDropsTheToken() {
        assertEquals(o, GatewayUrls.origin("http://127.0.0.1:41234/AbCdEf0123456789xyzQ/"))
        assertEquals(o, GatewayUrls.origin("http://127.0.0.1:41234/tok/api/v1/files/download?t=x"))
    }

    @Test fun originRefusesAnythingButLoopbackHttp() {
        assertNull(GatewayUrls.origin("https://127.0.0.1:41234/tok/"))
        assertNull(GatewayUrls.origin("http://localhost:41234/tok/"))
        assertNull(GatewayUrls.origin("http://127.0.0.2:41234/tok/"))
        assertNull(GatewayUrls.origin("http://127.0.0.1/tok/"))
        assertNull(GatewayUrls.origin("http://user@127.0.0.1:41234/tok/"))
        assertNull(GatewayUrls.origin("http://evil.example/http://127.0.0.1:41234/"))
        assertNull(GatewayUrls.origin("javascript:alert(1)"))
        assertNull(GatewayUrls.origin("not a url at all"))
    }

    @Test fun navigationStaysOnTheGateway() {
        assertTrue(GatewayUrls.sameOrigin("http://127.0.0.1:41234/tok/#x", o))
        assertFalse(GatewayUrls.sameOrigin("http://127.0.0.1:41235/tok/", o))
        assertFalse(GatewayUrls.sameOrigin("https://example.com/", o))
        assertFalse(GatewayUrls.sameOrigin("intent://scan/#Intent;scheme=zxing;end", o))
    }

    @Test fun languageGoesInTheQuery() {
        val u = "http://127.0.0.1:41234/AbCdEf0123456789xyzQ/"
        assertEquals("$u?lang=zh", GatewayUrls.withLang(u, "zh"))
        assertEquals(o, GatewayUrls.origin(GatewayUrls.withLang(u, "en")))
        assertEquals("$u?x=1&lang=en#f", GatewayUrls.withLang("$u?lang=zh&x=1#f", "en"))
        assertEquals(u, GatewayUrls.withLang(u, "fr"))
    }

    @Test fun downloads() {
        assertTrue(GatewayUrls.isDownload("http://127.0.0.1:41234/tok/api/v1/files/download?t=a&c=b&path=%2Fx", o))
        assertFalse(GatewayUrls.isDownload("http://127.0.0.1:41234/tok/api/v1/files/list?c=b", o))
        assertFalse(GatewayUrls.isDownload("http://127.0.0.1:9/tok/api/v1/files/download?t=a", o))
        assertFalse(GatewayUrls.isDownload("http://example.com/api/v1/files/download", o))
    }

    @Test fun safeFileNames() {
        assertEquals("report.pdf", GatewayUrls.safeFileName("/home/u/report.pdf"))
        assertEquals("b.txt", GatewayUrls.safeFileName("C:\\Users\\a\\b.txt"))
        assertEquals("passwd", GatewayUrls.safeFileName("../../etc/passwd"))
        assertEquals("bashrc", GatewayUrls.safeFileName(".bashrc"))
        assertEquals("ab", GatewayUrls.safeFileName("a\u0000\nb"))
        assertEquals("download", GatewayUrls.safeFileName(""))
        assertEquals("download", GatewayUrls.safeFileName(null))
        assertEquals("download", GatewayUrls.safeFileName("..."))
        val long = GatewayUrls.safeFileName("x".repeat(300) + ".tar.gz")
        assertEquals(120, long.length)
        assertTrue(long.endsWith(".gz"))
    }
}
