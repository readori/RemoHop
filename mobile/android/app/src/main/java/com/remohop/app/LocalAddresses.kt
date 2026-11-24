package com.remohop.app

import android.content.Context
import android.net.ConnectivityManager
import android.net.LinkProperties
import android.net.Network
import com.remohop.app.core.remohopcore.Remohopcore
import java.net.InetAddress

/**
 * Android 11+ refuses apps the netlink calls Go uses to list interfaces, so
 * the core would advertise no LAN addresses and two devices on the same
 * Wi-Fi could not find their direct path. This reports the default
 * network's addresses to the core whenever they change.
 */
object LocalAddresses {
    fun watch(context: Context) {
        val cm = context.getSystemService(ConnectivityManager::class.java) ?: return
        try {
            cm.activeNetwork?.let { n -> cm.getLinkProperties(n)?.let(::report) }
            cm.registerDefaultNetworkCallback(object : ConnectivityManager.NetworkCallback() {
                override fun onLinkPropertiesChanged(network: Network, lp: LinkProperties) = report(lp)
                override fun onLost(network: Network) = Remohopcore.setLocalAddresses("")
            })
        } catch (e: SecurityException) {
            // Without ACCESS_NETWORK_STATE the core falls back to Go's own view.
        }
    }

    private fun report(lp: LinkProperties) {
        Remohopcore.setLocalAddresses(format(lp.linkAddresses.map { it.address }))
    }

    /** "a, b, c" of the addresses, in the form internal/mobile parses. */
    fun format(addrs: List<InetAddress>): String =
        addrs.mapNotNull { it.hostAddress?.substringBefore('%') }.joinToString(", ")
}
