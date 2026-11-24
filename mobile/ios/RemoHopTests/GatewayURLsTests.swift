import XCTest
@testable import RemoHop

final class GatewayURLsTests: XCTestCase {
    let o = "http://127.0.0.1:41234"

    func testOriginDropsTheToken() {
        XCTAssertEqual(GatewayURLs.origin("http://127.0.0.1:41234/AbCdEf0123456789xyzQ/"), o)
        XCTAssertEqual(GatewayURLs.origin("http://127.0.0.1:41234/tok/api/v1/files/download?t=x"), o)
    }

    func testOriginRefusesAnythingButLoopbackHTTP() {
        for bad in ["https://127.0.0.1:41234/tok/", "http://localhost:41234/tok/", "http://127.0.0.2:41234/tok/",
                    "http://127.0.0.1/tok/", "http://user@127.0.0.1:41234/tok/", "http://evil.example/http://127.0.0.1:41234/",
                    "javascript:alert(1)", "not a url at all", ""] {
            XCTAssertNil(GatewayURLs.origin(bad), bad)
        }
    }

    func testNavigationStaysOnTheGateway() {
        XCTAssertTrue(GatewayURLs.sameOrigin("http://127.0.0.1:41234/tok/#x", o))
        XCTAssertFalse(GatewayURLs.sameOrigin("http://127.0.0.1:41235/tok/", o))
        XCTAssertFalse(GatewayURLs.sameOrigin("https://example.com/", o))
    }

    func testLanguageGoesInTheQuery() {
        let u = URL(string: "http://127.0.0.1:41234/AbCdEf0123456789xyzQ/")!
        XCTAssertEqual(GatewayURLs.withLang(u, "zh").absoluteString, "http://127.0.0.1:41234/AbCdEf0123456789xyzQ/?lang=zh")
        XCTAssertEqual(GatewayURLs.origin(GatewayURLs.withLang(u, "en").absoluteString), o)
        let v = URL(string: "http://127.0.0.1:41234/tok/?lang=zh&x=1#f")!
        XCTAssertEqual(GatewayURLs.withLang(v, "en").absoluteString, "http://127.0.0.1:41234/tok/?x=1&lang=en#f")
        XCTAssertEqual(GatewayURLs.withLang(u, "fr"), u)
    }

    func testLanguageChoice() {
        XCTAssertEqual(L10n.resolve(.system, preferred: ["zh-Hans-CN", "en"]), "zh")
        XCTAssertEqual(L10n.resolve(.system, preferred: ["zh-Hant-TW"]), "zh")
        XCTAssertEqual(L10n.resolve(.system, preferred: ["de-DE", "zh-Hans"]), "en")
        XCTAssertEqual(L10n.resolve(.system, preferred: []), "en")
        XCTAssertEqual(L10n.resolve(.zh, preferred: ["en"]), "zh")
        XCTAssertEqual(L10n.resolve(.en, preferred: ["zh-Hans"]), "en")
        XCTAssertEqual(L10n.text("Settings", lang: "zh"), "设置")
        XCTAssertEqual(L10n.text("Settings", lang: "en"), "Settings")
        for (code, english) in L10n.pairErrors {
            XCTAssertNotNil(L10n.zh[english], "no Chinese for the pairing error \(code)")
        }
    }

    func testDownloads() {
        XCTAssertTrue(GatewayURLs.isDownload("http://127.0.0.1:41234/tok/api/v1/files/download?t=a&c=b&path=%2Fx", o))
        XCTAssertFalse(GatewayURLs.isDownload("http://127.0.0.1:41234/tok/api/v1/files/list?c=b", o))
        XCTAssertFalse(GatewayURLs.isDownload("http://127.0.0.1:9/tok/api/v1/files/download?t=a", o))
        XCTAssertFalse(GatewayURLs.isDownload("http://example.com/api/v1/files/download", o))
    }

    func testSafeFileNames() {
        XCTAssertEqual(GatewayURLs.safeFileName("/home/u/report.pdf"), "report.pdf")
        XCTAssertEqual(GatewayURLs.safeFileName("C:\\Users\\a\\b.txt"), "b.txt")
        XCTAssertEqual(GatewayURLs.safeFileName("../../etc/passwd"), "passwd")
        XCTAssertEqual(GatewayURLs.safeFileName(".bashrc"), "bashrc")
        XCTAssertEqual(GatewayURLs.safeFileName("a\u{0}\nb"), "ab")
        XCTAssertEqual(GatewayURLs.safeFileName(""), "download")
        XCTAssertEqual(GatewayURLs.safeFileName(nil), "download")
        XCTAssertEqual(GatewayURLs.safeFileName("..."), "download")
        let long = GatewayURLs.safeFileName(String(repeating: "x", count: 300) + ".tar.gz")
        XCTAssertEqual(long.count, 120)
        XCTAssertTrue(long.hasSuffix(".gz"))
    }
}
