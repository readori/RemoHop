import XCTest
@testable import RemoHop

/// The broadcast extension's pure logic (compiled into the app too, so it
/// is tested here): orientation and sizes, frame rate, bitrate, memory
/// steps, Annex-B, the approval handoff and the shared state.
final class BroadcastLogicTests: XCTestCase {
    // MARK: orientation and size

    func testOrientationToRotation() {
        XCTAssertEqual(StreamGeometry.rotation(orientation: 1), .none)   // up
        XCTAssertEqual(StreamGeometry.rotation(orientation: 2), .none)   // upMirrored
        XCTAssertEqual(StreamGeometry.rotation(orientation: 3), .cw180)  // down
        XCTAssertEqual(StreamGeometry.rotation(orientation: 4), .cw180)
        XCTAssertEqual(StreamGeometry.rotation(orientation: 6), .cw90)   // right
        XCTAssertEqual(StreamGeometry.rotation(orientation: 7), .cw90)
        XCTAssertEqual(StreamGeometry.rotation(orientation: 8), .cw270)  // left
        XCTAssertEqual(StreamGeometry.rotation(orientation: 5), .cw270)
        XCTAssertEqual(StreamGeometry.rotation(orientation: 0), .none)   // missing
        XCTAssertEqual(StreamGeometry.rotation(orientation: 99), .none)
    }

    func testScaledKeepsAspectAndEvenSides() {
        // iPhone 15 Pro: 1179x2556 -> long edge 1920.
        XCTAssertEqual(StreamGeometry.scaled(FrameSize(width: 1179, height: 2556), maxLongEdge: 1920), FrameSize(width: 884, height: 1920))
        // Smaller than the cap: not scaled up, odd sides made even.
        XCTAssertEqual(StreamGeometry.scaled(FrameSize(width: 750, height: 1334), maxLongEdge: 1920), FrameSize(width: 750, height: 1334))
        XCTAssertEqual(StreamGeometry.scaled(FrameSize(width: 641, height: 1137), maxLongEdge: 1920), FrameSize(width: 640, height: 1136))
        // iPad 13": 2064x2752 -> 1440x1920.
        XCTAssertEqual(StreamGeometry.scaled(FrameSize(width: 2064, height: 2752), maxLongEdge: 1920), FrameSize(width: 1440, height: 1920))
        // Lower memory steps.
        XCTAssertEqual(StreamGeometry.scaled(FrameSize(width: 1179, height: 2556), maxLongEdge: 1280), FrameSize(width: 590, height: 1280))
        XCTAssertEqual(StreamGeometry.scaled(FrameSize(width: 1179, height: 2556), maxLongEdge: 960), FrameSize(width: 442, height: 960))
        // Degenerate input.
        XCTAssertEqual(StreamGeometry.scaled(FrameSize(width: 0, height: 0), maxLongEdge: 1920), FrameSize(width: 2, height: 2))
        XCTAssertEqual(StreamGeometry.scaled(FrameSize(width: 1, height: 3000), maxLongEdge: 1921), FrameSize(width: 2, height: 1920))
        for w in stride(from: 300, to: 3000, by: 37) {
            for h in stride(from: 300, to: 3000, by: 53) {
                let s = StreamGeometry.scaled(FrameSize(width: w, height: h), maxLongEdge: 1920)
                XCTAssertTrue(s.width % 2 == 0 && s.height % 2 == 0 && s.longEdge <= 1920, "\(w)x\(h) -> \(s)")
                let a = Double(w) / Double(h), b = Double(s.width) / Double(s.height)
                XCTAssertEqual(a, b, accuracy: a * 0.02, "\(w)x\(h) -> \(s)")
            }
        }
    }

    func testLayoutTurnsLandscapeUpright() {
        let src = FrameSize(width: 1179, height: 2556)
        let up = StreamGeometry.layout(source: src, rotation: .none, maxLongEdge: 1920)
        XCTAssertEqual(up.output, FrameSize(width: 884, height: 1920))
        let left = StreamGeometry.layout(source: src, rotation: .cw270, maxLongEdge: 1920)
        XCTAssertEqual(left.scaled, FrameSize(width: 884, height: 1920))
        XCTAssertEqual(left.output, FrameSize(width: 1920, height: 884))
        XCTAssertEqual(StreamGeometry.layout(source: src, rotation: .cw90, maxLongEdge: 1920).output, FrameSize(width: 1920, height: 884))
        XCTAssertEqual(StreamGeometry.layout(source: src, rotation: .cw180, maxLongEdge: 1920).output, FrameSize(width: 884, height: 1920))
    }

    // MARK: frame rate

    func testThrottleKeepsThirtyFromSixtyAndOneTwenty() {
        for hz in [60.0, 120.0] {
            var t = FrameThrottle(fps: 30)
            var n = 0
            for i in 0..<Int(hz) * 10 where t.admit(Double(i) / hz) { n += 1 }
            XCTAssertEqual(Double(n) / 10, 30, accuracy: 1, "\(hz) Hz")
        }
        // A 30 Hz source with jitter keeps every frame.
        var t = FrameThrottle(fps: 30)
        var n = 0
        for i in 0..<300 where t.admit(Double(i) / 30 + (i % 2 == 0 ? 0.002 : -0.002)) { n += 1 }
        XCTAssertEqual(n, 300)
        // Time going backwards (a new timeline) is accepted.
        XCTAssertTrue(t.admit(1))
        // A lower rate asked by the core.
        var slow = FrameThrottle(fps: 30)
        slow.setFPS(10)
        n = 0
        for i in 0..<600 where slow.admit(Double(i) / 60) { n += 1 }
        XCTAssertEqual(Double(n) / 10, 10, accuracy: 1)
        slow.setFPS(0)
        XCTAssertEqual(slow.fps, 1)
    }

    // MARK: bitrate

    func testBitrateDecisions() {
        let size = FrameSize(width: 884, height: 1920)
        var b = BitrateController(size: size, fps: 30)
        XCTAssertEqual(b.ceiling, 5_091_840)
        XCTAssertEqual(b.current, 2_500_000)
        let t0 = Date(timeIntervalSince1970: 1000)
        // Down: at once.
        XCTAssertEqual(b.request(1_000_000, now: t0), 1_000_000)
        // A tiny change is ignored.
        XCTAssertNil(b.request(980_000, now: t0))
        XCTAssertNil(b.request(1_050_000, now: t0))
        // Up: once a second at most.
        XCTAssertEqual(b.request(2_000_000, now: t0), 2_000_000)
        XCTAssertNil(b.request(3_000_000, now: t0.addingTimeInterval(0.5)))
        XCTAssertEqual(b.request(3_000_000, now: t0.addingTimeInterval(1.1)), 3_000_000)
        // Clamped to the ceiling and the floor.
        XCTAssertEqual(b.request(50_000_000, now: t0.addingTimeInterval(3)), 5_091_840)
        XCTAssertEqual(b.request(10, now: t0.addingTimeInterval(4)), BitrateController.floor)
        XCTAssertEqual(b.bytesPerSecondLimit, BitrateController.floor / 8 * 3 / 2)
        // A smaller frame lowers the ceiling and the current value.
        XCTAssertEqual(b.request(4_000_000, now: t0.addingTimeInterval(10)), 4_000_000)
        XCTAssertEqual(b.resize(FrameSize(width: 442, height: 960), fps: 30), 1_272_960)
        XCTAssertNil(b.resize(FrameSize(width: 884, height: 1920), fps: 30))
        // Never below 1 Mbit/s of ceiling, never above 8.
        XCTAssertEqual(BitrateController.ceiling(for: FrameSize(width: 100, height: 100), fps: 1), 1_000_000)
        XCTAssertEqual(BitrateController.ceiling(for: FrameSize(width: 4000, height: 4000), fps: 60), 8_000_000)
    }

    // MARK: memory

    func testMemoryGovernorStepsDown() {
        var g = MemoryGovernor()
        let t0 = Date(timeIntervalSince1970: 0)
        XCTAssertEqual(g.longEdge, 1920)
        XCTAssertEqual(g.observe(availableMB: 30, footprintMB: 20, now: t0), .none)
        XCTAssertEqual(g.observe(availableMB: 10, footprintMB: 40, now: t0), .stepDown(1280))
        XCTAssertEqual(g.observe(availableMB: 8, footprintMB: 42, now: t0.addingTimeInterval(1)), .none) // too soon
        XCTAssertEqual(g.observe(availableMB: 8, footprintMB: 42, now: t0.addingTimeInterval(6)), .stepDown(960))
        XCTAssertEqual(g.observe(availableMB: 8, footprintMB: 42, now: t0.addingTimeInterval(12)), .freeMemory)
        XCTAssertEqual(g.longEdge, 960)
        // Without os_proc_available_memory: the footprint against 50 MB.
        var h = MemoryGovernor()
        XCTAssertEqual(h.observe(availableMB: 0, footprintMB: 30, now: t0), .none)
        XCTAssertEqual(h.observe(availableMB: 0, footprintMB: 40, now: t0), .stepDown(1280))
    }

    // MARK: Annex-B

    func testAnnexBInPlace() {
        // Two NAL units: an IDR slice of 3 bytes and an SEI of 2.
        var bytes: [UInt8] = [0, 0, 0, 3, 0x65, 1, 2, 0, 0, 0, 2, 0x06, 9]
        let ok = bytes.withUnsafeMutableBytes { AnnexB.convertInPlace($0) }
        XCTAssertTrue(ok)
        XCTAssertEqual(bytes, [0, 0, 0, 1, 0x65, 1, 2, 0, 0, 0, 1, 0x06, 9])
        XCTAssertEqual(AnnexB.nalTypes(Data(bytes)), [5, 6])
        // A length past the end is refused.
        var bad: [UInt8] = [0, 0, 0, 9, 0x65, 1]
        XCTAssertFalse(bad.withUnsafeMutableBytes { AnnexB.convertInPlace($0) })
        var zero: [UInt8] = [0, 0, 0, 0]
        XCTAssertFalse(zero.withUnsafeMutableBytes { AnnexB.convertInPlace($0) })
        let head = AnnexB.parameterSets(sps: Data([0x67, 0x64]), pps: Data([0x68, 0xee]))
        XCTAssertEqual([UInt8](head), [0, 0, 0, 1, 0x67, 0x64, 0, 0, 0, 1, 0x68, 0xee])
        XCTAssertEqual(AnnexB.nalTypes(head), [7, 8])
    }

    // MARK: approval handoff

    private func book() -> ApprovalBook {
        var i = 0
        return ApprovalBook(timeout: 60) { i += 1; return "k\(i)" }
    }

    private func req(_ n: Int, first: Bool = true) -> CoreRequest {
        CoreRequest(n: n, device: "Pixel \(n)", identity: "ab:cd", from: "203.0.113.\(n)", firstSeen: first)
    }

    func testApprovalNotifyAndAllow() {
        var b = book()
        let t0 = Date(timeIntervalSince1970: 100)
        let a = b.sync([req(1)], now: t0)
        guard case .notify(let r)? = a.first, a.count == 1 else { return XCTFail("\(a)") }
        XCTAssertEqual(r.key, "k1")
        XCTAssertEqual(r.n, 1)
        XCTAssertTrue(r.firstSeen)
        XCTAssertEqual(r.deadline, t0.addingTimeInterval(60))
        // The same list again: nothing new.
        XCTAssertEqual(b.sync([req(1)], now: t0.addingTimeInterval(1)), [])
        // Allow from the notification.
        XCTAssertEqual(b.decide(key: "k1", allow: true, now: t0.addingTimeInterval(5)), [.decide(n: 1, allow: true), .withdraw(key: "k1")])
        // A second answer (the app page and the notification both) is ignored.
        XCTAssertEqual(b.decide(key: "k1", allow: false, now: t0.addingTimeInterval(6)), [])
        XCTAssertTrue(b.open.isEmpty)
        // The core drops it after the decision: nothing to do.
        XCTAssertEqual(b.sync([], now: t0.addingTimeInterval(7)), [])
    }

    func testApprovalTimeoutDeniesAndLateAnswerIsIgnored() {
        var b = book()
        let t0 = Date(timeIntervalSince1970: 100)
        _ = b.sync([req(1), req(2, first: false)], now: t0)
        XCTAssertEqual(b.nextDeadline, t0.addingTimeInterval(60))
        XCTAssertEqual(b.expire(now: t0.addingTimeInterval(59)), [])
        XCTAssertEqual(b.decide(key: "k2", allow: false, now: t0.addingTimeInterval(10)), [.decide(n: 2, allow: false), .withdraw(key: "k2")])
        XCTAssertEqual(b.expire(now: t0.addingTimeInterval(60)), [.decide(n: 1, allow: false), .withdraw(key: "k1")])
        // Allow arriving after the deadline does nothing.
        XCTAssertEqual(b.decide(key: "k1", allow: true, now: t0.addingTimeInterval(61)), [])
        XCTAssertNil(b.nextDeadline)
    }

    func testApprovalAnswerAtTheDeadlineIsADenial() {
        var b = book()
        let t0 = Date(timeIntervalSince1970: 100)
        _ = b.sync([req(1)], now: t0)
        // An Allow processed only after the deadline (the tick was late) still denies.
        XCTAssertEqual(b.decide(key: "k1", allow: true, now: t0.addingTimeInterval(60)), [.decide(n: 1, allow: false), .withdraw(key: "k1")])
    }

    func testApprovalWithdrawnWhenTheCoreDropsIt() {
        var b = book()
        let t0 = Date(timeIntervalSince1970: 100)
        _ = b.sync([req(1), req(2)], now: t0)
        // The viewer gave up on request 1; a third one arrives.
        let a = b.sync([req(2), req(3)], now: t0.addingTimeInterval(3))
        XCTAssertEqual(a.first, .withdraw(key: "k1"))
        guard a.count == 2, case .notify(let r) = a[1] else { return XCTFail("\(a)") }
        XCTAssertEqual(r.n, 3)
        XCTAssertEqual(r.key, "k3")
        // An answer for the dropped one goes nowhere.
        XCTAssertEqual(b.decide(key: "k1", allow: true, now: t0.addingTimeInterval(4)), [])
        XCTAssertEqual(b.open.map(\.n), [2, 3])
        // Unknown keys go nowhere.
        XCTAssertEqual(b.decide(key: "nope", allow: true, now: t0.addingTimeInterval(4)), [])
        // Sharing stops: everything open is denied.
        XCTAssertEqual(b.finish(), [.decide(n: 2, allow: false), .withdraw(key: "k2"), .decide(n: 3, allow: false), .withdraw(key: "k3")])
        XCTAssertTrue(b.open.isEmpty)
    }

    func testApprovalTakesTheCoresSoonerDeadline() {
        var b = book()
        let t0 = Date(timeIntervalSince1970: 100)
        var r = req(1)
        r.left = 12 // the core started counting before the extension saw it
        _ = b.sync([r], now: t0)
        XCTAssertEqual(b.nextDeadline, t0.addingTimeInterval(12))
        var later = req(2)
        later.left = 600
        _ = b.sync([r, later], now: t0)
        XCTAssertEqual(b.open.last?.deadline, t0.addingTimeInterval(60))
    }

    func testHostStatusFromTheCore() {
        let json = """
        {"on":true,"phase":"ready","id":"123 456 789","password":"pw-1","online":true,"viewers":1,
         "requests":[{"n":4,"name":"laptop","device":"","identity":"ab12:cd34","from":"203.0.113.9","auth":"password","left":42,"first_seen":true},
                     {"n":5,"device":"Mac","identity":"ee:ff","auth":"session","left":50,"first_seen":false,"access":["input"],"session":2}],
         "sessions":[{"id":2,"label":"Mac","identity":"ee:ff","from":"198.51.100.2","path":"direct","seconds":30,"control":false}]}
        """
        let s = HostStatus(json: json)
        XCTAssertEqual(s.phase, "ready")
        XCTAssertEqual(s.id, "123 456 789")
        XCTAssertEqual(s.password, "pw-1")
        XCTAssertTrue(s.online)
        XCTAssertEqual(s.requests, [CoreRequest(n: 4, device: "laptop", identity: "ab12:cd34", from: "203.0.113.9", firstSeen: true, left: 42)])
        XCTAssertEqual(s.accessRequests, [5])
        XCTAssertEqual(s.viewers, [HostStatus.Viewer(id: 2, label: "Mac", from: "198.51.100.2")])
        let off = HostStatus(json: #"{"on":false,"phase":"no_server","addresses":["192.168.1.5:21118"],"requests":[],"sessions":[]}"#)
        XCTAssertEqual(off.phase, "no_server")
        XCTAssertEqual(off.addresses, ["192.168.1.5:21118"])
        XCTAssertEqual(HostStatus(json: "not json"), HostStatus())
    }

    func testRealKeysAreUnique() {
        var b = ApprovalBook()
        let a = b.sync([req(1), req(2)], now: Date())
        let keys = a.compactMap { if case .notify(let r) = $0 { return r.key } else { return nil } }
        XCTAssertEqual(Set(keys).count, 2)
        XCTAssertTrue(keys.allSatisfy { $0.count >= 32 })
    }

    // MARK: shared state and commands

    func testStateLivenessAndRoundTrip() throws {
        let t0 = Date(timeIntervalSince1970: 1_700_000_000)
        var s = BroadcastState(phase: .ready, id: "123 456 789", password: "secret", updated: t0)
        s.viewers = [BroadcastState.Viewer(id: 3, label: "Mac", from: "198.51.100.7", since: t0)]
        XCTAssertTrue(s.isLive(now: t0.addingTimeInterval(5)))
        XCTAssertFalse(s.isLive(now: t0.addingTimeInterval(11)))
        // A state that stopped updating is shown as ended, without its password.
        let gone = s.effective(now: t0.addingTimeInterval(30))
        XCTAssertEqual(gone.phase, .stopped)
        XCTAssertEqual(gone.password, "")
        XCTAssertTrue(gone.viewers.isEmpty)
        XCTAssertEqual(s.effective(now: t0.addingTimeInterval(1)), s)
        let failed = BroadcastState(phase: .failed, error: "host", updated: t0)
        XCTAssertEqual(failed.effective(now: t0.addingTimeInterval(999)), failed)

        let data = try BroadcastStore.encoder.encode(s)
        XCTAssertEqual(try BroadcastStore.decoder.decode(BroadcastState.self, from: data), s)
        let c = BroadcastCommand(kind: .decide, key: "k1", allow: true, issued: t0)
        XCTAssertEqual(try BroadcastStore.decoder.decode(BroadcastCommand.self, from: BroadcastStore.encoder.encode(c)), c)
    }

    func testNotificationTexts() {
        XCTAssertEqual(BroadcastStrings.requestTitle(device: "Pixel 8", lang: "en"), "Pixel 8 wants to view your screen")
        XCTAssertEqual(BroadcastStrings.requestTitle(device: "", lang: "zh"), "一台设备 请求查看你的屏幕")
        let en = BroadcastStrings.requestBody(firstSeen: true, from: "203.0.113.5", lang: "en")
        XCTAssertTrue(en.hasPrefix("New device · 203.0.113.5\n"), en)
        XCTAssertTrue(en.contains("scam"))
        let zh = BroadcastStrings.requestBody(firstSeen: false, from: "", lang: "zh")
        XCTAssertTrue(zh.hasPrefix("以前连接过\n"), zh)
        XCTAssertTrue(zh.contains("诈骗"))
        XCTAssertEqual(AppGroup.language(preferred: ["zh-Hans-CN"]), AppGroup.defaults?.string(forKey: AppGroup.languageKey) == "en" ? "en" : "zh")
    }

    func testApprovalResponsesBecomeCommands() {
        // Not about screen sharing: left alone.
        XCTAssertFalse(BroadcastApprovals.handle(action: BroadcastStrings.allowAction, category: "other", userInfo: [BroadcastStrings.requestKey: "k"]))
        XCTAssertFalse(BroadcastApprovals.handle(action: BroadcastStrings.allowAction, category: BroadcastStrings.category, userInfo: [:]))
    }

    func testShareScreenStringsAreTranslated() {
        for s in L10n.shareStrings { XCTAssertNotNil(L10n.zh[s], s) }
        for (en, zh) in BroadcastStrings.zh {
            XCTAssertFalse(zh.isEmpty, en)
            XCTAssertEqual(en.contains("%@"), zh.contains("%@"), en)
        }
        XCTAssertEqual(BroadcastPicker.extensionID, (Bundle.main.bundleIdentifier ?? "com.remohop.app") + ".broadcast")
    }
}
