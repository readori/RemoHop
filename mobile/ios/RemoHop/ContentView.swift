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
                    // follows the visual viewport); full screen also covers
                    // the status bar and home indicator areas.
                    .ignoresSafeArea(model.fullscreen ? .all : .keyboard)
            } else if let error = model.error {
                VStack(spacing: 16) {
                    Text(L("RemoHop could not start: %@", error)).multilineTextAlignment(.center)
                    Button(L("Try again")) { model.ensureGateway() }
                }
                .padding()
            } else {
                ProgressView()
            }
        }
        .environment(\.locale, L10n.locale(lang))
        .statusBarHidden(model.fullscreen)
        .persistentSystemOverlays(model.fullscreen ? .hidden : .automatic)
        .sheet(isPresented: $model.showSettings) {
            SettingsView()
        }
    }
}
