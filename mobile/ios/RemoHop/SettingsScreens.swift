import SwiftUI
import UIKit

// The detail screens of Settings. Visible text is labels and values only;
// explanations are behind the ⓘ in each screen's navigation bar.

/// One explained term: a title and a short text.
struct InfoItem: Identifiable {
    let title: String
    let text: String
    var id: String { title }
}

/// The ⓘ button: opens a sheet with short explanations.
struct InfoButton: View {
    let title: String
    let items: [InfoItem]
    @State private var shown = false

    init(title: String, items: [InfoItem]) {
        self.title = title
        self.items = items
    }

    var body: some View {
        Button { shown = true } label: { Image(systemName: "info.circle") }
            .accessibilityLabel(L("About this screen"))
            .sheet(isPresented: $shown) {
                NavigationStack {
                    ScrollView {
                        VStack(alignment: .leading, spacing: 18) {
                            ForEach(items) { item in
                                VStack(alignment: .leading, spacing: 4) {
                                    if !item.title.isEmpty { Text(item.title).font(.headline) }
                                    Text(markdown: item.text).foregroundStyle(.secondary)
                                }
                            }
                        }
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding()
                    }
                    .navigationTitle(title)
                    .navigationBarTitleDisplayMode(.inline)
                    .toolbar { ToolbarItem(placement: .confirmationAction) { Button(L("Done")) { shown = false } } }
                }
                .presentationDetents([.medium, .large])
            }
    }
}

/// A green checkmark for a moment after a field was saved.
struct SavedMark: View {
    let shown: Bool
    var body: some View {
        if shown {
            Image(systemName: "checkmark.circle.fill").foregroundStyle(.green)
                .accessibilityLabel(L("Saved"))
                .transition(.opacity)
        }
    }
}

// MARK: Rendezvous server

@MainActor
struct RendezvousView: View {
    @ObservedObject var m: SettingsModel
    private enum Focus { case signal, pin }
    @FocusState private var focus: Focus?
    @State private var lastFocus: Focus?

    init(m: SettingsModel) { self.m = m }

    var body: some View {
        Form {
            Section(L("Server")) {
                HStack {
                    TextField(L("Address"), text: $m.signal, prompt: Text(verbatim: "rendezvous.example.net:21116"))
                        .keyboardType(.URL).textInputAutocapitalization(.never).autocorrectionDisabled()
                        .submitLabel(.done)
                        .focused($focus, equals: .signal)
                        .onSubmit { m.commit(.signal) }
                    SavedMark(shown: m.saved == .signal)
                }
                HStack {
                    TextField(L("Server key"), text: $m.pin, prompt: Text(L("Optional")))
                        .textInputAutocapitalization(.never).autocorrectionDisabled()
                        .submitLabel(.done)
                        .focused($focus, equals: .pin)
                        .onSubmit { m.commit(.pin) }
                    SavedMark(shown: m.saved == .pin)
                }
            }
            if !m.fieldError.isEmpty {
                Section { Label(m.fieldError, systemImage: "exclamationmark.triangle.fill").foregroundStyle(.red) }
            }
            // No key: the first connection trusts whichever server answers
            // (trust on first use). Say so, and offer the key it presented.
            if !m.signal.isEmpty && m.pin.trimmingCharacters(in: .whitespaces).isEmpty {
                Section {
                    Label {
                        VStack(alignment: .leading, spacing: 4) {
                            Text(L("No server key set")).font(.subheadline.weight(.semibold))
                            Text(markdown: L("Without the key, this device trusts whichever server answers at this address the first time it connects (trust on first use). Paste the key that `remohop signal` printed, or use the one this server presented after comparing it."))
                                .font(.footnote).foregroundStyle(.secondary)
                        }
                    } icon: {
                        Image(systemName: "exclamationmark.shield").foregroundStyle(.orange)
                    }
                    if m.rendezvousStatus == .ok && !m.rendezvousKey.isEmpty {
                        Button { m.useServerKey() } label: {
                            VStack(alignment: .leading, spacing: 4) {
                                Text(L("Use this key"))
                                Text(verbatim: m.rendezvousKey).font(.system(.footnote, design: .monospaced)).foregroundStyle(.secondary)
                                    .textSelection(.enabled)
                            }
                        }
                        .accessibilityHint(Text(m.rendezvousKey))
                    }
                }
            }
            if RendezvousCheck.available && !m.signal.isEmpty {
                Section {
                    Button { m.checkRendezvous() } label: {
                        HStack {
                            Text(L("Test connection"))
                            Spacer()
                            if m.checking { ProgressView() } else if let s = m.rendezvousStatus { RendezvousStatusView(status: s) }
                        }
                    }
                    .disabled(m.checking)
                }
            }
            Section {
                HStack {
                    Toggle(L("Relay when there is no direct path"), isOn: $m.relay)
                    SavedMark(shown: m.saved == .relay)
                }
            }
        }
        .navigationTitle(L("Rendezvous server"))
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .primaryAction) {
                InfoButton(title: L("Rendezvous server"), items: [
                    InfoItem(title: L("Rendezvous server"), text: L("Finds your computers by their 9-digit ID. Not needed when you connect by address (host:port). RemoHop has no default server: use your own.")),
                    InfoItem(title: L("Server key"), text: L("The server's public key, if you want this device to accept only that server. Leave it empty to trust the address.")),
                    InfoItem(title: L("Relay"), text: L("When no direct path works, the connection goes through the server. It stays end-to-end encrypted, but is slower.")),
                    InfoItem(title: L("For administrators"), text: L("Run a server with `remohop signal`; it prints its address and key.")),
                ])
            }
        }
        .onChange(of: focus) { new in
            if let old = lastFocus, old != new { m.commit(old == .signal ? .signal : .pin) }
            lastFocus = new
        }
        .onChange(of: m.relay) { _ in m.commit(.relay) }
        .onAppear { if m.rendezvousStatus == nil { m.checkRendezvous() } }
        .onDisappear { m.commitAll() }
        .animation(.default, value: m.saved)
    }
}

struct RendezvousStatusView: View {
    let status: RendezvousStatus
    var body: some View {
        switch status {
        case .ok: Label(L("Working"), systemImage: "checkmark.circle.fill").foregroundStyle(.green)
        case .isHost: Label(L("This is a host, not a server"), systemImage: "exclamationmark.triangle.fill").foregroundStyle(.orange)
        case .wrongProtocol: Label(L("Not a rendezvous server"), systemImage: "exclamationmark.triangle.fill").foregroundStyle(.orange)
        case .unreachable: Label(L("Unreachable"), systemImage: "xmark.circle.fill").foregroundStyle(.red)
        case .keyMismatch: Label(L("Key does not match"), systemImage: "exclamationmark.shield.fill").foregroundStyle(.red)
        case .failed(let s): Text(s).foregroundStyle(.red).lineLimit(2)
        }
    }
}

// MARK: This device

@MainActor
struct DeviceNameView: View {
    @ObservedObject var m: SettingsModel
    @FocusState private var focused: Bool

    init(m: SettingsModel) { self.m = m }

    var body: some View {
        Form {
            Section {
                HStack {
                    TextField(L("Device name"), text: $m.name, prompt: Text(m.defaultName))
                        .focused($focused)
                        .submitLabel(.done)
                        .onSubmit { m.commit(.name) }
                    SavedMark(shown: m.saved == .name)
                }
            }
            if !m.fieldError.isEmpty {
                Section { Label(m.fieldError, systemImage: "exclamationmark.triangle.fill").foregroundStyle(.red) }
            }
        }
        .navigationTitle(L("Device name"))
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .primaryAction) {
                InfoButton(title: L("Device name"), items: [
                    InfoItem(title: "", text: L("The name hosts show for this device when it pairs with them. Empty: the device model.")),
                ])
            }
        }
        .onAppear { focused = true }
        .onChange(of: focused) { f in if !f { m.commit(.name) } }
        .onDisappear { m.commit(.name) }
        .animation(.default, value: m.saved)
    }
}

@MainActor
struct IdentityView: View {
    @ObservedObject var m: SettingsModel
    @State private var copied = false

    init(m: SettingsModel) { self.m = m }

    var body: some View {
        Form {
            Section(L("Fingerprint")) {
                Text(m.fingerprint).font(.system(.body, design: .monospaced)).textSelection(.enabled)
            }
            Section {
                Button {
                    m.copyKeyLine()
                    copied = true
                } label: {
                    HStack {
                        Label(L("Copy key line"), systemImage: "doc.on.doc")
                        Spacer()
                        if copied { Image(systemName: "checkmark").foregroundStyle(.green) }
                    }
                }
            }
        }
        .navigationTitle(L("Identity key"))
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .primaryAction) {
                InfoButton(title: L("Identity key"), items: [
                    InfoItem(title: L("Identity key"), text: L("This device's own key, created on first use and kept only here. Hosts remember it, so they can tell it is the same device next time.")),
                    InfoItem(title: L("Key line"), text: L("Lets this device log in to a host without any password. On the host, run `remohop authorize \"LINE\"` with the copied line.")),
                ])
            }
        }
    }
}

// MARK: Hosts

@MainActor
struct SavedHostsView: View {
    @ObservedObject var m: SettingsModel
    @State private var forgetting: SavedHost?

    init(m: SettingsModel) { self.m = m }

    var body: some View {
        List {
            if m.hosts.isEmpty && m.hostsLoaded {
                Section {
                    Text(m.hostsStatus.isEmpty ? L("No saved hosts yet.") : m.hostsStatus).foregroundStyle(.secondary)
                }
            }
            Section {
                // Every row opens the host's page: name, address, status, Connect, Forget.
                ForEach(m.hosts) { h in
                    NavigationLink(value: SavedHostRef(target: h.target)) {
                        HStack(spacing: 12) {
                            Image(systemName: "desktopcomputer").foregroundStyle(.tint).frame(width: 28)
                            VStack(alignment: .leading, spacing: 2) {
                                Text(h.label)
                                Text(h.detail).font(.footnote).foregroundStyle(.secondary)
                            }
                            Spacer()
                            if h.presence == "online" { Circle().fill(.green).frame(width: 8, height: 8).accessibilityLabel(L10n.presence("online")) }
                        }
                    }
                    .swipeActions {
                        Button(L("Forget"), role: .destructive) { forgetting = h }
                    }
                }
            }
            Section {
                NavigationLink(value: SettingsPage.pair) { Label(L("Pair with a host"), systemImage: "link") }
            }
        }
        .listStyle(.insetGrouped)
        .navigationTitle(L("Saved hosts"))
        .navigationBarTitleDisplayMode(.inline)
        .refreshable { m.loadHosts(online: true) }
        .alert(L("Forget %@?", forgetting?.label ?? ""), isPresented: Binding(get: { forgetting != nil }, set: { if !$0 { forgetting = nil } })) {
            Button(L("Cancel"), role: .cancel) {}
            Button(L("Forget"), role: .destructive) { if let h = forgetting { m.forget(h.target) } }
        } message: {
            Text(L("Its saved pairing and key are deleted from this device, and any open session to it ends. To connect again you will need its password."))
        }
        .toolbar {
            ToolbarItemGroup(placement: .primaryAction) {
                if m.checkingOnline { ProgressView() } else {
                    Button { m.loadHosts(online: true) } label: { Image(systemName: "arrow.clockwise") }
                        .accessibilityLabel(L("Check which are online"))
                }
                InfoButton(title: L("Saved hosts"), items: [
                    InfoItem(title: "", text: L("Hosts this device has connected to or paired with. Tap one to rename it, connect to it or forget it; pull down to check which are online.")),
                ])
            }
        }
    }
}

/// One saved host: its name (saved when the field is left), its ID or
/// address (tap to copy), how it logs in, when it was last connected, and
/// Connect and Forget.
@MainActor
struct SavedHostDetailView: View {
    @ObservedObject var m: SettingsModel
    let target: String
    let connect: (String) -> Void
    @Environment(\.dismiss) private var dismiss
    @State private var name = ""
    @State private var loaded = false
    @State private var error = ""
    @State private var saved = false
    @State private var copied = false
    @State private var confirmForget = false
    @FocusState private var focused: Bool

    init(m: SettingsModel, target: String, connect: @escaping (String) -> Void) {
        self.m = m
        self.target = target
        self.connect = connect
    }

    private var host: SavedHost? { m.hosts.first { $0.target == target } }

    var body: some View {
        let h = host ?? SavedHost(target: target, alias: "", login: "", presence: "")
        Form {
            Section {
                HStack {
                    TextField(L("Name"), text: $name, prompt: Text(verbatim: h.target))
                        .focused($focused)
                        .submitLabel(.done)
                        .autocorrectionDisabled()
                        .onSubmit { save() }
                    SavedMark(shown: saved)
                }
            } header: {
                Text(L("Name"))
            } footer: {
                if !error.isEmpty { Text(error).foregroundStyle(.red) }
            }
            Section {
                Button {
                    UIPasteboard.general.string = h.target
                    copied = true
                    UINotificationFeedbackGenerator().notificationOccurred(.success)
                } label: {
                    HStack {
                        LabeledContent(h.isID ? L("ID") : L("Address")) {
                            Text(verbatim: h.target).font(.body.monospacedDigit()).textSelection(.enabled)
                        }
                        Image(systemName: copied ? "checkmark" : "doc.on.doc").foregroundStyle(copied ? Color.green : Color.accentColor)
                    }
                }
                .foregroundStyle(.primary)
                .accessibilityLabel(L("Copy") + " " + h.target)
                LabeledContent(L("Status")) { Text(h.status).multilineTextAlignment(.trailing) }
                if let d = h.lastSeen {
                    LabeledContent(L("Last connected")) {
                        Text(d, format: .relative(presentation: .named))
                    }
                }
                if !h.presence.isEmpty && h.presence != "not_checked" {
                    LabeledContent(L("Online status"), value: L10n.presence(h.presence))
                }
            }
            Section {
                Button { save(); connect(h.target) } label: { Label(L("Connect"), systemImage: "play.fill") }
            }
            Section {
                Button(role: .destructive) { confirmForget = true } label: { Label(L("Forget this computer"), systemImage: "trash") }
            }
        }
        .navigationTitle(h.label)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .primaryAction) {
                InfoButton(title: h.label, items: [
                    InfoItem(title: L("Name"), text: L("Shown instead of the ID or address, and usable in place of it when you connect. Leave it empty to show the ID or address.")),
                    InfoItem(title: L("Forget this computer"), text: L("Removes the pairing and the pinned key from this device. The computer then needs its password again, and a changed key is no longer noticed.")),
                ])
            }
        }
        .confirmationDialog(L("Forget %@?", h.label), isPresented: $confirmForget, titleVisibility: .visible) {
            Button(L("Forget this computer"), role: .destructive) {
                m.forget(target)
                dismiss()
            }
            Button(L("Cancel"), role: .cancel) {}
        } message: {
            Text(L("Its saved pairing and key are deleted from this device, and any open session to it ends. To connect again you will need its password."))
        }
        .onAppear {
            if !loaded { name = h.alias; loaded = true }
        }
        .onChange(of: focused) { f in if !f { save() } }
        .onDisappear { save() }
        .animation(.default, value: saved)
    }

    private func save() {
        let n = name.trimmingCharacters(in: .whitespaces)
        guard let h = host, n != h.alias else { return }
        m.rename(target, n) { err in
            error = err ?? ""
            if err == nil {
                saved = true
                Task { try? await Task.sleep(nanoseconds: 2_000_000_000); saved = false }
            }
        }
    }
}

@MainActor
struct PairView: View {
    @ObservedObject var m: SettingsModel

    var body: some View {
        Form {
            Section {
                TextField(L("Host ID or address"), text: $m.pairTarget)
                    .keyboardType(.URL).textInputAutocapitalization(.never).autocorrectionDisabled()
                SecureField(L("Host password"), text: $m.pairPassword)
                TextField(L("Name (optional)"), text: $m.pairAlias)
                    .textInputAutocapitalization(.never).autocorrectionDisabled()
            }
            Section {
                if m.pairing {
                    HStack {
                        ProgressView()
                        Text(m.pairStatus).foregroundStyle(.secondary).font(.footnote)
                        Spacer()
                        Button(L("Cancel"), role: .cancel) { m.cancelPair() }
                    }
                } else {
                    Button(L("Pair")) { m.pair() }.disabled(m.pairTarget.trimmingCharacters(in: .whitespaces).isEmpty)
                }
            }
            if !m.pairing && !m.pairStatus.isEmpty {
                Section {
                    Label(m.pairStatus, systemImage: m.pairOK ? "checkmark.circle.fill" : "exclamationmark.triangle.fill")
                        .foregroundStyle(m.pairOK ? Color.green : Color.red)
                }
            }
        }
        .navigationTitle(L("Pair with a host"))
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .primaryAction) {
                InfoButton(title: L("Pair with a host"), items: [
                    InfoItem(title: "", text: L("Log in once with the host's password. The host gives this device a token, so later connections need no password. The password is used once and not stored.")),
                    InfoItem(title: L("Host ID or address"), text: L("The host's 9-digit ID (needs a rendezvous server), a saved name, or its address such as 192.168.1.20:21118.")),
                ])
            }
        }
    }
}

// MARK: Diagnostics

@MainActor
struct DiagnosticsView: View {
    @ObservedObject var m: SettingsModel
    @State private var format = "txt"
    @State private var confirmClear = false
    @State private var error = ""

    init(m: SettingsModel) { self.m = m }

    var body: some View {
        Form {
            Section {
                Toggle(L("Diagnostic logs"), isOn: Binding(get: { m.diagOn }, set: { m.setDiagnostics($0) }))
                if m.diagOn { LabeledContent(L("Log size"), value: AppInfo.size(m.diagSize)) }
            }
            if m.diagOn || m.diagSize > 0 {
                Section {
                    Picker(L("File format"), selection: $format) {
                        Text(verbatim: "TXT").tag("txt")
                        Text(verbatim: "JSON").tag("json")
                    }
                    Button { share() } label: { Label(L("Share diagnostic logs"), systemImage: "square.and.arrow.up") }
                    Button { email() } label: { Label(L("Email support"), systemImage: "envelope") }
                }
                Section {
                    Button(role: .destructive) { confirmClear = true } label: { Label(L("Clear logs"), systemImage: "trash") }
                }
            }
            if !error.isEmpty {
                Section { Text(error).foregroundStyle(.red) }
            }
        }
        .navigationTitle(L("Diagnostic logs"))
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .primaryAction) {
                InfoButton(title: L("Diagnostic logs"), items: [
                    InfoItem(title: L("What is recorded"), text: L("While this is on, the app keeps a log of what it does (connection steps, errors, app events) on this device, up to about 1 MB. It is off by default, and turning it off deletes the log.")),
                    InfoItem(title: L("What is removed"), text: L("Passwords, access tokens, pairing tokens and keys are removed before anything is written. Host IDs, host names and IP addresses stay in, because support needs them to follow a connection.")),
                    InfoItem(title: L("Who sees it"), text: L("Nobody, unless you share it: the log is never uploaded. Share it or email it to support yourself when something does not work.")),
                ])
            }
        }
        .confirmationDialog(L("Clear logs"), isPresented: $confirmClear, titleVisibility: .hidden) {
            Button(L("Clear logs"), role: .destructive) { m.clearDiagnostics() }
        }
        .onAppear { m.refreshDiagnostics() }
    }

    private func report() -> URL? {
        do {
            error = ""
            return try Diag.export(format: format)
        } catch let e {
            error = e.localizedDescription
            return nil
        }
    }

    private func share() {
        guard let f = report() else { return }
        Presenter.share(f)
    }

    private func email() {
        guard let f = report() else { return }
        Presenter.emailSupport(subject: L("RemoHop diagnostic logs"),
                               body: L("Please describe what happened (what you did, what you expected, what you saw):") + "\n\n\n" +
                                   "RemoHop " + AppInfo.versionLine(Bundle.main.infoDictionary) + ", iOS " + UIDevice.current.systemVersion,
                               file: f)
    }
}

// MARK: Privacy

@MainActor
struct PrivacyView: View {
    var body: some View {
        List {
            Section {
                Label(L("No analytics, ads or tracking"), systemImage: "eye.slash")
                Label(L("Your keys, pairings and settings stay on this device"), systemImage: "iphone")
                Label(L("Sessions are end-to-end encrypted"), systemImage: "lock.fill")
                Label(L("Diagnostic logs stay here unless you share them"), systemImage: "stethoscope")
            }
            Section {
                Button { Presenter.open(AppInfo.privacyPolicy(L10n.effective)) } label: {
                    Label(L("Privacy policy"), systemImage: "hand.raised")
                }
                Button { Presenter.open(URL(string: "mailto:" + AppInfo.privacyEmail)!) } label: {
                    LabeledContent {
                        Text(verbatim: AppInfo.privacyEmail)
                    } label: {
                        Label(L("Privacy questions"), systemImage: "envelope")
                    }
                }
            }
        }
        .listStyle(.insetGrouped)
        .navigationTitle(L("Privacy"))
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .primaryAction) {
                InfoButton(title: L("Privacy"), items: [
                    InfoItem(title: L("What the app talks to"), text: L("Only the hosts you connect to and the rendezvous server you set. No Readori server is involved, and nothing is sent to us.")),
                    InfoItem(title: L("The local gateway"), text: L("The app runs its remote-control page on a gateway inside the app itself (127.0.0.1). Only the app's own page, holding a random token made at each launch, can use it.")),
                    InfoItem(title: L("Diagnostic logs"), text: L("Off by default. When you turn them on they stay on this device, with passwords and tokens removed, and leave it only when you share them.")),
                ])
            }
        }
    }
}

// MARK: About

@MainActor
struct AboutView: View {
    @State private var showNotice = false

    var body: some View {
        let info = Bundle.main.infoDictionary
        let v = AppInfo.version(info)
        List {
            Section {
                VStack(spacing: 10) {
                    AppIconImage().frame(width: 76, height: 76)
                    Text(verbatim: "RemoHop").font(.title2.bold())
                    Text(L("Version %@ (%@)", v.version, v.build)).foregroundStyle(.secondary).font(.subheadline)
                }
                .frame(maxWidth: .infinity)
                .padding(.vertical, 8)
                .listRowBackground(Color.clear)
            }
            Section {
                LabeledContent(L("Version"), value: v.version)
                LabeledContent(L("Build"), value: v.build)
                LabeledContent(L("Core"), value: Diag.coreVersion)
            }
            Section {
                link(L("Website"), "globe", AppInfo.website)
                link(L("Source code"), "chevron.left.forwardslash.chevron.right", AppInfo.sourceCode)
                link(L("License: AGPL-3.0"), "doc.text", AppInfo.licensing)
                Button { showNotice = true } label: { Label(L("Open-source notices"), systemImage: "text.book.closed") }
            }
            Section(L("Contact")) {
                link(AppInfo.contactEmail, "envelope", URL(string: "mailto:" + AppInfo.contactEmail)!)
                link(L("Telegram"), "paperplane", AppInfo.telegram)
            }
            Section {
                link(L("Terms of use"), "doc.plaintext", AppInfo.terms(L10n.effective))
                link(L("Privacy policy"), "hand.raised", AppInfo.privacyPolicy(L10n.effective))
            }
        }
        .listStyle(.insetGrouped)
        .navigationTitle(L("About RemoHop"))
        .navigationBarTitleDisplayMode(.inline)
        .sheet(isPresented: $showNotice) {
            NavigationStack {
                ScrollView {
                    VStack(alignment: .leading, spacing: 16) {
                        Text(verbatim: AppInfo.notice).font(.footnote).textSelection(.enabled)
                        Button(L("Third-party licenses")) { Presenter.open(AppInfo.thirdParty) }
                        Button(L("License: AGPL-3.0")) { Presenter.open(AppInfo.licensing) }
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding()
                }
                .navigationTitle(L("Open-source notices"))
                .navigationBarTitleDisplayMode(.inline)
                .toolbar { ToolbarItem(placement: .confirmationAction) { Button(L("Done")) { showNotice = false } } }
            }
        }
    }

    private func link(_ title: String, _ symbol: String, _ url: URL) -> some View {
        Button { Presenter.open(url) } label: {
            HStack {
                Label(title, systemImage: symbol)
                Spacer()
                Image(systemName: "arrow.up.right").font(.footnote).foregroundStyle(.tertiary)
            }
        }
    }
}

/// The app's icon from the bundle (the asset catalog's AppIcon), or the
/// mark drawn in the brand colour.
struct AppIconImage: View {
    var body: some View {
        if let image = Self.load() {
            Image(uiImage: image).resizable().clipShape(RoundedRectangle(cornerRadius: 17, style: .continuous))
        } else {
            RoundedRectangle(cornerRadius: 17, style: .continuous).fill(Color(red: 0x25 / 255, green: 0x46 / 255, blue: 0xd8 / 255))
                .overlay(Image(systemName: "arrow.left.arrow.right").font(.system(size: 30, weight: .bold)).foregroundStyle(.white))
        }
    }

    static func load() -> UIImage? {
        guard let icons = Bundle.main.infoDictionary?["CFBundleIcons"] as? [String: Any],
              let primary = icons["CFBundlePrimaryIcon"] as? [String: Any],
              let files = primary["CFBundleIconFiles"] as? [String], let name = files.last else { return nil }
        return UIImage(named: name)
    }
}
