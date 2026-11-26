import ReplayKit
import SwiftUI
import UIKit
import UniformTypeIdentifiers

/// Settings → Share this screen: view-only screen sharing through the
/// broadcast extension. Only the essentials are on the page (status, the
/// start button, ID and password with Copy, Password settings, requests,
/// viewers); every explanation, the scam warning included, is behind the ⓘ.
/// Each request keeps a short scam reminder of its own.
///
/// The password is a new temporary one at every start, or the owner's own
/// (Password settings): that one is shown as dots until the eye is tapped,
/// and hidden again when the app leaves the foreground.
@MainActor
struct ShareScreenView: View {
    @StateObject private var b = BroadcastModel()
    @State private var copied: String?
    @State private var confirmStop = false
    @State private var passwordSettings = false
    @State private var revealed = false
    @Environment(\.scenePhase) private var scenePhase

    var body: some View {
        let s = b.state
        let live = b.live
        Form {
            Section {
                LabeledContent(L("Status")) {
                    Text(statusText(s, live: live)).foregroundStyle(live ? .green : .secondary)
                }
                if !live {
                    BroadcastButton(title: L("Start sharing"))
                        .disabled(!b.available)
                } else {
                    Button(role: .destructive) { confirmStop = true } label: {
                        Label(L("Stop sharing"), systemImage: "stop.circle")
                    }
                }
            }
            if live || b.available {
                Section {
                    if live {
                        if !s.id.isEmpty { copyRow(L("ID"), s.id, mono: true) }
                        ForEach(s.id.isEmpty ? s.addresses : [], id: \.self) { a in copyRow(L("Address"), a, mono: true) }
                        if !s.password.isEmpty {
                            if s.passwordFixed { ownPasswordRow(s.password) } else { copyRow(L("Password"), s.password, mono: true) }
                        }
                    }
                    Button { passwordSettings = true } label: {
                        LabeledContent(L("Password settings")) {
                            Text(b.passwordFixed ? L("My own") : L("Random"))
                        }
                        .foregroundStyle(.primary)
                    }
                    Picker(selection: Binding(get: { ReadmitChoice.index(b.readmitMinutes) },
                                              set: { b.setReadmit(ReadmitChoice.minutes[$0]) })) {
                        ForEach(ReadmitChoice.minutes.indices, id: \.self) { i in
                            Text(ReadmitChoice.label(ReadmitChoice.minutes[i])).tag(i)
                        }
                    } label: {
                        Text(L("Reconnect without asking"))
                    }
                    .pickerStyle(.navigationLink)
                }
            }
            if !b.notificationsAllowed {
                Section {
                    Label(L("Notifications are off: answer requests here."), systemImage: "bell.slash")
                        .foregroundStyle(.orange)
                    Button(L("Open Settings")) {
                        if let u = URL(string: UIApplication.openNotificationSettingsURLString) { UIApplication.shared.open(u) }
                    }
                }
            }
            if live && !s.requests.isEmpty {
                Section(L("Requests")) {
                    ForEach(s.requests) { r in RequestRow(request: r) { b.decide(r, allow: $0) } }
                }
            }
            if live && !s.viewers.isEmpty {
                Section(L("Viewers")) {
                    ForEach(s.viewers) { v in
                        HStack {
                            VStack(alignment: .leading, spacing: 2) {
                                Text(v.label.isEmpty ? L("A device") : v.label)
                                if !v.from.isEmpty { Text(v.from).font(.caption).foregroundStyle(.secondary) }
                            }
                            Spacer()
                            Button(L("Disconnect"), role: .destructive) { b.disconnect(v.id) }
                                .buttonStyle(.borderless)
                        }
                    }
                    if s.viewers.count > 1 {
                        Button(L("Disconnect all"), role: .destructive) { b.disconnect(0) }
                    }
                }
            }
        }
        .navigationTitle(L("Share this screen"))
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .primaryAction) {
                InfoButton(title: L("Share this screen"), items: Self.info)
            }
        }
        .confirmationDialog(L("Stop sharing"), isPresented: $confirmStop, titleVisibility: .hidden) {
            Button(L("Stop sharing"), role: .destructive) { b.stop() }
        }
        .sheet(isPresented: $passwordSettings) {
            SharePasswordSheet(fixedNow: b.passwordFixed) { pw, done in
                b.savePassword(pw) { failure in
                    if failure == nil { revealed = false }
                    done(failure)
                }
            }
        }
        .onAppear { b.appear() }
        .onDisappear { b.disappear(); revealed = false }
        .onChange(of: scenePhase) { p in if p != .active { revealed = false } }
    }

    /// The ⓘ: what sharing is, how it starts and ends, what viewers see, and the scam warning.
    static var info: [InfoItem] {
        [
            InfoItem(title: L("View only"), text: L("Viewers see this screen and nothing else. They cannot tap, type or open anything on this device, and no sound is shared.")),
            InfoItem(title: L("Start and stop"), text: L("Sharing starts only when you tap Start sharing and then Start Broadcast. Stop it any time from the red status bar or Control Center, or with Stop sharing here.")),
            InfoItem(title: L("Who can connect"), text: L("Someone with the ID and the password shown here. You are asked every time, in a notification and on this page; a request you do not answer is refused after a minute. The password is new each time sharing starts, unless you set your own in Password settings.")),
            InfoItem(title: L("Coming back"), text: L("A viewer you allowed that drops out, for example because its app went to the background, comes back without asking you again: for 10 minutes, or as long as you choose in Reconnect without asking, up to always (unattended). Disconnecting it or stopping sharing ends that; a new device is always asked.")),
            InfoItem(title: L("What viewers see"), text: L("Everything on the screen while sharing, including notifications that appear. Close anything private first.")),
            InfoItem(title: L("When it ends"), text: L("When you stop it, when the phone restarts, or when the system ends it, for example when memory runs short. Viewers are disconnected at once.")),
            InfoItem(title: L("Scams"), text: L("Did someone call you saying they are from support, your bank or the police, and ask you to install this or share your screen? Do not share. That is a scam.")),
        ]
    }

    private func statusText(_ s: BroadcastState, live: Bool) -> String {
        if !b.available { return L("Not available in this build") }
        if live { return s.phase == .ready ? L("Sharing") : L("Starting…") }
        if s.phase == .failed { return L("Stopped because of an error") }
        return L("Not sharing")
    }

    private func copyRow(_ title: String, _ value: String, mono: Bool) -> some View {
        Button {
            UIPasteboard.general.string = value
            UINotificationFeedbackGenerator().notificationOccurred(.success)
            copied = title
        } label: {
            HStack {
                Text(title).foregroundStyle(.primary)
                Spacer()
                Text(value).font(mono ? .system(.body, design: .monospaced) : .body).foregroundStyle(.primary)
                Image(systemName: copied == title ? "checkmark" : "doc.on.doc")
                    .foregroundStyle(copied == title ? Color.green : Color.accentColor)
            }
        }
        .accessibilityLabel("\(title) \(value)")
        .accessibilityHint(L("Copy"))
    }

    /// The owner's own password: dots until the eye is tapped; Copy copies
    /// the password itself, to this device only and for two minutes (unlike
    /// a temporary one, it does not change by itself). No "new password"
    /// here: only Password settings changes it.
    private func ownPasswordRow(_ value: String) -> some View {
        let title = L("My own password", context: "label")
        return HStack {
            Text(title)
            Spacer()
            if revealed {
                Text(verbatim: value).font(.system(.body, design: .monospaced)).lineLimit(1).truncationMode(.middle)
            } else {
                Text(verbatim: "••••••••").accessibilityLabel(title)
            }
            Button { revealed.toggle() } label: {
                Image(systemName: revealed ? "eye.slash" : "eye")
            }
            .buttonStyle(.borderless)
            .accessibilityLabel(revealed ? L("Hide password") : L("Show password"))
            Button {
                UIPasteboard.general.setItems([[UTType.utf8PlainText.identifier: value]],
                                              options: [.localOnly: true, .expirationDate: Date().addingTimeInterval(120)])
                UINotificationFeedbackGenerator().notificationOccurred(.success)
                copied = title
            } label: {
                Image(systemName: copied == title ? "checkmark" : "doc.on.doc")
                    .foregroundStyle(copied == title ? Color.green : Color.accentColor)
            }
            .buttonStyle(.borderless)
            .accessibilityLabel(L("Copy"))
        }
    }
}

/// The owner's own password, by the core's rule (hostd.CheckOwnPassword,
/// behind SetHostFixedPassword), so that the sheet says what is wrong
/// before saving; the core checks again when it saves.
enum OwnPassword {
    enum Problem: Equatable {
        /// The two entries differ.
        case mismatch
        /// Too short, too long, or fewer than two kinds of characters.
        case weak
    }

    enum Outcome: Equatable {
        /// Save this (trimmed, otherwise as typed).
        case save(String)
        /// Both entries empty while an own password is set: keep it.
        case unchanged
        case problem(Problem)
    }

    /// Without spaces and dashes, as the core compares passwords
    /// (auth.NormalizePassword).
    static func normalized(_ s: String) -> String {
        var out = String.UnicodeScalarView()
        for u in s.unicodeScalars where u != "-" && !u.properties.isWhitespace { out.append(u) }
        return String(out)
    }

    /// Normalized: at least 8 and at most 256 bytes of UTF-8 (the core
    /// counts bytes), with at least two of: letters, digits, other
    /// characters.
    static func isStrong(_ password: String) -> Bool {
        let n = normalized(password)
        guard n.utf8.count >= 8, n.utf8.count <= 256 else { return false }
        var letters = false, digits = false, other = false
        for u in n.unicodeScalars {
            switch u.properties.generalCategory {
            case .uppercaseLetter, .lowercaseLetter, .titlecaseLetter, .modifierLetter, .otherLetter: letters = true
            case .decimalNumber: digits = true
            default: other = true
            }
        }
        return [letters, digits, other].filter { $0 }.count >= 2
    }

    /// What Save does with the two entries of "My own password".
    static func check(_ first: String, _ second: String, alreadyFixed: Bool) -> Outcome {
        if normalized(first).isEmpty && normalized(second).isEmpty {
            return alreadyFixed ? .unchanged : .problem(.weak)
        }
        if first != second { return .problem(.mismatch) }
        if !isStrong(first) { return .problem(.weak) }
        return .save(first.trimmingCharacters(in: .whitespacesAndNewlines))
    }
}

/// Password settings: a new random password at every start, or the
/// owner's own (typed twice, with an eye to show what was typed). The core
/// saves it in the configuration directory shared with the broadcast
/// extension: used from the next start, and at once while sharing.
/// Explanations are behind the ⓘ.
@MainActor
struct SharePasswordSheet: View {
    enum Mode { case temporary, fixed }
    enum Field { case first, second }

    let fixedNow: Bool
    /// Saves ("" = temporary); the callback gets nil, or the text to show.
    let save: @MainActor (String, @escaping @MainActor (String?) -> Void) -> Void
    @Environment(\.dismiss) private var dismiss
    @State private var mode: Mode
    @State private var first = ""
    @State private var second = ""
    @State private var shown = false
    @State private var error = ""
    @State private var saving = false
    @FocusState private var focus: Field?

    init(fixedNow: Bool, save: @escaping @MainActor (String, @escaping @MainActor (String?) -> Void) -> Void) {
        self.fixedNow = fixedNow
        self.save = save
        _mode = State(initialValue: fixedNow ? .fixed : .temporary)
    }

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    choice(.temporary, L("A new random password"))
                    choice(.fixed, L("My own password"))
                } footer: {
                    if mode == .temporary && !error.isEmpty { Text(error).foregroundStyle(.red) }
                }
                if mode == .fixed {
                    Section {
                        HStack {
                            field(L("Password"), $first, .first)
                            Button { shown.toggle() } label: {
                                Image(systemName: shown ? "eye.slash" : "eye")
                            }
                            .buttonStyle(.borderless)
                            .accessibilityLabel(shown ? L("Hide password") : L("Show password"))
                        }
                        field(L("Repeat password"), $second, .second)
                    } footer: {
                        if !error.isEmpty { Text(error).foregroundStyle(.red) }
                    }
                }
            }
            .navigationTitle(L("Password for incoming connections"))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button(L("Cancel")) { dismiss() }
                }
                ToolbarItemGroup(placement: .confirmationAction) {
                    InfoButton(title: L("Password settings"), items: Self.info)
                    if saving {
                        ProgressView()
                    } else {
                        Button(L("Save")) { submit() }
                    }
                }
            }
            .interactiveDismissDisabled(saving)
            .onChange(of: first) { _ in error = "" }
            .onChange(of: second) { _ in error = "" }
        }
    }

    static var info: [InfoItem] {
        [
            InfoItem(title: L("A new random password"), text: L("Made each time sharing starts.")),
            InfoItem(title: L("My own password"), text: L("Stays the same until you change it; at least 8 characters, with letters and digits.")),
            InfoItem(title: L("While sharing"), text: L("A change applies at once, also while sharing; viewers already connected stay connected.")),
            InfoItem(title: "", text: L("Either way, every connection still asks you first.")),
        ]
    }

    private func choice(_ m: Mode, _ title: String) -> some View {
        Button {
            mode = m
            error = ""
            // The fields appear with this choice: focus once they are there.
            if m == .fixed && !fixedNow { DispatchQueue.main.async { focus = .first } }
        } label: {
            HStack {
                Text(title).foregroundStyle(.primary)
                Spacer()
                if mode == m { Image(systemName: "checkmark").foregroundStyle(Color.accentColor) }
            }
        }
        .accessibilityAddTraits(mode == m ? .isSelected : [])
    }

    @ViewBuilder
    private func field(_ title: String, _ text: Binding<String>, _ f: Field) -> some View {
        Group {
            if shown {
                TextField(title, text: text).font(.body.monospaced())
            } else {
                SecureField(title, text: text)
            }
        }
        .textInputAutocapitalization(.never)
        .autocorrectionDisabled()
        .focused($focus, equals: f)
        .submitLabel(f == .first ? .next : .done)
        .onSubmit { if f == .first { focus = .second } else { submit() } }
    }

    private func submit() {
        guard !saving else { return }
        error = ""
        switch mode {
        case .temporary:
            if fixedNow { run("") } else { dismiss() }
        case .fixed:
            switch OwnPassword.check(first, second, alreadyFixed: fixedNow) {
            case .unchanged: dismiss()
            case .problem(.mismatch): error = L("The two passwords do not match.")
            case .problem(.weak): error = L("Use at least 8 characters, with at least two of: letters, digits, other characters.")
            case .save(let pw): run(pw)
            }
        }
    }

    private func run(_ password: String) {
        saving = true
        save(password) { failure in
            saving = false
            if let failure {
                error = failure
            } else {
                first = ""
                second = ""
                dismiss()
            }
        }
    }
}

/// A waiting login: who, whether it is new, the short scam reminder, and Allow / Deny.
struct RequestRow: View {
    let request: BroadcastState.Request
    let decide: (Bool) -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                Text(request.device.isEmpty ? L("A device") : request.device).font(.headline)
                Text(request.firstSeen ? L("New device") : L("Connected before"))
                    .font(.caption).padding(.horizontal, 6).padding(.vertical, 2)
                    .background((request.firstSeen ? Color.orange : Color.gray).opacity(0.2), in: Capsule())
                Spacer()
                TimelineView(.periodic(from: .now, by: 1)) { ctx in
                    Text(L("Refused in %d s", max(0, Int(request.deadline.timeIntervalSince(ctx.date).rounded(.up)))))
                        .font(.caption).foregroundStyle(.secondary).monospacedDigit()
                }
            }
            if !request.from.isEmpty { Text(request.from).font(.caption).foregroundStyle(.secondary) }
            Label(L("Did someone call and ask you to share your screen? Deny: that is a scam."), systemImage: "exclamationmark.triangle.fill")
                .font(.footnote).foregroundStyle(.orange)
            HStack {
                Button(L("Deny"), role: .destructive) { decide(false) }.buttonStyle(.bordered)
                Spacer()
                Button(L("Allow (view only)")) { decide(true) }.buttonStyle(.borderedProminent)
            }
        }
        .padding(.vertical, 4)
    }
}

/// The start button: the system's broadcast picker (it alone may start a
/// broadcast) set to RemoHop's extension, behind a button in the app's style.
struct BroadcastButton: View {
    let title: String
    @State private var picker = BroadcastPicker.make()

    var body: some View {
        Button {
            BroadcastPicker.tap(picker)
        } label: {
            Label(title, systemImage: "record.circle")
        }
        .background(BroadcastPickerHost(picker: picker).frame(width: 1, height: 1).opacity(0.01))
    }
}

enum BroadcastPicker {
    /// The extension's bundle ID: the app's plus ".broadcast".
    static var extensionID: String { (Bundle.main.bundleIdentifier ?? "com.remohop.app") + ".broadcast" }

    @MainActor
    static func make() -> RPSystemBroadcastPickerView {
        let p = RPSystemBroadcastPickerView(frame: CGRect(x: 0, y: 0, width: 44, height: 44))
        p.preferredExtension = extensionID
        p.showsMicrophoneButton = false
        return p
    }

    /// The picker has no API to open it: its own button is tapped.
    @MainActor
    static func tap(_ p: RPSystemBroadcastPickerView) {
        Diag.log("screen sharing: broadcast picker opened")
        for case let button as UIButton in p.subviews {
            button.sendActions(for: .touchUpInside)
            return
        }
    }
}

/// Keeps the picker in the view hierarchy (it presents from its window).
struct BroadcastPickerHost: UIViewRepresentable {
    let picker: RPSystemBroadcastPickerView
    func makeUIView(context: Context) -> RPSystemBroadcastPickerView { picker }
    func updateUIView(_ uiView: RPSystemBroadcastPickerView, context: Context) {}
}

/// The choices of "Reconnect without asking" (Core.setShareReadmit):
/// minutes, and `always` for as long as sharing runs (unattended).
enum ReadmitChoice {
    static let defaultMinutes = 10
    static let always = -1
    static let minutes = [10, 30, 60, 24 * 60, 7 * 24 * 60, 30 * 24 * 60, always]

    /// The choice that shows `m`: itself, or the nearest shorter one (a value set elsewhere).
    static func index(_ m: Int) -> Int {
        if m == always { return minutes.count - 1 }
        return minutes.indices.last { minutes[$0] != always && minutes[$0] <= m } ?? 0
    }

    static func label(_ m: Int, lang: String = L10n.effective) -> String {
        let t = { (en: String) in L10n.text(en, lang: lang) }
        switch m {
        case always: return t("Always (unattended)")
        case 60: return t("1 hour")
        case 24 * 60: return t("1 day")
        case let x where x % (24 * 60) == 0: return String(format: t("%d days"), x / (24 * 60))
        default: return String(format: t("%d minutes"), m)
        }
    }
}
