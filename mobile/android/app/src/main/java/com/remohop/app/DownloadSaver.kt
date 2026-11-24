package com.remohop.app

import android.app.Activity
import android.content.Intent
import android.net.Uri
import android.widget.Toast
import java.io.File
import java.net.HttpURLConnection
import java.net.Proxy
import java.net.URL
import java.util.concurrent.Executors

/**
 * Saves a gateway download where the user chooses. The page hands over the
 * download URL (it carries a single-use ticket valid for 30 seconds), so
 * the file is fetched at once into the app's cache and then copied to the
 * document the user creates with the system's save dialog (no storage
 * permission needed on any Android version).
 */
class DownloadSaver(private val activity: Activity, private val requestCode: Int) {
    private val io = Executors.newSingleThreadExecutor()
    private var pending: File? = null
    private val dir = File(activity.cacheDir, "downloads")

    // The app's language, which can change while the activity lives.
    private fun str(id: Int, vararg args: Any): String = Lang.localized(activity).getString(id, *args)

    init {
        // Leftovers from a previous run that never got saved.
        io.execute { dir.listFiles()?.forEach { it.delete() } }
    }

    fun start(url: String, name: String) {
        toast(str(R.string.downloading, name))
        io.execute {
            val result = runCatching { fetch(url, name) }
            activity.runOnUiThread {
                result.onSuccess { (file, mime) -> ask(file, name, mime) }
                    .onFailure { toast(str(R.string.download_failed, it.message ?: "")) }
            }
        }
    }

    private fun fetch(url: String, name: String): Pair<File, String> {
        dir.mkdirs()
        // Loopback only: never through a proxy.
        val c = URL(url).openConnection(Proxy.NO_PROXY) as HttpURLConnection
        c.connectTimeout = 10_000
        c.readTimeout = 60_000
        c.instanceFollowRedirects = false
        try {
            if (c.responseCode != 200) {
                val msg = c.errorStream?.bufferedReader()?.use { it.readText().take(300) } ?: ""
                throw IllegalStateException("HTTP ${c.responseCode} ${Regex("\"message\":\"([^\"]*)\"").find(msg)?.groupValues?.get(1) ?: ""}".trim())
            }
            val file = File.createTempFile("dl-", "-" + name, dir)
            c.inputStream.use { inp -> file.outputStream().use { inp.copyTo(it, 256 * 1024) } }
            val mime = c.contentType?.substringBefore(';')?.trim()?.takeIf { it.contains('/') } ?: "application/octet-stream"
            return file to mime
        } finally {
            c.disconnect()
        }
    }

    private fun ask(file: File, name: String, mime: String) {
        pending?.delete()
        pending = file
        val intent = Intent(Intent.ACTION_CREATE_DOCUMENT).addCategory(Intent.CATEGORY_OPENABLE)
            .setType(mime).putExtra(Intent.EXTRA_TITLE, name)
        try {
            activity.startActivityForResult(intent, requestCode)
        } catch (e: Exception) {
            file.delete(); pending = null
            toast(str(R.string.download_failed, e.message ?: ""))
        }
    }

    fun onResult(ok: Boolean, target: Uri?) {
        val file = pending ?: return
        pending = null
        if (!ok || target == null) { io.execute { file.delete() }; return }
        io.execute {
            val r = runCatching {
                activity.contentResolver.openOutputStream(target, "w")!!.use { out -> file.inputStream().use { it.copyTo(out, 256 * 1024) } }
            }
            file.delete()
            activity.runOnUiThread {
                r.onSuccess { toast(str(R.string.saved)) }
                    .onFailure { toast(str(R.string.download_failed, it.message ?: "")) }
            }
        }
    }

    private fun toast(s: String) = Toast.makeText(activity, s, Toast.LENGTH_SHORT).show()
}
