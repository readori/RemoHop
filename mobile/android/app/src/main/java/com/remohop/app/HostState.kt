package com.remohop.app

import org.json.JSONArray
import org.json.JSONObject

/**
 * Host mode's state as the core reports it (HostStateJSON in
 * internal/mobile/host.go), parsed for the "This device" screen, the
 * approval dialog and the notifications. Pure Kotlin: unit-tested.
 */
data class HostState(
    val on: Boolean,
    val phase: String,
    val error: String,
    val id: String,
    val password: String,
    val online: Boolean,
    val server: String,
    val addresses: List<String>,
    val device: String,
    val viewers: Int,
    val controlAvailable: Boolean,
    val controlUnavailable: String,
    val askTimeout: Int,
    val requests: List<HostRequest>,
    val sessions: List<HostSession>,
    /** Control is offered at all (the core has an input injector). */
    val controlOffered: Boolean = true,
) {
    /** What the status line says, as one of a few kinds (the screen picks the words). */
    enum class Status { OFF, STARTING, READY, OFFLINE, NO_SERVER, ERROR }

    val status: Status
        get() = when {
            phase == PHASE_ERROR -> Status.ERROR
            !on -> Status.OFF
            phase == PHASE_READY && online -> Status.READY
            phase == PHASE_READY -> Status.OFFLINE
            phase == PHASE_NO_SERVER -> Status.NO_SERVER
            else -> Status.STARTING
        }

    /** Someone is connected. */
    val connected: Boolean get() = sessions.isNotEmpty()

    /** Some connected session may control the phone. */
    val controlled: Boolean get() = sessions.any { it.control }

    /** The ID, or else the first address, to tell the other side; "" when neither is known yet. */
    val reachableAs: String get() = id.ifEmpty { addresses.firstOrNull().orEmpty() }

    companion object {
        const val PHASE_OFF = "off"
        const val PHASE_STARTING = "starting"
        const val PHASE_READY = "ready"
        const val PHASE_NO_SERVER = "no_server"
        const val PHASE_ERROR = "error"

        val OFF = HostState(false, PHASE_OFF, "", "", "", false, "", emptyList(), "", 0, false, "", 60, emptyList(), emptyList())

        fun parse(json: String?): HostState {
            if (json.isNullOrBlank()) return OFF
            return try {
                val o = JSONObject(json)
                HostState(
                    on = o.optBoolean("on"),
                    phase = o.optString("phase", PHASE_OFF),
                    error = o.optString("error"),
                    id = o.optString("id"),
                    password = o.optString("password"),
                    online = o.optBoolean("online"),
                    server = o.optString("server"),
                    addresses = strings(o.optJSONArray("addresses")),
                    device = o.optString("device"),
                    viewers = o.optInt("viewers"),
                    controlAvailable = o.optBoolean("control_available"),
                    controlUnavailable = o.optString("control_unavailable"),
                    askTimeout = o.optInt("ask_timeout", 60),
                    requests = objects(o.optJSONArray("requests")).map { HostRequest.of(it) },
                    sessions = objects(o.optJSONArray("sessions")).map { HostSession.of(it) },
                    controlOffered = o.optBoolean("control_offered", true),
                )
            } catch (e: Exception) {
                OFF
            }
        }

        private fun strings(a: JSONArray?): List<String> =
            if (a == null) emptyList() else (0 until a.length()).map { a.optString(it) }.filter { it.isNotEmpty() }

        private fun objects(a: JSONArray?): List<JSONObject> =
            if (a == null) emptyList() else (0 until a.length()).mapNotNull { a.optJSONObject(it) }
    }
}

/** A connection (or access) request waiting for the phone's owner. */
data class HostRequest(
    val n: Int,
    val name: String,
    val device: String,
    val identity: String,
    val from: String,
    val left: Int,
    val firstSeen: Boolean,
    /** Set when a connected session asks for more ("input"). */
    val access: List<String>,
    val session: Int,
) {
    /** The name to show: what the device calls itself, its pairing name, or "". */
    val label: String get() = device.ifEmpty { name }

    /** A connected session asks for more; otherwise it is a login. */
    val isAccess: Boolean get() = access.isNotEmpty()

    companion object {
        fun of(o: JSONObject) = HostRequest(
            n = o.optInt("n"),
            name = o.optString("name"),
            device = o.optString("device"),
            identity = o.optString("identity"),
            from = o.optString("from"),
            left = o.optInt("left"),
            firstSeen = o.optBoolean("first_seen"),
            access = o.optJSONArray("access")?.let { a -> (0 until a.length()).map { a.optString(it) } } ?: emptyList(),
            session = o.optInt("session"),
        )

        fun parse(json: String?): HostRequest? = try {
            if (json.isNullOrBlank()) null else of(JSONObject(json)).takeIf { it.n > 0 }
        } catch (e: Exception) {
            null
        }
    }
}

/** A device connected to the phone. */
data class HostSession(
    val id: Int,
    val label: String,
    val identity: String,
    val from: String,
    val path: String,
    val seconds: Int,
    val control: Boolean,
) {
    companion object {
        fun of(o: JSONObject) = HostSession(
            id = o.optInt("id"),
            label = o.optString("label"),
            identity = o.optString("identity"),
            from = o.optString("from"),
            path = o.optString("path"),
            seconds = o.optInt("seconds"),
            control = o.optBoolean("control"),
        )
    }
}
