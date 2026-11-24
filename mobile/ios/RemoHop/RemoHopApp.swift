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
            // iOS takes a suspended app's listening socket away, so the
            // gateway's is closed on the way into the background and
            // checked (reopened) on the way back, before the page needs it.
            switch phase {
            case .active: model.ensureGateway()
            case .background: model.pauseGateway()
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

    /// Gateway calls run here in the order they were made (pausing on the
    /// way into the background must not overtake the check on the way back).
    private let gatewayQueue = DispatchQueue(label: "com.remohop.gateway", qos: .userInitiated)

    init() {
        ensureGateway()
    }

    /// Makes sure the gateway answers (starting it, or reopening its
    /// listener) and loads the page again only if its URL changed. `done`
    /// (on the main actor) gets whether the gateway is up; the page's
    /// "gateway.ensure" bridge call waits for it.
    func ensureGateway(done: (@MainActor (Bool) -> Void)? = nil) {
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
