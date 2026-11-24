import Foundation

/// Facts about the app for the About, Privacy and Diagnostics screens:
/// versions, links (per language) and the header of a diagnostic report.
/// Foundation only, so it is unit tested without UIKit.
enum AppInfo {
    static let supportEmail = "admin@remohop.com"
    static let telegram = URL(string: "https://t.me/remohop")!
    static let website = URL(string: "https://remohop.com")!
    static let sourceCode = URL(string: "https://github.com/readori/RemoHop")!
    static let licensing = URL(string: "https://github.com/readori/RemoHop/blob/main/LICENSING.md")!
    static let thirdParty = URL(string: "https://github.com/readori/RemoHop/blob/main/packaging/third_party_licenses.sh")!

    /// The privacy policy in the app's language ("zh" or "en").
    static func privacyPolicy(_ lang: String) -> URL {
        URL(string: lang == "zh" ? "https://remohop.com/privacy/" : "https://remohop.com/en/privacy/")!
    }

    /// The terms of use in the app's language.
    static func terms(_ lang: String) -> URL {
        URL(string: lang == "zh" ? "https://remohop.com/terms/" : "https://remohop.com/en/terms/")!
    }

    /// "1.2.0" and "34" from an Info.plist dictionary.
    static func version(_ info: [String: Any]?) -> (version: String, build: String) {
        (info?["CFBundleShortVersionString"] as? String ?? "dev", info?["CFBundleVersion"] as? String ?? "0")
    }

    /// "1.2.0 (34)".
    static func versionLine(_ info: [String: Any]?) -> String {
        let v = version(info)
        return "\(v.version) (\(v.build))"
    }

    /// The app's part of a diagnostic report, as the JSON object the core's
    /// DiagnosticsExport takes. No secrets: versions, OS, model, language.
    static func reportInfo(info: [String: Any]?, os: String, device: String, language: String) -> String {
        let v = version(info)
        let obj: [String: String] = ["app_version": v.version, "build": v.build, "platform": "iOS", "os": os,
                                     "device": device, "language": language]
        guard let data = try? JSONSerialization.data(withJSONObject: obj, options: [.sortedKeys]) else { return "{}" }
        return String(decoding: data, as: UTF8.self)
    }

    /// The hardware model identifier, e.g. "iPhone15,2" (the simulator
    /// reports the model it simulates).
    static func modelIdentifier() -> String {
        if let sim = ProcessInfo.processInfo.environment["SIMULATOR_MODEL_IDENTIFIER"] { return sim }
        var u = utsname()
        uname(&u)
        return withUnsafeBytes(of: &u.machine) { raw in
            String(decoding: raw.prefix(while: { $0 != 0 }), as: UTF8.self)
        }
    }

    /// A human size: "0 KB", "12 KB", "1.4 MB".
    static func size(_ bytes: Int64) -> String {
        if bytes < 1024 * 1024 { return "\((bytes + 1023) / 1024) KB" }
        return String(format: "%.1f MB", Double(bytes) / 1024 / 1024)
    }

    /// The mailto: fallback when Mail cannot compose with an attachment.
    static func supportMailto(subject: String, body: String) -> URL? {
        // Query values with "&", "=", "+" and "?" escaped too (URLComponents leaves them).
        var allowed = CharacterSet.urlQueryAllowed
        allowed.remove(charactersIn: "&=+?")
        func enc(_ s: String) -> String { s.addingPercentEncoding(withAllowedCharacters: allowed) ?? "" }
        return URL(string: "mailto:\(supportEmail)?subject=\(enc(subject))&body=\(enc(body))")
    }

    /// The project's NOTICE (kept with the app as its attribution).
    static let notice = """
    RemoHop
    Copyright (C) 2026 Readori
    https://github.com/readori/RemoHop

    This product is RemoHop, or is based on RemoHop, a peer-to-peer remote control \
    project by Readori, licensed under the GNU Affero General Public License, \
    version 3 (AGPL-3.0-only), with the additional terms in LICENSING.md \
    ("Additional terms under section 7").

    本产品是 RemoHop 或基于 RemoHop 开发。RemoHop 是 Readori 的点对点远程控制\
    开源项目，按 GNU Affero 通用公共许可证第 3 版（AGPL-3.0-only）授权，并附有 \
    LICENSING.md 中“依据第 7 条的附加条款”。

    Works based on RemoHop must keep this notice. See LICENSING.md.
    基于 RemoHop 的作品必须保留本声明，详见 LICENSING.md。
    """
}

/// What a rendezvous server check found. The core's check (from the
/// connect-fix branch) reports one of these codes; `RendezvousCheck.run`
/// is the one place that calls it.
enum RendezvousStatus: Equatable {
    case ok
    case wrongProtocol // the address answers, but it is a host, not a rendezvous server
    case unreachable
    case keyMismatch
    case failed(String)

    /// From the core's result code ("ok", "wrong_protocol", "unreachable",
    /// "key_mismatch"; anything else is a failure with that text).
    init(code: String, message: String = "") {
        switch code {
        case "ok": self = .ok
        case "wrong_protocol", "signal_wrong_protocol", "signal_is_host", "is_host": self = .wrongProtocol
        case "unreachable", "rendezvous_unreachable", "timeout": self = .unreachable
        case "key_mismatch", "pin_mismatch": self = .keyMismatch
        default: self = .failed(message.isEmpty ? code : message)
        }
    }
}
