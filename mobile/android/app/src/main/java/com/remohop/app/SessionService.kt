package com.remohop.app

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.Handler
import android.os.IBinder
import android.os.Looper
import androidx.core.app.NotificationCompat
import androidx.core.app.ServiceCompat
import com.remohop.app.core.remohopcore.Remohopcore
import org.json.JSONArray
import java.util.concurrent.Executors

/**
 * Keeps the app's host sessions alive while the app is in the background,
 * until the user disconnects: a foreground service with an ongoing
 * notification ("Connected to <host>", with Disconnect). Without it Android
 * freezes or kills a cached app's process, and with it the in-process
 * gateway and its P2P connections.
 *
 * The activity starts it (from the foreground, as Android 12+ requires)
 * once a host session exists, see [SessionWatch]; it watches the core's
 * session list and stops itself when the last session ends. The
 * notification's Disconnect ends every session (Remohopcore.disconnectAll),
 * which the page then shows.
 *
 * Type: specialUse (Android 14+), with the reason in the manifest's
 * PROPERTY_SPECIAL_USE_FGS_SUBTYPE. dataSync would be capped at 6 hours a
 * day from Android 15 and is meant for transfers; connectedDevice is for
 * accessories. The Play Console declaration is described in docs/app-store.md.
 */
class SessionService : Service() {
    private val main = Handler(Looper.getMainLooper())
    private val bg = Executors.newSingleThreadExecutor()
    private var lastText: String? = null
    private val poll = object : Runnable {
        override fun run() {
            bg.execute {
                val sessions = SessionWatch.sessions()
                val pending = SessionWatch.pending()
                main.post {
                    if (sessions.isEmpty() && pending == 0) stopSelf() else update(sessions)
                    if (running) main.postDelayed(this, POLL_MS)
                }
            }
        }
    }

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        running = true
        val nm = getSystemService(NotificationManager::class.java)
        nm?.createNotificationChannel(
            NotificationChannel(CHANNEL, str(R.string.session_channel), NotificationManager.IMPORTANCE_LOW).apply {
                setShowBadge(false)
            }
        )
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_DISCONNECT) {
            bg.execute {
                Remohopcore.disconnectAll()
                main.post { stopSelf() }
            }
            return START_NOT_STICKY
        }
        // A second start (two SessionWatch checks can race before onCreate
        // sets running) must not put the generic text back: update() only
        // posts when the text changes, so it would stay generic.
        val n = notification(lastText ?: str(R.string.session_connected_generic))
        val type = if (Build.VERSION.SDK_INT >= 34) ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE else 0
        try {
            ServiceCompat.startForeground(this, NOTIFICATION_ID, n, type)
        } catch (e: Exception) {
            // Not allowed now. SessionWatch starts this service only while
            // the activity is in front (onResume, the poll while resumed,
            // onPause), which is what allows startForeground here, so this
            // is not expected; it is logged for support.
            Diag.log("session service could not enter the foreground: ${e.javaClass.simpleName}")
            stopSelf()
            return START_NOT_STICKY
        }
        main.removeCallbacks(poll)
        main.post(poll)
        // The process (and with it every session) is gone if Android kills
        // it: a restarted service would have nothing to show.
        return START_NOT_STICKY
    }

    override fun onDestroy() {
        running = false
        main.removeCallbacks(poll)
        bg.shutdown()
        super.onDestroy()
    }

    private fun str(id: Int, vararg args: Any): String = Lang.localized(this).getString(id, *args)

    private fun update(sessions: List<Pair<String, String>>) {
        val text = if (sessions.isEmpty()) str(R.string.session_connecting) else {
            val (target, state) = sessions.first()
            when {
                sessions.size > 1 -> str(R.string.session_connected_many, sessions.size)
                state == "connected" -> str(R.string.session_connected, target)
                state == "failed" -> str(R.string.session_lost, target)
                else -> str(R.string.session_reconnecting, target)
            }
        }
        if (text == lastText) return
        lastText = text
        if (Build.VERSION.SDK_INT >= 33 &&
            checkSelfPermission(android.Manifest.permission.POST_NOTIFICATIONS) != android.content.pm.PackageManager.PERMISSION_GRANTED
        ) return // the notification is not shown anyway; the service still keeps the sessions
        getSystemService(NotificationManager::class.java)?.notify(NOTIFICATION_ID, notification(text))
    }

    private fun notification(text: String): Notification {
        // The launcher's own intent: it brings the task back as it was
        // (Settings stays open if it was), like tapping the app's icon.
        val launch = packageManager.getLaunchIntentForPackage(packageName)
            ?: Intent(this, MainActivity::class.java).setAction(Intent.ACTION_MAIN).addCategory(Intent.CATEGORY_LAUNCHER)
        launch.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_RESET_TASK_IF_NEEDED)
        val open = PendingIntent.getActivity(this, 0, launch, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
        val disconnect = PendingIntent.getService(
            this, 1, Intent(this, SessionService::class.java).setAction(ACTION_DISCONNECT),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT
        )
        return NotificationCompat.Builder(this, CHANNEL)
            .setSmallIcon(R.drawable.ic_remohop_monochrome)
            .setContentTitle(getString(R.string.app_name))
            .setContentText(text)
            .setContentIntent(open)
            .setOngoing(true)
            .setOnlyAlertOnce(true)
            .setSilent(true)
            .setCategory(NotificationCompat.CATEGORY_SERVICE)
            .setForegroundServiceBehavior(NotificationCompat.FOREGROUND_SERVICE_IMMEDIATE)
            .addAction(0, str(R.string.session_disconnect), disconnect)
            .build()
    }

    companion object {
        private const val CHANNEL = "sessions"
        private const val NOTIFICATION_ID = 1
        private const val POLL_MS = 5_000L
        const val ACTION_DISCONNECT = "com.remohop.app.DISCONNECT"

        /** Whether the service runs (for the instrumented test). */
        @Volatile var running = false
            private set
    }
}

/**
 * Starts [SessionService] while the activity is in front and a host session
 * (or a connection attempt) exists; MainActivity calls [check] when it resumes and every few seconds
 * while it is resumed (a session is made in the page, which tells the app
 * nothing).
 */
object SessionWatch {
    /** The core's sessions as (target, state), newest information. */
    fun sessions(): List<Pair<String, String>> = try {
        val a = JSONArray(Remohopcore.sessionsJSON())
        List(a.length()) { a.getJSONObject(it).let { o -> o.optString("target") to o.optString("state") } }
    } catch (e: Exception) {
        emptyList()
    }

    /**
     * Connection attempts still dialing or waiting for the host's owner to
     * approve: the process is kept for them too (the user switches to a
     * chat to ask for approval exactly then).
     */
    fun pending(): Int = try { Remohopcore.pendingConnections().toInt() } catch (e: Throwable) { 0 }

    /** Whether there is anything to keep the process for. */
    fun busy(): Boolean = sessions().isNotEmpty() || pending() > 0

    /** Off the UI thread: starts the service if a session or an attempt exists and it is not running. */
    fun check(context: Context) {
        if (SessionService.running || !busy()) return
        val app = context.applicationContext
        try {
            app.startForegroundService(Intent(app, SessionService::class.java))
        } catch (e: Exception) {
            // ForegroundServiceStartNotAllowedException: we were not in front after all; the next resume tries again.
        }
    }
}
