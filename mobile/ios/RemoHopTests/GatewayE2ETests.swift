import UIKit
import WebKit
import XCTest
@testable import RemoHop

/// Keeps tokens out of anything that may end up in a log.
private func redact(_ s: String) -> String {
    s.replacingOccurrences(of: #"(127\.0\.0\.1:\d+/)[A-Za-z0-9_-]{16,}"#, with: "$1<token>", options: .regularExpression)
}

/// A JavaScript string literal for `s`.
private func lit(_ s: String) -> String {
    let data = (try? JSONSerialization.data(withJSONObject: [s])) ?? Data("[\"\"]".utf8)
    let arr = String(decoding: data, as: UTF8.self)
    return String(arr.dropFirst().dropLast())
}

/// End to end, inside the running app: the test bundle is hosted by
/// RemoHop.app, so the app has started its in-process gateway and shows the
/// gateway page in its WKWebView before the first test runs. The test drives
/// that page the way a user would (the same DOM ids as
/// mobile/shared/test/phone_test.mjs): it connects to a `remohop host` with its
/// direct address and password, runs a command in the terminal and lists a
/// directory in the Files tab.
///
/// It needs a host to talk to, so it is skipped unless one is named:
///
///     TEST_RUNNER_REMOHOP_E2E_TARGET=127.0.0.1:PORT \
///     TEST_RUNNER_REMOHOP_E2E_PASSWORD=... \
///     TEST_RUNNER_REMOHOP_E2E_DIR=/dir/with/hello.txt \
///     xcodebuild test -only-testing:RemoHopTests/GatewayE2ETests ...
///
/// (xcodebuild hands TEST_RUNNER_* variables to the tests without the
/// prefix.) On the simulator the same three values may instead be in the
/// JSON file /tmp/remohop-ios-e2e.json as {"target","password","dir"}: the
/// simulator shares the Mac's file system and its network, which is also
/// why 127.0.0.1 reaches a host running on the Mac.
///
/// Nothing here prints the gateway URL (it carries the access token) or the
/// password.
@MainActor
final class GatewayE2ETests: XCTestCase {
    private struct Failure: Error, CustomStringConvertible {
        let description: String
    }

    private var web: WKWebView!

    private struct Settings {
        var target = ""
        var password = ""
        var dir = ""
    }

    private func settings() -> Settings {
        var s = Settings()
        let env = ProcessInfo.processInfo.environment
        s.target = env["REMOHOP_E2E_TARGET"] ?? ""
        s.password = env["REMOHOP_E2E_PASSWORD"] ?? ""
        s.dir = env["REMOHOP_E2E_DIR"] ?? ""
        #if targetEnvironment(simulator)
        if s.target.isEmpty, let data = FileManager.default.contents(atPath: "/tmp/remohop-ios-e2e.json"),
           let obj = try? JSONSerialization.jsonObject(with: data) as? [String: String] {
            s.target = obj["target"] ?? ""
            s.password = obj["password"] ?? ""
            s.dir = obj["dir"] ?? ""
        }
        #endif
        return s
    }

    private func findWebView(in view: UIView) -> WKWebView? {
        if let w = view as? WKWebView { return w }
        for sub in view.subviews {
            if let w = findWebView(in: sub) { return w }
        }
        return nil
    }

    private func appWebView() -> WKWebView? {
        for scene in UIApplication.shared.connectedScenes {
            guard let windows = (scene as? UIWindowScene)?.windows else { continue }
            for window in windows {
                if let w = findWebView(in: window) { return w }
            }
        }
        return nil
    }

    /// Runs `body` (the body of a function, with `return`) in the page and
    /// returns the result as a string.
    private func js(_ body: String) async throws -> String {
        let source = "String((() => { \(body) })())"
        return try await withCheckedThrowingContinuation { (cont: CheckedContinuation<String, Error>) in
            web.evaluateJavaScript(source) { value, error in
                if let error {
                    let ns = error as NSError
                    let msg = (ns.userInfo["WKJavaScriptExceptionMessage"] as? String) ?? ns.localizedDescription
                    cont.resume(throwing: Failure(description: "JavaScript failed: " + redact(msg)))
                } else {
                    cont.resume(returning: (value as? String) ?? "")
                }
            }
        }
    }

    private func pause(_ seconds: Double) async {
        try? await Task.sleep(nanoseconds: UInt64(seconds * 1_000_000_000))
    }

    /// Polls a JavaScript condition; false when it did not come true in time.
    private func eventually(_ condition: String, timeout: Double = 30) async -> Bool {
        let end = Date().addingTimeInterval(timeout)
        while Date() < end {
            if (try? await js("return !!(\(condition))")) == "true" { return true }
            await pause(0.25)
        }
        return false
    }

    private func require(_ what: String, _ condition: String, timeout: Double = 30, detail: String? = nil) async throws {
        if await eventually(condition, timeout: timeout) { return }
        var message = "timed out waiting for " + what
        if let detail, let d = try? await js(detail), !d.isEmpty {
            message += ": " + redact(String(d.prefix(300)))
        }
        throw Failure(description: message)
    }

    /// How many clearly different colours a snapshot of the web view has:
    /// a page that rendered has text and controls, a blank view has one.
    private func snapshotColours() async throws -> Int {
        let image: UIImage = try await withCheckedThrowingContinuation { cont in
            web.takeSnapshot(with: nil) { image, error in
                if let image {
                    cont.resume(returning: image)
                } else {
                    cont.resume(throwing: Failure(description: "snapshot failed: " + (error?.localizedDescription ?? "no image")))
                }
            }
        }
        guard let cg = image.cgImage else { throw Failure(description: "snapshot has no bitmap") }
        let n = 64
        var pixels = [UInt8](repeating: 0, count: n * n * 4)
        let drawn: Bool = pixels.withUnsafeMutableBytes { buf in
            guard let ctx = CGContext(data: buf.baseAddress, width: n, height: n, bitsPerComponent: 8, bytesPerRow: n * 4,
                                      space: CGColorSpaceCreateDeviceRGB(), bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue) else { return false }
            ctx.draw(cg, in: CGRect(x: 0, y: 0, width: n, height: n))
            return true
        }
        guard drawn else { throw Failure(description: "could not read the snapshot") }
        var seen = Set<UInt32>()
        for i in stride(from: 0, to: pixels.count, by: 4) {
            // 4 bits per channel: ignore antialiasing noise.
            seen.insert(UInt32(pixels[i] >> 4) << 8 | UInt32(pixels[i + 1] >> 4) << 4 | UInt32(pixels[i + 2] >> 4))
        }
        return seen.count
    }

    func testConnectTerminalFiles() async throws {
        let cfg = settings()
        guard !cfg.target.isEmpty, !cfg.password.isEmpty else {
            throw XCTSkip("no host to connect to: set TEST_RUNNER_REMOHOP_E2E_TARGET and TEST_RUNNER_REMOHOP_E2E_PASSWORD")
        }

        // The app is up: its gateway runs and the web view is on screen.
        var found: WKWebView?
        for _ in 0..<240 {
            found = appWebView()
            if found != nil { break }
            await pause(0.25)
        }
        web = try XCTUnwrap(found, "the app never showed its web view (the gateway did not start)")
        XCTAssertTrue(Core.gatewayRunning, "gateway not running")
        XCTAssertNotNil(web.window, "the web view is not in a window")

        try await require("the connect form", "document.getElementById('connect') && !document.getElementById('connect').hidden", timeout: 60)
        try await require("the injected script", "typeof window.remohopMobile === 'object'")
        try await require("the app's settings button", "document.getElementById('sdm-settings')")
        let bridge = try await js("return window.webkit && window.webkit.messageHandlers && window.webkit.messageHandlers.remohop ? 'bridge' : 'none'")
        XCTAssertEqual(bridge, "bridge", "the native bridge is not exposed to the page")

        // Connect with the host's direct address and its password.
        _ = try await js("""
            document.getElementById('target').value = \(lit(cfg.target));
            document.getElementById('hostpass').value = \(lit(cfg.password));
            document.getElementById('connectbtn').click();
            return 'ok';
            """)
        try await require("the session to the host", "!document.getElementById('workspace').hidden", timeout: 90,
                          detail: "return document.getElementById('connerr').textContent || document.getElementById('connstatus').textContent")

        // Terminal: a command typed into the terminal's input runs on the host.
        _ = try await js("document.getElementById('tab-terminal').click(); return 'ok';")
        try await require("the terminal", "document.querySelector('#t-host textarea')", detail: "return document.getElementById('t-err').textContent")
        _ = await eventually("document.getElementById('t-host').textContent.trim() !== ''", timeout: 10)
        await pause(0.5)
        var ran = false
        for _ in 0..<3 where !ran {
            _ = try await js("""
                const t = document.querySelector('#t-host textarea');
                t.focus();
                const type = v => { t.value = v; t.dispatchEvent(new InputEvent('input', {inputType: 'insertText', data: v, bubbles: true})); };
                type('echo remohop-$((6*7))');
                type('\\n');
                return 'ok';
                """)
            ran = await eventually("document.getElementById('t-host').textContent.includes('remohop-42')", timeout: 10)
        }
        if !ran {
            let err = (try? await js("return document.getElementById('t-err').textContent + ' ' + document.getElementById('t-status').textContent")) ?? ""
            throw Failure(description: "the terminal never showed the command's output: " + redact(String(err.prefix(300))))
        }
        let fits = try await js("return document.documentElement.scrollWidth <= innerWidth ? 'fits' : 'overflows'")
        XCTAssertEqual(fits, "fits", "the terminal tab does not fit the phone's width")
        let colours = try await snapshotColours()
        XCTAssertGreaterThan(colours, 3, "the web view rendered nothing (terminal tab)")

        // Files: the tab lists a directory of the host.
        _ = try await js("document.getElementById('tab-files').click(); return 'ok';")
        try await require("the Files tab's first listing", "document.getElementById('f-path').value !== ''",
                          detail: "return document.getElementById('f-err').textContent")
        if !cfg.dir.isEmpty {
            _ = try await js("document.getElementById('f-path').value = \(lit(cfg.dir)); document.getElementById('f-go').click(); return 'ok';")
            try await require("hello.txt in the host directory", "document.getElementById('f-body').textContent.includes('hello.txt')",
                              detail: "return document.getElementById('f-err').textContent")
        } else {
            try await require("a directory listing", "document.getElementById('f-body').children.length > 0 || !document.getElementById('f-empty').hidden")
        }
        let panel = try await js("return document.getElementById('panel-files').hidden ? 'hidden' : 'shown'")
        XCTAssertEqual(panel, "shown", "the Files panel is not showing")

        // Back to the connect screen, the way the app's back handling does
        // (each call undoes one thing: a dialog, the keyboard, the workspace).
        var left = false
        for _ in 0..<4 where !left {
            _ = try await js("return remohopMobile.back()")
            left = await eventually("!document.getElementById('connect').hidden", timeout: 3)
        }
        XCTAssertTrue(left, "back never returned to the connect screen")
    }

    /// The release-blocking bug: after a trip to another app (to copy the
    /// password) the gateway's listening socket was gone, the page showed
    /// "Load failed" and nothing left the phone. The simulator never
    /// suspends the app, so the socket is broken with the core's test hook,
    /// exactly as iOS leaves it (connections refused, Go's accept loop none
    /// the wiser). Then: the page's own recovery (it asks the app through
    /// the bridge, the app reopens the listener on the same port, the page
    /// retries) must make the very next Connect work; and the app's
    /// background/foreground path (pause, then ensure) must keep the URL.
    func testConnectAfterListenerLoss() async throws {
        let cfg = settings()
        guard !cfg.target.isEmpty, !cfg.password.isEmpty else {
            throw XCTSkip("no host to connect to: set TEST_RUNNER_REMOHOP_E2E_TARGET and TEST_RUNNER_REMOHOP_E2E_PASSWORD")
        }
        var found: WKWebView?
        for _ in 0..<240 {
            found = appWebView()
            if found != nil { break }
            await pause(0.25)
        }
        web = try XCTUnwrap(found, "the app never showed its web view (the gateway did not start)")
        try await require("the connect form", "document.getElementById('connect') && !document.getElementById('connect').hidden", timeout: 60)
        try await require("the injected script", "typeof window.remohopMobile === 'object'")
        let before = try await Task.detached { try Core.gatewayURL() }.value

        // 1. The socket dies under the running gateway; the user taps Connect.
        Core.breakListenerForTest(stall: true)
        XCTAssertTrue(Core.gatewayRunning, "the stalled listener should be invisible without I/O (that was the bug)")
        _ = try await js("""
            document.getElementById('target').value = \(lit(cfg.target));
            document.getElementById('hostpass').value = \(lit(cfg.password));
            document.getElementById('connectbtn').click();
            return 'ok';
            """)
        try await require("the session to the host after the listener was lost", "!document.getElementById('workspace').hidden", timeout: 90,
                          detail: "return document.getElementById('connerr').textContent || document.getElementById('connstatus').textContent")
        let after = try await Task.detached { try Core.gatewayURL() }.value
        XCTAssertEqual(after, before, "the gateway moved to another URL (the page would have been reloaded)")
        let errText = try await js("return document.getElementById('connerr').textContent")
        XCTAssertFalse(errText.contains("Load failed"), "the raw WebKit error reached the page")

        // 2. Background and foreground, as RemoHopApp does on scene phases.
        _ = try await js("document.getElementById('disconnect').click(); return 'ok';")
        try await require("the connect form after disconnecting", "!document.getElementById('connect').hidden", timeout: 15)
        await Task.detached { Core.pauseGateway() }.value
        XCTAssertFalse(Core.gatewayRunning, "the listener is still open after pausing")
        let resumed = try await Task.detached { try Core.gatewayURL() }.value
        XCTAssertEqual(resumed, before, "the gateway did not reopen on the same port and token")
        XCTAssertTrue(Core.gatewayRunning)
        _ = try await js("""
            document.getElementById('target').value = \(lit(cfg.target));
            document.getElementById('hostpass').value = \(lit(cfg.password));
            document.getElementById('connectbtn').click();
            return 'ok';
            """)
        try await require("the session to the host after background/foreground", "!document.getElementById('workspace').hidden", timeout: 90,
                          detail: "return document.getElementById('connerr').textContent || document.getElementById('connstatus').textContent")
        _ = try await js("document.getElementById('disconnect').click(); return 'ok';")
        try await require("the connect form at the end", "!document.getElementById('connect').hidden", timeout: 15)
    }

    // MARK: App Store screenshots

    /// Tells the CI script that the screen is ready to be captured and waits until it did.
    /// The simulator shares the Mac's file system, so marker files are the whole protocol:
    /// `<name>.ready` is written here, `<name>.done` is written by the script after
    /// `xcrun simctl io <udid> screenshot`.
    private func shot(_ dir: String, _ name: String) async throws {
        await pause(1.0) // let the last layout and animation settle
        FileManager.default.createFile(atPath: dir + "/" + name + ".ready", contents: Data())
        for _ in 0..<240 {
            if FileManager.default.fileExists(atPath: dir + "/" + name + ".done") { return }
            await pause(0.25)
        }
        throw Failure(description: "screenshot " + name + " was not taken in time")
    }

    /// Walks the real app through the same flow as `testConnectTerminalFiles` and stops at three
    /// screens for the CI `screenshots` mode. Skipped unless TEST_RUNNER_REMOHOP_SHOTS_DIR names the
    /// directory for the marker files (and a host is named as for the end-to-end test).
    func testScreenshots() async throws {
        guard let dir = ProcessInfo.processInfo.environment["REMOHOP_SHOTS_DIR"], !dir.isEmpty else {
            throw XCTSkip("not taking screenshots: REMOHOP_SHOTS_DIR is not set")
        }
        let cfg = settings()
        guard !cfg.target.isEmpty, !cfg.password.isEmpty else {
            throw XCTSkip("no host to connect to")
        }
        var found: WKWebView?
        for _ in 0..<240 {
            found = appWebView()
            if found != nil { break }
            await pause(0.25)
        }
        web = try XCTUnwrap(found, "the app never showed its web view")
        // REMOHOP_SHOTS_LANG ("zh" or "en") is the simulator's system language,
        // set by CI before the run; the app follows the system, as it does by
        // default, so the store's zh-Hans page gets Chinese screens.
        let lang = ProcessInfo.processInfo.environment["REMOHOP_SHOTS_LANG"] ?? "en"
        UserDefaults.standard.set(AppLanguage.system.rawValue, forKey: L10n.key)
        try await require("the connect form", "document.getElementById('connect') && !document.getElementById('connect').hidden", timeout: 60)
        try await require("the injected script", "typeof window.remohopMobile === 'object'")
        try await require("the page in the chosen language", "document.documentElement.lang.indexOf(\(lit(lang == "zh" ? "zh" : "en"))) === 0", timeout: 15)
        await pause(0.5)
        try await shot(dir, "1-connect")

        _ = try await js("""
            document.getElementById('target').value = \(lit(cfg.target));
            document.getElementById('hostpass').value = \(lit(cfg.password));
            document.getElementById('connectbtn').click();
            return 'ok';
            """)
        try await require("the session to the host", "!document.getElementById('workspace').hidden", timeout: 90,
                          detail: "return document.getElementById('connerr').textContent || document.getElementById('connstatus').textContent")

        _ = try await js("document.getElementById('tab-terminal').click(); return 'ok';")
        try await require("the terminal", "document.querySelector('#t-host textarea')", detail: "return document.getElementById('t-err').textContent")
        _ = await eventually("document.getElementById('t-host').textContent.trim() !== ''", timeout: 10)
        await pause(0.5)
        let command = cfg.dir.isEmpty ? "uname -a" : "cd \(cfg.dir) && ls -l"
        var ran = false
        for _ in 0..<3 where !ran {
            _ = try await js("""
                const t = document.querySelector('#t-host textarea');
                t.focus();
                const type = v => { t.value = v; t.dispatchEvent(new InputEvent('input', {inputType: 'insertText', data: v, bubbles: true})); };
                type(\(lit(command)));
                type('\\n');
                return 'ok';
                """)
            ran = await eventually("document.getElementById('t-host').textContent.length > 40", timeout: 10)
        }
        try await shot(dir, "2-terminal")

        _ = try await js("document.getElementById('tab-files').click(); return 'ok';")
        try await require("the Files tab's first listing", "document.getElementById('f-path').value !== ''",
                          detail: "return document.getElementById('f-err').textContent")
        if !cfg.dir.isEmpty {
            _ = try await js("document.getElementById('f-path').value = \(lit(cfg.dir)); document.getElementById('f-go').click(); return 'ok';")
            try await require("the host directory listing", "document.getElementById('f-body').textContent.includes('hello.txt')",
                              detail: "return document.getElementById('f-err').textContent")
        }
        try await shot(dir, "3-files")
    }
}
