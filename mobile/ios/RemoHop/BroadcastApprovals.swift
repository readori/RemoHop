import Foundation
import SwiftUI
import UIKit
import UserNotifications

/// The app's half of screen sharing's approval handoff. The broadcast
/// extension posts a notification for each login; its actions come here
/// (iOS delivers notification responses to the app, launching it in the
/// background if needed, never to the extension). The answer becomes a
/// command file in the App Group plus a Darwin notification, which the
/// extension reads (see ApprovalBook and BroadcastStore).
///
/// Allow needs the phone unlocked; Deny and dismissing the notification
/// deny; tapping the notification opens Settings → Share this screen, which
/// lists the request with the same two buttons.
enum BroadcastApprovals {
    /// Posted (on the main thread) when the user tapped a request notification.
    static let openSharePage = Notification.Name("remohop.openSharePage")

    /// Handles a notification response; false when it is not about screen sharing.
    @discardableResult
    static func handle(action: String, category: String, userInfo: [AnyHashable: Any]) -> Bool {
        guard category == BroadcastStrings.category, let key = userInfo[BroadcastStrings.requestKey] as? String else { return false }
        switch action {
        case BroadcastStrings.allowAction:
            Diag.log("screen sharing: request allowed from the notification")
            decide(key: key, allow: true)
        case BroadcastStrings.denyAction, UNNotificationDismissActionIdentifier:
            Diag.log("screen sharing: request denied from the notification")
            decide(key: key, allow: false)
        default:
            DispatchQueue.main.async { NotificationCenter.default.post(name: openSharePage, object: nil) }
        }
        return true
    }

    static func decide(key: String, allow: Bool) {
        BroadcastStore.send(BroadcastCommand(kind: .decide, key: key, allow: allow))
    }

    /// The language the extension's texts use: the app's choice, mirrored
    /// into the App Group, and the action titles in it.
    static func languageChanged(_ choice: String) {
        AppGroup.defaults?.set(choice, forKey: AppGroup.languageKey)
        BroadcastStrings.registerCategory(lang: L10n.resolve(AppLanguage(rawValue: choice) ?? .system))
    }
}

/// The app delegate: only for notification responses, which must have a
/// delegate before launch ends.
final class AppDelegate: NSObject, UIApplicationDelegate, UNUserNotificationCenterDelegate {
    func application(_ application: UIApplication, didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]? = nil) -> Bool {
        UNUserNotificationCenter.current().delegate = self
        BroadcastApprovals.languageChanged(L10n.choice.rawValue)
        return true
    }

    /// A request while the app is open: shown as a banner too (the sharing
    /// page may not be on screen).
    func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification,
                                withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void) {
        completionHandler([.banner, .list, .sound])
    }

    func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse,
                                withCompletionHandler completionHandler: @escaping () -> Void) {
        let content = response.notification.request.content
        BroadcastApprovals.handle(action: response.actionIdentifier, category: content.categoryIdentifier, userInfo: content.userInfo)
        completionHandler()
    }
}

/// The sharing page's view of the extension: its state file, read again on
/// the extension's Darwin notification and once a second while shown (a
/// state that stops updating means the extension is gone).
@MainActor
final class BroadcastModel: ObservableObject {
    @Published private(set) var state = BroadcastState.idle()
    @Published private(set) var notificationsAllowed = true
    /// The password setting: the owner's own (else a new temporary one at
    /// every start). What is in force while sharing is `state.passwordFixed`.
    @Published private(set) var passwordFixed = false
    /// Whether the App Group is there (a build without it cannot share).
    let available = AppGroup.containerURL != nil
    private var note: DarwinNote.Observation?
    private var timer: Timer?

    func appear() {
        reload()
        loadPasswordSetting()
        note = DarwinNote.observe(DarwinNote.state) { [weak self] in
            Task { @MainActor in self?.reload() }
        }
        timer = Timer.scheduledTimer(withTimeInterval: 1, repeats: true) { [weak self] _ in
            Task { @MainActor in self?.reload() }
        }
        let center = UNUserNotificationCenter.current()
        center.requestAuthorization(options: [.alert, .sound]) { granted, _ in
            Task { @MainActor in self.notificationsAllowed = granted }
        }
    }

    func disappear() {
        note = nil
        timer?.invalidate()
        timer = nil
    }

    func reload() {
        let s = (BroadcastStore.readState() ?? .idle()).effective()
        if s != state { state = s }
    }

    var live: Bool { state.isLive() }

    func decide(_ r: BroadcastState.Request, allow: Bool) {
        Diag.log("screen sharing: request \(allow ? "allowed" : "denied") in the app")
        BroadcastApprovals.decide(key: r.key, allow: allow)
        state.requests.removeAll { $0.key == r.key }
    }

    func disconnect(_ viewer: Int) {
        Diag.log("screen sharing: disconnect \(viewer == 0 ? "all" : "a viewer")")
        BroadcastStore.send(BroadcastCommand(kind: .disconnect, viewer: viewer))
    }

    func stop() {
        Diag.log("screen sharing: stop from the app")
        BroadcastStore.send(BroadcastCommand(kind: .stop))
    }

    // MARK: the password setting

    func loadPasswordSetting() {
        let device = UIDevice.current.model
        let version = Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "dev"
        Task.detached {
            try? Core.initialize(deviceName: device, appVersion: version)
            let fixed = Core.sharePasswordFixed
            await MainActor.run { self.passwordFixed = fixed }
        }
    }

    /// Saves the owner's own password (`password`), or with "" goes back to
    /// a new temporary password at every start. The core keeps it in the
    /// configuration directory shared with the broadcast extension, which
    /// reads it when sharing starts; while sharing, the command ping makes
    /// the extension apply it at once (BroadcastHost's tick). `done` gets
    /// nil when saved, else the text to show.
    func savePassword(_ password: String, done: @escaping @MainActor (String?) -> Void) {
        let device = UIDevice.current.model
        let version = Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "dev"
        Task.detached {
            var failure: String?
            do {
                try Core.initialize(deviceName: device, appVersion: version)
                try Core.setSharePassword(password)
            } catch {
                // The core's own text is English: the app's words instead.
                failure = !password.isEmpty && !OwnPassword.isStrong(password)
                    ? L("Use at least 8 characters, with at least two of: letters, digits, other characters.")
                    : L("The password could not be saved.")
            }
            let fixed = Core.sharePasswordFixed
            await MainActor.run {
                self.passwordFixed = fixed
                if failure == nil {
                    Diag.log(password.isEmpty ? "screen sharing: back to a temporary password" : "screen sharing: own password set")
                    DarwinNote.post(DarwinNote.command)
                } else {
                    Diag.log("screen sharing: password setting not saved")
                }
                done(failure)
            }
        }
    }
}
