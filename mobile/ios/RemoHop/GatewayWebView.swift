import SwiftUI
import UIKit
import WebKit

/// The controller: a WKWebView on the in-process gateway's loopback page,
/// with remohop-mobile.js injected at document start and a native bridge for
/// what a web view does badly: clipboard, downloads, keep-screen-on, full
/// screen, the keyboard and haptics. File uploads use WebKit's own picker
/// (photo library, camera, Files).
///
/// The gateway URL carries its access token: it goes to `load` and nowhere
/// else. Only its origin is kept, to scope navigation and the bridge.
///
/// The page shows the app's language ("zh" or "en", see L10n): it is loaded
/// with ?lang=, and a change of the setting is passed to the running page
/// (remohopMobile.setLang) without reloading it, so the sessions stay.
struct GatewayWebView: UIViewRepresentable {
    let url: URL
    let loadID: UUID
    let lang: String
    let model: AppModel

    func makeCoordinator() -> Coordinator { Coordinator(model: model) }

    func makeUIView(context: Context) -> WKWebView { context.coordinator.makeWebView() }

    func updateUIView(_ web: WKWebView, context: Context) {
        context.coordinator.load(url, loadID: loadID, lang: lang)
    }

    static func dismantleUIView(_ web: WKWebView, coordinator: Coordinator) {
        web.configuration.userContentController.removeAllScriptMessageHandlers()
    }

    @MainActor
    final class Coordinator: NSObject, WKNavigationDelegate, WKUIDelegate, BridgeHost {
        private let model: AppModel
        private weak var web: WKWebView?
        private var origin: String?
        private var bridge: Bridge?
        private var loaded: (URL, UUID)?
        private var gatewayURL: URL?
        private var lang = "en"

        init(model: AppModel) { self.model = model }

        func makeWebView() -> WKWebView {
            let config = WKWebViewConfiguration()
            // Nothing persists: the loopback page needs no cookies or caches.
            config.websiteDataStore = .nonPersistent()
            config.preferences.javaScriptCanOpenWindowsAutomatically = false
            config.dataDetectorTypes = []
            config.allowsInlineMediaPlayback = true
            let ucc = config.userContentController
            if let file = Bundle.main.url(forResource: "remohop-mobile", withExtension: "js"),
               let js = try? String(contentsOf: file, encoding: .utf8) {
                ucc.addUserScript(WKUserScript(source: js, injectionTime: .atDocumentStart, forMainFrameOnly: true, in: .page))
            }
            ucc.addScriptMessageHandler(WeakHandler(self), contentWorld: .page, name: "remohop")
            let web = WKWebView(frame: .zero, configuration: config)
            web.navigationDelegate = self
            web.uiDelegate = self
            web.allowsBackForwardNavigationGestures = false
            web.allowsLinkPreview = false
            web.isOpaque = false
            #if DEBUG
            if #available(iOS 16.4, *) { web.isInspectable = true }
            #endif
            self.web = web
            return web
        }

        func load(_ url: URL, loadID: UUID, lang: String) {
            if let l = loaded, l.0 == url, l.1 == loadID {
                setLang(lang)
                return
            }
            guard let web, let o = GatewayURLs.origin(url.absoluteString) else { return }
            loaded = (url, loadID)
            gatewayURL = url
            origin = o
            self.lang = lang
            bridge = Bridge(host: self, origin: o)
            web.load(URLRequest(url: GatewayURLs.withLang(url, lang)))
        }

        /// Tells the loaded page about a new language.
        private func setLang(_ lang: String) {
            guard lang != self.lang, lang == "zh" || lang == "en" else { return }
            self.lang = lang
            web?.evaluateJavaScript("window.remohopMobile && window.remohopMobile.setLang('\(lang)')", completionHandler: nil)
        }

        // ---- script messages (through WeakHandler) ----

        func receive(_ message: WKScriptMessage, reply: @escaping @MainActor @Sendable (Any?, String?) -> Void) {
            let so = message.frameInfo.securityOrigin
            let source = Bridge.originString(protocol: so.protocol, host: so.host, port: so.port)
            let async = bridge?.handleAsync(body: message.body, sourceOrigin: source, isMainFrame: message.frameInfo.isMainFrame) { result in
                switch result {
                case .success(let obj): reply(obj, nil)
                case .failure(let e): reply(nil, e.message)
                }
            }
            if async == true { return }
            guard let result = bridge?.handle(body: message.body, sourceOrigin: source, isMainFrame: message.frameInfo.isMainFrame) else {
                reply(nil, "refused")
                return
            }
            switch result {
            case .success(let obj): reply(obj, nil)
            case .failure(let e): reply(nil, e.message)
            }
        }

        // ---- navigation: the gateway's origin only ----

        func webView(_ webView: WKWebView, decidePolicyFor action: WKNavigationAction,
                     decisionHandler: @escaping @MainActor @Sendable (WKNavigationActionPolicy) -> Void) {
            let u = action.request.url?.absoluteString ?? ""
            guard let o = origin, GatewayURLs.sameOrigin(u, o) else {
                decisionHandler(u == "about:blank" ? .allow : .cancel)
                return
            }
            if GatewayURLs.isDownload(u, o), let url = action.request.url {
                // A download the page did not hand over itself.
                decisionHandler(.cancel)
                let name = URLComponents(string: u)?.queryItems?.first(where: { $0.name == "path" })?.value
                download(url: url, name: GatewayURLs.safeFileName(name))
                return
            }
            decisionHandler(.allow)
        }

        func webViewWebContentProcessDidTerminate(_ webView: WKWebView) {
            // The content process was killed (memory): load the page again.
            if let url = gatewayURL { webView.load(URLRequest(url: GatewayURLs.withLang(url, lang))) }
        }

        @available(iOS 15.0, *)
        func webView(_ webView: WKWebView, requestMediaCapturePermissionFor origin: WKSecurityOrigin,
                     initiatedByFrame frame: WKFrameInfo, type: WKMediaCaptureType,
                     decisionHandler: @escaping @MainActor @Sendable (WKPermissionDecision) -> Void) {
            decisionHandler(.deny)
        }

        // ---- BridgeHost ----

        func clipboardRead() -> String { UIPasteboard.general.string ?? "" }

        func clipboardWrite(_ text: String) { UIPasteboard.general.string = text }

        func download(url: URL, name: String) {
            guard let web else { return }
            DownloadSaver.save(url: url, name: name, from: web)
        }

        func setState(keepScreenOn: Bool, fullscreen: Bool) {
            UIApplication.shared.isIdleTimerDisabled = keepScreenOn
            if model.fullscreen != fullscreen { model.fullscreen = fullscreen }
        }

        func keyboard(show: Bool) {
            // Showing needs the page's own focus() inside the tap; hiding is ours.
            if !show { web?.endEditing(true) }
        }

        func haptic() { UIImpactFeedbackGenerator(style: .medium).impactOccurred() }

        func openSettings() { model.showSettings = true }

        func ensureGateway(done: @escaping @MainActor (Bool) -> Void) { model.ensureGateway(done: done) }
    }
}

/// WKUserContentController retains its handlers; this keeps it from
/// retaining the coordinator (and through it the web view).
@MainActor
private final class WeakHandler: NSObject, WKScriptMessageHandlerWithReply {
    weak var target: GatewayWebView.Coordinator?

    init(_ target: GatewayWebView.Coordinator) { self.target = target }

    func userContentController(_ userContentController: WKUserContentController, didReceive message: WKScriptMessage,
                               replyHandler: @escaping @MainActor @Sendable (Any?, String?) -> Void) {
        guard let target else { replyHandler(nil, "gone"); return }
        target.receive(message, reply: replyHandler)
    }
}
