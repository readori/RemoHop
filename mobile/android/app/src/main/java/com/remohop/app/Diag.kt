package com.remohop.app

import android.content.ActivityNotFoundException
import android.content.ClipData
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.os.Bundle
import androidx.core.content.FileProvider
import com.remohop.app.core.remohopcore.Remohopcore
import java.io.File

/**
 * The app side of the diagnostic log (internal/mobile/diag.go): off by
 * default, kept in the app's private storage, size-capped and redacted by
 * the core, and only ever shared by the user from Settings → Diagnostics.
 */
object Diag {
    /** Records an app event while the log is on (the core drops it otherwise). */
    fun log(message: String) {
        runCatching { Remohopcore.diagnosticsLog("android", message) }
    }

    /** Records something the page reported (errors, failed connections). */
    fun logPage(message: String) {
        runCatching { Remohopcore.diagnosticsLog("web", message.take(2000)) }
    }

    val enabled: Boolean get() = runCatching { Remohopcore.diagnosticsEnabled() }.getOrDefault(false)

    fun setEnabled(on: Boolean) {
        Remohopcore.setDiagnosticsEnabled(on)
        if (on) {
            log("app ${BuildConfig.VERSION_NAME} (${BuildConfig.VERSION_CODE}), Android ${Build.VERSION.RELEASE} (API ${Build.VERSION.SDK_INT}), ${device()}")
            CrashLog.flush() // a crash from before the log was turned on
        }
    }

    val size: Long get() = runCatching { Remohopcore.diagnosticsSize() }.getOrDefault(0L)

    fun clear() = Remohopcore.clearDiagnostics()

    val coreVersion: String get() = runCatching { Remohopcore.coreVersion() }.getOrDefault("?")

    fun device(): String = listOf(Build.MANUFACTURER, Build.MODEL).filter { it.isNotBlank() }.distinct().joinToString(" ")

    /** Writes a report ("txt" or "json") into the app's cache and returns a content URI for it. */
    fun export(context: Context, format: String): Uri {
        val info = AppInfo.reportInfo(BuildConfig.VERSION_NAME, BuildConfig.VERSION_CODE, Build.VERSION.RELEASE,
            Build.VERSION.SDK_INT, device(), Lang.effective(context))
        val dir = File(context.cacheDir, "diagnostics")
        val path = Remohopcore.diagnosticsExport(info, format, dir.absolutePath)
        return FileProvider.getUriForFile(context, context.packageName + ".files", File(path))
    }

    private fun mime(uri: Uri) = if (uri.toString().endsWith(".json")) "application/json" else "text/plain"

    /** The system share sheet with the report (Gmail, Messages, Drive, Nearby Share...). */
    fun share(context: Context, uri: Uri, chooserTitle: String) {
        val send = Intent(Intent.ACTION_SEND)
            .setType(mime(uri))
            .putExtra(Intent.EXTRA_STREAM, uri)
            .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
        send.clipData = ClipData.newRawUri("", uri)
        context.startActivity(Intent.createChooser(send, chooserTitle))
    }

    /** A mail to support with the report attached, offered to mail apps only. */
    fun email(context: Context, uri: Uri, subject: String, body: String, chooserTitle: String) {
        val send = Intent(Intent.ACTION_SEND)
            .setType(mime(uri))
            .putExtra(Intent.EXTRA_EMAIL, arrayOf(AppInfo.SUPPORT_EMAIL))
            .putExtra(Intent.EXTRA_SUBJECT, subject)
            .putExtra(Intent.EXTRA_TEXT, body)
            .putExtra(Intent.EXTRA_STREAM, uri)
            .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
        send.clipData = ClipData.newRawUri("", uri)
        // Mail apps only, where there is one; with none (createChooser never
        // throws, it would show an empty list) any app that takes a file.
        val mail = Intent(Intent.ACTION_SENDTO, Uri.parse("mailto:"))
        if (context.packageManager.queryIntentActivities(mail, 0).isNotEmpty()) send.selector = mail
        try {
            context.startActivity(Intent.createChooser(send, chooserTitle))
        } catch (e: ActivityNotFoundException) {
            send.selector = null
            context.startActivity(Intent.createChooser(send, chooserTitle))
        }
    }

    /**
     * Opens a web page in a Custom Tab where the browser supports them (the
     * Custom Tabs session extra, without the androidx.browser library), in
     * the browser otherwise; mailto: and other links go to their apps.
     */
    fun open(context: Context, url: String) {
        val i = Intent(Intent.ACTION_VIEW, Uri.parse(url))
        if (url.startsWith("https://")) {
            i.putExtras(Bundle().apply { putBinder(CUSTOM_TABS_SESSION, null) })
            i.putExtra(CUSTOM_TABS_TITLE, 1)
        }
        try {
            context.startActivity(i)
        } catch (e: ActivityNotFoundException) {
            // No app for it; nothing else to try.
        }
    }

    private const val CUSTOM_TABS_SESSION = "android.support.customtabs.extra.SESSION"
    private const val CUSTOM_TABS_TITLE = "android.support.customtabs.extra.TITLE_VISIBILITY"
}
