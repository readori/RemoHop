package com.remohop.app

import java.net.URI

/**
 * Pure helpers about the gateway's loopback URL. They take strings, not
 * android.net.Uri, so the JVM unit tests exercise them as they run on a
 * device.
 *
 * The full URL contains the gateway's access token. Only [origin] (scheme,
 * host and port, no token) is ever kept outside the WebView's initial load.
 */
object GatewayUrls {
    /** "http://127.0.0.1:PORT" for a gateway URL, or null if it is not one. */
    fun origin(url: String): String? {
        val u = parse(url) ?: return null
        if (u.scheme != "http" || u.host != "127.0.0.1" || u.port !in 1..65535) return null
        if (u.userInfo != null) return null
        return "http://127.0.0.1:${u.port}"
    }

    /** Whether the WebView may navigate to [url]: only the gateway's own origin. */
    fun sameOrigin(url: String, origin: String): Boolean = origin(url) == origin

    /**
     * Whether [url] is a file download of this gateway, which the page hands
     * to the app (see remohop-mobile.js) instead of the WebView fetching it.
     */
    fun isDownload(url: String, origin: String): Boolean {
        if (!sameOrigin(url, origin)) return false
        val u = parse(url) ?: return false
        return u.rawPath.endsWith("/api/v1/files/download") && u.rawFragment == null
    }

    /**
     * [url] with the page's language ("zh" or "en") in its lang query
     * parameter, replacing one already there. The path, which holds the
     * token, is untouched. Any other [lang] leaves [url] as it is.
     */
    fun withLang(url: String, lang: String): String {
        if (lang != "zh" && lang != "en") return url
        val hash = url.indexOf('#')
        val base = if (hash >= 0) url.substring(0, hash) else url
        val fragment = if (hash >= 0) url.substring(hash) else ""
        val q = base.indexOf('?')
        val path = if (q >= 0) base.substring(0, q) else base
        val params = if (q < 0) emptyList() else base.substring(q + 1).split('&').filter { it.isNotEmpty() && it != "lang" && !it.startsWith("lang=") }
        return path + "?" + (params + "lang=$lang").joinToString("&") + fragment
    }

    /**
     * A file name that is safe to offer in the system's save dialog and to
     * use for a temporary file: the last path element, no control or
     * reserved characters, not hidden, at most 120 characters.
     */
    fun safeFileName(name: String?): String {
        var n = (name ?: "").replace('\\', '/').substringAfterLast('/')
        n = n.filter { it >= ' ' && it != '\u007f' && it !in "<>:\"|?*" }.trim().trimStart('.')
        if (n.length > 120) {
            val dot = n.lastIndexOf('.')
            val ext = if (dot > 0 && n.length - dot <= 10) n.substring(dot) else ""
            n = n.take(120 - ext.length) + ext
        }
        return n.ifEmpty { "download" }
    }

    private fun parse(url: String): URI? = try {
        URI(url)
    } catch (e: Exception) {
        null
    }
}
