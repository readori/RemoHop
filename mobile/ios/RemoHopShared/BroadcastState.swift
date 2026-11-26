import Foundation

/// What the broadcast extension tells the app: written by the extension to
/// `<group>/Library/Broadcast/state.json` whenever it changes and at least
/// every `heartbeat` seconds, read by the app's "Share this screen" page.
///
/// It holds the temporary password, which the page shows with Copy: the
/// file lives in the App Group container (only this app and its extension
/// can read it) and is cleared when sharing stops. Never log it.
struct BroadcastState: Codable, Equatable {
    enum Phase: String, Codable {
        /// The extension runs and the host is starting (no ID yet).
        case starting
        /// Reachable: an ID from the rendezvous server, or addresses without one.
        case ready
        /// Sharing ended normally.
        case stopped
        /// Sharing could not start or ended with an error (`error` says why).
        case failed
    }

    /// A login waiting for the owner. `key` is the extension's random name
    /// for it (the notification's identifier and the decision's address);
    /// `n` is the core's number, meaningful only to that extension run.
    struct Request: Codable, Equatable, Identifiable {
        var key: String
        var n: Int
        /// The name the device gave itself (may be empty).
        var device: String
        /// Its identity fingerprint, shortened for display.
        var identity: String
        var from: String
        /// This device never asked before.
        var firstSeen: Bool
        /// Refused automatically at this time.
        var deadline: Date
        var id: String { key }
    }

    /// A connected viewer.
    struct Viewer: Codable, Equatable, Identifiable {
        var id: Int
        var label: String
        var from: String
        var since: Date
    }

    var phase: Phase
    /// The 9-digit ID as shown ("123 456 789"), or "" (none yet, or no server).
    var id: String = ""
    /// The temporary password (new for every broadcast).
    var password: String = ""
    /// Direct addresses (host:port), shown when there is no ID.
    var addresses: [String] = []
    /// A short code when phase is failed ("host": host mode could not start or stopped).
    var error: String = ""
    var requests: [Request] = []
    var viewers: [Viewer] = []
    /// When the extension last wrote this (the heartbeat).
    var updated: Date
    /// The extension's memory footprint in MB at that time (diagnostics).
    var footprintMB: Double = 0

    /// The extension writes at least this often while it runs.
    static let heartbeat: TimeInterval = 2
    /// Older than this, a running state means the extension is gone (killed
    /// by the system, for example for memory).
    static let staleAfter: TimeInterval = 10

    static func idle(_ now: Date = Date()) -> BroadcastState { BroadcastState(phase: .stopped, updated: now) }

    /// Whether the extension is (still) sharing.
    func isLive(now: Date = Date()) -> Bool {
        (phase == .starting || phase == .ready) && now.timeIntervalSince(updated) < Self.staleAfter
    }

    /// What the app should show: a running state that stopped updating is
    /// treated as ended, without its password and requests.
    func effective(now: Date = Date()) -> BroadcastState {
        if phase == .stopped || phase == .failed || isLive(now: now) { return self }
        return BroadcastState(phase: .stopped, updated: updated)
    }
}

/// A command from the app to the extension. Each one is its own file in
/// `commands/` (written atomically, read and deleted by the extension), so
/// the two processes never write the same file.
struct BroadcastCommand: Codable, Equatable {
    enum Kind: String, Codable {
        /// Answer request `key`.
        case decide
        /// End viewer `viewer` (0: every viewer).
        case disconnect
        /// Stop sharing.
        case stop
    }

    var kind: Kind
    var key: String = ""
    var allow: Bool = false
    var viewer: Int = 0
    var issued: Date = Date()

    /// Commands older than this are dropped unread (left over from an
    /// earlier broadcast).
    static let maxAge: TimeInterval = 120
}

/// The files in the group container. Every method is safe to call from
/// either process; a missing container (no App Group entitlement) makes
/// them no-ops.
enum BroadcastStore {
    static var stateURL: URL? { AppGroup.broadcastDirectory?.appendingPathComponent("state.json") }
    static var commandsURL: URL? { AppGroup.broadcastDirectory?.appendingPathComponent("commands", isDirectory: true) }

    static let encoder: JSONEncoder = {
        let e = JSONEncoder()
        e.dateEncodingStrategy = .millisecondsSince1970
        return e
    }()

    static let decoder: JSONDecoder = {
        let d = JSONDecoder()
        d.dateDecodingStrategy = .millisecondsSince1970
        return d
    }()

    // ---- state (written by the extension) ----

    static func writeState(_ s: BroadcastState) {
        guard let url = stateURL, let data = try? encoder.encode(s) else { return }
        try? FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true,
                                                 attributes: [.posixPermissions: 0o700])
        try? data.write(to: url, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
        DarwinNote.post(DarwinNote.state)
    }

    static func readState() -> BroadcastState? {
        guard let url = stateURL, let data = try? Data(contentsOf: url) else { return nil }
        return try? decoder.decode(BroadcastState.self, from: data)
    }

    // ---- commands (written by the app) ----

    @discardableResult
    static func send(_ c: BroadcastCommand) -> Bool {
        guard let dir = commandsURL, let data = try? encoder.encode(c) else { return false }
        do {
            try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
            // The name sorts by time, so commands run in the order they were sent.
            let name = String(format: "%013.0f-%@.json", c.issued.timeIntervalSince1970 * 1000, UUID().uuidString)
            try data.write(to: dir.appendingPathComponent(name), options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
        } catch {
            return false
        }
        DarwinNote.post(DarwinNote.command)
        return true
    }

    /// Reads and deletes the waiting commands, oldest first; stale ones are dropped.
    static func takeCommands(now: Date = Date()) -> [BroadcastCommand] {
        guard let dir = commandsURL,
              let names = try? FileManager.default.contentsOfDirectory(atPath: dir.path) else { return [] }
        var out: [BroadcastCommand] = []
        for name in names.sorted() where name.hasSuffix(".json") {
            let url = dir.appendingPathComponent(name)
            let data = try? Data(contentsOf: url)
            try? FileManager.default.removeItem(at: url)
            guard let data, let c = try? decoder.decode(BroadcastCommand.self, from: data) else { continue }
            if now.timeIntervalSince(c.issued) <= BroadcastCommand.maxAge { out.append(c) }
        }
        return out
    }

    /// Removes every waiting command (the extension starts with none).
    static func clearCommands() {
        guard let dir = commandsURL else { return }
        try? FileManager.default.removeItem(at: dir)
    }
}
