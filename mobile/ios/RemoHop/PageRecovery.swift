import Foundation

/// When the gateway page must be loaded again (the same rules as the
/// Android app's PageRecovery.kt), so that the user never sees a blank web
/// view: the web content process was killed (iOS does that to a suspended
/// app for memory), a load failed (the gateway's listener was closed while
/// the app was suspended), or a load never finished.
///
/// The controller reports what happened; this answers what to do. It holds
/// no URL (the gateway URL carries its token) and no timer: the controller
/// keeps those and passes `generation` back so that a late timer or probe of
/// an earlier load is ignored.
struct PageRecovery {
    enum Phase: Equatable {
        /// Nothing loaded yet.
        case idle
        /// A load the app started is under way.
        case loading
        /// The page finished loading and is shown.
        case ready
        /// The page is gone (process killed, load failed or interrupted):
        /// load it again, at the next return to the foreground if the app is
        /// in the background.
        case dead
        /// Loading failed `maxRetries` times in a row: the "Try again" screen.
        case failed
    }

    /// What to do when the app comes back to the foreground.
    enum Foreground: Equatable {
        case nothing
        /// Ask the page whether it is alive (`probeScript`, `probeOK`).
        case probe
        /// Make sure the gateway answers, then load the page again.
        case reload
    }

    /// What to do after a load failed or timed out.
    enum AfterFailure: Equatable {
        /// Not about a load the app is waiting for: nothing to do.
        case ignored
        /// In the background: reload at the next return to the foreground.
        case waitForForeground
        /// Load again after this many seconds.
        case retry(after: TimeInterval)
        /// Show the "Try again" screen.
        case giveUp
    }

    static let maxRetries = 3
    /// How long a probe of the page may take before it counts as dead.
    static let probeTimeout: TimeInterval = 3
    /// How long a load may take before it counts as failed.
    static let loadTimeout: TimeInterval = 20

    /// The script `probeOK` reads: origin, ready state and how much of the page there is.
    static let probeScript = "location.origin+'|'+document.readyState+'|'+(document.body?document.body.childElementCount:0)"

    private(set) var phase: Phase = .idle
    /// Failed loads in a row.
    private(set) var failures = 0
    /// Counts the app's loads.
    private(set) var generation = 0

    /// The native cover is shown (logo, or "Try again" when `phase` is failed)
    /// whenever the page is not ready.
    var covered: Bool { phase != .ready }

    /// The app starts loading the page.
    mutating func loadStarted() {
        phase = .loading
        generation += 1
    }

    /// The main frame finished loading.
    mutating func loadFinished() {
        phase = .ready
        failures = 0
    }

    /// The web content process died. Returns whether to reload now (in the
    /// foreground) rather than at the next return to the foreground.
    mutating func processTerminated(inBackground: Bool) -> Bool {
        phase = .dead
        return !inBackground
    }

    /// The app went to the background: a load under way may be cut off by
    /// the suspension, so it is done again on return.
    mutating func enteredBackground() {
        if phase == .loading { phase = .dead }
    }

    /// The app's load failed (a network error, not a cancellation).
    mutating func loadFailed(inBackground: Bool) -> AfterFailure {
        guard phase == .loading else { return .ignored }
        if inBackground {
            phase = .dead
            return .waitForForeground
        }
        failures += 1
        if failures > Self.maxRetries {
            phase = .failed
            return .giveUp
        }
        phase = .dead
        return .retry(after: Self.backoff(failures))
    }

    /// The check on the return to the foreground found the page of load
    /// `generation` dead. Returns whether to reload (no other load started
    /// meanwhile).
    @discardableResult
    mutating func probeFailed(generation: Int) -> Bool {
        guard generation == self.generation, phase == .ready else { return false }
        phase = .dead
        return true
    }

    /// The load timer of load `generation` fired.
    mutating func loadTimedOut(generation: Int, inBackground: Bool) -> AfterFailure {
        guard generation == self.generation else { return .ignored }
        return loadFailed(inBackground: inBackground)
    }

    /// Whether the retry timer of load `generation` should still load again
    /// (nothing else reloaded meanwhile).
    func retryDue(generation: Int) -> Bool {
        generation == self.generation && phase == .dead
    }

    func foregroundAction() -> Foreground {
        switch phase {
        case .idle, .loading: return .nothing
        case .ready: return .probe
        case .dead, .failed: return .reload
        }
    }

    /// The user tapped "Try again", or came back to the app: a fresh round of retries.
    mutating func retryNow() {
        failures = 0
    }

    /// Seconds before retry number `failure` (1, 2, 4, then at most 8).
    static func backoff(_ failure: Int) -> TimeInterval {
        min(8, pow(2, Double(max(0, failure - 1))))
    }

    /// Whether a failed navigation means the page did not load: a load
    /// replaced by another one (NSURLErrorCancelled) or stopped by the
    /// navigation policy (WebKit's frame load interrupted, a download the
    /// app handles) does not.
    static func isRealFailure(domain: String, code: Int) -> Bool {
        if domain == NSURLErrorDomain && code == NSURLErrorCancelled { return false }
        if domain == "WebKitErrorDomain" && code == 102 { return false }
        return true
    }

    /// Whether `probeScript`'s result shows a live page of the gateway at
    /// `origin`: the right origin, parsed, and with content.
    static func probeOK(_ result: String?, origin: String) -> Bool {
        guard let parts = result?.split(separator: "|", omittingEmptySubsequences: false), parts.count == 3 else { return false }
        return String(parts[0]) == origin && parts[1] != "loading" && (Int(parts[2]) ?? 0) > 0
    }
}
