import Foundation

/// A login the core reports as waiting for the owner (its pending list).
struct CoreRequest: Equatable {
    var n: Int
    var device: String
    var identity: String
    var from: String
    var firstSeen: Bool
}

/// The approval handoff's state machine, run by the broadcast extension.
///
/// Every login waits for the phone's owner. The extension cannot show UI,
/// so each waiting login becomes a local notification (Allow / Deny) and a
/// row on the app's "Share this screen" page. Notification actions are
/// delivered to the app, not the extension: the app writes the answer as a
/// command file in the App Group and posts a Darwin notification; the
/// extension reads the file (also by polling) and passes the answer to the
/// core. This type decides what each event means:
///
///   - `sync` with the core's pending list: a new login gets a random key
///     and a deadline (notify); one that left the list (the core timed it
///     out, the viewer gave up) is withdrawn.
///   - `decide`: the first answer for a key that is still open and not past
///     its deadline goes to the core; any later, duplicate, unknown or late
///     answer is ignored. Keys are random per request and per extension
///     run, so an answer cannot reach a different login.
///   - `expire`: a login nobody answered by its deadline is denied.
///   - `finish`: sharing stops; everything open is denied and withdrawn.
///
/// Pure (no clocks, files or notifications), so it is unit tested.
struct ApprovalBook {
    enum Action: Equatable {
        /// Show the request (notification and app page).
        case notify(BroadcastState.Request)
        /// Tell the core.
        case decide(n: Int, allow: Bool)
        /// Remove the request's notification.
        case withdraw(key: String)
    }

    /// How long a request waits before it is denied (the desktop app's minute).
    static let defaultTimeout: TimeInterval = 60

    let timeout: TimeInterval
    private let makeKey: () -> String
    /// Waiting requests, oldest first.
    private(set) var open: [BroadcastState.Request] = []

    init(timeout: TimeInterval = ApprovalBook.defaultTimeout, makeKey: @escaping () -> String = { UUID().uuidString }) {
        self.timeout = timeout
        self.makeKey = makeKey
    }

    /// Takes the core's current pending list.
    mutating func sync(_ core: [CoreRequest], now: Date) -> [Action] {
        var actions: [Action] = []
        let live = Set(core.map(\.n))
        for r in open where !live.contains(r.n) {
            actions.append(.withdraw(key: r.key))
        }
        open.removeAll { !live.contains($0.n) }
        let known = Set(open.map(\.n))
        for c in core where !known.contains(c.n) {
            let r = BroadcastState.Request(key: makeKey(), n: c.n, device: c.device, identity: c.identity, from: c.from,
                                           firstSeen: c.firstSeen, deadline: now.addingTimeInterval(timeout))
            open.append(r)
            actions.append(.notify(r))
        }
        return actions
    }

    /// The owner's answer for `key`.
    mutating func decide(key: String, allow: Bool, now: Date) -> [Action] {
        guard let i = open.firstIndex(where: { $0.key == key }) else { return [] }
        let r = open[i]
        guard now < r.deadline else { return expire(now: now) }
        open.remove(at: i)
        return [.decide(n: r.n, allow: allow), .withdraw(key: r.key)]
    }

    /// Denies what passed its deadline.
    mutating func expire(now: Date) -> [Action] {
        var actions: [Action] = []
        for r in open where now >= r.deadline {
            actions += [.decide(n: r.n, allow: false), .withdraw(key: r.key)]
        }
        open.removeAll { now >= $0.deadline }
        return actions
    }

    /// Sharing stops: everything open is denied and withdrawn.
    mutating func finish() -> [Action] {
        let actions = open.flatMap { [Action.decide(n: $0.n, allow: false), .withdraw(key: $0.key)] }
        open.removeAll()
        return actions
    }

    /// The earliest deadline, for the extension's timer.
    var nextDeadline: Date? { open.map(\.deadline).min() }
}
