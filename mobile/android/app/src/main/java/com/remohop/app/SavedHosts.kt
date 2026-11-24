package com.remohop.app

import org.json.JSONObject

/**
 * A saved host as the core's DevicesJSON reports it, in the words the
 * Settings screens use: paired (or authorised by key), so it connects
 * without a password, or recent, so it needs the password each time.
 */
data class SavedHost(
    val target: String,
    val alias: String,
    val login: String,
    val presence: String,
    /** Seconds since 1970 of the last login (or of seeing it online), 0 if never. */
    val lastSeen: Long,
) {
    enum class Kind { PAIRED, KEY, RECENT }

    val label: String get() = alias.ifEmpty { target }
    val kind: Kind get() = when (login) {
        "pairing" -> Kind.PAIRED
        "key" -> Kind.KEY
        else -> Kind.RECENT
    }
    /** A 9-digit ID (shown as "ID"), or an address. */
    val isId: Boolean get() = target.length == 9 && target.all { it in '0'..'9' }

    companion object {
        fun of(o: JSONObject) = SavedHost(o.optString("target"), o.optString("alias"), o.optString("login"),
            o.optString("presence"), parseTime(o.optString("last_seen")))

        /** RFC 3339 (Go's time.Time JSON) to seconds since 1970; 0 if absent or unreadable. */
        fun parseTime(s: String): Long {
            if (s.isEmpty() || s == "null") return 0
            return runCatching { java.time.OffsetDateTime.parse(s).toEpochSecond() }.getOrDefault(0)
        }
    }
}
