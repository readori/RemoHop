import XCTest
@testable import RemoHop

final class AppInfoTests: XCTestCase {
    func testLinksFollowTheLanguage() {
        XCTAssertEqual(AppInfo.privacyPolicy("zh").absoluteString, "https://remohop.com/privacy/")
        XCTAssertEqual(AppInfo.privacyPolicy("en").absoluteString, "https://remohop.com/en/privacy/")
        XCTAssertEqual(AppInfo.terms("zh").absoluteString, "https://remohop.com/terms/")
        XCTAssertEqual(AppInfo.terms("en").absoluteString, "https://remohop.com/en/terms/")
        XCTAssertEqual(AppInfo.supportEmail, "info@remohop.com")
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
        XCTAssertTrue(m.hasPrefix("mailto:info@remohop.com?subject=RemoHop%20logs&body=a%20%26%20b"), m)
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
}
