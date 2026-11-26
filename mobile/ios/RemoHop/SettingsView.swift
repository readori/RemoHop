import Remohopcore
import SwiftUI
import UIKit

/// The screens Settings drills into.
enum SettingsPage: String, Hashable {
    case rendezvous, deviceName, identity, hosts, pair, diagnostics, privacy, about

    /// A page the web page may open directly (openSettings with a page).
    init?(bridge name: String?) {
        guard let name, let p = SettingsPage(rawValue: name), [.privacy, .about, .diagnostics, .rendezvous].contains(p) else { return nil }
        self = p
    }
}

/// Native settings, laid out like the system's: grouped sections of rows
/// with a value on the right and a chevron into a detail screen. No
/// paragraphs: every explanation is behind an ⓘ on the screen it is about.
/// Fields save themselves when you leave them (no Save buttons); the
/// language applies at once (see L10n), the connection settings from the
/// next connection.
struct SettingsView: View {
    @Environment(\.dismiss) private var dismiss
    @StateObject private var m = SettingsModel()
    /// Read here so that the screen redraws in the new language.
    @AppStorage(L10n.key) private var language = AppLanguage.system.rawValue
    var initialPage: SettingsPage?
    /// Settings → Saved hosts → Connect: the app closes Settings and the page connects.
    var onConnect: (String) -> Void
    @State private var path = NavigationPath()

    init(initialPage: SettingsPage? = nil, onConnect: @escaping (String) -> Void = { _ in }) {
        self.initialPage = initialPage
        self.onConnect = onConnect
    }

    var body: some View {
        NavigationStack(path: $path) {
            List {
                Section(L("Connection")) {
                    NavigationLink(value: SettingsPage.rendezvous) {
                        SettingsRow(L("Rendezvous server"), value: m.rendezvousSummary, symbol: "network", tint: .blue)
                    }
                }
                Section(L("This device")) {
                    NavigationLink(value: SettingsPage.deviceName) {
                        SettingsRow(L("Device name"), value: m.name.isEmpty ? m.defaultName : m.name, symbol: "iphone", tint: .gray)
                    }
                    NavigationLink(value: SettingsPage.identity) {
                        SettingsRow(L("Identity key"), value: m.shortFingerprint, symbol: "key.fill", tint: .orange)
                    }
                }
                Section(L("Hosts")) {
                    NavigationLink(value: SettingsPage.hosts) {
                        SettingsRow(L("Saved hosts"), value: m.hostsLoaded ? "\(m.hosts.count)" : "", symbol: "desktopcomputer", tint: .indigo)
                    }
                    NavigationLink(value: SettingsPage.pair) {
                        SettingsRow(L("Pair with a host"), value: "", symbol: "link", tint: .green)
                    }
                }
                Section(L("General")) {
                    Picker(selection: $language) {
                        Text(L("Follow system")).tag(AppLanguage.system.rawValue)
                        // Each language in its own words.
                        Text(verbatim: "简体中文").tag(AppLanguage.zh.rawValue)
                        Text(verbatim: "English").tag(AppLanguage.en.rawValue)
                    } label: {
                        SettingsRow(L("Language"), value: "", symbol: "globe", tint: .blue)
                    }
                    .pickerStyle(.navigationLink)
                }
                Section(L("Support")) {
                    NavigationLink(value: SettingsPage.diagnostics) {
                        SettingsRow(L("Diagnostic logs"), value: m.diagOn ? L("On") : L("Off"), symbol: "stethoscope", tint: .teal)
                    }
                    NavigationLink(value: SettingsPage.privacy) {
                        SettingsRow(L("Privacy"), value: "", symbol: "hand.raised.fill", tint: .blue)
                    }
                    NavigationLink(value: SettingsPage.about) {
                        SettingsRow(L("About RemoHop"), value: AppInfo.version(Bundle.main.infoDictionary).version, symbol: "info", tint: .gray)
                    }
                }
            }
            .listStyle(.insetGrouped)
            .navigationTitle(L("Settings"))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { ToolbarItem(placement: .confirmationAction) { Button(L("Done")) { dismiss() } } }
            .navigationDestination(for: SettingsPage.self) { page in
                switch page {
                case .rendezvous: RendezvousView(m: m)
                case .deviceName: DeviceNameView(m: m)
                case .identity: IdentityView(m: m)
                case .hosts: SavedHostsView(m: m)
                case .pair: PairView(m: m)
                case .diagnostics: DiagnosticsView(m: m)
                case .privacy: PrivacyView()
                case .about: AboutView()
                }
            }
            .navigationDestination(for: SavedHostRef.self) { ref in
                SavedHostDetailView(m: m, target: ref.target) { t in
                    dismiss()
                    onConnect(t)
                }
            }
        }
        .environment(\.locale, L10n.locale(L10n.resolve(AppLanguage(rawValue: language) ?? .system)))
        .onAppear {
            m.load()
            if let p = initialPage, path.isEmpty { path.append(p) }
        }
        .onChange(of: language) { _ in m.languageChanged() }
    }
}

/// A settings row: a coloured icon, the title, and a value on the right.
struct SettingsRow: View {
    let title: String
    let value: String
    let symbol: String
    let tint: Color

    init(_ title: String, value: String, symbol: String, tint: Color) {
        self.title = title
        self.value = value
        self.symbol = symbol
        self.tint = tint
    }

    var body: some View {
        HStack(spacing: 12) {
            Image(systemName: symbol)
                .font(.system(size: 15, weight: .semibold))
                .foregroundStyle(.white)
                .frame(width: 29, height: 29)
                .background(tint, in: RoundedRectangle(cornerRadius: 7, style: .continuous))
                .accessibilityHidden(true)
            Text(title)
            Spacer(minLength: 8)
            if !value.isEmpty {
                Text(value).foregroundStyle(.secondary).lineLimit(1).truncationMode(.middle)
            }
        }
    }
}

/// A saved host as the core's DevicesJSON reports it, in the words the
/// screens use: paired (or authorised by key, or with its password saved
/// on this device because it does not pair), so it connects without a
/// password, or recent, so it needs the password each time.
struct SavedHost: Identifiable, Equatable {
    enum Kind { case paired, key, savedPassword, recent }

    let target: String
    let alias: String
    let login: String
    let presence: String
    let lastSeen: Date?
    var id: String { target }
    var label: String { alias.isEmpty ? target : alias }
    var kind: Kind {
        switch login {
        case "pairing": return .paired
        case "key": return .key
        case "saved_password": return .savedPassword
        default: return .recent
        }
    }
    /// A 9-digit ID (shown as "ID"), or an address.
    var isID: Bool { target.count == 9 && target.allSatisfy { $0.isASCII && $0.isNumber } }
    /// The list's second line: the address under a name, how it logs in, and whether it is online.
    var detail: String {
        var parts: [String] = []
        if !alias.isEmpty { parts.append(target) }
        parts.append(kindSummary)
        if !presence.isEmpty && presence != "not_checked" { parts.append(L10n.presence(presence)) }
        return parts.joined(separator: " · ")
    }
    var kindSummary: String {
        switch kind {
        case .paired: return L("Paired · connects without a password")
        case .key: return L("Authorised by key · connects without a password")
        case .savedPassword: return L("Password saved · connects without a password")
        case .recent: return L("Recent · needs the password")
        }
    }
    var status: String {
        switch kind {
        case .paired: return L("Paired: connects without a password")
        case .key: return L("Authorised by key: connects without a password")
        case .savedPassword: return L("Password saved: connects without typing a password")
        case .recent: return L("Recent: needs the password each time")
        }
    }

    init(target: String, alias: String, login: String, presence: String, lastSeen: Date? = nil) {
        self.target = target
        self.alias = alias
        self.login = login
        self.presence = presence
        self.lastSeen = lastSeen
    }

    /// One object of DevicesJSON.
    init(json d: [String: Any]) {
        self.init(target: d["target"] as? String ?? "", alias: d["alias"] as? String ?? "", login: d["login"] as? String ?? "",
                  presence: d["presence"] as? String ?? "", lastSeen: Self.date(d["last_seen"] as? String))
    }

    /// Go's time.Time JSON (RFC 3339, with or without fractions of a second).
    static func date(_ s: String?) -> Date? {
        guard let s, !s.isEmpty else { return nil }
        let f = ISO8601DateFormatter()
        if let d = f.date(from: s) { return d }
        f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return f.date(from: s)
    }
}

/// A saved host's page in the Settings navigation.
struct SavedHostRef: Hashable {
    let target: String
}

/// The settings the screens edit. Each field is saved when it is left
/// (`commit`); `saved` marks the field that was just saved for a moment.
@MainActor
final class SettingsModel: ObservableObject {
    enum Field: String { case signal, pin = "signal-pin", relay, name }

    @Published var signal = ""
    @Published var pin = ""
    @Published var relay = true
    @Published var name = ""
    @Published var fingerprint = ""
    @Published var saved: Field?
    @Published var fieldError = ""
    @Published var rendezvousStatus: RendezvousStatus?
    /// The key the saved server presented in the last check (offered when none is saved).
    @Published var rendezvousKey = ""
    @Published var checking = false
    @Published var pairTarget = ""
    @Published var pairPassword = ""
    @Published var pairAlias = ""
    @Published var pairing = false
    @Published var pairStatus = ""
    @Published var pairOK = false
    /// The last pairing failure's code (to offer "Set up" for a rendezvous problem).
    @Published var pairCode = ""
    @Published var hosts: [SavedHost] = []
    @Published var hostsLoaded = false
    @Published var hostsStatus = ""
    @Published var checkingOnline = false
    @Published var diagOn = false
    @Published var diagSize: Int64 = 0
    /// What was last read from or written to the core, to save only changes.
    private var stored: [Field: String] = [:]
    private var savedTimer: Task<Void, Never>?

    var defaultName: String { UIDevice.current.model }

    var rendezvousSummary: String {
        if signal.isEmpty { return L("Not set") }
        switch rendezvousStatus {
        case .ok?: return signal + " · " + L("Working")
        case .some: return signal + " · " + L("Problem")
        case nil: return signal
        }
    }

    var shortFingerprint: String {
        let parts = fingerprint.split(separator: ":")
        return parts.count > 2 ? parts.prefix(2).joined(separator: ":") + "…" : fingerprint
    }

    /// Status lines were written in the old language; the saved hosts'
    /// details are built again in the new one.
    func languageChanged() {
        fieldError = ""
        if !pairing { pairStatus = "" }
        objectWillChange.send()
    }

    func load() {
        Task.detached {
            let s = (try? Core.settings()) ?? [:]
            let fp = (try? Core.identity()) ?? ""
            let on = Diag.enabled, size = Diag.size
            await MainActor.run {
                self.signal = s["signal"] ?? ""
                self.pin = s["signal-pin"] ?? ""
                self.relay = (s["relay"] ?? "on") != "off"
                self.name = s["name"] ?? ""
                self.stored = [.signal: self.signal, .pin: self.pin, .relay: self.relay ? "on" : "off", .name: self.name]
                self.fingerprint = fp
                self.diagOn = on
                self.diagSize = size
            }
        }
        loadHosts(online: false)
    }

    private func value(_ f: Field) -> String {
        switch f {
        case .signal: return signal.trimmingCharacters(in: .whitespaces)
        case .pin: return pin.trimmingCharacters(in: .whitespaces)
        case .relay: return relay ? "on" : "off"
        case .name: return name.trimmingCharacters(in: .whitespaces)
        }
    }

    /// Saves one field if it changed; the core validates it. A changed
    /// rendezvous server is checked right away.
    func commit(_ f: Field) {
        let v = value(f)
        guard stored[f] != v else { return }
        Task.detached {
            let failure = Core.setSettingCode(f.rawValue, v)
            let s = (try? Core.settings()) ?? [:]
            await MainActor.run {
                if let failure {
                    // In the app's words; the core's text goes to the log.
                    Diag.log("setting \(f.rawValue) not saved: \(failure.code) \(failure.message)")
                    self.fieldError = L10n.settingError(code: failure.code)
                    return
                }
                self.fieldError = ""
                self.stored[f] = v
                // A new server address clears the key of the old one.
                if f == .signal { self.pin = s["signal-pin"] ?? ""; self.stored[.pin] = self.pin; self.rendezvousStatus = nil; self.rendezvousKey = "" }
                if f == .pin { self.rendezvousStatus = nil; self.rendezvousKey = "" }
                Diag.log("setting \(f.rawValue) changed")
                self.flashSaved(f)
                if (f == .signal || f == .pin) && !self.signal.isEmpty { self.checkRendezvous() }
            }
        }
    }

    func commitAll() {
        for f in [Field.signal, .pin, .relay, .name] { commit(f) }
    }

    /// Test connection: tests what is typed. A changed address or key is
    /// saved first (a saved change is checked right away); otherwise the
    /// saved server is checked again.
    func testRendezvous() {
        if stored[.signal] != value(.signal) {
            commit(.signal) // a new address drops the old server's key
        } else if stored[.pin] != value(.pin) {
            commit(.pin)
        } else {
            checkRendezvous()
        }
    }

    private func flashSaved(_ f: Field) {
        saved = f
        savedTimer?.cancel()
        savedTimer = Task { [weak self] in
            try? await Task.sleep(nanoseconds: 2_000_000_000)
            if !Task.isCancelled { self?.saved = nil }
        }
    }

    /// Asks the core whether the rendezvous server answers as one (see
    /// RendezvousCheck); nothing happens while the core has no check.
    func checkRendezvous() {
        guard RendezvousCheck.available, !signal.isEmpty else { return }
        checking = true
        Task.detached {
            let r = RendezvousCheck.run()
            await MainActor.run {
                self.checking = false
                self.rendezvousStatus = r?.status
                self.rendezvousKey = r?.key ?? ""
            }
        }
    }

    /// Saves the key the server presented as the pinned server key.
    func useServerKey() {
        guard !rendezvousKey.isEmpty else { return }
        pin = rendezvousKey
        commit(.pin)
    }

    func copyKeyLine() {
        Task.detached {
            let line = try? Core.publicKeyLine()
            await MainActor.run {
                if let line { UIPasteboard.general.string = line; UINotificationFeedbackGenerator().notificationOccurred(.success) }
            }
        }
    }

    func pair() {
        let t = pairTarget.trimmingCharacters(in: .whitespaces), pw = pairPassword, a = pairAlias
        pairPassword = ""
        pairing = true
        pairOK = false
        pairCode = ""
        pairStatus = L("Connecting… the host may ask its owner to approve.")
        Task.detached {
            let r = (try? Core.pair(target: t, password: pw, alias: a)) ?? ["ok": false, "error": "pairing failed"]
            await MainActor.run {
                self.pairing = false
                if (r["ok"] as? Bool) == true {
                    self.pairOK = true
                    self.pairStatus = L("Paired with %@.", r["target"] as? String ?? t)
                    Diag.log("paired with a host")
                    self.loadHosts(online: false)
                } else {
                    // A request turned down before trying says why in "reason".
                    let reason = r["reason"] as? String ?? ""
                    let code = reason.isEmpty ? (r["code"] as? String ?? "") : reason
                    self.pairCode = code
                    self.pairStatus = L10n.pairError(code: code)
                    Diag.log("pairing failed: \(code) \(r["error"] as? String ?? "")")
                }
            }
        }
    }

    func cancelPair() {
        Task.detached { Core.cancelPair() }
    }

    func loadHosts(online: Bool) {
        if online { checkingOnline = true }
        Task.detached {
            do {
                let list = try Core.devices(checkOnline: online)
                let hosts = list.map { SavedHost(json: $0) }
                await MainActor.run { self.hosts = hosts; self.hostsLoaded = true; self.hostsStatus = ""; self.checkingOnline = false }
            } catch {
                await MainActor.run { self.hostsStatus = error.localizedDescription; self.hostsLoaded = true; self.checkingOnline = false }
            }
        }
    }

    /// Names a saved host ("" removes the name); `done` gets an error text, or nil.
    func rename(_ target: String, _ alias: String, done: @escaping @MainActor (String?) -> Void = { _ in }) {
        let a = alias.trimmingCharacters(in: .whitespaces)
        Task.detached {
            var err: NSError?
            let ok = RemohopcoreSetAlias(target, a, &err)
            await MainActor.run {
                if ok && err == nil {
                    Diag.log("saved host renamed")
                    self.loadHosts(online: false)
                    done(nil)
                } else {
                    done(L("Start with a letter; then use letters, digits, single spaces, ., _ or - (up to 63 characters)."))
                }
            }
        }
    }

    func forget(_ target: String) {
        hosts.removeAll { $0.target == target }
        Task.detached {
            try? Core.forget(target)
            Diag.log("saved host forgotten")
            await MainActor.run { self.loadHosts(online: false) }
        }
    }

    /// Reads the password saved for a host, for the detail screen to show or
    /// copy (nil when it cannot). It is passed on, never kept here or logged.
    func savedPassword(_ target: String, done: @escaping @MainActor (String?) -> Void) {
        Task.detached {
            let pw = try? Core.savedPassword(target)
            if pw == nil { Diag.log("saved password not read") }
            await MainActor.run { done(pw) }
        }
    }

    /// Deletes the password saved for a host that does not pair; the host
    /// stays saved and needs its password again. `done` gets whether it went.
    func forgetSavedPassword(_ target: String, done: @escaping @MainActor (Bool) -> Void = { _ in }) {
        Task.detached {
            let ok = (try? Core.forgetSavedPassword(target)) != nil
            Diag.log(ok ? "saved password deleted" : "saved password not deleted")
            await MainActor.run {
                self.loadHosts(online: false)
                done(ok)
            }
        }
    }

    // ---- diagnostics ----

    func setDiagnostics(_ on: Bool) {
        do { try Diag.setEnabled(on) } catch { fieldError = error.localizedDescription }
        diagOn = Diag.enabled
        diagSize = Diag.size
    }

    func refreshDiagnostics() {
        diagOn = Diag.enabled
        diagSize = Diag.size
    }

    func clearDiagnostics() {
        try? Diag.clear()
        diagSize = Diag.size
    }
}

/// The core's rendezvous server check (CheckRendezvous): it connects to
/// the saved server the way a connection by ID does, with the saved key,
/// and says what answered. Run after the server or key was saved, and from
/// the Test row.
enum RendezvousCheck {
    static let available = true

    /// What answered at the saved server, and the key it presented (empty unless it is a rendezvous server).
    static func run() -> (status: RendezvousStatus, key: String)? {
        var err: NSError?
        let s = RemohopcoreCheckRendezvous("", &err)
        if err != nil { return nil }
        let r = parse(s)
        Diag.log("rendezvous check: \(r?.status == .ok ? "ok" : "problem")")
        return r
    }

    /// CheckRendezvous's JSON outcome.
    static func parse(_ s: String) -> (status: RendezvousStatus, key: String)? {
        guard let obj = (try? JSONSerialization.jsonObject(with: Data(s.utf8))) as? [String: Any] else { return nil }
        let code = obj["code"] as? String ?? "error"
        let key = code == "ok" ? (obj["key"] as? String ?? "").trimmingCharacters(in: .whitespaces) : ""
        return (RendezvousStatus(code: code, message: obj["error"] as? String ?? ""), key)
    }
}
