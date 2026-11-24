import Foundation

/// Pure helpers about the gateway's loopback URL (the same rules as the
/// Android app's GatewayUrls.kt).
///
/// The full URL contains the gateway's access token. Only `origin` (scheme,
/// host and port, no token) is kept outside the web view's initial load.
enum GatewayURLs {
    /// "http://127.0.0.1:PORT" for a gateway URL, or nil if it is not one.
    static func origin(_ url: String) -> String? {
        guard let c = URLComponents(string: url), c.scheme == "http", c.host == "127.0.0.1",
              let port = c.port, (1...65535).contains(port), c.user == nil, c.password == nil else { return nil }
        return "http://127.0.0.1:\(port)"
    }

    /// Whether the web view may navigate to `url`: only the gateway's own origin.
    static func sameOrigin(_ url: String, _ origin: String) -> Bool {
        Self.origin(url) == origin
    }

    /// Whether `url` is a file download of this gateway, which the page hands
    /// to the app instead of the web view fetching it.
    static func isDownload(_ url: String, _ origin: String) -> Bool {
        guard sameOrigin(url, origin), let c = URLComponents(string: url) else { return false }
        return c.percentEncodedPath.hasSuffix("/api/v1/files/download") && c.fragment == nil
    }

    /// `url` with the page's language ("zh" or "en") in its lang query
    /// parameter, replacing one already there. The path, which holds the
    /// token, is untouched. Any other `lang` leaves `url` as it is.
    static func withLang(_ url: URL, _ lang: String) -> URL {
        guard lang == "zh" || lang == "en", var c = URLComponents(url: url, resolvingAgainstBaseURL: false) else { return url }
        var items = (c.queryItems ?? []).filter { $0.name != "lang" }
        items.append(URLQueryItem(name: "lang", value: lang))
        c.queryItems = items
        return c.url ?? url
    }

    /// A file name safe to offer in the share sheet and to use for a
    /// temporary file: the last path element, no control or reserved
    /// characters, not hidden, at most 120 characters.
    static func safeFileName(_ name: String?) -> String {
        var n = (name ?? "").replacingOccurrences(of: "\\", with: "/")
        if let slash = n.lastIndex(of: "/") { n = String(n[n.index(after: slash)...]) }
        let reserved = Set("<>:\"|?*")
        n = String(n.unicodeScalars.filter { $0.value >= 0x20 && $0.value != 0x7f && !reserved.contains(Character($0)) }.map(Character.init))
        n = n.trimmingCharacters(in: .whitespaces)
        while n.hasPrefix(".") { n.removeFirst() }
        if n.count > 120 {
            var ext = ""
            if let dot = n.lastIndex(of: "."), dot > n.startIndex, n.distance(from: dot, to: n.endIndex) <= 10 { ext = String(n[dot...]) }
            n = String(n.prefix(120 - ext.count)) + ext
        }
        return n.isEmpty ? "download" : n
    }
}
