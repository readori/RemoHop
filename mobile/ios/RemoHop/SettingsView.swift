import SwiftUI
import UIKit

/// Native settings: the app's language, the rendezvous server (no default:
/// the user's own), the relay switch, the device name and identity, pairing
/// with a host by password, and the saved hosts. Settings apply from the
/// next connection; the language at once (see L10n).
struct SettingsView: View {
    @Environment(\.dismiss) private var dismiss
    @StateObject private var m = SettingsModel()
    /// Read here so that the screen redraws in the new language.
    @AppStorage(L10n.key) private var language = AppLanguage.system.rawValue

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    Picker(L("Language"), selection: $language) {
                        Text(L("Follow system")).tag(AppLanguage.system.rawValue)
                        // Each language in its own words.
                        Text(verbatim: "简体中文").tag(AppLanguage.zh.rawValue)
                        Text(verbatim: "English").tag(AppLanguage.en.rawValue)
                    }
                } footer: {
                    Text(L("Applies to the app and its remote-control page at once."))
                }
                Section {
                    TextField("rendezvous.example.net:21116", text: $m.signal)
                        .keyboardType(.URL).textInputAutocapitalization(.never).autocorrectionDisabled()
                    TextField(L("Server key (optional)"), text: $m.pin)
                        .textInputAutocapitalization(.never).autocorrectionDisabled()
                    Toggle(L("Allow relayed connections"), isOn: $m.relay)
                } header: {
                    Text(L("Rendezvous server"))
                } footer: {
                    Text(markdown: L("Introduces this device to your hosts by their 9-digit ID. RemoHop has no default server: run your own with `remohop signal`. Without one you can connect by address (host:port). Relayed connections stay end-to-end encrypted."))
                }
                Section(L("This device")) {
                    TextField(L("Device name shown to hosts (optional)"), text: $m.name)
                    Button(L("Save")) { m.save() }
                    if !m.saveStatus.isEmpty { Text(m.saveStatus).font(.footnote) }
                }
                Section {
                    Text(m.fingerprint).font(.system(.footnote, design: .monospaced)).textSelection(.enabled)
                    Button(L("Copy key line")) { m.copyKeyLine() }
                } header: {
                    Text(L("Identity"))
                } footer: {
                    Text(markdown: L("Hosts pin this key. To log in without any password, run `remohop authorize \"LINE\"` on a host with the copied line."))
                }
                Section {
                    TextField(L("Host ID, name or host:port"), text: $m.pairTarget)
                        .textInputAutocapitalization(.never).autocorrectionDisabled()
                    SecureField(L("Host password (used once, not stored)"), text: $m.pairPassword)
                    TextField(L("Name for this host (optional)"), text: $m.pairAlias)
                        .textInputAutocapitalization(.never).autocorrectionDisabled()
                    if m.pairing {
                        HStack { ProgressView(); Button(L("Cancel"), role: .cancel) { m.cancelPair() } }
                    } else {
                        Button(L("Pair")) { m.pair() }
                    }
                    if !m.pairStatus.isEmpty { Text(m.pairStatus).font(.footnote) }
                } header: {
                    Text(L("Pair with a host"))
                } footer: {
                    Text(L("Log in once with the host's password; the host gives this device a token, so later connections need no password."))
                }
                Section(L("Saved hosts")) {
                    if m.hosts.isEmpty { Text(m.hostsStatus.isEmpty ? L("No saved hosts yet.") : m.hostsStatus).foregroundStyle(.secondary) }
                    ForEach(m.hosts) { h in
                        VStack(alignment: .leading) {
                            Text(h.label)
                            Text(h.detail).font(.footnote).foregroundStyle(.secondary)
                        }
                        .swipeActions { Button(L("Forget"), role: .destructive) { m.forget(h.target) } }
                    }
                    Button(L("Check which are online")) { m.loadHosts(online: true) }
                }
                Section {
                    Text(L("RemoHop talks only to the hosts you connect to and the rendezvous server you set here. Its identity, pairings and settings stay in this app's private storage. The app runs a local gateway on 127.0.0.1 that only its own page, holding a random per-launch token, can use."))
                        .font(.footnote).foregroundStyle(.secondary)
                }
            }
            .navigationTitle(L("Settings"))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { ToolbarItem(placement: .confirmationAction) { Button(L("Done")) { dismiss() } } }
        }
        .environment(\.locale, L10n.locale(L10n.resolve(AppLanguage(rawValue: language) ?? .system)))
        .onAppear { m.load() }
        .onChange(of: language) { _ in m.languageChanged() }
    }
}

struct SavedHost: Identifiable {
    let target: String
    let label: String
    let detail: String
    var id: String { target }
}

@MainActor
final class SettingsModel: ObservableObject {
    @Published var signal = ""
    @Published var pin = ""
    @Published var relay = true
    @Published var name = ""
    @Published var fingerprint = ""
    @Published var saveStatus = ""
    @Published var pairTarget = ""
    @Published var pairPassword = ""
    @Published var pairAlias = ""
    @Published var pairing = false
    @Published var pairStatus = ""
    @Published var hosts: [SavedHost] = []
    @Published var hostsStatus = ""

    /// Status lines were written in the old language; the saved hosts'
    /// details are built again in the new one.
    func languageChanged() {
        saveStatus = ""
        if !pairing { pairStatus = "" }
        loadHosts(online: false)
    }

    func load() {
        Task.detached {
            let s = (try? Core.settings()) ?? [:]
            let fp = (try? Core.identity()) ?? ""
            await MainActor.run {
                self.signal = s["signal"] ?? ""
                self.pin = s["signal-pin"] ?? ""
                self.relay = (s["relay"] ?? "on") != "off"
                self.name = s["name"] ?? ""
                self.fingerprint = fp
            }
        }
        loadHosts(online: false)
    }

    func save() {
        let values = [("signal", signal), ("signal-pin", pin), ("relay", relay ? "on" : "off"), ("name", name)]
        Task.detached {
            var err: String?
            // Address first: changing it clears a pin for the old server.
            for (k, v) in values where err == nil {
                do { try Core.setSetting(k, v.trimmingCharacters(in: .whitespaces)) } catch { err = error.localizedDescription }
            }
            await MainActor.run {
                self.saveStatus = err ?? L("Settings saved.")
                if err == nil { self.load() }
            }
        }
    }

    func copyKeyLine() {
        Task.detached {
            let line = try? Core.publicKeyLine()
            await MainActor.run {
                if let line { UIPasteboard.general.string = line; self.saveStatus = L("Key line copied.") }
            }
        }
    }

    func pair() {
        let t = pairTarget, pw = pairPassword, a = pairAlias
        pairPassword = ""
        pairing = true
        pairStatus = L("Connecting… the host may ask its owner to approve.")
        Task.detached {
            let r = (try? Core.pair(target: t, password: pw, alias: a)) ?? ["ok": false, "error": "pairing failed"]
            await MainActor.run {
                self.pairing = false
                if (r["ok"] as? Bool) == true {
                    self.pairStatus = L("Paired with %@ (host key %@).", r["target"] as? String ?? t, r["host_identity"] as? String ?? "?")
                    self.loadHosts(online: false)
                } else {
                    self.pairStatus = L10n.pairError(code: r["code"] as? String, raw: r["error"] as? String ?? L("Pairing failed."))
                }
            }
        }
    }

    func cancelPair() {
        Task.detached { Core.cancelPair() }
    }

    func loadHosts(online: Bool) {
        Task.detached {
            do {
                let list = try Core.devices(checkOnline: online)
                let hosts = list.map { d -> SavedHost in
                    let target = d["target"] as? String ?? ""
                    let alias = d["alias"] as? String ?? ""
                    let label = alias.isEmpty ? target : "\(alias) · \(target)"
                    let presence = L10n.presence(d["presence"] as? String ?? "")
                    return SavedHost(target: target, label: label, detail: "\(L10n.login(d["login"] as? String ?? "")) · \(presence)")
                }
                await MainActor.run { self.hosts = hosts; self.hostsStatus = "" }
            } catch {
                await MainActor.run { self.hosts = []; self.hostsStatus = error.localizedDescription }
            }
        }
    }

    func forget(_ target: String) {
        Task.detached {
            try? Core.forget(target)
            await MainActor.run { self.loadHosts(online: false) }
        }
    }
}
