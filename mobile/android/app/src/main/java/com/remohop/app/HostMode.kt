package com.remohop.app

import android.app.Activity
import android.app.Application
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import androidx.core.app.NotificationCompat
import com.remohop.app.core.remohopcore.HostListener
import com.remohop.app.core.remohopcore.Remohopcore
import java.util.concurrent.CopyOnWriteArraySet
import java.util.concurrent.Executors

/**
 * Host mode on the app side: this phone can be viewed, and with its owner's
 * consent controlled, from other RemoHop devices (internal/mobile/host.go).
 *
 * Process wide, like the core. It hands the core the native screen
 * ([ScreenCapture]) and input ([ControlInjector]), keeps the latest state
 * for the screens ([state], [listen]), shows every incoming request as the
 * approval dialog ([ApprovalActivity]) when the app is in front and as a
 * high-priority notification otherwise, and runs [HostService] while host
 * mode is on (it keeps the process reachable in the background and holds
 * the screen capture while someone watches).
 *
 * Host mode is never on by itself: the owner switches it on, and it is off
 * again whenever the process starts.
 */
object HostMode : HostListener {
    private val main = Handler(Looper.getMainLooper())
    private val bg = Executors.newSingleThreadExecutor()
    private val listeners = CopyOnWriteArraySet<(HostState) -> Unit>()
    private lateinit var app: Context
    @Volatile private var installed = false

    /** The latest state (main thread or not: it is replaced whole). */
    @Volatile var state: HostState = HostState.OFF
        private set

    /** Requests a dialog or notification was shown for, by number. */
    private val announced = HashSet<Int>()

    /** Activities of the app in front (started and not stopped). */
    @Volatile private var visible = 0

    /** Once, from the Application: wires the core to the native side. */
    fun install(application: Application) {
        if (installed) return
        installed = true
        app = application
        application.registerActivityLifecycleCallbacks(object : Application.ActivityLifecycleCallbacks {
            override fun onActivityStarted(a: Activity) { visible++ }
            override fun onActivityStopped(a: Activity) { visible = maxOf(0, visible - 1) }
            override fun onActivityCreated(a: Activity, b: Bundle?) {}
            override fun onActivityResumed(a: Activity) {}
            override fun onActivityPaused(a: Activity) {}
            override fun onActivitySaveInstanceState(a: Activity, b: Bundle) {}
            override fun onActivityDestroyed(a: Activity) {}
        })
        runCatching {
            Remohopcore.setHostListener(this)
            Remohopcore.setScreenSource(ScreenCapture)
            Remohopcore.setInputInjector(ControlInjector(application))
        }.onFailure { Diag.log("host mode: the core could not be wired: ${it.javaClass.simpleName}") }
        createChannels(application)
    }

    /** Calls [l] on the main thread with every new state (and now with the current one). */
    fun listen(l: (HostState) -> Unit) {
        listeners += l
        main.post { l(state) }
    }

    fun unlisten(l: (HostState) -> Unit) { listeners -= l }

    /** Switches host mode on or off; the outcome arrives as a new state. */
    fun setEnabled(context: Context, on: Boolean) {
        val ctx = context.applicationContext
        bg.execute {
            if (on) {
                Diag.log("host mode: switched on")
                runCatching { Remohopcore.startHost() }.onFailure { Diag.log("host mode: could not start: ${it.message?.take(200)}") }
                if (Remohopcore.hostRunning()) main.post { HostService.start(ctx) }
            } else {
                Diag.log("host mode: switched off")
                Remohopcore.stopHost()
                main.post { HostService.stop(ctx) }
            }
            refresh()
        }
    }

    /** Asks the core for the state again (after something native changed, e.g. the accessibility service). */
    fun refresh() {
        bg.execute { runCatching { Remohopcore.hostStateJSON() }.getOrNull()?.let { hostChanged(it) } }
    }

    fun regenerate() = bg.execute {
        runCatching { Remohopcore.regenerateHostPassword() }
        refresh()
    }

    /** Ends session [id], or every session with 0. */
    fun disconnect(id: Int) = bg.execute {
        runCatching { Remohopcore.disconnectHostSession(id.toLong()) }
        Diag.log("host mode: disconnected ${if (id == 0) "every session" else "a session"} on the phone")
        refresh()
    }

    fun setControl(id: Int, control: Boolean) = bg.execute {
        runCatching { Remohopcore.setHostSessionControl(id.toLong(), control) }
        refresh()
    }

    /** Answers request [n]; [done] gets the error text on the main thread, or null. */
    fun decide(n: Int, allow: Boolean, control: Boolean, done: (String?) -> Unit = {}) = bg.execute {
        val err = runCatching { Remohopcore.decideHostRequest(n.toLong(), allow, control) }.exceptionOrNull()
        Diag.log("host mode: request ${if (allow) "allowed" else "denied"}${if (allow && control) " with control" else ""}")
        cancelRequestNotification(n)
        refresh()
        main.post { done(err?.message) }
    }

    // ---- HostListener (core threads) ----

    override fun hostChanged(json: String) {
        val s = HostState.parse(json)
        main.post {
            state = s
            val waiting = s.requests.map { it.n }.toSet()
            // Requests that are no longer waiting lose their notification.
            for (n in announced.toList()) if (n !in waiting) { announced -= n; cancelRequestNotification(n) }
            for (l in listeners) l(s)
        }
    }

    override fun hostRequest(json: String) {
        val r = HostRequest.parse(json) ?: return
        main.post { announce(r) }
    }

    private fun announce(r: HostRequest) {
        if (!announced.add(r.n)) return
        Diag.log("host mode: a ${if (r.isAccess) "request for control" else "connection request"} arrived${if (r.firstSeen) " from a new device" else ""}")
        // The dialog at once when the app is in front; a notification in any
        // case (Android does not let an app open a window from the
        // background, and the notification is how the owner gets to it).
        if (visible > 0) {
            runCatching { app.startActivity(ApprovalActivity.intent(app, r.n).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)) }
        }
        notifyRequest(r)
    }

    // ---- notifications ----

    const val CHANNEL_REQUESTS = "host_requests"
    const val CHANNEL_HOST = "host"

    private fun str(id: Int, vararg args: Any): String = Lang.localized(app).getString(id, *args)

    private fun createChannels(context: Context) {
        val nm = context.getSystemService(NotificationManager::class.java) ?: return
        val c = Lang.localized(context)
        nm.createNotificationChannel(NotificationChannel(CHANNEL_REQUESTS, c.getString(R.string.host_channel_requests), NotificationManager.IMPORTANCE_HIGH).apply {
            setShowBadge(true)
        })
        nm.createNotificationChannel(NotificationChannel(CHANNEL_HOST, c.getString(R.string.host_channel_active), NotificationManager.IMPORTANCE_LOW).apply {
            setShowBadge(false)
        })
    }

    private fun canNotify(): Boolean = Build.VERSION.SDK_INT < 33 ||
        app.checkSelfPermission(android.Manifest.permission.POST_NOTIFICATIONS) == PackageManager.PERMISSION_GRANTED

    private fun notifyRequest(r: HostRequest) {
        if (!canNotify()) return
        val who = r.label.ifEmpty { str(R.string.host_unknown_device) }
        val open = PendingIntent.getActivity(app, REQUEST_BASE + r.n, ApprovalActivity.intent(app, r.n).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
        val deny = PendingIntent.getService(app, REQUEST_BASE + r.n,
            Intent(app, HostService::class.java).setAction(HostService.ACTION_DENY).putExtra(HostService.EXTRA_REQUEST, r.n),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
        val n = NotificationCompat.Builder(app, CHANNEL_REQUESTS)
            .setSmallIcon(R.drawable.ic_remohop_monochrome)
            .setContentTitle(str(if (r.isAccess) R.string.host_req_title_access else R.string.host_req_title, who))
            .setContentText(str(R.string.host_req_notification_text))
            .setPriority(NotificationCompat.PRIORITY_HIGH)
            .setCategory(NotificationCompat.CATEGORY_CALL)
            .setContentIntent(open)
            .setAutoCancel(true)
            .setTimeoutAfter(state.askTimeout * 1000L)
            .addAction(0, str(R.string.host_deny), deny)
            .addAction(0, str(R.string.host_review), open)
            .build()
        app.getSystemService(NotificationManager::class.java)?.notify(REQUEST_BASE + r.n, n)
    }

    private fun cancelRequestNotification(n: Int) {
        app.getSystemService(NotificationManager::class.java)?.cancel(REQUEST_BASE + n)
    }

    private const val REQUEST_BASE = 1000
}
