import Foundation

/// What the app and its broadcast upload extension (RemoHopBroadcast) share
/// through their App Group. Compiled into both targets.
///
/// The extension runs the host in its own process while the app may be
/// suspended, so everything they exchange goes through the group container:
///
///   - `configDirectory`: the Go core's configuration directory (identity
///     key, saved hosts, settings, the diagnostic log). Both processes use
///     it, so the shared screen has this device's own identity and ID. It
///     also holds the owner's own password for sharing (the core's
///     `host-password`, 0600): the app saves it with the core's
///     SetHostFixedPassword, and the extension's host reads it when it
///     starts and picks up a change within a second (ReloadHostPassword).
///   - `broadcastDirectory`: `state.json`, written by the extension (ID,
///     password, requests, viewers), and `commands/`, one file per
///     command from the app (decide, disconnect, stop); see BroadcastStore.
///   - `defaults`: the app's language choice, mirrored for the extension's
///     notifications.
///
/// Darwin notifications (DarwinNote) only say "look again": the files are
/// what counts, so a missed notification costs a poll interval, not a
/// decision.
enum AppGroup {
    /// Registered for com.remohop.app and com.remohop.app.broadcast.
    static let identifier = "group.com.remohop.app"

    /// The group container, or nil when the build has no App Group
    /// entitlement (an unsigned simulator build, a profile without it).
    static var containerURL: URL? {
        FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: identifier)
    }

    /// `<group>/Library/Application Support/remohop`, the core's
    /// configuration directory shared by the app and the extension.
    static var configDirectory: URL? {
        containerURL?.appendingPathComponent("Library/Application Support/remohop", isDirectory: true)
    }

    /// The file in `configDirectory` that says it is ready: written by the
    /// app once it has moved its settings there (Core.configDirectory). The
    /// extension does not start without it, so that it never makes an
    /// identity of its own that the app's would then lose to.
    static let readyMarker = ".app-group-ready"

    static var isReady: Bool {
        guard let d = configDirectory else { return false }
        return FileManager.default.fileExists(atPath: d.appendingPathComponent(readyMarker).path)
    }

    /// `<group>/Library/Broadcast`: the extension's state and the app's commands.
    static var broadcastDirectory: URL? {
        containerURL?.appendingPathComponent("Library/Broadcast", isDirectory: true)
    }

    static var defaults: UserDefaults? { UserDefaults(suiteName: identifier) }

    /// The app's language choice ("system", "zh" or "en"), mirrored here by
    /// the app (L10n.key in its own defaults) for the extension.
    static let languageKey = "remohop.language"

    /// "zh" or "en" for the extension: the app's choice, or the system's.
    static func language(preferred: [String] = Locale.preferredLanguages) -> String {
        let choice = defaults?.string(forKey: languageKey) ?? "system"
        switch choice {
        case "zh": return "zh"
        case "en": return "en"
        default: return (preferred.first ?? "").lowercased().hasPrefix("zh") ? "zh" : "en"
        }
    }
}

/// Cross-process "something changed" pings (Darwin notifications carry no
/// data). The extension posts `state` after writing state.json; the app
/// posts `command` after writing a command file.
enum DarwinNote {
    static let state = "com.remohop.app.broadcast.state"
    static let command = "com.remohop.app.broadcast.command"

    static func post(_ name: String) {
        CFNotificationCenterPostNotification(CFNotificationCenterGetDarwinNotifyCenter(),
                                             CFNotificationName(name as CFString), nil, nil, true)
    }

    /// Calls `handler` (on an arbitrary thread) whenever `name` is posted,
    /// until the returned token is released.
    static func observe(_ name: String, _ handler: @escaping @Sendable () -> Void) -> Observation {
        Observation(name: name, handler: handler)
    }

    final class Observation: @unchecked Sendable {
        let name: String
        let handler: @Sendable () -> Void

        init(name: String, handler: @escaping @Sendable () -> Void) {
            self.name = name
            self.handler = handler
            let center = CFNotificationCenterGetDarwinNotifyCenter()
            let me = Unmanaged.passUnretained(self).toOpaque()
            CFNotificationCenterAddObserver(center, me, { _, observer, _, _, _ in
                guard let observer else { return }
                Unmanaged<Observation>.fromOpaque(observer).takeUnretainedValue().handler()
            }, name as CFString, nil, .deliverImmediately)
        }

        deinit {
            CFNotificationCenterRemoveObserver(CFNotificationCenterGetDarwinNotifyCenter(),
                                               Unmanaged.passUnretained(self).toOpaque(),
                                               CFNotificationName(name as CFString), nil)
        }
    }
}
