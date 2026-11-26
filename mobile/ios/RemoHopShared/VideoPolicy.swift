import Foundation

// The broadcast extension's video decisions, kept free of ReplayKit and
// VideoToolbox so they are unit tested: how a frame is turned upright and
// scaled, how many frames go to the encoder, which bitrate it gets, and
// when memory pressure lowers the resolution.

/// A frame size in pixels.
struct FrameSize: Equatable, CustomStringConvertible {
    var width: Int
    var height: Int
    var longEdge: Int { max(width, height) }
    var description: String { "\(width)x\(height)" }
}

/// The clockwise rotation that turns a ReplayKit frame upright.
enum VideoRotation: Int, Equatable {
    case none = 0, cw90 = 90, cw180 = 180, cw270 = 270
    var swapsSides: Bool { self == .cw90 || self == .cw270 }
}

enum StreamGeometry {
    /// The rotation for the `RPVideoSampleOrientationKey` attachment (a
    /// CGImagePropertyOrientation raw value). ReplayKit keeps delivering the
    /// screen in its native portrait buffer and says how the picture lies:
    /// `.right` (6) is stored rotated 90° counter-clockwise, so it turns 90°
    /// clockwise; `.left` (8) the other way; `.down` (3) half a turn. The
    /// screen is never mirrored, so mirrored values count as their base.
    static func rotation(orientation raw: UInt32) -> VideoRotation {
        switch raw {
        case 3, 4: return .cw180     // down, downMirrored
        case 6, 7: return .cw90      // right, rightMirrored
        case 8, 5: return .cw270     // left, leftMirrored
        default: return .none        // up, upMirrored, missing
        }
    }

    /// `source` scaled to fit `maxLongEdge` (never up), aspect kept, both
    /// sides even (H.264 4:2:0) and at least 2.
    static func scaled(_ source: FrameSize, maxLongEdge: Int) -> FrameSize {
        let cap = max(2, maxLongEdge & ~1)
        let long = source.longEdge
        guard long > 0 else { return FrameSize(width: 2, height: 2) }
        let s = min(1.0, Double(cap) / Double(long))
        // The epsilon keeps 2556 * (1920 / 2556) at 1920, not 1918.
        func even(_ v: Int) -> Int { max(2, Int((Double(v) * s / 2 + 1e-6).rounded(.down)) * 2) }
        var out = FrameSize(width: even(source.width), height: even(source.height))
        // Rounding must not push the long side past the cap.
        if out.width > cap { out.width = cap }
        if out.height > cap { out.height = cap }
        return out
    }

    /// The size the encoder works at: `source` scaled, then turned upright.
    /// `scaled` is the size before rotation (what a rotation step reads).
    static func layout(source: FrameSize, rotation: VideoRotation, maxLongEdge: Int) -> (scaled: FrameSize, output: FrameSize) {
        let s = scaled(source, maxLongEdge: maxLongEdge)
        return (s, rotation.swapsSides ? FrameSize(width: s.height, height: s.width) : s)
    }
}

/// Passes at most `fps` frames a second. ReplayKit delivers a frame when the
/// screen changes, up to the display's rate (60 or 120 Hz). Each admitted
/// frame books the next slot one interval later, and a frame up to 20% of
/// an interval early takes it, so a 60 Hz source gives an even 30 and the
/// average never exceeds `fps`. After a pause (a still screen) or a jump
/// back in time the schedule starts again from the frame.
struct FrameThrottle {
    private(set) var fps: Int
    private var due: Double?

    init(fps: Int) { self.fps = max(1, min(fps, 60)) }

    mutating func setFPS(_ v: Int) {
        fps = max(1, min(v, 60))
        due = nil
    }

    /// Whether the frame at `t` seconds goes to the encoder.
    mutating func admit(_ t: Double) -> Bool {
        let interval = 1.0 / Double(fps)
        guard let d = due, t >= d - 2 * interval, t <= d + interval else {
            due = t + interval
            return true
        }
        if t < d - interval * 0.2 { return false }
        due = d + interval
        return true
    }
}

/// The encoder's average bitrate: the core asks for one (its flow control
/// follows the viewer's network); this keeps it within what the frame size
/// needs and avoids reconfiguring the encoder for small changes.
struct BitrateController {
    static let floor = 250_000
    static let absoluteCeiling = 12_000_000
    /// Increases are applied at most this often; decreases at once.
    static let raiseInterval: TimeInterval = 1

    private(set) var ceiling: Int
    private(set) var current: Int
    private var lastRaise: Date?

    /// About 0.2 bit per pixel and frame. Screen content compresses well,
    /// but a phone's small text needs more than a desktop's: the core asks
    /// about 0.12 on a fast path and this must not cut that, and an encoder
    /// spends less whenever the screen is still.
    static func ceiling(for size: FrameSize, fps: Int) -> Int {
        let v = size.width * size.height * max(fps, 1) / 5
        return min(max(v, 1_000_000), absoluteCeiling)
    }

    init(size: FrameSize, fps: Int) {
        ceiling = Self.ceiling(for: size, fps: fps)
        current = min(ceiling, 2_500_000)
    }

    /// The core's request; returns the bitrate to apply, or nil to keep the current one.
    mutating func request(_ bps: Int, now: Date) -> Int? {
        let v = min(max(bps, Self.floor), ceiling)
        guard v != current else { return nil }
        if v < current {
            // Down: at once, unless the change is tiny (< 5%).
            guard Double(current - v) >= Double(current) * 0.05 else { return nil }
        } else {
            guard Double(v - current) >= Double(current) * 0.1 else { return nil }
            if let l = lastRaise, now.timeIntervalSince(l) < Self.raiseInterval { return nil }
            lastRaise = now
        }
        current = v
        return v
    }

    /// A new frame size or rate: a new ceiling; returns the bitrate to apply when it changed.
    mutating func resize(_ size: FrameSize, fps: Int) -> Int? {
        ceiling = Self.ceiling(for: size, fps: fps)
        guard current > ceiling else { return nil }
        current = ceiling
        return current
    }

    /// The limit the encoder may not exceed over one second, in bytes (1.5 times the average).
    var bytesPerSecondLimit: Int { current / 8 * 3 / 2 }
}

/// Lowers the resolution when the extension runs short of memory (the
/// system ends a broadcast upload extension that goes over about 50 MB).
/// Steps down, never back up during one broadcast.
struct MemoryGovernor {
    enum Advice: Equatable {
        case none
        /// Encode at this long edge from now on (and let the Go heap go).
        case stepDown(Int)
        /// Already at the lowest step: return what can be returned.
        case freeMemory
    }

    static let steps = [1920, 1280, 960]
    /// Step down when less than this is left before the limit.
    static let lowMB = 12.0
    /// Assumed limit when the system does not say what is left.
    static let assumedLimitMB = 50.0
    static let minInterval: TimeInterval = 5

    private(set) var step = 0
    private var lastAdvice: Date?

    var longEdge: Int { Self.steps[step] }

    /// `availableMB` from os_proc_available_memory (0 when unknown) and the footprint.
    mutating func observe(availableMB: Double, footprintMB: Double, now: Date) -> Advice {
        let left = availableMB > 0 ? availableMB : Self.assumedLimitMB - footprintMB
        guard left < Self.lowMB else { return .none }
        if let l = lastAdvice, now.timeIntervalSince(l) < Self.minInterval { return .none }
        lastAdvice = now
        if step + 1 < Self.steps.count {
            step += 1
            return .stepDown(Self.steps[step])
        }
        return .freeMemory
    }
}
