package com.remohop.app

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class BridgeTest {
    private val origin = "http://127.0.0.1:5000"

    private class FakeHost : BridgeHost {
        val calls = mutableListOf<String>()
        var clip = "native text"
        override fun clipboardRead(): String { calls += "read"; return clip }
        override fun clipboardWrite(text: String) { calls += "write:$text" }
        override fun download(url: String, name: String) { calls += "download:$name" }
        override fun setState(keepScreenOn: Boolean, fullscreen: Boolean) { calls += "state:$keepScreenOn,$fullscreen" }
        override fun keyboard(show: Boolean) { calls += "keyboard:$show" }
        override fun haptic() { calls += "haptic" }
        override fun openSettings() { calls += "settings" }
        override fun openSettings(page: String) { calls += "settings:$page" }
        override fun diagLog(text: String) { calls += "log:${text.length}:${text.take(12)}" }
    }

    private fun call(host: FakeHost, msg: String, src: String? = origin, main: Boolean = true): JSONObject? {
        var out: String? = null
        Bridge(host, origin).handle(msg, src, main) { out = it }
        return out?.let { JSONObject(it) }
    }

    @Test fun clipboardRoundTrip() {
        val h = FakeHost()
        val r = call(h, """{"id":7,"method":"clipboard.read","args":{}}""")!!
        assertEquals(7, r.getInt("id"))
        assertEquals("native text", r.getJSONObject("result").getString("text"))
        val w = call(h, """{"id":8,"method":"clipboard.write","args":{"text":"from page"}}""")!!
        assertTrue(w.has("result"))
        assertEquals(listOf("read", "write:from page"), h.calls)
    }

    @Test fun refusesOtherOriginsAndFrames() {
        val h = FakeHost()
        assertNull(call(h, """{"id":1,"method":"clipboard.read"}""", src = "http://127.0.0.1:5001"))
        assertNull(call(h, """{"id":1,"method":"clipboard.read"}""", src = null))
        assertNull(call(h, """{"id":1,"method":"clipboard.read"}""", main = false))
        assertTrue(h.calls.isEmpty())
    }

    @Test fun downloadsMustBeThisGateways() {
        val h = FakeHost()
        val ok = call(h, """{"id":2,"method":"download","args":{"url":"http://127.0.0.1:5000/tok/api/v1/files/download?t=x","name":"../a/report.pdf"}}""")!!
        assertTrue(ok.has("result"))
        val bad = call(h, """{"id":3,"method":"download","args":{"url":"http://example.com/api/v1/files/download","name":"x"}}""")!!
        assertEquals("not a download of this gateway", bad.getString("error"))
        assertEquals(listOf("download:report.pdf"), h.calls)
    }

    @Test fun stateKeyboardHapticSettings() {
        val h = FakeHost()
        call(h, """{"id":4,"method":"state","args":{"keepScreenOn":true,"fullscreen":false}}""")
        call(h, """{"id":5,"method":"keyboard","args":{"show":true}}""")
        call(h, """{"id":6,"method":"haptic"}""")
        call(h, """{"id":9,"method":"openSettings"}""")
        assertEquals(listOf("state:true,false", "keyboard:true", "haptic", "settings"), h.calls)
    }

    @Test fun settingsPagesAndDiagnosticLog() {
        val h = FakeHost()
        call(h, """{"id":1,"method":"openSettings","args":{"page":"privacy"}}""")
        call(h, """{"id":2,"method":"openSettings","args":{"page":"../../evil"}}""")
        call(h, """{"id":3,"method":"log","args":{"text":"page error: x"}}""")
        val long = call(h, JSONObject().put("id", 4).put("method", "log").put("args", JSONObject().put("text", "y".repeat(5000))).toString())!!
        assertTrue(long.has("result"))
        assertEquals(listOf("settings:privacy", "settings", "log:13:page error: ", "log:${Bridge.MAX_LOG}:yyyyyyyyyyyy"), h.calls)
    }

    @Test fun badInput() {
        val h = FakeHost()
        assertNull(call(h, "not json"))
        assertEquals("unknown method", call(h, """{"id":1,"method":"exec"}""")!!.getString("error"))
        assertEquals("text too long", call(h, JSONObject().put("id", 2).put("method", "clipboard.write")
            .put("args", JSONObject().put("text", "x".repeat(Bridge.MAX_CLIP + 1))).toString())!!.getString("error"))
        assertNull(call(h, """{"method":"haptic"}""")) // no id: no reply, but done
        assertEquals(listOf("haptic"), h.calls)
    }

    @Test fun backResult() {
        assertTrue(Bridge.backHandled("\"handled\""))
        assertFalse(Bridge.backHandled("\"unhandled\""))
        assertFalse(Bridge.backHandled("null"))
        assertFalse(Bridge.backHandled(null))
    }
}
