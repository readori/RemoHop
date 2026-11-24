package com.remohop.app

import org.json.JSONObject

/** What the page may ask of the app; implemented by [MainActivity]. */
interface BridgeHost {
    fun clipboardRead(): String
    fun clipboardWrite(text: String)
    /** Starts saving a gateway download; problems are reported to the user by the host. */
    fun download(url: String, name: String)
    fun setState(keepScreenOn: Boolean, fullscreen: Boolean)
    fun keyboard(show: Boolean)
    fun haptic()
    fun openSettings()
    /** Opens Settings on one of its pages ("privacy", "about", ...). */
    fun openSettings(page: String) = openSettings()
    /** Something the page reported for the diagnostic log (kept only while the user has it on). */
    fun diagLog(text: String) {}
    /**
     * Makes sure the gateway answers (see [Core.gatewayUrl]) and calls [done]
     * on the UI thread with whether it does. If its URL changed the page is
     * loaded again.
     */
    fun ensureGateway(done: (Boolean) -> Unit) = done(true)
    /** Shows a native dialog for the page; [done] gets the user's answer (UI thread), once. */
    fun showDialog(request: DialogRequest, done: (DialogAnswer) -> Unit) = done(DialogAnswer.Cancelled)
}

/**
 * A dialog the page asks for (remohop-mobile.js): a confirmation, a text
 * prompt, or a few actions, with the page's own (translated) labels.
 */
data class DialogRequest(
    val kind: String, // "confirm", "prompt" or "actions"
    val title: String,
    val message: String,
    /** The confirming button (confirm, prompt). */
    val confirm: String,
    val cancel: String,
    /** The confirming button ends something: shown in the error colour. */
    val destructive: Boolean,
    /** The prompt's starting text and placeholder. */
    val value: String,
    val placeholder: String,
    val actions: List<Action>,
) {
    data class Action(val label: String, val destructive: Boolean)

    companion object {
        const val MAX_TITLE = 200
        const val MAX_MESSAGE = 1000
        const val MAX_LABEL = 60
        const val MAX_VALUE = 500
        const val MAX_ACTIONS = 8
        val KINDS = setOf("confirm", "prompt", "actions")

        /** The request in a bridge message, or null when it is not a usable one. */
        fun parse(method: String, args: JSONObject): DialogRequest? {
            if (method !in KINDS) return null
            fun text(k: String, max: Int) = args.optString(k).trim().take(max)
            val list = ArrayList<Action>()
            val arr = args.optJSONArray("actions")
            if (arr != null) for (i in 0 until minOf(arr.length(), MAX_ACTIONS)) {
                val o = arr.optJSONObject(i) ?: continue
                val label = o.optString("label").trim().take(MAX_LABEL)
                if (label.isNotEmpty()) list += Action(label, o.optBoolean("destructive"))
            }
            val r = DialogRequest(method, text("title", MAX_TITLE), text("message", MAX_MESSAGE), text("confirm", MAX_LABEL),
                text("cancel", MAX_LABEL), args.optBoolean("destructive"), args.optString("value").take(MAX_VALUE),
                text("placeholder", MAX_LABEL), list)
            if (r.title.isEmpty() && r.message.isEmpty()) return null
            if (r.cancel.isEmpty()) return null
            if (method == "actions") { if (list.isEmpty()) return null } else if (r.confirm.isEmpty()) return null
            return r
        }
    }
}

/** What the user chose in a [DialogRequest]'s dialog. */
sealed class DialogAnswer {
    object Cancelled : DialogAnswer()
    /** Confirmed (a prompt with its text). */
    data class Confirmed(val text: String = "") : DialogAnswer()
    data class Picked(val index: Int) : DialogAnswer()

    /** The reply to the page: {ok}, {ok, text} for a prompt, {index} (-1: cancelled) for actions. */
    fun reply(kind: String): JSONObject = when (kind) {
        "confirm" -> JSONObject().put("ok", this is Confirmed)
        "prompt" -> JSONObject().put("ok", this is Confirmed).put("text", (this as? Confirmed)?.text?.take(DialogRequest.MAX_VALUE) ?: "")
        else -> JSONObject().put("index", (this as? Picked)?.index ?: -1)
    }
}

/**
 * The app side of remohop-mobile.js's bridge. Messages arrive through
 * WebViewCompat.addWebMessageListener, which only exposes the bridge to the
 * gateway's origin; [handle] checks that again, and that the sender is the
 * main frame, before doing anything.
 *
 * A message is {"id":N,"method":"...","args":{...}}; the reply is
 * {"id":N,"result":{...}} or {"id":N,"error":"..."}.
 */
class Bridge(private val host: BridgeHost, private val origin: String) {

    fun handle(message: String, sourceOrigin: String?, isMainFrame: Boolean, reply: (String) -> Unit) {
        if (!isMainFrame || sourceOrigin != origin) return
        val msg = try {
            JSONObject(message)
        } catch (e: Exception) {
            return
        }
        val id = msg.optLong("id", -1)
        if (msg.optString("method") == "gateway.ensure") {
            // The page got no answer from the gateway (remohop-mobile.js
            // ensureGateway): answered once the app has checked it.
            host.ensureGateway { ok ->
                if (id >= 0) {
                    val out = JSONObject().put("id", id)
                    if (ok) out.put("result", JSONObject()) else out.put("error", "gateway unavailable")
                    reply(out.toString())
                }
            }
            return
        }
        val args = msg.optJSONObject("args") ?: JSONObject()
        val method = msg.optString("method")
        if (method in DialogRequest.KINDS) {
            // A native dialog: answered once the user chose.
            val req = DialogRequest.parse(method, args)
            if (req == null) {
                if (id >= 0) reply(JSONObject().put("id", id).put("error", "bad dialog").toString())
                return
            }
            host.showDialog(req) { answer ->
                if (id >= 0) reply(JSONObject().put("id", id).put("result", answer.reply(req.kind)).toString())
            }
            return
        }
        val result = JSONObject()
        val error: String? = try {
            when (msg.optString("method")) {
                "clipboard.read" -> { result.put("text", host.clipboardRead()); null }
                "clipboard.write" -> {
                    val text = args.optString("text")
                    if (text.length > MAX_CLIP) "text too long" else { host.clipboardWrite(text); null }
                }
                "download" -> {
                    val url = args.optString("url")
                    if (!GatewayUrls.isDownload(url, origin)) "not a download of this gateway"
                    else { host.download(url, GatewayUrls.safeFileName(args.optString("name"))); null }
                }
                "state" -> { host.setState(args.optBoolean("keepScreenOn"), args.optBoolean("fullscreen")); null }
                "keyboard" -> { host.keyboard(args.optBoolean("show")); null }
                "haptic" -> { host.haptic(); null }
                "openSettings" -> {
                    val page = args.optString("page")
                    if (page in SETTINGS_PAGES) host.openSettings(page) else host.openSettings()
                    null
                }
                "log" -> { host.diagLog(args.optString("text").take(MAX_LOG)); null }
                "ready" -> null
                else -> "unknown method"
            }
        } catch (e: Exception) {
            e.message ?: e.javaClass.simpleName
        }
        if (id < 0) return
        val out = JSONObject().put("id", id)
        if (error != null) out.put("error", error) else out.put("result", result)
        reply(out.toString())
    }

    companion object {
        /** Clipboard text larger than this is refused (the remote side caps it too). */
        const val MAX_CLIP = 1 shl 20

        /** A page's diagnostic message is cut to this many characters. */
        const val MAX_LOG = 2000

        /** Settings pages the page may open directly. */
        val SETTINGS_PAGES = setOf("privacy", "about", "diagnostics", "rendezvous")

        /** The page's answer to remohopMobile.back(), as evaluateJavascript returns it. */
        fun backHandled(jsResult: String?): Boolean = jsResult == "\"handled\""

        /** Script asking the page to handle the back button. */
        const val BACK_SCRIPT = "(window.remohopMobile ? window.remohopMobile.back() : 'unhandled')"
    }
}
