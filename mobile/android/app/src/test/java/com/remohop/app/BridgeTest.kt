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
        var pending: ((Boolean) -> Unit)? = null
        override fun ensureGateway(done: (Boolean) -> Unit) { calls += "ensure"; pending = done }
        val dialogs = mutableListOf<DialogRequest>()
        var dialogDone: ((DialogAnswer) -> Unit)? = null
        override fun showDialog(request: DialogRequest, done: (DialogAnswer) -> Unit) { dialogs += request; dialogDone = done }
    }

    /** The page asks for the app's colour roles; the host's answer is the result (nothing from another origin). */
    @Test fun theme() {
        val h = object : BridgeHost by FakeHost() {
            override fun theme(): JSONObject = JSONObject().put("light", JSONObject().put("primary", "#386a20")).put("dynamic", true)
        }
        var out: String? = null
        val b = Bridge(h, origin)
        b.handle("""{"id":3,"method":"theme"}""", "http://127.0.0.1:5001", true) { out = it }
        assertNull(out)
        b.handle("""{"id":3,"method":"theme"}""", origin, true) { out = it }
        val r = JSONObject(out!!).getJSONObject("result")
        assertEquals("#386a20", r.getJSONObject("light").getString("primary"))
        assertTrue(r.getBoolean("dynamic"))
        // A host without one answers an empty result: the page keeps its own colours.
        b.handle("""{"id":4,"method":"theme"}""", origin, true) { out = it }
        val plain = Bridge(FakeHost(), origin)
        plain.handle("""{"id":4,"method":"theme"}""", origin, true) { out = it }
        assertEquals(0, JSONObject(out!!).getJSONObject("result").length())
    }

    /** The page's confirmations become native dialogs, answered with the user's choice. */
    @Test fun confirmDialog() {
        val h = FakeHost()
        var out: String? = null
        val b = Bridge(h, origin)
        val msg = """{"id":5,"method":"confirm","args":{"title":"Disconnect from lab?","message":"The desktop ends.","confirm":"Disconnect","cancel":"Cancel","destructive":true}}"""
        b.handle(msg, "http://127.0.0.1:5001", true) { out = it }
        b.handle(msg, origin, false) { out = it }
        assertTrue(h.dialogs.isEmpty())
        b.handle(msg, origin, true) { out = it }
        assertNull("answered before the user chose", out)
        val d = h.dialogs.single()
        assertEquals(DialogRequest("confirm", "Disconnect from lab?", "The desktop ends.", "Disconnect", "Cancel", true, "", "", emptyList()), d)
        h.dialogDone!!(DialogAnswer.Confirmed())
        assertEquals(5, JSONObject(out!!).getInt("id"))
        assertTrue(JSONObject(out!!).getJSONObject("result").getBoolean("ok"))
        b.handle(msg, origin, true) { out = it }
        h.dialogDone!!(DialogAnswer.Cancelled)
        assertFalse(JSONObject(out!!).getJSONObject("result").getBoolean("ok"))
    }

    @Test fun promptAndActionDialogs() {
        val h = FakeHost()
        var out: String? = null
        val b = Bridge(h, origin)
        b.handle("""{"id":1,"method":"prompt","args":{"title":"Rename","value":"lab","placeholder":"10.0.0.2:21118","confirm":"Save","cancel":"Cancel"}}""", origin, true) { out = it }
        assertEquals("lab", h.dialogs.last().value)
        h.dialogDone!!(DialogAnswer.Confirmed("Office PC"))
        val p = JSONObject(out!!).getJSONObject("result")
        assertTrue(p.getBoolean("ok"))
        assertEquals("Office PC", p.getString("text"))
        b.handle("""{"id":2,"method":"actions","args":{"title":"lab","cancel":"Cancel","actions":[{"label":"Rename"},{"label":"Forget this computer","destructive":true},{"label":" "}]}}""", origin, true) { out = it }
        assertEquals(listOf(DialogRequest.Action("Rename", false), DialogRequest.Action("Forget this computer", true)), h.dialogs.last().actions)
        h.dialogDone!!(DialogAnswer.Picked(1))
        assertEquals(1, JSONObject(out!!).getJSONObject("result").getInt("index"))
        b.handle("""{"id":3,"method":"actions","args":{"title":"lab","cancel":"Cancel","actions":[{"label":"Rename"}]}}""", origin, true) { out = it }
        h.dialogDone!!(DialogAnswer.Cancelled)
        assertEquals(-1, JSONObject(out!!).getJSONObject("result").getInt("index"))
    }

    /** Requests that cannot make a sensible dialog are refused without showing one; long text is cut. */
    @Test fun badDialogs() {
        val h = FakeHost()
        for (m in listOf(
            """{"id":1,"method":"confirm","args":{"title":"x","cancel":"Cancel"}}""",
            """{"id":1,"method":"confirm","args":{"confirm":"OK","cancel":"Cancel"}}""",
            """{"id":1,"method":"actions","args":{"title":"x","cancel":"Cancel","actions":[]}}""",
            """{"id":1,"method":"prompt","args":{"title":"x","confirm":"OK"}}""",
        )) assertEquals(m, "bad dialog", call(h, m)!!.getString("error"))
        assertTrue(h.dialogs.isEmpty())
        val acts = org.json.JSONArray()
        repeat(20) { acts.put(JSONObject().put("label", "a$it")) }
        call(h, JSONObject().put("id", 9).put("method", "actions").put("args", JSONObject().put("title", "t".repeat(5000)).put("cancel", "Cancel").put("actions", acts)).toString())
        assertEquals(DialogRequest.MAX_TITLE, h.dialogs.last().title.length)
        assertEquals(DialogRequest.MAX_ACTIONS, h.dialogs.last().actions.size)
    }

    /** The page's "gateway.ensure" is answered once the app has checked the gateway. */
    @Test fun ensureGatewayAnsweredLater() {
        val h = FakeHost()
        var out: String? = null
        val b = Bridge(h, origin)
        b.handle("""{"id":3,"method":"gateway.ensure"}""", "http://127.0.0.1:5001", true) { out = it }
        assertTrue(h.calls.isEmpty())
        b.handle("""{"id":3,"method":"gateway.ensure"}""", origin, true) { out = it }
        assertEquals(listOf("ensure"), h.calls)
        assertNull("answered before the gateway was checked", out)
        h.pending!!(true)
        assertEquals(3, JSONObject(out!!).getInt("id"))
        assertTrue(JSONObject(out!!).has("result"))
        b.handle("""{"id":4,"method":"gateway.ensure"}""", origin, true) { out = it }
        h.pending!!(false)
        assertEquals("gateway unavailable", JSONObject(out!!).getString("error"))
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
