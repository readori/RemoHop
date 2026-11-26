import ReplayKit
import SwiftUI
import UIKit

/// Settings → Share this screen: view-only screen sharing through the
/// broadcast extension. Only the essentials are on the page (status, the
/// start button, ID and password with Copy, requests, viewers); every
/// explanation, the scam warning included, is behind the ⓘ. Each request
/// keeps a short scam reminder of its own.
@MainActor
struct ShareScreenView: View {
    @StateObject private var b = BroadcastModel()
    @State private var copied: String?
    @State private var confirmStop = false

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
            if live {
                Section {
                    if !s.id.isEmpty { copyRow(L("ID"), s.id, mono: true) }
                    ForEach(s.id.isEmpty ? s.addresses : [], id: \.self) { a in copyRow(L("Address"), a, mono: true) }
                    if !s.password.isEmpty { copyRow(L("Password"), s.password, mono: true) }
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
        .onAppear { b.appear() }
        .onDisappear { b.disappear() }
    }

    /// The ⓘ: what sharing is, how it starts and ends, what viewers see, and the scam warning.
    static var info: [InfoItem] {
        [
            InfoItem(title: L("View only"), text: L("Viewers see this screen and nothing else. They cannot tap, type or open anything on this device, and no sound is shared.")),
            InfoItem(title: L("Start and stop"), text: L("Sharing starts only when you tap Start sharing and then Start Broadcast. Stop it any time from the red status bar or Control Center, or with Stop sharing here.")),
            InfoItem(title: L("Who can connect"), text: L("Someone with the ID and the temporary password shown here. You are asked every time, in a notification and on this page; a request you do not answer is refused after a minute. The password is new each time sharing starts.")),
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
