package com.remohop.app

import org.json.JSONObject

/**
 * Facts about the app for the About, Privacy and Diagnostics screens:
 * links (per language), the app's part of a diagnostic report, sizes.
 * Plain JVM code, unit tested without a device.
 */
object AppInfo {
    /** General contact (About → Contact). */
    const val CONTACT_EMAIL = "info@remohop.com"
    /** Support: the diagnostic-log mail. */
    const val SUPPORT_EMAIL = "support@remohop.com"
    /** Privacy questions (Settings → Privacy). */
    const val PRIVACY_EMAIL = "privacy@remohop.com"
    const val TELEGRAM = "https://t.me/remohop"
    const val WEBSITE = "https://remohop.com"
    const val SOURCE_CODE = "https://github.com/readori/RemoHop"
    const val LICENSING = "https://github.com/readori/RemoHop/blob/main/LICENSING.md"
    const val THIRD_PARTY = "https://github.com/readori/RemoHop/blob/main/packaging/third_party_licenses.sh"

    /** The privacy policy in the app's language ("zh" or "en"). */
    fun privacyPolicy(lang: String) = if (lang == Lang.ZH) "https://remohop.com/privacy/" else "https://remohop.com/en/privacy/"

    /** The terms of use in the app's language. */
    fun terms(lang: String) = if (lang == Lang.ZH) "https://remohop.com/terms/" else "https://remohop.com/en/terms/"

    /**
     * The app's part of a diagnostic report, as the JSON object the core's
     * diagnosticsExport takes. No secrets: versions, OS, model, language.
     */
    fun reportInfo(version: String, build: Int, release: String, sdk: Int, device: String, language: String): String =
        JSONObject()
            .put("app_version", version)
            .put("build", build.toString())
            .put("platform", "Android")
            .put("os", "$release (API $sdk)")
            .put("device", device)
            .put("language", language)
            .toString()

    /** A human size: "0 KB", "12 KB", "1.4 MB". */
    fun size(bytes: Long): String =
        if (bytes < 1024 * 1024) "${(bytes + 1023) / 1024} KB" else String.format(java.util.Locale.ROOT, "%.1f MB", bytes / 1024.0 / 1024.0)

    /** The first two groups of a fingerprint, for a row's value. */
    fun shortFingerprint(fp: String): String {
        val parts = fp.split(':')
        return if (parts.size > 2) parts.take(2).joinToString(":") + "…" else fp
    }

    /** The project's NOTICE (kept with the app as its attribution). */
    const val NOTICE = """RemoHop
Copyright (C) 2026 RemoHop
https://github.com/readori/RemoHop

This product is RemoHop, or is based on RemoHop, an independent peer-to-peer remote control project, licensed under the GNU Affero General Public License, version 3 (AGPL-3.0-only), with the additional terms in LICENSING.md ("Additional terms under section 7").

本产品是 RemoHop 或基于 RemoHop 开发。RemoHop 是独立的点对点远程控制开源项目，按 GNU Affero 通用公共许可证第 3 版（AGPL-3.0-only）授权，并附有 LICENSING.md 中“依据第 7 条的附加条款”。

Works based on RemoHop must keep this notice. See LICENSING.md.
基于 RemoHop 的作品必须保留本声明，详见 LICENSING.md。"""
}

/** What a rendezvous server check found: the codes of the core's CheckRendezvous. */
enum class RendezvousStatus {
    OK, IS_HOST, WRONG_PROTOCOL, UNREACHABLE, KEY_MISMATCH, FAILED;

    companion object {
        fun of(code: String): RendezvousStatus = when (code) {
            "ok" -> OK
            "signal_is_host", "is_host" -> IS_HOST
            "wrong_protocol", "signal_wrong_protocol" -> WRONG_PROTOCOL
            "unreachable", "rendezvous_unreachable", "timeout" -> UNREACHABLE
            "key_mismatch", "pin_mismatch" -> KEY_MISMATCH
            else -> FAILED
        }
    }
}

/**
 * The core's rendezvous server check (CheckRendezvous): it connects to the
 * saved server the way a connection by ID does, with the saved key, and
 * says what answered. Run after the server or key was saved, when the page
 * opens, and from the Test row. Blocks for a few seconds at most: call it
 * off the UI thread.
 */
object RendezvousCheck {
    const val AVAILABLE = true

    @Suppress("UNUSED_PARAMETER")
    fun run(server: String, key: String): RendezvousStatus? = check()?.status

    /** The check of the saved server, with the key it presented (empty unless it answered). */
    fun check(): RendezvousResult? {
        val out = runCatching { JSONObject(com.remohop.app.core.remohopcore.Remohopcore.checkRendezvous("")) }.getOrNull() ?: return null
        return parse(out).also { Diag.log("rendezvous check: ${out.optString("code", "error")}") }
    }

    /** CheckRendezvous's JSON outcome. */
    fun parse(out: JSONObject): RendezvousResult {
        val code = out.optString("code", "error")
        return RendezvousResult(RendezvousStatus.of(code), if (code == "ok") out.optString("key").trim() else "")
    }
}

/** A rendezvous check: what answered, and the server's key when it was a rendezvous server. */
data class RendezvousResult(val status: RendezvousStatus, val key: String) {
    /** Offer "Use this key": the server answered with a key and none is saved. */
    fun offerKey(savedKey: String): Boolean = status == RendezvousStatus.OK && key.isNotEmpty() && savedKey.isBlank()
}
