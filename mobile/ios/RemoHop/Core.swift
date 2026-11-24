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

    /// Points the core at the app's private directory (Application Support,
    /// excluded from backups: it holds the identity key and pairing tokens).
    static func initialize(deviceName: String, appVersion: String) throws {
        lock.lock()
        defer { lock.unlock() }
        if initialized { return }
        let fm = FileManager.default
        var dir = try fm.url(for: .applicationSupportDirectory, in: .userDomainMask, appropriateFor: nil, create: true)
            .appendingPathComponent("remohop", isDirectory: true)
        try fm.createDirectory(at: dir, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        var values = URLResourceValues()
        values.isExcludedFromBackup = true
        try? dir.setResourceValues(values)
        let tmp = fm.temporaryDirectory.appendingPathComponent("go-tmp", isDirectory: true)
        var err: NSError?
        if !RemohopcoreInit(dir.path, tmp.path, &err) { throw err ?? Failure(message: "init failed") }
        RemohopcoreSetAppVersion("\(appVersion) (iOS)")
        RemohopcoreSetDeviceName(deviceName)
        initialized = true
    }

    /// The gateway's URL (with its token), starting the gateway if needed.
    static func gatewayURL() throws -> URL {
        lock.lock()
        defer { lock.unlock() }
        if let url, RemohopcoreGatewayRunning() { return url }
        var err: NSError?
        let s = RemohopcoreStartGateway(&err)
        if let err { throw err }
        guard let u = URL(string: s), GatewayURLs.origin(s) != nil else { throw Failure(message: "unexpected gateway address") }
        url = u
        return u
    }

    static var gatewayRunning: Bool { RemohopcoreGatewayRunning() }

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
}
