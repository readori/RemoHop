import Network
import SwiftUI
import UIKit

@main
struct RemoHopApp: App {
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
            // the gateway re-establishes host sessions that dropped.
            switch phase {
            case .active: model.ensureGateway()
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
    }

    private func networkChanged(_ desc: String) {
        defer { lastPath = desc }
        guard let last = lastPath, last != desc, desc != "none" else { return }
        gatewayQueue.async { Core.checkSessions() }
    }

    /// The app went to the background. iOS suspends it within seconds
    /// unless it asks for time: it asks (about 30 seconds) when a host
    /// session exists, so that copying a password from another app does not
    /// drop the session, and closes the gateway's listener when the time is
    /// up or right away when there is nothing to keep (Apple's TN2277). No
    /// background mode is used: once suspended, the sessions drop, and the
    /// gateway re-establishes them on return.
    func enterBackground() {
        endBackgroundTask()
        gatewayQueue.async {
            let keep = Core.hasSessions
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
