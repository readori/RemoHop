import Foundation

/// What the page may ask of the app; implemented by the web view's coordinator.
protocol BridgeHost: AnyObject {
    func clipboardRead() -> String
    func clipboardWrite(_ text: String)
    /// Starts saving a gateway download; problems are reported to the user by the host.
    func download(url: URL, name: String)
    func setState(keepScreenOn: Bool, fullscreen: Bool)
    func keyboard(show: Bool)
    func haptic()
    func openSettings()
    /// Opens Settings on one of its pages ("privacy", "about", ...).
    func openSettings(page: String)
    /// Something the page reported for the diagnostic log (kept only while the user has it on).
    func diagLog(_ text: String)
    /// Makes sure the gateway answers (see AppModel.ensureGateway); `done`
    /// gets whether it does. If its URL changed the page is loaded again.
    func ensureGateway(done: @escaping @MainActor (Bool) -> Void)
}

extension BridgeHost {
    func openSettings(page: String) { openSettings() }
    func diagLog(_ text: String) {}
    func ensureGateway(done: @escaping @MainActor (Bool) -> Void) { Task { @MainActor in done(true) } }
}

struct BridgeError: Error, Equatable {
    let message: String
}

/// The app side of remohop-mobile.js's bridge: the "remohop" script message
/// handler (with reply). WebKit exposes the handler to every page in the web
/// view, so `handle` checks that the message comes from the gateway's origin
/// and from the main frame before doing anything. A message body is
/// {"method": "...", "args": {...}}; the reply is an object, or an error text.
final class Bridge {
    /// Clipboard text larger than this is refused.
    static let maxClip = 1 << 20
    /// A page's diagnostic message is cut to this many characters.
    static let maxLog = 2000
    /// Settings pages the page may open directly.
    static let settingsPages: Set<String> = ["privacy", "about", "diagnostics", "rendezvous"]

    let origin: String
    private weak var host: BridgeHost?

    init(host: BridgeHost, origin: String) {
        self.host = host
        self.origin = origin
    }

    /// Methods answered later rather than by `handle`:
    ///
    /// - "gateway.ensure": the page could not reach the gateway (remohop-mobile.js
    ///   ensureGateway); the app checks it, reopens its listener if iOS took
    ///   it away, and answers once the gateway answers again ({} or the error
    ///   "gateway unavailable"). If the gateway's URL changed the app loads
    ///   the page again instead.
    ///
    /// Returns false when the message is not one of these (or not for us);
    /// then `handle` gets it.
    func handleAsync(body: Any, sourceOrigin: String?, isMainFrame: Bool,
                     reply: @escaping @MainActor (Result<[String: Any], BridgeError>) -> Void) -> Bool {
        guard isMainFrame, sourceOrigin == origin, let msg = body as? [String: Any],
              msg["method"] as? String == "gateway.ensure", let host else { return false }
        host.ensureGateway { ok in reply(ok ? .success([:]) : .failure(BridgeError(message: "gateway unavailable"))) }
        return true
    }

    /// Returns nil when the message is not for us (wrong origin or frame, or
    /// malformed) and must be ignored.
    func handle(body: Any, sourceOrigin: String?, isMainFrame: Bool) -> Result<[String: Any], BridgeError>? {
        guard isMainFrame, sourceOrigin == origin, let msg = body as? [String: Any],
              let method = msg["method"] as? String, let host else { return nil }
        let args = msg["args"] as? [String: Any] ?? [:]
        switch method {
        case "clipboard.read":
            return .success(["text": host.clipboardRead()])
        case "clipboard.write":
            let text = args["text"] as? String ?? ""
            if text.utf16.count > Self.maxClip { return .failure(BridgeError(message: "text too long")) }
            host.clipboardWrite(text)
        case "download":
            let url = args["url"] as? String ?? ""
            guard GatewayURLs.isDownload(url, origin), let u = URL(string: url) else {
                return .failure(BridgeError(message: "not a download of this gateway"))
            }
            host.download(url: u, name: GatewayURLs.safeFileName(args["name"] as? String))
        case "state":
            host.setState(keepScreenOn: Self.bool(args["keepScreenOn"]), fullscreen: Self.bool(args["fullscreen"]))
        case "keyboard":
            host.keyboard(show: Self.bool(args["show"]))
        case "haptic":
            host.haptic()
        case "openSettings":
            if let page = args["page"] as? String, Self.settingsPages.contains(page) { host.openSettings(page: page) } else { host.openSettings() }
        case "log":
            host.diagLog(String((args["text"] as? String ?? "").prefix(Self.maxLog)))
        case "ready":
            break
        default:
            return .failure(BridgeError(message: "unknown method"))
        }
        return .success([:])
    }

    /// WebKit hands JavaScript booleans over as NSNumber.
    private static func bool(_ v: Any?) -> Bool {
        if let b = v as? Bool { return b }
        if let n = v as? NSNumber { return n.boolValue }
        return false
    }

    /// "http://127.0.0.1:PORT" for a frame's security origin parts.
    static func originString(protocol proto: String, host: String, port: Int) -> String? {
        GatewayURLs.origin("\(proto)://\(host):\(port)/")
    }
}
