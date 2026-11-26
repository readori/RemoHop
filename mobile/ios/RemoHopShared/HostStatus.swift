import Foundation

/// What the Go core's host mode reports (HostStateJSON), as the extension uses it.
struct HostStatus: Equatable {
    struct Viewer: Equatable {
        var id: Int
        var label: String
        var from: String
    }

    /// off, starting, ready, no_server or error.
    var phase = "off"
    var error = ""
    /// Formatted by the core ("123 456 789").
    var id = ""
    /// The password in force: the temporary one, or the owner's own.
    var password = ""
    /// The owner's own password is set ("fixed", Settings → Share this
    /// screen → Password settings), not a new temporary one at every start.
    var passwordFixed = false
    var addresses: [String] = []
    var online = false
    /// Logins waiting for the owner.
    var requests: [CoreRequest] = []
    /// Connected sessions asking for more than viewing (control): this
    /// device cannot be controlled, so the extension refuses them at once.
    var accessRequests: [Int] = []
    /// Connected viewers (the extension adds when it first saw each).
    var viewers: [Viewer] = []

    /// The core's HostStateJSON document.
    init(json: String) {
        guard let o = (try? JSONSerialization.jsonObject(with: Data(json.utf8))) as? [String: Any] else { return }
        phase = o["phase"] as? String ?? "off"
        error = o["error"] as? String ?? ""
        id = o["id"] as? String ?? ""
        password = o["password"] as? String ?? ""
        passwordFixed = o["password_mode"] as? String == "fixed"
        addresses = o["addresses"] as? [String] ?? []
        online = o["online"] as? Bool ?? false
        for r in o["requests"] as? [[String: Any]] ?? [] {
            let n = (r["n"] as? NSNumber)?.intValue ?? 0
            if let access = r["access"] as? [String], !access.isEmpty {
                accessRequests.append(n)
                continue
            }
            let device = r["device"] as? String ?? ""
            requests.append(CoreRequest(n: n, device: device.isEmpty ? (r["name"] as? String ?? "") : device,
                                        identity: r["identity"] as? String ?? "", from: r["from"] as? String ?? "",
                                        firstSeen: r["first_seen"] as? Bool ?? true, left: (r["left"] as? NSNumber)?.intValue))
        }
        for s in o["sessions"] as? [[String: Any]] ?? [] {
            viewers.append(Viewer(id: (s["id"] as? NSNumber)?.intValue ?? 0, label: s["label"] as? String ?? "", from: s["from"] as? String ?? ""))
        }
    }

    init() {}
}
