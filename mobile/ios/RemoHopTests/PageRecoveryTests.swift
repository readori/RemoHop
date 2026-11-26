import XCTest
@testable import RemoHop

final class PageRecoveryTests: XCTestCase {
    let o = "http://127.0.0.1:41234"

    func testCoveredUntilTheFirstLoadFinishes() {
        var r = PageRecovery()
        XCTAssertTrue(r.covered)
        XCTAssertEqual(r.foregroundAction(), .nothing)
        r.loadStarted()
        XCTAssertTrue(r.covered)
        XCTAssertEqual(r.foregroundAction(), .nothing)
        r.loadFinished()
        XCTAssertFalse(r.covered)
        XCTAssertEqual(r.foregroundAction(), .probe)
    }

    func testTerminatedInTheForegroundReloadsAtOnce() {
        var r = PageRecovery()
        r.loadStarted()
        r.loadFinished()
        XCTAssertTrue(r.processTerminated(inBackground: false))
        XCTAssertTrue(r.covered)
    }

    func testTerminatedInTheBackgroundReloadsOnReturn() {
        var r = PageRecovery()
        r.loadStarted()
        r.loadFinished()
        XCTAssertFalse(r.processTerminated(inBackground: true))
        XCTAssertEqual(r.phase, .dead)
        XCTAssertTrue(r.covered)
        XCTAssertEqual(r.foregroundAction(), .reload)
    }

    func testALoadCutOffByTheBackgroundIsDoneAgain() {
        var r = PageRecovery()
        r.loadStarted()
        r.enteredBackground()
        XCTAssertEqual(r.foregroundAction(), .reload)
        // A shown page stays shown.
        var s = PageRecovery()
        s.loadStarted()
        s.loadFinished()
        s.enteredBackground()
        XCTAssertEqual(s.foregroundAction(), .probe)
    }

    func testFailuresRetryWithBackoffThenGiveUp() {
        var r = PageRecovery()
        for (i, wait) in [1.0, 2, 4].enumerated() {
            r.loadStarted()
            XCTAssertEqual(r.loadFailed(inBackground: false), .retry(after: wait), "failure \(i + 1)")
            XCTAssertTrue(r.retryDue(generation: r.generation))
        }
        r.loadStarted()
        XCTAssertEqual(r.loadFailed(inBackground: false), .giveUp)
        XCTAssertEqual(r.phase, .failed)
        XCTAssertTrue(r.covered)
        // Back in the foreground (or Try again): a fresh round.
        XCTAssertEqual(r.foregroundAction(), .reload)
        r.retryNow()
        r.loadStarted()
        XCTAssertEqual(r.loadFailed(inBackground: false), .retry(after: 1))
        r.loadStarted()
        r.loadFinished()
        XCTAssertEqual(r.failures, 0)
    }

    func testAFailureInTheBackgroundWaitsForTheForeground() {
        var r = PageRecovery()
        r.loadStarted()
        XCTAssertEqual(r.loadFailed(inBackground: true), .waitForForeground)
        XCTAssertEqual(r.failures, 0)
        XCTAssertEqual(r.foregroundAction(), .reload)
    }

    func testLateEventsOfAnEarlierLoadAreIgnored() {
        var r = PageRecovery()
        r.loadStarted()
        let first = r.generation
        r.loadStarted()
        XCTAssertEqual(r.loadTimedOut(generation: first, inBackground: false), .ignored)
        XCTAssertEqual(r.phase, .loading)
        XCTAssertEqual(r.loadTimedOut(generation: r.generation, inBackground: false), .retry(after: 1))
        // A retry timer is stale once another load started.
        let failed = r.generation
        r.loadStarted()
        XCTAssertFalse(r.retryDue(generation: failed))
        // A failure of a page that is shown (a navigation the page started) leaves it.
        r.loadFinished()
        XCTAssertEqual(r.loadFailed(inBackground: false), .ignored)
        XCTAssertEqual(r.phase, .ready)
    }

    func testProbeFailureOnlyCountsForTheShownPage() {
        var r = PageRecovery()
        r.loadStarted()
        r.loadFinished()
        let gen = r.generation
        XCTAssertTrue(r.probeFailed(generation: gen))
        XCTAssertEqual(r.phase, .dead)
        XCTAssertFalse(r.probeFailed(generation: gen))
        r.loadStarted()
        r.loadFinished()
        XCTAssertFalse(r.probeFailed(generation: gen))
    }

    func testProbeResult() {
        XCTAssertTrue(PageRecovery.probeOK("\(o)|complete|3", origin: o))
        XCTAssertTrue(PageRecovery.probeOK("\(o)|interactive|1", origin: o))
        XCTAssertFalse(PageRecovery.probeOK("\(o)|loading|3", origin: o))
        XCTAssertFalse(PageRecovery.probeOK("\(o)|complete|0", origin: o))
        XCTAssertFalse(PageRecovery.probeOK("null|complete|3", origin: o))
        XCTAssertFalse(PageRecovery.probeOK("http://127.0.0.1:41235|complete|3", origin: o))
        XCTAssertFalse(PageRecovery.probeOK(nil, origin: o))
        XCTAssertFalse(PageRecovery.probeOK("", origin: o))
    }

    func testCancelledLoadsAreNotFailures() {
        XCTAssertFalse(PageRecovery.isRealFailure(domain: NSURLErrorDomain, code: NSURLErrorCancelled))
        XCTAssertFalse(PageRecovery.isRealFailure(domain: "WebKitErrorDomain", code: 102))
        XCTAssertTrue(PageRecovery.isRealFailure(domain: NSURLErrorDomain, code: NSURLErrorCannotConnectToHost))
        XCTAssertTrue(PageRecovery.isRealFailure(domain: NSURLErrorDomain, code: NSURLErrorTimedOut))
        XCTAssertTrue(PageRecovery.isRealFailure(domain: NSURLErrorDomain, code: NSURLErrorNetworkConnectionLost))
    }

    func testBackoff() {
        XCTAssertEqual((1...6).map(PageRecovery.backoff), [1, 2, 4, 8, 8, 8])
    }

    func testCoverFollowsThePhase() {
        XCTAssertEqual(PageCover(.ready), .hidden)
        XCTAssertEqual(PageCover(.failed), .failed)
        for p: PageRecovery.Phase in [.idle, .loading, .dead] { XCTAssertEqual(PageCover(p), .loading) }
    }

    func testCoverStringsAreTranslated() {
        for s in ["The remote-control page could not be loaded.", "Loading", "Try again"] { XCTAssertNotNil(L10n.zh[s], s) }
    }
}
