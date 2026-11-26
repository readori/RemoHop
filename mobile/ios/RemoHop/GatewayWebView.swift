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
///
/// The page is never left blank (see PageRecovery): a native cover (the
/// logo, or "Try again") hides the web view until the page has loaded, and
/// the page is loaded again when its content process died, a load failed or
/// never finished, or a check on the return to the foreground finds it dead.
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
        private var recovery = PageRecovery()
        /// A reload is under way after the page was lost (for the log).
        private var recovering = false
        /// The probe of the page waiting for its answer, if any, and what gets it.
        private var pendingProbe: Int?
        private var probeDone: (@MainActor (Bool) -> Void)?
        private var probes = 0

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
            // The page draws under the status bar and home indicator itself
            // (viewport-fit=cover and env(safe-area-inset-*) in app mode).
            web.scrollView.contentInsetAdjustmentBehavior = .never
            web.allowsLinkPreview = false
            web.isOpaque = false
            #if DEBUG
            if #available(iOS 16.4, *) { web.isInspectable = true }
            #endif
            self.web = web
            model.runOnPage = { [weak web] js in web?.evaluateJavaScript(js, completionHandler: nil) }
            model.onForeground = { [weak self] up in self?.foreground(gatewayUp: up) }
            model.onBackground = { [weak self] in self?.background() }
            model.retryPage = { [weak self] in self?.userRetry() }
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
            recovery.retryNow()
            startLoad()
        }

        // ---- recovery: the page is never left blank ----

        private var inBackground: Bool { UIApplication.shared.applicationState == .background }

        /// Loads the gateway page, covered until it has finished; a load that
        /// does not finish in time counts as failed.
        private func startLoad() {
            guard let web, let url = gatewayURL else { return }
            recovery.loadStarted()
            publishCover()
            let gen = recovery.generation
            web.load(URLRequest(url: GatewayURLs.withLang(url, lang)))
            Task { @MainActor [weak self] in
                try? await Task.sleep(nanoseconds: UInt64(PageRecovery.loadTimeout * 1_000_000_000))
                guard let self else { return }
                self.handle(self.recovery.loadTimedOut(generation: gen, inBackground: self.inBackground), "page load timed out")
            }
        }

        /// Loads the page again after it was lost: first makes sure the
        /// gateway answers (its listener may have been closed while the app
        /// was suspended), unless that was just checked.
        private func recover(_ reason: String, gatewayUp: Bool = false) {
            Diag.log(reason)
            recovering = true
            publishCover()
            if gatewayUp { startLoad(); return }
            model.ensureGateway { [weak self] ok in
                guard let self else { return }
                // A new gateway URL is on its way to load(), which loads it.
                guard self.model.gatewayURL == self.gatewayURL else { return }
                if ok {
                    self.startLoad()
                } else {
                    self.recovery.loadStarted()
                    self.handle(self.recovery.loadFailed(inBackground: self.inBackground), "gateway did not answer")
                }
            }
        }

        /// What to do after a failed or timed-out load.
        private func handle(_ after: PageRecovery.AfterFailure, _ what: String) {
            switch after {
            case .ignored:
                return
            case .waitForForeground:
                Diag.log("\(what); reloading on return to the app")
            case .retry(let seconds):
                Diag.log("\(what); retry \(recovery.failures) of \(PageRecovery.maxRetries) in \(Int(seconds)) s")
                let gen = recovery.generation
                Task { @MainActor [weak self] in
                    try? await Task.sleep(nanoseconds: UInt64(seconds * 1_000_000_000))
                    // In the background the page stays dead: the return to the foreground reloads it.
                    guard let self, self.recovery.retryDue(generation: gen), !self.inBackground else { return }
                    self.recover("page load retry")
                }
            case .giveUp:
                recovering = false
                Diag.log("\(what); showing Try again")
            }
            publishCover()
        }

        /// Back in the foreground, after the gateway check (`gatewayUp`): a
        /// dead page is loaded again, a shown one is asked whether it is alive.
        private func foreground(gatewayUp: Bool) {
            // A new gateway URL is on its way to load(), which loads it.
            guard model.gatewayURL == gatewayURL else { return }
            switch recovery.foregroundAction() {
            case .nothing:
                return
            case .reload:
                let failed = recovery.phase == .failed
                recovery.retryNow()
                recover(failed ? "foreground check: page failed to load, reloading" : "foreground check: page dead, reloading",
                        gatewayUp: gatewayUp)
            case .probe:
                guard gatewayUp else {
                    recovery.probeFailed(generation: recovery.generation)
                    recover("foreground check: gateway did not answer, reloading")
                    return
                }
                let gen = recovery.generation
                probe { [weak self] alive in
                    guard let self, !alive, self.recovery.probeFailed(generation: gen) else { return }
                    self.recover("foreground check: page dead, reloading", gatewayUp: true)
                }
            }
        }

        private func background() {
            recovery.enteredBackground()
            publishCover()
        }

        /// "Try again" on the cover.
        private func userRetry() {
            recovery.retryNow()
            recover("page reload requested (Try again)")
        }

        /// Asks the page whether it is alive; no answer in time counts as dead.
        private func probe(_ done: @escaping @MainActor (Bool) -> Void) {
            guard let web, let origin else { done(false); return }
            probes += 1
            let id = probes
            pendingProbe = id
            probeDone = done
            web.evaluateJavaScript(PageRecovery.probeScript) { [weak self] result, _ in
                self?.probeAnswered(id, alive: PageRecovery.probeOK(result as? String, origin: origin))
            }
            Task { @MainActor [weak self] in
                try? await Task.sleep(nanoseconds: UInt64(PageRecovery.probeTimeout * 1_000_000_000))
                self?.probeAnswered(id, alive: false)
            }
        }

        /// The first answer to probe `id` (the page's, or the timeout's) counts.
        private func probeAnswered(_ id: Int, alive: Bool) {
            guard pendingProbe == id, let done = probeDone else { return }
            pendingProbe = nil
            probeDone = nil
            done(alive)
        }

        /// Shows the recovery state on the native cover, outside the view
        /// update that may have started a load.
        private func publishCover() {
            Task { @MainActor [weak self] in
                guard let self else { return }
                let cover = PageCover(self.recovery.phase)
                if self.model.pageCover != cover { self.model.pageCover = cover }
            }
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
            let handledLater = bridge?.handleAsync(body: message.body, sourceOrigin: source, isMainFrame: message.frameInfo.isMainFrame) { result in
                switch result {
                case .success(let obj): reply(obj, nil)
                case .failure(let e): reply(nil, e.message)
                }
            }
            if handledLater == true { return }
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

        func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {
            if recovering { Diag.log("page loaded again") }
            recovering = false
            recovery.loadFinished()
            publishCover()
        }

        func webView(_ webView: WKWebView, didFailProvisionalNavigation navigation: WKNavigation!, withError error: Error) {
            navigationFailed(error)
        }

        func webView(_ webView: WKWebView, didFail navigation: WKNavigation!, withError error: Error) {
            navigationFailed(error)
        }

        /// The main frame did not load (only its errors come here): load it
        /// again a few times, then show "Try again". The error's text is not
        /// logged (it may name the URL).
        private func navigationFailed(_ error: Error) {
            let e = error as NSError
            guard PageRecovery.isRealFailure(domain: e.domain, code: e.code) else { return }
            handle(recovery.loadFailed(inBackground: inBackground), "page load failed (\(e.domain) \(e.code))")
        }

        func webViewWebContentProcessDidTerminate(_ webView: WKWebView) {
            Diag.log("web content process terminated")
            // The page that asked for full screen and a screen kept on is
            // gone; the reloaded one starts in the normal layout.
            setState(keepScreenOn: false, fullscreen: false)
            // The content process was killed (memory, often while the app
            // was suspended): load the page again now, or when the app is
            // back in front. A load started in the background may never
            // finish, and the gateway's listener is closed until then.
            if recovery.processTerminated(inBackground: inBackground) {
                recover("page reloaded after termination")
            } else {
                Diag.log("page will reload on return to the app")
                publishCover()
            }
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

        func openSettings() { model.settingsPage = nil; model.showSettings = true }

        func openSettings(page: String) { model.settingsPage = SettingsPage(bridge: page); model.showSettings = true }

        func diagLog(_ text: String) { Diag.logPage(text) }

        func ensureGateway(done: @escaping @MainActor (Bool) -> Void) { model.ensureGateway(done: done) }

        func showDialog(_ request: DialogRequest, done: @escaping @MainActor (DialogAnswer) -> Void) {
            NativeDialog.present(request, done: done)
        }
    }
}

/// The page's dialogs as the system's own (HIG): an alert with a Cancel
/// and a confirming button (red when it ends something) for a
/// confirmation, an alert with a text field for a prompt, and an action
/// sheet for a few actions. Each answers exactly once.
@MainActor
enum NativeDialog {
    static func present(_ r: DialogRequest, done: @escaping @MainActor (DialogAnswer) -> Void) {
        var answered = false
        let finish: @MainActor (DialogAnswer) -> Void = { a in
            if answered { return }
            answered = true
            done(a)
        }
        let title = r.title.isEmpty ? nil : r.title, message = r.message.isEmpty ? nil : r.message
        let ac: UIAlertController
        switch r.kind {
        case .confirm:
            ac = UIAlertController(title: title, message: message, preferredStyle: .alert)
            ac.addAction(UIAlertAction(title: r.cancel, style: .cancel) { _ in finish(.cancelled) })
            let ok = UIAlertAction(title: r.confirm, style: r.destructive ? .destructive : .default) { _ in finish(.confirmed(text: "")) }
            ac.addAction(ok)
            // A safe choice stays the bold one; a destructive one is never preferred.
            if !r.destructive { ac.preferredAction = ok }
        case .prompt:
            ac = UIAlertController(title: title, message: message, preferredStyle: .alert)
            ac.addTextField { f in
                f.text = r.value
                f.placeholder = r.placeholder
                f.clearButtonMode = .whileEditing
                f.autocorrectionType = .no
                f.returnKeyType = .done
            }
            ac.addAction(UIAlertAction(title: r.cancel, style: .cancel) { _ in finish(.cancelled) })
            let ok = UIAlertAction(title: r.confirm, style: .default) { [weak ac] _ in
                finish(.confirmed(text: ac?.textFields?.first?.text ?? ""))
            }
            ac.addAction(ok)
            ac.preferredAction = ok
        case .actions:
            ac = UIAlertController(title: title, message: message, preferredStyle: .actionSheet)
            for (i, a) in r.actions.enumerated() {
                ac.addAction(UIAlertAction(title: a.label, style: a.destructive ? .destructive : .default) { _ in finish(.action(i)) })
            }
            ac.addAction(UIAlertAction(title: r.cancel, style: .cancel) { _ in finish(.cancelled) })
        }
        guard Presenter.top() != nil else { finish(.cancelled); return }
        Presenter.present(ac)
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
