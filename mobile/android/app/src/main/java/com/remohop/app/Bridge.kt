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
    /**
     * Makes sure the gateway answers (see [Core.gatewayUrl]) and calls [done]
     * on the UI thread with whether it does. If its URL changed the page is
     * loaded again.
     */
    fun ensureGateway(done: (Boolean) -> Unit) = done(true)
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
                "openSettings" -> { host.openSettings(); null }
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

        /** The page's answer to remohopMobile.back(), as evaluateJavascript returns it. */
        fun backHandled(jsResult: String?): Boolean = jsResult == "\"handled\""

        /** Script asking the page to handle the back button. */
        const val BACK_SCRIPT = "(window.remohopMobile ? window.remohopMobile.back() : 'unhandled')"
    }
}
