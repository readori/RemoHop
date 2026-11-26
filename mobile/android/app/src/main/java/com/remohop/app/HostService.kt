package com.remohop.app

import android.app.Notification
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.media.projection.MediaProjectionManager
import android.os.Build
import android.os.Handler
import android.os.IBinder
import android.os.Looper
import androidx.core.app.NotificationCompat
import androidx.core.app.ServiceCompat

/**
 * Host mode's foreground service, running while host mode is on:
 *
 * - waiting for connections (type specialUse): Android freezes a cached
 *   app within seconds, and a frozen app cannot hear an incoming request;
 *   the ongoing notification says the phone accepts connection requests
 *   and has a Turn off button;
 * - while someone is connected and the owner allowed screen sharing (type
 *   mediaProjection, after the consent of that sharing, as Android 14
 *   requires): it holds the MediaProjection ([ScreenCapture]) and the
 *   notification says who views or controls the phone, with Stop.
 *
 * The sharing ends when the last device disconnects (a few seconds' grace
 * for the one being admitted just after the consent): the projection is
 * released, and the next connection asks for consent again.
 */
class HostService : Service() {
    private val main = Handler(Looper.getMainLooper())
    private var projecting = false
    private var emptySince = 0L
    private val tick = object : Runnable {
        override fun run() {
            checkSharing()
            if (running) main.postDelayed(this, 1000)
        }
    }

    override fun onBind(intent: Intent?): IBinder? = null

    private var shown: Triple<Boolean, String, String>? = null
    private val listener: (HostState) -> Unit = { s ->
        if (!s.on && !projecting && foreground && !HostModeRunning.now()) stopSelf()
        val key = Triple(s.controlled, s.sessions.joinToString(", ") { it.label }, s.reachableAs)
        if (key != shown) {
            shown = key
            refreshNotification(s)
        }
    }

    override fun onCreate() {
        super.onCreate()
        running = true
        main.post(tick)
        HostMode.listen(listener)
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_OFF -> {
                HostMode.setEnabled(this, false)
                return START_NOT_STICKY
            }
            ACTION_STOP -> {
                // Stop sharing: every device is disconnected; host mode stays on.
                HostMode.disconnect(0)
                stopProjection()
                return START_NOT_STICKY
            }
            ACTION_DENY -> {
                val n = intent!!.getIntExtra(EXTRA_REQUEST, 0)
                if (n > 0) HostMode.decide(n, allow = false, control = false)
                if (!foreground) stopSelf()
                return START_NOT_STICKY
            }
            ACTION_PROJECT -> {
                val code = intent!!.getIntExtra(EXTRA_CODE, 0)
                @Suppress("DEPRECATION")
                val data: Intent? = if (Build.VERSION.SDK_INT >= 33) intent.getParcelableExtra(EXTRA_DATA, Intent::class.java) else intent.getParcelableExtra(EXTRA_DATA)
                if (data != null && enterForeground(true)) {
                    val mp = getSystemService(MediaProjectionManager::class.java)?.getMediaProjection(code, data)
                    if (mp != null) {
                        projecting = true
                        emptySince = System.currentTimeMillis()
                        ScreenCapture.attach(this, mp) { main.post { onProjectionEnded() } }
                    } else enterForeground(false)
                }
                return START_NOT_STICKY
            }
        }
        if (!HostMode.state.on && !HostModeRunning.now()) {
            stopSelf()
            return START_NOT_STICKY
        }
        if (!enterForeground(projecting)) stopSelf()
        return START_NOT_STICKY
    }

    private var foreground = false

    /** Enters (or updates) the foreground with the type the current sharing needs. */
    private fun enterForeground(project: Boolean): Boolean {
        var type = if (Build.VERSION.SDK_INT >= 34) ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE else 0
        if (project && Build.VERSION.SDK_INT >= 29) type = type or ServiceInfo.FOREGROUND_SERVICE_TYPE_MEDIA_PROJECTION
        return try {
            ServiceCompat.startForeground(this, NOTIFICATION_ID, notification(HostMode.state), type)
            foreground = true
            true
        } catch (e: Exception) {
            Diag.log("host service could not enter the foreground: ${e.javaClass.simpleName}")
            false
        }
    }

    /** Ends the sharing once nobody has been connected for a little while. */
    private fun checkSharing() {
        if (!projecting) return
        val s = HostMode.state
        val now = System.currentTimeMillis()
        if (s.connected || s.requests.isNotEmpty()) {
            emptySince = now
        } else if (now - emptySince > SHARING_GRACE_MS) {
            Diag.log("screen: nobody is connected; screen sharing ends")
            stopProjection()
        }
    }

    private fun stopProjection() {
        if (!projecting) return
        projecting = false
        ScreenCapture.detach()
        if (HostMode.state.on) enterForeground(false) else stopSelf()
    }

    /** The system (or the owner, from its cast indicator) ended the projection: the viewers are disconnected. */
    private fun onProjectionEnded() {
        if (!projecting) return
        projecting = false
        HostMode.disconnect(0)
        if (HostMode.state.on) enterForeground(false) else stopSelf()
    }

    override fun onDestroy() {
        running = false
        HostMode.unlisten(listener)
        main.removeCallbacks(tick)
        if (projecting) ScreenCapture.detach()
        projecting = false
        super.onDestroy()
    }

    private fun refreshNotification(s: HostState) {
        if (!foreground) return
        if (Build.VERSION.SDK_INT >= 33 &&
            checkSelfPermission(android.Manifest.permission.POST_NOTIFICATIONS) != android.content.pm.PackageManager.PERMISSION_GRANTED
        ) return
        getSystemService(NotificationManager::class.java)?.notify(NOTIFICATION_ID, notification(s))
    }

    private fun str(id: Int, vararg args: Any): String = Lang.localized(this).getString(id, *args)

    private fun notification(s: HostState): Notification {
        val open = PendingIntent.getActivity(this, 0, thisDeviceIntent(this), PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
        val b = NotificationCompat.Builder(this, HostMode.CHANNEL_HOST)
            .setSmallIcon(R.drawable.ic_remohop_monochrome)
            .setContentIntent(open)
            .setOngoing(true)
            .setOnlyAlertOnce(true)
            .setSilent(true)
            .setCategory(NotificationCompat.CATEGORY_SERVICE)
            .setForegroundServiceBehavior(NotificationCompat.FOREGROUND_SERVICE_IMMEDIATE)
        if (s.connected) {
            val names = s.sessions.joinToString(", ") { it.label }
            b.setContentTitle(str(if (s.controlled) R.string.hm_notif_controlled else R.string.hm_notif_viewed, names))
                .setContentText(str(R.string.hm_notif_stop_hint))
                .addAction(0, str(R.string.hm_stop), service(ACTION_STOP, 2))
        } else {
            b.setContentTitle(str(R.string.hm_notif_waiting))
                .setContentText(if (s.reachableAs.isNotEmpty()) str(R.string.hm_notif_id, s.reachableAs) else str(R.string.hm_status_starting))
                .addAction(0, str(R.string.hm_turn_off), service(ACTION_OFF, 3))
        }
        return b.build()
    }

    private fun service(action: String, code: Int): PendingIntent =
        PendingIntent.getService(this, code, Intent(this, HostService::class.java).setAction(action), PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)

    companion object {
        private const val NOTIFICATION_ID = 2
        private const val SHARING_GRACE_MS = 8_000L
        const val ACTION_OFF = "com.remohop.app.HOST_OFF"
        const val ACTION_STOP = "com.remohop.app.HOST_STOP"
        const val ACTION_DENY = "com.remohop.app.HOST_DENY"
        const val ACTION_PROJECT = "com.remohop.app.HOST_PROJECT"
        const val EXTRA_REQUEST = "request"
        const val EXTRA_CODE = "code"
        const val EXTRA_DATA = "data"

        @Volatile var running = false
            private set

        /** The "This device" page, from the notification. */
        fun thisDeviceIntent(context: Context): Intent =
            Intent(context, SettingsActivity::class.java).putExtra(SettingsActivity.EXTRA_PAGE, SettingsActivity.PAGE_THIS_DEVICE)
                .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)

        /** Host mode was switched on (from the app, in front). */
        fun start(context: Context) {
            try {
                context.startForegroundService(Intent(context, HostService::class.java))
            } catch (e: Exception) {
                Diag.log("host service could not start: ${e.javaClass.simpleName}")
            }
        }

        fun stop(context: Context) {
            context.stopService(Intent(context, HostService::class.java))
        }

        /** The owner allowed screen sharing ([code] and [data] from the consent). */
        fun project(context: Context, code: Int, data: Intent) {
            context.startForegroundService(Intent(context, HostService::class.java).setAction(ACTION_PROJECT)
                .putExtra(EXTRA_CODE, code).putExtra(EXTRA_DATA, data))
        }
    }
}

/** Whether the core's host runs right now (the state may lag behind it). */
private object HostModeRunning {
    fun now(): Boolean = runCatching { com.remohop.app.core.remohopcore.Remohopcore.hostRunning() }.getOrDefault(false)
}
