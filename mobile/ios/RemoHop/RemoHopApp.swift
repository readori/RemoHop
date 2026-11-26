import Network
import SwiftUI
import UIKit

@main
struct RemoHopApp: App {
    @UIApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate
    @StateObject private var model = AppModel()
    @Environment(\.scenePhase) private var scenePhase

    var body: some Scene {
        WindowGroup {
            ContentView()
                .environmentObject(model)
        }
        .onChange(of: scenePhase) { phase in
            // iOS takes a suspended app's sockets away. On the way into the
            // background the app asks for time to finish (so that a short
            // trip to another app keeps the host sessions), and closes the
            // gateway's listener when that time is up; on the way back it
            // checks (reopens) the listener before the page needs it, and
            // the gateway re-establishes host sessions that dropped. Then
            // the web view checks its page (it may have been killed or left
            // half loaded meanwhile) and loads it again if needed.
            switch phase {
            case .active: model.enterForeground()
            case .background: model.enterBackground()
            default: break
            }
        }
    }
}

/// App state shared by the web view and the native screens.
@MainActor
final class AppModel: ObservableObject {
    /// The gateway URL with its token: handed to the web view only.
    @Published private(set) var gatewayURL: URL?
    /// Changes when the page must be loaded again (a new gateway).
    @Published private(set) var loadID = UUID()
    @Published var error: String?
    @Published var fullscreen = false
    @Published var showSettings = false
    /// The Settings page to open on (the page asked for one), or nil.
    @Published var settingsPage: SettingsPage?
    /// A saved host chosen with Settings → Saved hosts → Connect.
    var pendingConnect: String?
    /// Runs a script in the loaded page (set by the web view's coordinator).
    var runOnPage: ((String) -> Void)?
    /// What covers the web view: nothing, the logo while the page loads, or
    /// "Try again" (set by the web view's coordinator, see PageRecovery).
    @Published var pageCover: PageCover = .loading
    /// Back in the foreground, after the gateway check (whether it answers):
    /// the coordinator checks its page. Set by the coordinator.
    var onForeground: (@MainActor (Bool) -> Void)?
    /// On the way into the background (set by the coordinator).
    var onBackground: (@MainActor () -> Void)?
    /// "Try again" on the cover (set by the coordinator).
    var retryPage: (@MainActor () -> Void)?

    /// Settings closed: the page connects to the host chosen there, or lists
    /// the saved hosts again (they may have been renamed or forgotten).
    func settingsClosed() {
        if let t = pendingConnect, let data = try? JSONEncoder().encode(t), let q = String(data: data, encoding: .utf8) {
            pendingConnect = nil
            runOnPage?("window.remohopMobile && remohopMobile.connect && remohopMobile.connect(\(q))")
        } else {
            runOnPage?("window.remohopMobile && remohopMobile.peersChanged && remohopMobile.peersChanged()")
        }
    }

    /// Gateway calls run here in the order they were made (pausing on the
    /// way into the background must not overtake the check on the way back).
    private let gatewayQueue = DispatchQueue(label: "com.remohop.gateway", qos: .userInitiated)

    /// Background time asked for on the way into the background.
    private var backgroundTask: UIBackgroundTaskIdentifier = .invalid
    /// Network changes: sessions on the old network are checked at once.
    private let pathMonitor = NWPathMonitor()
    private var lastPath: String?

    init() {
        ensureGateway()
        pathMonitor.pathUpdateHandler = { [weak self] path in
            let desc = path.status == .satisfied ? path.availableInterfaces.map(\.name).joined(separator: ",") : "none"
            Task { @MainActor in self?.networkChanged(desc) }
        }
        pathMonitor.start(queue: DispatchQueue(label: "com.remohop.path"))
        // A screen-sharing request notification was tapped: its page.
        sharePageObserver = NotificationCenter.default.addObserver(forName: BroadcastApprovals.openSharePage, object: nil, queue: .main) { [weak self] _ in
            Task { @MainActor in
                self?.settingsPage = .shareScreen
                self?.showSettings = true
            }
        }
    }

    private var sharePageObserver: NSObjectProtocol?

    private func networkChanged(_ desc: String) {
        defer { lastPath = desc }
        guard let last = lastPath, last != desc, desc != "none" else { return }
        gatewayQueue.async { Core.checkSessions() }
    }

    /// The app went to the background. iOS suspends it within seconds
    /// unless it asks for time: it asks (about 30 seconds) when a host
    /// session or a connection attempt exists, so that copying a password from another app does not
    /// drop the session, and closes the gateway's listener when the time is
    /// up or right away when there is nothing to keep (Apple's TN2277). No
    /// background mode is used: once suspended, the sessions drop, and the
    /// gateway re-establishes them on return.
    func enterBackground() {
        onBackground?()
        endBackgroundTask()
        gatewayQueue.async {
            // A session, or an attempt waiting for the host's owner to approve.
            let keep = Core.busy
            Task { @MainActor in
                // Back in front meanwhile: nothing to do (ensureGateway ran).
                guard UIApplication.shared.applicationState == .background else { return }
                guard keep else { self.pauseGateway(); return }
                self.backgroundTask = UIApplication.shared.beginBackgroundTask(withName: "RemoHop sessions") { [weak self] in
                    // Time is up: close the listener before iOS suspends us.
                    Core.pauseGateway()
                    self?.endBackgroundTask() // on the main thread, before the handler returns
                }
                if self.backgroundTask == .invalid { self.pauseGateway() }
            }
        }
    }

    private func endBackgroundTask() {
        guard backgroundTask != .invalid else { return }
        UIApplication.shared.endBackgroundTask(backgroundTask)
        backgroundTask = .invalid
    }

    /// The app is active again: the gateway is checked (reopened), then the
    /// page (the coordinator reloads it if it is dead).
    func enterForeground() {
        ensureGateway { [weak self] up in self?.onForeground?(up) }
    }

    /// Makes sure the gateway answers (starting it, or reopening its
    /// listener) and loads the page again only if its URL changed. `done`
    /// (on the main actor) gets whether the gateway is up; the page's
    /// "gateway.ensure" bridge call waits for it.
    func ensureGateway(done: (@MainActor (Bool) -> Void)? = nil) {
        endBackgroundTask()
        let device = UIDevice.current.model
        let version = Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "dev"
        gatewayQueue.async {
            let result = Result { () throws -> URL in
                try Core.initialize(deviceName: device, appVersion: version)
                return try Core.gatewayURL()
            }
            Task { @MainActor in
                switch result {
                case .success(let url):
                    if url != self.gatewayURL {
                        self.gatewayURL = url
                        self.loadID = UUID()
                    }
                    self.error = nil
                    done?(true)
                case .failure(let error):
                    self.error = error.localizedDescription
                    done?(false)
                }
            }
        }
    }

    /// The app is going to the background: close the gateway's listener.
    func pauseGateway() {
        gatewayQueue.async { Core.pauseGateway() }
    }
}
