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

    let origin: String
    private weak var host: BridgeHost?

    init(host: BridgeHost, origin: String) {
        self.host = host
        self.origin = origin
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
            host.openSettings()
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
