import Foundation
import Remohopcore

/// What the Go core's host mode reports.
struct HostStatus: Equatable {
    var id = ""
    var password = ""
    var addresses: [String] = []
    var online = false
    var requests: [CoreRequest] = []
    var viewers: [BroadcastState.Viewer] = []
}

/// Calls from the core (on its own threads).
protocol HostCoreEvents: AnyObject {
    /// A viewer needs a keyframe (it joined, or lost frames).
    func coreWantsKeyframe()
    /// The flow control's bitrate for the encoder, in bits per second.
    func coreWantsBitrate(_ bps: Int)
    /// The frame rate the viewers can take.
    func coreWantsFPS(_ fps: Int)
    /// Requests, viewers, ID or password changed.
    func coreStateChanged()
}

/// The Go core's host mode (an in-process hostd with the quick-support
/// policy: every login asks, a temporary password, no pairing, view only)
/// and its provided-stream screen source, as the extension uses them. This
/// file is the only place that names those core functions.
protocol HostCore: AnyObject {
    /// Starts the host with the provided stream as its screen.
    func start(events: HostCoreEvents) throws
    func stop()
    func status() -> HostStatus
    /// Answers login `n` (allowed logins see the screen only).
    func decide(n: Int, allow: Bool)
    /// Ends viewer `id`; 0 ends every viewer.
    func disconnect(viewer id: Int)
    /// The stream's format: the coded size and the parameter sets (raw NAL
    /// units without start codes). Called before the first frame and
    /// whenever the size changes (rotation, a lower resolution step).
    func setFormat(width: Int, height: Int, sps: Data, pps: Data)
    /// One access unit in Annex-B (start codes; a keyframe begins with SPS
    /// and PPS), with its presentation time in microseconds. `data` is
    /// only valid during the call: the core copies what it keeps.
    func push(_ data: UnsafeRawBufferPointer, ptsMicros: Int64, keyframe: Bool)
}

enum HostCoreError: LocalizedError {
    /// The linked core has no host mode yet.
    case unsupported
    /// The app has not moved its identity into the App Group yet.
    case openAppFirst
    case failed(String)

    var code: String {
        switch self {
        case .unsupported: return "core_unsupported"
        case .openAppFirst: return "open_app"
        case .failed: return "host"
        }
    }

    var errorDescription: String? {
        switch self {
        case .unsupported: return "the core has no host mode"
        case .openAppFirst: return "the app has not set up the App Group directory"
        case .failed(let s): return s
        }
    }
}

enum HostCoreFactory {
    /// The core's host mode. Until the provided-stream source and the
    /// mobile host-mode API are in the linked core, this is
    /// UnavailableHostCore and a broadcast ends at once with a message.
    static func make() -> HostCore { UnavailableHostCore() }
}

/// A core without host mode: starting fails with `.unsupported`.
final class UnavailableHostCore: HostCore {
    func start(events: HostCoreEvents) throws { throw HostCoreError.unsupported }
    func stop() {}
    func status() -> HostStatus { HostStatus() }
    func decide(n: Int, allow: Bool) {}
    func disconnect(viewer id: Int) {}
    func setFormat(width: Int, height: Int, sps: Data, pps: Data) {}
    func push(_ data: UnsafeRawBufferPointer, ptsMicros: Int64, keyframe: Bool) {}
}
