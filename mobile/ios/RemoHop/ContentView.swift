import SwiftUI

struct ContentView: View {
    @EnvironmentObject private var model: AppModel
    /// The language setting (see L10n): read here so that a change redraws
    /// the screen and reaches the page.
    @AppStorage(L10n.key) private var language = AppLanguage.system.rawValue

    var body: some View {
        let lang = L10n.resolve(AppLanguage(rawValue: language) ?? .system)
        ZStack {
            Color(model.fullscreen ? .black : .systemBackground).ignoresSafeArea()
            if let url = model.gatewayURL {
                GatewayWebView(url: url, loadID: model.loadID, lang: lang, model: model)
                    // The web view handles the keyboard itself (the page
                    // follows the visual viewport) and draws under the status
                    // bar and home indicator, padding with the safe-area
                    // insets (viewport-fit=cover in app mode).
                    .ignoresSafeArea()
                if model.pageCover != .hidden {
                    // Never a blank web view: the logo until the page has
                    // loaded (again), or "Try again" when it could not be.
                    PageCoverView(failed: model.pageCover == .failed) { model.retryPage?() }
                        .transition(.opacity)
                        .zIndex(1)
                }
            } else if let error = model.error {
                VStack(spacing: 16) {
                    Text(L("RemoHop could not start: %@", error)).multilineTextAlignment(.center)
                    Button(L("Try again")) { model.ensureGateway() }
                }
                .padding()
            } else {
                PageCoverView(failed: false) {}
            }
        }
        .animation(.easeOut(duration: 0.2), value: model.pageCover)
        .environment(\.locale, L10n.locale(lang))
        .statusBarHidden(model.fullscreen)
        .persistentSystemOverlays(model.fullscreen ? .hidden : .automatic)
        // When Settings closes, the page hears what it did to the saved
        // hosts: it lists them again, or connects to the one chosen there.
        .sheet(isPresented: $model.showSettings, onDismiss: { model.settingsClosed() }) {
            SettingsView(initialPage: model.settingsPage) { target in model.pendingConnect = target }
        }
    }
}

/// What covers the web view (AppModel.pageCover).
enum PageCover: Equatable {
    case hidden, loading, failed

    init(_ phase: PageRecovery.Phase) {
        switch phase {
        case .ready: self = .hidden
        case .failed: self = .failed
        case .idle, .loading, .dead: self = .loading
        }
    }
}

/// The native cover over the web view: the RemoHop logo on the app's
/// background while the page loads, with "Try again" under it when loading
/// failed.
struct PageCoverView: View {
    let failed: Bool
    let retry: () -> Void

    var body: some View {
        ZStack {
            Color(.systemBackground).ignoresSafeArea()
            VStack(spacing: 24) {
                BreathingLogo(breathes: !failed)
                if failed {
                    Text(L("The remote-control page could not be loaded."))
                        .multilineTextAlignment(.center)
                        .foregroundStyle(.secondary)
                    Button(L("Try again"), action: retry)
                        .buttonStyle(.borderedProminent)
                }
            }
            .padding(32)
        }
    }
}

/// The logo, gently breathing (scale 0.94 to 1 and opacity 0.7 to 1, 1.6 s
/// each way); still when Reduce Motion is on or loading failed.
struct BreathingLogo: View {
    let breathes: Bool
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var inhaled = false

    var body: some View {
        let still = !breathes || reduceMotion
        Image("Logo")
            .resizable()
            .scaledToFit()
            .frame(width: 88, height: 88)
            .scaleEffect(still || inhaled ? 1 : 0.94)
            .opacity(still || inhaled ? 1 : 0.7)
            .accessibilityLabel(L("Loading"))
            .accessibilityHidden(!breathes)
            .onAppear {
                guard !still else { return }
                withAnimation(.easeInOut(duration: 1.6).repeatForever(autoreverses: true)) { inhaled = true }
            }
    }
}
