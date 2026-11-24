import XCTest
@testable import RemoHop

final class BridgeTests: XCTestCase {
    let origin = "http://127.0.0.1:5000"

    final class FakeHost: BridgeHost {
        var calls: [String] = []
        func clipboardRead() -> String { calls.append("read"); return "native text" }
        func clipboardWrite(_ text: String) { calls.append("write:\(text)") }
        func download(url: URL, name: String) { calls.append("download:\(name)") }
        func setState(keepScreenOn: Bool, fullscreen: Bool) { calls.append("state:\(keepScreenOn),\(fullscreen)") }
        func keyboard(show: Bool) { calls.append("keyboard:\(show)") }
        func haptic() { calls.append("haptic") }
        func openSettings() { calls.append("settings") }
        func openSettings(page: String) { calls.append("settings:\(page)") }
        func diagLog(_ text: String) { calls.append("log:\(text.count):\(text.prefix(12))") }
        var ensureOK = true
        func ensureGateway(done: @escaping @MainActor (Bool) -> Void) {
            calls.append("ensure")
            let ok = ensureOK
            Task { @MainActor in done(ok) }
        }
    }

    /// The page's "gateway.ensure" is answered once the app has checked the gateway.
    @MainActor
    func testEnsureGatewayAnsweredLater() async {
        let h = FakeHost(), b = Bridge(host: h, origin: origin)
        let msg: [String: Any] = ["method": "gateway.ensure"]
        XCTAssertFalse(b.handleAsync(body: msg, sourceOrigin: "http://127.0.0.1:5001", isMainFrame: true) { _ in })
        XCTAssertFalse(b.handleAsync(body: msg, sourceOrigin: origin, isMainFrame: false) { _ in })
        XCTAssertFalse(b.handleAsync(body: ["method": "haptic"], sourceOrigin: origin, isMainFrame: true) { _ in })
        XCTAssertTrue(h.calls.isEmpty)

        let exp1 = expectation(description: "reply")
        var got: Result<[String: Any], BridgeError>?
        XCTAssertTrue(b.handleAsync(body: msg, sourceOrigin: origin, isMainFrame: true) { r in got = r; exp1.fulfill() })
        await fulfillment(of: [exp1], timeout: 5)
        XCTAssertNotNil(try? got?.get())

        h.ensureOK = false
        let exp2 = expectation(description: "failure reply")
        XCTAssertTrue(b.handleAsync(body: msg, sourceOrigin: origin, isMainFrame: true) { r in got = r; exp2.fulfill() })
        await fulfillment(of: [exp2], timeout: 5)
        XCTAssertNil(try? got?.get())
        XCTAssertEqual(h.calls, ["ensure", "ensure"])
    }

    func testClipboardRoundTrip() throws {
        let h = FakeHost(), b = Bridge(host: h, origin: origin)
        let r = try XCTUnwrap(b.handle(body: ["method": "clipboard.read", "args": [:]], sourceOrigin: origin, isMainFrame: true))
        XCTAssertEqual(try r.get()["text"] as? String, "native text")
        _ = b.handle(body: ["method": "clipboard.write", "args": ["text": "from page"]], sourceOrigin: origin, isMainFrame: true)
        XCTAssertEqual(h.calls, ["read", "write:from page"])
    }

    func testRefusesOtherOriginsAndFrames() {
        let h = FakeHost(), b = Bridge(host: h, origin: origin)
        let msg: [String: Any] = ["method": "clipboard.read"]
        XCTAssertNil(b.handle(body: msg, sourceOrigin: "http://127.0.0.1:5001", isMainFrame: true))
        XCTAssertNil(b.handle(body: msg, sourceOrigin: nil, isMainFrame: true))
        XCTAssertNil(b.handle(body: msg, sourceOrigin: origin, isMainFrame: false))
        XCTAssertNil(b.handle(body: "not an object", sourceOrigin: origin, isMainFrame: true))
        XCTAssertTrue(h.calls.isEmpty)
    }

    func testDownloadsMustBeThisGateways() {
        let h = FakeHost(), b = Bridge(host: h, origin: origin)
        let ok = b.handle(body: ["method": "download", "args": ["url": "http://127.0.0.1:5000/tok/api/v1/files/download?t=x", "name": "../a/report.pdf"]],
                          sourceOrigin: origin, isMainFrame: true)
        XCTAssertNotNil(try? ok?.get())
        let bad = b.handle(body: ["method": "download", "args": ["url": "http://example.com/api/v1/files/download", "name": "x"]],
                           sourceOrigin: origin, isMainFrame: true)
        if case .failure(let e)? = bad { XCTAssertEqual(e.message, "not a download of this gateway") } else { XCTFail("accepted a foreign download") }
        XCTAssertEqual(h.calls, ["download:report.pdf"])
    }

    func testStateKeyboardHapticSettings() {
        let h = FakeHost(), b = Bridge(host: h, origin: origin)
        _ = b.handle(body: ["method": "state", "args": ["keepScreenOn": NSNumber(value: true), "fullscreen": false]], sourceOrigin: origin, isMainFrame: true)
        _ = b.handle(body: ["method": "keyboard", "args": ["show": true]], sourceOrigin: origin, isMainFrame: true)
        _ = b.handle(body: ["method": "haptic"], sourceOrigin: origin, isMainFrame: true)
        _ = b.handle(body: ["method": "openSettings"], sourceOrigin: origin, isMainFrame: true)
        XCTAssertEqual(h.calls, ["state:true,false", "keyboard:true", "haptic", "settings"])
    }

    func testSettingsPagesAndDiagnosticLog() {
        let h = FakeHost(), b = Bridge(host: h, origin: origin)
        _ = b.handle(body: ["method": "openSettings", "args": ["page": "privacy"]], sourceOrigin: origin, isMainFrame: true)
        _ = b.handle(body: ["method": "openSettings", "args": ["page": "../../evil"]], sourceOrigin: origin, isMainFrame: true)
        _ = b.handle(body: ["method": "log", "args": ["text": "page error: x"]], sourceOrigin: origin, isMainFrame: true)
        let r = b.handle(body: ["method": "log", "args": ["text": String(repeating: "y", count: 5000)]], sourceOrigin: origin, isMainFrame: true)
        XCTAssertNotNil(try? r?.get())
        XCTAssertEqual(h.calls, ["settings:privacy", "settings", "log:13:page error: ", "log:\(Bridge.maxLog):yyyyyyyyyyyy"])
        XCTAssertEqual(SettingsPage(bridge: "about"), .about)
        XCTAssertNil(SettingsPage(bridge: "pair"))
        XCTAssertNil(SettingsPage(bridge: nil))
    }

    func testBadInput() {
        let h = FakeHost(), b = Bridge(host: h, origin: origin)
        if case .failure(let e)? = b.handle(body: ["method": "exec"], sourceOrigin: origin, isMainFrame: true) {
            XCTAssertEqual(e.message, "unknown method")
        } else { XCTFail("unknown method accepted") }
        let big = String(repeating: "x", count: Bridge.maxClip + 1)
        if case .failure(let e)? = b.handle(body: ["method": "clipboard.write", "args": ["text": big]], sourceOrigin: origin, isMainFrame: true) {
            XCTAssertEqual(e.message, "text too long")
        } else { XCTFail("huge clipboard accepted") }
        XCTAssertTrue(h.calls.isEmpty)
    }

    func testOriginString() {
        XCTAssertEqual(Bridge.originString(protocol: "http", host: "127.0.0.1", port: 5000), origin)
        XCTAssertNil(Bridge.originString(protocol: "https", host: "127.0.0.1", port: 5000))
        XCTAssertNil(Bridge.originString(protocol: "http", host: "example.com", port: 5000))
    }
}
