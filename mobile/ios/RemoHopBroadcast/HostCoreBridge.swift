import Foundation
import Remohopcore

/// Calls from the core (on its goroutines; they must return quickly).
protocol HostCoreEvents: AnyObject {
    /// Someone watches: capture and encode with these limits (long edge,
    /// frames per second, bits per second), or apply new ones.
    func coreStartScreen(maxSize: Int, fps: Int, bitrate: Int64)
    /// Nobody watches any more.
    func coreStopScreen()
    func coreWantsKeyframe()
    func coreWantsBitrate(_ bps: Int64)
    /// Requests, viewers, ID or password changed (the new state document).
    func coreStateChanged(_ json: String)
}

/// The Go core's host mode (internal/mobile/host.go: an in-process hostd
/// with the quick-support policy: every login asks, temporary password
/// only, no pairing; view only, since no input injector is set) and its
/// provided screen (internal/svc/desktop/provided.go), which turns the
/// H.264 this extension pushes into the desktop wire protocol, so every
/// client works unchanged. This file is the only one that names those
/// core functions.
protocol HostCore: AnyObject {
    func start(events: HostCoreEvents) throws
    func stop()
    func status() -> HostStatus
    /// Answers request `n`: allowed logins only view.
    func decide(n: Int, allow: Bool)
    /// Ends viewer `id`; 0 ends every viewer.
    func disconnect(viewer id: Int)
    /// The upright picture's size before scaling, and the rotation applied to it.
    func setScreenInfo(width: Int, height: Int, rotation: Int)
    /// One access unit in Annex-B (a keyframe carries SPS and PPS in
    /// front); valid only during the call (the core copies it).
    /// `captureMicros` is the capture time in Unix microseconds.
    func push(_ data: UnsafeRawBufferPointer, captureMicros: Int64)
}

enum HostCoreError: LocalizedError {
    /// The app has not moved its identity into the App Group yet.
    case openAppFirst
    case failed(String)

    var code: String {
        switch self {
        case .openAppFirst: return "open_app"
        case .failed: return "host"
        }
    }

    var errorDescription: String? {
        switch self {
        case .openAppFirst: return "the app has not set up the App Group directory"
        case .failed(let s): return s
        }
    }
}

enum HostCoreFactory {
    static func make() -> HostCore { GoHostCore() }
}

/// HostCore on the gomobile-bound core.
final class GoHostCore: HostCore {
    private var adapter: Adapter?

    func start(events: HostCoreEvents) throws {
        let a = Adapter(events)
        adapter = a
        // VideoToolbox at 30 fps (the extension's encoder); no input
        // injector is set, so the core offers viewing only.
        RemohopcoreSetScreenOptions("videotoolbox", 30)
        RemohopcoreSetScreenSource(a)
        RemohopcoreSetHostListener(a)
        var err: NSError?
        if !RemohopcoreStartHost(&err) {
            RemohopcoreSetHostListener(nil)
            RemohopcoreSetScreenSource(nil)
            adapter = nil
            throw HostCoreError.failed(err?.localizedDescription ?? "host mode did not start")
        }
    }

    func stop() {
        RemohopcoreStopHost()
        RemohopcoreSetHostListener(nil)
        RemohopcoreSetScreenSource(nil)
        adapter = nil
    }

    func status() -> HostStatus {
        var err: NSError?
        let s = RemohopcoreHostStateJSON(&err)
        return err == nil ? HostStatus(json: s) : HostStatus()
    }

    func decide(n: Int, allow: Bool) {
        var err: NSError?
        // Never control: iOS lets no app control the device.
        if !RemohopcoreDecideHostRequest(n, allow, false, &err) {
            ExtLog.log("request \(n) not answered: \(err?.localizedDescription ?? "?")")
        }
    }

    func disconnect(viewer id: Int) {
        var err: NSError?
        if !RemohopcoreDisconnectHostSession(id, &err) {
            ExtLog.log("disconnect \(id) failed: \(err?.localizedDescription ?? "?")")
        }
    }

    func setScreenInfo(width: Int, height: Int, rotation: Int) {
        RemohopcoreSetScreenInfo(width, height, rotation)
    }

    func push(_ data: UnsafeRawBufferPointer, captureMicros: Int64) {
        guard let base = data.baseAddress, data.count > 0 else { return }
        // No copy here: gomobile hands Go the bytes for the call, and the core copies them.
        let d = NSData(bytesNoCopy: UnsafeMutableRawPointer(mutating: base), length: data.count, freeWhenDone: false)
        var err: NSError?
        if !RemohopcorePushScreenFrame(d as Data, captureMicros, &err), let err {
            ExtLog.log("frame not taken: \(err.localizedDescription)")
        }
    }

    /// The core's ScreenSource and HostListener, forwarding to the events.
    private final class Adapter: NSObject, RemohopcoreScreenSourceProtocol, RemohopcoreHostListenerProtocol {
        weak var events: HostCoreEvents?

        init(_ events: HostCoreEvents) { self.events = events }

        func startScreen(_ maxSize: Int, fps: Int, bitrate: Int64) throws {
            events?.coreStartScreen(maxSize: maxSize, fps: fps, bitrate: bitrate)
        }

        func stopScreen() { events?.coreStopScreen() }
        func requestKeyframe() { events?.coreWantsKeyframe() }
        func setBitrate(_ bitrate: Int64) { events?.coreWantsBitrate(bitrate) }
        func hostChanged(_ state: String?) { events?.coreStateChanged(state ?? "") }
        func hostRequest(_ request: String?) {} // the next HostChanged lists it too
    }
}
