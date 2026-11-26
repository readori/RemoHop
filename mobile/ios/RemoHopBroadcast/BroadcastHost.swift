import Foundation
import Remohopcore
import UIKit
import UserNotifications

/// The host inside the broadcast upload extension: the Go core's host mode
/// fed by the H.264 encoder, the approval handoff, the state file the app
/// shows, and the memory watch. Everything but the encoder runs on `queue`.
///
/// Approval handoff (see ApprovalBook): each waiting login is a local
/// notification with Allow (view only; needs the phone unlocked) and Deny,
/// and a row on the app's sharing page. The app turns an answer into a
/// command file plus a Darwin notification; this side reads the commands
/// on the notification and on every one-second tick, so a lost ping only
/// delays an answer. A request nobody answers is denied after a minute.
///
/// The owner's own password (Password settings in the app) is a file in
/// the shared configuration directory: the host reads it when it starts,
/// and every tick applies a change the app made meanwhile (the app pings
/// `DarwinNote.command` after saving, so that tick comes at once).
/// Its state is used on `queue` only, hence @unchecked Sendable.
final class BroadcastHost: HostCoreEvents, H264EncoderOutput, @unchecked Sendable {
    private let core: HostCore
    private(set) var encoder: H264Encoder!
    private let queue = DispatchQueue(label: "com.remohop.broadcast.host")
    private var book = ApprovalBook()
    private var state = BroadcastState(phase: .starting, updated: Date())
    private var lastWritten: BroadcastState?
    private var timer: DispatchSourceTimer?
    private var pressure: DispatchSourceMemoryPressure?
    private var commandNote: DarwinNote.Observation?
    private var governor = MemoryGovernor()
    /// When each viewer was first seen (the core reports seconds connected).
    private var viewerSince: [Int: Date] = [:]
    private var lastStatsLog = Date()
    private var started = Date()
    private var running = false
    private var paused = false
    private let lang = AppGroup.language()

    /// Ends the broadcast with a message for the user (set by SampleHandler).
    var onFinish: ((String) -> Void)?

    /// The Go heap's soft limit: about 40% of the extension's limit, leaving
    /// room for the encoder's buffers, ReplayKit and the Swift side.
    static let goMemoryLimit: Int64 = 20 << 20
    static let goGCPercent = 50

    init(core: HostCore = HostCoreFactory.make()) {
        self.core = core
    }

    func text(_ s: String) -> String { BroadcastStrings.text(s, lang: lang) }

    // MARK: lifecycle

    func start() throws {
        // Memory first: before the core allocates anything of note.
        let procs = RemohopcoreSetMaxProcs(2)
        RemohopcoreSetMemoryLimit(Self.goMemoryLimit, Self.goGCPercent)
        guard let dir = AppGroup.configDirectory else { throw HostCoreError.failed("no App Group container") }
        // The app moves its identity into the group on its first launch;
        // until then, starting here would make a second identity.
        guard AppGroup.isReady else { throw HostCoreError.openAppFirst }
        let tmp = FileManager.default.temporaryDirectory.appendingPathComponent("go-tmp", isDirectory: true)
        var err: NSError?
        if !RemohopcoreInit(dir.path, tmp.path, &err) { throw HostCoreError.failed(err?.localizedDescription ?? "init failed") }
        let version = Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "dev"
        RemohopcoreSetAppVersion("\(version) (iOS broadcast)")
        RemohopcoreSetDeviceName(UIDevice.current.model)
        ExtLog.log("broadcast started: iOS \(UIDevice.current.systemVersion), GOMAXPROCS \(procs) -> 2, Go limit \(Self.goMemoryLimit >> 20) MB, GOGC \(Self.goGCPercent)")
        ExtLog.memory("at start")

        BroadcastStore.clearCommands()
        BroadcastStrings.registerCategory(lang: lang)
        removeRequestNotifications()
        state = BroadcastState(phase: .starting, updated: Date())
        BroadcastStore.writeState(state)

        encoder = H264Encoder(output: self)
        do {
            try core.start(events: self)
        } catch {
            state.phase = .failed
            state.error = (error as? HostCoreError)?.code ?? "host"
            state.updated = Date()
            BroadcastStore.writeState(state)
            ExtLog.log("host did not start: \(state.error) \(error.localizedDescription)")
            throw error
        }
        running = true
        started = Date()
        commandNote = DarwinNote.observe(DarwinNote.command) { [weak self] in self?.queue.async { self?.tick() } }
        let t = DispatchSource.makeTimerSource(queue: queue)
        t.schedule(deadline: .now() + 1, repeating: 1)
        t.setEventHandler { [weak self] in self?.tick() }
        t.resume()
        timer = t
        let p = DispatchSource.makeMemoryPressureSource(eventMask: [.warning, .critical], queue: queue)
        p.setEventHandler { [weak self] in self?.memoryWarning(critical: p.data.contains(.critical)) }
        p.resume()
        pressure = p
        queue.async { self.refresh() }
    }

    /// Stops everything; safe to call more than once.
    func stop(reason: String) {
        let wasRunning: Bool = queue.sync {
            guard running else { return false }
            running = false
            timer?.cancel()
            pressure?.cancel()
            timer = nil
            pressure = nil
            commandNote = nil
            perform(book.finish())
            let s = encoder.snapshot()
            ExtLog.log("broadcast finished (\(reason)) after \(Int(Date().timeIntervalSince(started))) s: \(s.stats.encoded) frames encoded, \(s.stats.dropped) dropped, \(s.stats.keyframes) keyframes, \(s.stats.errors) errors")
            ExtLog.memory("at finish")
            return true
        }
        // A start that failed left its failed state for the app to show.
        guard wasRunning else { return }
        encoder?.stop()
        core.stop()
        // Without the password and requests; an error stays for the app to show.
        state = BroadcastState(phase: state.phase == .failed ? .failed : .stopped, error: state.error, updated: Date())
        BroadcastStore.writeState(state)
        BroadcastStore.clearCommands()
        removeRequestNotifications()
    }

    func setPaused(_ p: Bool) {
        queue.async {
            self.paused = p
            ExtLog.log(p ? "broadcast paused" : "broadcast resumed")
            if !p { self.encoder.requestKeyframe() }
        }
    }

    // MARK: the one-second tick (on queue)

    private func tick() {
        guard running else { return }
        let now = Date()
        for c in BroadcastStore.takeCommands(now: now) {
            switch c.kind {
            case .decide:
                perform(book.decide(key: c.key, allow: c.allow, now: now))
            case .disconnect:
                ExtLog.log("viewer \(c.viewer == 0 ? "all" : String(c.viewer)) disconnected from the app")
                core.disconnect(viewer: c.viewer)
            case .stop:
                ExtLog.log("stop requested from the app")
                let msg = text("You stopped screen sharing in RemoHop.")
                DispatchQueue.main.async { self.onFinish?(msg) }
                return
            }
        }
        perform(book.expire(now: now))
        core.reloadPassword()
        refresh()
        watchMemory(now: now)
        if now.timeIntervalSince(lastStatsLog) >= 30 {
            lastStatsLog = now
            let s = encoder.snapshot()
            ExtLog.log("encoder: \(s.size) at \(s.fps) fps, \(s.bitrate / 1000) kbit/s; \(s.stats.encoded) frames (\(s.stats.keyframes) key, \(s.stats.dropped) dropped, \(s.stats.errors) errors), \(s.stats.bytes >> 10) KB")
            ExtLog.memory("periodic")
        }
    }

    /// Reads the core's status (or takes the one it just sent), reconciles
    /// the approvals and writes the state file.
    private func refresh(_ json: String? = nil) {
        guard running else { return }
        let st = json.map { HostStatus(json: $0) } ?? core.status()
        let now = Date()
        for n in st.accessRequests {
            // A viewer asking for control: this device cannot be controlled.
            ExtLog.log("request \(n) for control refused: view only")
            core.decide(n: n, allow: false)
        }
        perform(book.sync(st.requests, now: now))
        var s = state
        s.id = st.id
        s.password = st.password
        s.passwordFixed = st.passwordFixed
        s.addresses = st.addresses
        s.requests = book.open
        for v in st.viewers where viewerSince[v.id] == nil { viewerSince[v.id] = now }
        viewerSince = viewerSince.filter { id, _ in st.viewers.contains { $0.id == id } }
        s.viewers = st.viewers.map { BroadcastState.Viewer(id: $0.id, label: $0.label, from: $0.from, since: viewerSince[$0.id] ?? now) }
        switch st.phase {
        case "ready", "no_server": s.phase = .ready
        case "error":
            s.phase = .failed
            s.error = "host"
        default: s.phase = .starting
        }
        if s.phase != .failed { s.error = "" }
        if st.phase == "error" && lastWritten?.phase != .failed {
            // The host stopped by itself: sharing the screen has no point any more.
            ExtLog.log("host mode stopped: \(st.error)")
            let msg = text("Screen sharing stopped because of an error.")
            DispatchQueue.main.async { self.onFinish?(msg) }
        }
        s.footprintMB = (Memory.footprintMB() * 10).rounded() / 10
        let changed: Bool = {
            guard let l = lastWritten else { return true }
            var a = l, b = s
            a.updated = .distantPast; b.updated = .distantPast
            a.footprintMB = 0; b.footprintMB = 0
            return a != b
        }()
        if changed {
            if let l = lastWritten, l.viewers.count != s.viewers.count {
                ExtLog.log("viewers: \(l.viewers.count) -> \(s.viewers.count)")
            }
            if lastWritten?.phase != s.phase { ExtLog.log("host \(s.phase.rawValue)\(s.id.isEmpty ? "" : " with an ID")") }
        }
        if changed || lastWritten == nil || now.timeIntervalSince(lastWritten!.updated) >= BroadcastState.heartbeat {
            s.updated = now
            BroadcastStore.writeState(s)
            lastWritten = s
        }
        state = s
    }

    private func perform(_ actions: [ApprovalBook.Action]) {
        for a in actions {
            switch a {
            case .notify(let r):
                ExtLog.log("login request \(r.n): \(r.firstSeen ? "new device" : "known device"), waiting for the owner")
                postNotification(r)
            case .decide(let n, let allow):
                ExtLog.log("login request \(n): \(allow ? "allowed (view only)" : "denied")")
                core.decide(n: n, allow: allow)
            case .withdraw(let key):
                let c = UNUserNotificationCenter.current()
                let ids = Self.notificationIDs(key)
                c.removeDeliveredNotifications(withIdentifiers: ids)
                c.removePendingNotificationRequests(withIdentifiers: ids)
            }
        }
    }

    // MARK: memory

    private func watchMemory(now: Date) {
        switch governor.observe(availableMB: Memory.availableMB(), footprintMB: Memory.footprintMB(), now: now) {
        case .none: break
        case .stepDown(let edge):
            encoder.setMemoryLongEdge(edge)
            RemohopcoreFreeOSMemory()
            ExtLog.memory("low, long edge -> \(edge)")
        case .freeMemory:
            RemohopcoreFreeOSMemory()
            ExtLog.memory("low at the lowest step")
        }
    }

    private func memoryWarning(critical: Bool) {
        RemohopcoreFreeOSMemory()
        if critical, case .stepDown(let edge) = governor.observe(availableMB: 1, footprintMB: 0, now: Date()) {
            encoder.setMemoryLongEdge(edge)
        }
        ExtLog.memory(critical ? "critical warning" : "warning")
    }

    // MARK: notifications

    /// Seconds after the first alert at which an unanswered request alerts
    /// again (sound and banner), within the core's 60 s before it is refused:
    /// a single banner is easy to miss while the phone is in a pocket.
    static let reminders: [TimeInterval] = [15, 35]

    /// The request's notification and its reminders.
    static func notificationIDs(_ key: String) -> [String] {
        [key] + reminders.indices.map { "\(key)#\($0 + 1)" }
    }

    private func postNotification(_ r: BroadcastState.Request) {
        let content = UNMutableNotificationContent()
        content.title = BroadcastStrings.requestTitle(device: r.device, lang: lang)
        content.body = BroadcastStrings.requestBody(firstSeen: r.firstSeen, from: r.from, lang: lang)
        content.categoryIdentifier = BroadcastStrings.category
        content.threadIdentifier = r.key
        content.userInfo = [BroadcastStrings.requestKey: r.key]
        content.sound = .default
        // Time Sensitive gets through a Focus and stays on the Lock Screen
        // longer; until the app has that entitlement iOS delivers it as
        // .active, as before.
        content.interruptionLevel = .timeSensitive
        content.relevanceScore = 1
        let c = UNUserNotificationCenter.current()
        let ids = Self.notificationIDs(r.key)
        for (i, id) in ids.enumerated() {
            let trigger = i == 0 ? nil : UNTimeIntervalNotificationTrigger(timeInterval: Self.reminders[i - 1], repeats: false)
            c.add(UNNotificationRequest(identifier: id, content: content, trigger: trigger)) { err in
                if let err, i == 0 { ExtLog.log("request notification not shown: \(err.localizedDescription)") }
            }
        }
    }

    private func removeRequestNotifications() {
        let c = UNUserNotificationCenter.current()
        c.getDeliveredNotifications { list in
            let ids = list.filter { $0.request.content.categoryIdentifier == BroadcastStrings.category }.map(\.request.identifier)
            if !ids.isEmpty { c.removeDeliveredNotifications(withIdentifiers: ids) }
        }
        c.getPendingNotificationRequests { list in
            let ids = list.filter { $0.content.categoryIdentifier == BroadcastStrings.category }.map(\.identifier)
            if !ids.isEmpty { c.removePendingNotificationRequests(withIdentifiers: ids) }
        }
    }

    // MARK: HostCoreEvents (core threads)

    func coreStartScreen(maxSize: Int, fps: Int, bitrate: Int64) {
        encoder?.start(maxSize: maxSize, fps: fps, bitrate: bitrate)
    }

    func coreStopScreen() { encoder?.stopEncoding() }
    func coreWantsKeyframe() { encoder?.requestKeyframe() }
    func coreWantsBitrate(_ bps: Int64) { encoder?.requestBitrate(bps) }
    func coreStateChanged(_ json: String) { queue.async { self.refresh(json.isEmpty ? nil : json) } }

    // MARK: H264EncoderOutput

    func encoderScreen(width: Int, height: Int, rotation: Int) {
        core.setScreenInfo(width: width, height: height, rotation: rotation)
    }

    func encoderFrame(_ data: UnsafeRawBufferPointer, captureMicros: Int64, keyframe: Bool) {
        core.push(data, captureMicros: captureMicros)
    }
}
