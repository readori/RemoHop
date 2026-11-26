import Foundation
import Remohopcore

/// The Go core (gomobile-bound internal/mobile through mobile/remohopcore).
///
/// The gateway runs in this process on 127.0.0.1 with a random port and a
/// fresh token per start. Never log `gatewayURL()`: it carries the token.
enum Core {
    struct Failure: LocalizedError {
        let message: String
        var errorDescription: String? { message }
    }

    private static let lock = NSLock()
    nonisolated(unsafe) private static var initialized = false
    nonisolated(unsafe) private static var url: URL?

    /// Points the core at its configuration directory (excluded from
    /// backups: it holds the identity key and pairing tokens): the App
    /// Group's, shared with the broadcast extension so that the shared
    /// screen has this device's identity and ID (see configDirectory).
    static func initialize(deviceName: String, appVersion: String) throws {
        lock.lock()
        defer { lock.unlock() }
        if initialized { return }
        var dir = try configDirectory()
        var values = URLResourceValues()
        values.isExcludedFromBackup = true
        try? dir.setResourceValues(values)
        let fm = FileManager.default
        let tmp = fm.temporaryDirectory.appendingPathComponent("go-tmp", isDirectory: true)
        var err: NSError?
        if !RemohopcoreInit(dir.path, tmp.path, &err) { throw err ?? Failure(message: "init failed") }
        RemohopcoreSetAppVersion("\(appVersion) (iOS)")
        RemohopcoreSetDeviceName(deviceName)
        initialized = true
    }

    /// The core's configuration directory. Up to 1.0.3 it was the app's
    /// private Application Support/remohop; now it is the App Group's
    /// (`AppGroup.configDirectory`), so that the broadcast extension uses
    /// the same identity. The first launch with the App Group copies the old
    /// directory there (the old one stays, unused, as a fallback). Without
    /// the App Group entitlement (an unsigned simulator build) it stays in
    /// the old place, where the broadcast extension cannot reach it.
    static func configDirectory() throws -> URL {
        let fm = FileManager.default
        let legacy = try fm.url(for: .applicationSupportDirectory, in: .userDomainMask, appropriateFor: nil, create: true)
            .appendingPathComponent("remohop", isDirectory: true)
        guard let shared = AppGroup.configDirectory else {
            try fm.createDirectory(at: legacy, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
            return legacy
        }
        let marker = shared.appendingPathComponent(AppGroup.readyMarker)
        if fm.fileExists(atPath: marker.path) { return shared }
        do {
            try fm.createDirectory(at: shared.deletingLastPathComponent(), withIntermediateDirectories: true)
            if fm.fileExists(atPath: legacy.path) {
                // Nothing in the group directory can be newer than the app's
                // own: the extension does not start before the marker exists.
                try? fm.removeItem(at: shared)
                try fm.copyItem(at: legacy, to: shared)
            } else {
                try fm.createDirectory(at: shared, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
            }
            try Data().write(to: marker)
            return shared
        } catch {
            // Keep working with the old directory; sharing reports it cannot start.
            try fm.createDirectory(at: legacy, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
            return legacy
        }
    }

    /// The URL (with its token) of a gateway that has just answered a
    /// request from this process: it is started if needed, and a listening
    /// socket that iOS took away while the app was suspended is opened
    /// again, on the same port when possible (see EnsureGateway in
    /// internal/mobile). Blocks for at most a couple of seconds: call it
    /// off the main thread. The caller reloads the page only if the URL
    /// changed.
    static func gatewayURL() throws -> URL {
        lock.lock()
        defer { lock.unlock() }
        var err: NSError?
        let s = RemohopcoreEnsureGateway(&err)
        if let err { throw err }
        guard let u = URL(string: s), GatewayURLs.origin(s) != nil else { throw Failure(message: "unexpected gateway address") }
        url = u
        return u
    }

    /// Closes the gateway's listening socket before the app is suspended
    /// (Apple's TN2277: a suspended app's listening sockets are taken away);
    /// the host sessions stay, and `gatewayURL()` reopens it on return.
    static func pauseGateway() {
        lock.lock()
        defer { lock.unlock() }
        RemohopcorePauseGateway()
    }

    static var gatewayRunning: Bool { RemohopcoreGatewayRunning() }

    /// Whether any host session exists (connected, reconnecting or failed).
    static var hasSessions: Bool {
        var err: NSError?
        let s = RemohopcoreSessionsJSON(&err)
        return err == nil && s != "[]" && !s.isEmpty
    }

    /// Connection attempts still dialing or waiting for the host's owner to approve.
    static var pendingConnections: Int { RemohopcorePendingConnections() }

    /// Whether there is anything to keep the app running for in the
    /// background: a host session, or an attempt (the user switches to a
    /// chat to ask for approval exactly then).
    static var busy: Bool { hasSessions || pendingConnections > 0 }

    /// Re-establishes host sessions that stopped answering (the network changed).
    static func checkSessions() { RemohopcoreCheckSessions() }

    /// Tests only: drops every host connection as an outage would; the sessions reconnect.
    static func dropSessionsForTest() { RemohopcoreDropSessionsForTest() }

    /// Tests only (GatewayE2ETests): breaks the listener the way iOS does to
    /// a suspended app's socket, since the simulator never suspends the app.
    static func breakListenerForTest(stall: Bool) { RemohopcoreBreakGatewayListenerForTest(stall) }

    // ---- screen sharing: the owner's own password ----

    /// Saves the owner's own password for sharing this screen, kept across
    /// starts ("" goes back to a new temporary password at every start), in
    /// the configuration directory the broadcast extension shares: its host
    /// reads it at start and applies a change within a second. Throws the
    /// core's refusal (a weak password) or a failure to save. Never log it.
    static func setSharePassword(_ password: String) throws {
        var err: NSError?
        if !RemohopcoreSetHostFixedPassword(password, &err) { throw err ?? Failure(message: "could not save the password") }
    }

    /// Whether the owner's own password is set for sharing (else a
    /// temporary one is made at every start).
    static var sharePasswordFixed: Bool {
        var err: NSError?
        let s = RemohopcoreHostStateJSON(&err)
        return err == nil && HostStatus(json: s).passwordFixed
    }

    // ---- settings screen ----

    static func settings() throws -> [String: String] {
        var err: NSError?
        let s = RemohopcoreSettingsJSON(&err)
        if let err { throw err }
        return (try? JSONSerialization.jsonObject(with: Data(s.utf8)) as? [String: String]) ?? [:]
    }

    static func setSetting(_ key: String, _ value: String) throws {
        var err: NSError?
        if !RemohopcoreSetSetting(key, value, &err) { throw err ?? Failure(message: "could not save \(key)") }
    }

    /// Saves a setting; nil when saved, else the core's code (bad_address,
    /// bad_key, bad_name, bad_value, error) and its text for the log.
    static func setSettingCode(_ key: String, _ value: String) -> (code: String, message: String)? {
        var err: NSError?
        let s = RemohopcoreSetSettingJSON(key, value, &err)
        if let err { return ("error", err.localizedDescription) }
        let obj = (try? JSONSerialization.jsonObject(with: Data(s.utf8))) as? [String: Any] ?? [:]
        if (obj["ok"] as? Bool) == true { return nil }
        return (obj["code"] as? String ?? "error", obj["error"] as? String ?? "")
    }

    static func identity() throws -> String {
        var err: NSError?
        let s = RemohopcoreIdentity(&err)
        if let err { throw err }
        return s
    }

    static func publicKeyLine() throws -> String {
        var err: NSError?
        let s = RemohopcorePublicKeyLine(&err)
        if let err { throw err }
        return s
    }

    static func devices(checkOnline: Bool) throws -> [[String: Any]] {
        var err: NSError?
        let s = RemohopcoreDevicesJSON(checkOnline, &err)
        if let err { throw err }
        return (try JSONSerialization.jsonObject(with: Data(s.utf8)) as? [[String: Any]]) ?? []
    }

    /// The outcome object of PairWithPassword: {"ok":true,...} or {"ok":false,"code","error"}.
    static func pair(target: String, password: String, alias: String) throws -> [String: Any] {
        var err: NSError?
        let s = RemohopcorePairWithPassword(target, password, alias, &err)
        if let err { throw err }
        return (try JSONSerialization.jsonObject(with: Data(s.utf8)) as? [String: Any]) ?? [:]
    }

    static func cancelPair() { RemohopcoreCancelPair() }

    static func forget(_ target: String) throws {
        var n = 0
        var err: NSError?
        if !RemohopcoreForget(target, &n, &err) { throw err ?? Failure(message: "could not forget \(target)") }
    }

    /// The password saved for a host that does not pair (an error when none
    /// is). Read only when the user asks to see or copy it; never log it.
    static func savedPassword(_ target: String) throws -> String {
        var err: NSError?
        let s = RemohopcoreSavedPassword(target, &err)
        if let err { throw err }
        return s
    }

    /// Deletes the password saved for a host; the rest of what is saved for it stays.
    static func forgetSavedPassword(_ target: String) throws {
        var err: NSError?
        if !RemohopcoreForgetSavedPassword(target, &err) { throw err ?? Failure(message: "could not delete the saved password") }
    }
}
