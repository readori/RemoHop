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
            if phase == .active { model.ensureGateway() }
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

    init() {
        ensureGateway()
    }

    func ensureGateway() {
        let device = UIDevice.current.model
        let version = Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "dev"
        Task.detached(priority: .userInitiated) {
            do {
                try Core.initialize(deviceName: device, appVersion: version)
                let url = try Core.gatewayURL()
                await MainActor.run {
                    if url != self.gatewayURL {
                        self.gatewayURL = url
                        self.loadID = UUID()
                    }
                    self.error = nil
                }
            } catch {
                await MainActor.run { self.error = error.localizedDescription }
            }
        }
    }
}
