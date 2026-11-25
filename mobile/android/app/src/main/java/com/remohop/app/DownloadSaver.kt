package com.remohop.app

import android.app.Activity
import android.content.Intent
import android.net.Uri
import android.provider.DocumentsContract
import android.text.format.Formatter
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.widget.Button
import android.widget.FrameLayout
import android.widget.LinearLayout
import android.widget.ProgressBar
import android.widget.TextView
import android.widget.Toast
import java.io.File
import java.io.InputStream
import java.io.OutputStream
import java.net.HttpURLConnection
import java.net.Proxy
import java.net.URL
import java.util.concurrent.Executors

/**
 * Saves a gateway download where the user chooses, with its progress on
 * screen and a Cancel button (no storage permission needed on any Android
 * version).
 *
 * The page hands over the download URL, which carries a single-use ticket
 * valid for 30 seconds, so the transfer starts at once, and the system's
 * save dialog opens at the same time. While the user chooses, the data goes
 * into the app's cache; once the document exists, what arrived so far is
 * copied into it and the rest is written there directly. Only the part that
 * arrived during the choice is ever held in the cache.
 */
class DownloadSaver(private val activity: Activity, private val requestCode: Int, private val parent: () -> FrameLayout) {
    private val io = Executors.newSingleThreadExecutor()
    private val dir = File(activity.cacheDir, "downloads")
    private var job: Job? = null
    private var bar: View? = null
    private var barText: TextView? = null
    private var barProgress: ProgressBar? = null

    // The app's language, which can change while the activity lives.
    private fun str(id: Int, vararg args: Any): String = Lang.localized(activity).getString(id, *args)

    init {
        // Leftovers from a previous run that never got saved.
        io.execute { dir.listFiles()?.forEach { it.delete() } }
    }

    private class Job(val name: String) {
        val lock = Object()
        @Volatile var canceled = false
        /** The user closed the save dialog without choosing. */
        @Volatile var declined = false
        /** The document the user created. */
        @Volatile var target: Uri? = null
        @Volatile var total = -1L
        @Volatile var done = 0L
        @Volatile var conn: HttpURLConnection? = null
        fun wake() = synchronized(lock) { lock.notifyAll() }
    }

    fun start(url: String, name: String) {
        if (job != null) { toast(str(R.string.download_busy)); return }
        val j = Job(name)
        job = j
        showBar(j)
        io.execute { run(j, url) }
        // The type from the name: the transfer's own is not known yet.
        val mime = android.webkit.MimeTypeMap.getSingleton().getMimeTypeFromExtension(name.substringAfterLast('.', "").lowercase())
            ?: "application/octet-stream"
        val intent = Intent(Intent.ACTION_CREATE_DOCUMENT).addCategory(Intent.CATEGORY_OPENABLE)
            .setType(mime).putExtra(Intent.EXTRA_TITLE, name)
        try {
            @Suppress("DEPRECATION")
            activity.startActivityForResult(intent, requestCode)
        } catch (e: Exception) {
            j.canceled = true
            j.wake()
            finish(j, str(R.string.download_failed, e.message ?: ""))
        }
    }

    /** The save dialog's answer (MainActivity.onActivityResult). */
    fun onResult(ok: Boolean, target: Uri?) {
        val j = job
        if (j == null) {
            // The app was restarted while the dialog was up: the transfer is
            // gone, so remove the empty document the dialog created.
            if (ok && target != null) {
                io.execute { runCatching { DocumentsContract.deleteDocument(activity.contentResolver, target) } }
                toast(str(R.string.download_interrupted))
            }
            return
        }
        if (ok && target != null) j.target = target else j.declined = true
        j.wake()
    }

    /** The activity is going away: stop the transfer. */
    fun close() {
        job?.let { it.canceled = true; it.wake(); runCatching { it.conn?.disconnect() } }
        io.shutdown()
    }

    private fun cancel(j: Job) {
        j.canceled = true
        j.wake()
        // Unblocks a read that waits for data.
        Thread { runCatching { j.conn?.disconnect() } }.start()
    }

    private fun run(j: Job, url: String) {
        var part: File? = null
        var out: OutputStream? = null
        var direct = false
        val result = runCatching {
            dir.mkdirs()
            // Loopback only: never through a proxy.
            val c = URL(url).openConnection(Proxy.NO_PROXY) as HttpURLConnection
            j.conn = c
            c.connectTimeout = 10_000
            c.readTimeout = 60_000
            c.instanceFollowRedirects = false
            try {
                if (c.responseCode != 200) {
                    val msg = c.errorStream?.bufferedReader()?.use { it.readText().take(300) } ?: ""
                    throw IllegalStateException("HTTP ${c.responseCode} ${Regex("\"message\":\"([^\"]*)\"").find(msg)?.groupValues?.get(1) ?: ""}".trim())
                }
                j.total = c.contentLengthLong
                part = File.createTempFile("dl-", ".part", dir)
                out = part!!.outputStream()
                val buf = ByteArray(256 * 1024)
                var shown = 0L
                c.inputStream.use { inp: InputStream ->
                    while (true) {
                        if (j.canceled || j.declined) throw InterruptedException()
                        if (!direct) j.target?.let { t ->
                            // Chosen: what arrived so far goes into the document, the rest straight there.
                            out!!.close()
                            val o = activity.contentResolver.openOutputStream(t, "w") ?: throw IllegalStateException("cannot write the document")
                            part!!.inputStream().use { it.copyTo(o, buf.size) }
                            part!!.delete()
                            out = o
                            direct = true
                        }
                        val n = inp.read(buf)
                        if (n < 0) break
                        out!!.write(buf, 0, n)
                        j.done += n
                        if (j.done - shown >= 256 * 1024) { shown = j.done; progress(j) }
                    }
                }
                out!!.close()
                progress(j)
                if (!direct) {
                    // All here before the user chose: wait for the choice.
                    synchronized(j.lock) { while (j.target == null && !j.declined && !j.canceled) j.lock.wait(1000) }
                    if (j.canceled || j.declined) throw InterruptedException()
                    val o = activity.contentResolver.openOutputStream(j.target!!, "w") ?: throw IllegalStateException("cannot write the document")
                    o.use { dst -> part!!.inputStream().use { it.copyTo(dst, buf.size) } }
                    direct = true
                }
            } finally {
                c.disconnect()
            }
        }
        runCatching { out?.close() }
        part?.delete()
        val failure = result.exceptionOrNull()
        if (failure != null) {
            // A document the user created gets no half file.
            j.target?.let { t -> runCatching { DocumentsContract.deleteDocument(activity.contentResolver, t) } }
        }
        activity.runOnUiThread {
            when {
                failure == null -> finish(j, str(R.string.saved))
                j.declined -> finish(j, null)
                j.canceled -> finish(j, str(R.string.download_canceled))
                else -> finish(j, str(R.string.download_failed, failure.message ?: ""))
            }
        }
    }

    private fun finish(j: Job, message: String?) {
        if (job !== j) return
        job = null
        bar?.let { (it.parent as? ViewGroup)?.removeView(it) }
        bar = null
        if (message != null && !activity.isDestroyed) toast(message)
    }

    // ---- the progress bar at the bottom of the screen ----

    private fun showBar(j: Job) {
        val dp = activity.resources.displayMetrics.density
        val box = LinearLayout(activity).apply {
            orientation = LinearLayout.VERTICAL
            setPadding((16 * dp).toInt(), (12 * dp).toInt(), (8 * dp).toInt(), (8 * dp).toInt())
            background = android.graphics.drawable.GradientDrawable().apply {
                cornerRadius = 12 * dp
                setColor(0xF0202124.toInt())
            }
            elevation = 6 * dp
            isClickable = true // the page underneath does not get the taps
        }
        val row = LinearLayout(activity).apply { orientation = LinearLayout.HORIZONTAL; gravity = Gravity.CENTER_VERTICAL }
        val text = TextView(activity).apply {
            setTextColor(0xFFFFFFFF.toInt())
            textSize = 14f
            maxLines = 2
            this.text = str(R.string.downloading, j.name)
        }
        row.addView(text, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
        row.addView(Button(activity, null, android.R.attr.borderlessButtonStyle).apply {
            this.text = str(R.string.download_cancel)
            setTextColor(0xFFB4C5FF.toInt())
            setOnClickListener { cancel(j) }
        })
        box.addView(row)
        val pb = ProgressBar(activity, null, android.R.attr.progressBarStyleHorizontal).apply {
            isIndeterminate = true
            max = 1000
        }
        box.addView(pb, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply { marginEnd = (8 * dp).toInt() })
        parent().addView(box, FrameLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT, Gravity.BOTTOM).apply {
            val m = (12 * dp).toInt()
            setMargins(m, m, m, m)
        })
        bar = box
        barText = text
        barProgress = pb
    }

    private fun progress(j: Job) = activity.runOnUiThread {
        if (job !== j) return@runOnUiThread
        val pb = barProgress ?: return@runOnUiThread
        if (j.total > 0) {
            val pct = (j.done * 100 / j.total).toInt().coerceIn(0, 100)
            pb.isIndeterminate = false
            pb.progress = (j.done * 1000 / j.total).toInt().coerceIn(0, 1000)
            barText?.text = str(R.string.download_progress_pct, j.name, pct)
        } else {
            barText?.text = str(R.string.download_progress, j.name, Formatter.formatShortFileSize(activity, j.done))
        }
    }

    private fun toast(s: String) = Toast.makeText(activity, s, Toast.LENGTH_SHORT).show()
}
