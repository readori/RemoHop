import XCTest
@testable import RemoHop

final class AppInfoTests: XCTestCase {
    func testLinksFollowTheLanguage() {
        XCTAssertEqual(AppInfo.privacyPolicy("zh").absoluteString, "https://remohop.com/privacy/")
        XCTAssertEqual(AppInfo.privacyPolicy("en").absoluteString, "https://remohop.com/en/privacy/")
        XCTAssertEqual(AppInfo.terms("zh").absoluteString, "https://remohop.com/terms/")
        XCTAssertEqual(AppInfo.terms("en").absoluteString, "https://remohop.com/en/terms/")
        XCTAssertEqual(AppInfo.contactEmail, "info@remohop.com")
        XCTAssertEqual(AppInfo.supportEmail, "support@remohop.com")
        XCTAssertEqual(AppInfo.privacyEmail, "privacy@remohop.com")
        XCTAssertTrue(AppInfo.notice.contains("AGPL-3.0-only"))
    }

    func testVersionAndReportInfo() throws {
        let info: [String: Any] = ["CFBundleShortVersionString": "1.2.3", "CFBundleVersion": "45"]
        XCTAssertEqual(AppInfo.versionLine(info), "1.2.3 (45)")
        XCTAssertEqual(AppInfo.versionLine(nil), "dev (0)")
        let json = AppInfo.reportInfo(info: info, os: "18.1", device: "iPhone15,2", language: "zh")
        let obj = try XCTUnwrap(JSONSerialization.jsonObject(with: Data(json.utf8)) as? [String: String])
        XCTAssertEqual(obj, ["app_version": "1.2.3", "build": "45", "platform": "iOS", "os": "18.1", "device": "iPhone15,2", "language": "zh"])
    }

    func testSizesMailtoAndStatus() {
        XCTAssertEqual(AppInfo.size(0), "0 KB")
        XCTAssertEqual(AppInfo.size(12 * 1024), "12 KB")
        XCTAssertEqual(AppInfo.size(1536 * 1024), "1.5 MB")
        let m = AppInfo.supportMailto(subject: "RemoHop logs", body: "a & b")?.absoluteString ?? ""
        XCTAssertTrue(m.hasPrefix("mailto:support@remohop.com?subject=RemoHop%20logs&body=a%20%26%20b"), m)
        XCTAssertEqual(RendezvousStatus(code: "ok"), .ok)
        XCTAssertEqual(RendezvousStatus(code: "signal_is_host"), .isHost)
        XCTAssertEqual(RendezvousStatus(code: "signal_wrong_protocol"), .wrongProtocol)
        XCTAssertEqual(RendezvousStatus(code: "unreachable"), .unreachable)
        XCTAssertEqual(RendezvousStatus(code: "key_mismatch"), .keyMismatch)
        XCTAssertEqual(RendezvousStatus(code: "boom", message: "x"), .failed("x"))
        XCTAssertFalse(AppInfo.modelIdentifier().isEmpty)
    }

    func testEveryNewStringIsTranslated() {
        for s in L10n.settingsStrings { XCTAssertNotNil(L10n.zh[s], s) }
    }

    /// Saved hosts in the screens' words: paired (or by key) connect without a password, the rest are recent.
    func testSavedHostKinds() {
        let paired = SavedHost(json: ["target": "123456789", "alias": "Office PC", "login": "pairing", "presence": "not_checked",
                                      "last_seen": "2026-11-24T10:00:00Z"])
        XCTAssertEqual(paired.kind, .paired)
        XCTAssertEqual(paired.label, "Office PC")
        XCTAssertTrue(paired.isID)
        XCTAssertEqual(paired.lastSeen?.timeIntervalSince1970, 1795514400)
        XCTAssertTrue(paired.detail.hasPrefix("123456789 · "))
        let recent = SavedHost(json: ["target": "10.0.0.2:21118", "login": "password", "presence": "online",
                                      "last_seen": "2026-11-24T10:00:00.5Z"])
        XCTAssertEqual(recent.kind, .recent)
        XCTAssertEqual(recent.label, "10.0.0.2:21118")
        XCTAssertFalse(recent.isID)
        XCTAssertNotNil(recent.lastSeen)
        XCTAssertEqual(SavedHost(json: ["target": "x:1", "login": "key"]).kind, .key)
        XCTAssertEqual(SavedHost(json: ["target": "x:1", "login": "account"]).kind, .recent)
        XCTAssertNil(SavedHost.date("yesterday"))
    }

    /// A host that does not pair, with its password saved: it connects without one, like a paired host.
    func testSavedPasswordHost() {
        let h = SavedHost(json: ["target": "10.0.0.3:21118", "login": "saved_password", "presence": "not_checked"])
        XCTAssertEqual(h.kind, .savedPassword)
        XCTAssertEqual(h.status, L("Password saved: connects without typing a password"))
        XCTAssertEqual(L10n.zh["Password saved · connects without a password"], "已保存密码 · 免密码连接")
        XCTAssertTrue(h.detail.contains(h.kindSummary))
        XCTAssertNotEqual(h.kindSummary, SavedHost(json: ["target": "x:1", "login": "password"]).kindSummary)
    }

    /// The rendezvous check offers the key the server presented.
    func testRendezvousCheckKey() {
        let ok = RendezvousCheck.parse(#"{"ok":true,"code":"ok","server":"s:21116","key":"AAAA"}"#)
        XCTAssertEqual(ok?.status, .ok)
        XCTAssertEqual(ok?.key, "AAAA")
        let bad = RendezvousCheck.parse(#"{"ok":false,"code":"unreachable","error":"no answer"}"#)
        XCTAssertEqual(bad?.status, .unreachable)
        XCTAssertEqual(bad?.key, "")
        XCTAssertNil(RendezvousCheck.parse("not json"))
    }
}
