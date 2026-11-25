package com.remohop.app

import android.content.Context
import java.io.File
import java.io.PrintWriter
import java.io.StringWriter
import java.time.Instant

/**
 * Keeps the stack trace of a crash for the diagnostic log. The log is off by
 * default and a crash ends the process before anything could be sent, so the
 * trace goes to a private file first; it is moved into the log (which
 * redacts it like everything else) the next time the log is on: at the next
 * start, or when the user turns the log on after the crash.
 */
object CrashLog {
    @Volatile private var dir: File? = null

    fun install(context: Context) {
        val d = File(context.applicationContext.filesDir, "remohop")
        dir = d
        val previous = Thread.getDefaultUncaughtExceptionHandler()
        Thread.setDefaultUncaughtExceptionHandler { thread, e ->
            runCatching {
                val trace = StringWriter().also { e.printStackTrace(PrintWriter(it)) }.toString()
                d.mkdirs()
                File(d, FILE).writeText(
                    "${BuildConfig.VERSION_NAME} (${BuildConfig.VERSION_CODE}) at ${Instant.now()} on thread ${thread.name}\n" +
                        trace.take(MAX_CHARS)
                )
            }
            previous?.uncaughtException(thread, e)
        }
    }

    /** Moves a recorded crash into the diagnostic log if the log is on; keeps it otherwise. */
    fun flush() {
        val f = dir?.let { File(it, FILE) } ?: return
        if (!f.exists() || !Diag.enabled) return
        val lines = runCatching { f.readLines() }.getOrDefault(emptyList()).map { it.trim() }.filter { it.isNotEmpty() }
        f.delete()
        if (lines.isEmpty()) return
        Diag.log("the previous run crashed: " + lines.first())
        lines.drop(1).take(MAX_LINES).chunked(15).forEach { Diag.log("crash: " + it.joinToString(" | ")) }
    }

    private const val FILE = "last-crash.txt"
    private const val MAX_CHARS = 16_000
    private const val MAX_LINES = 90
}
