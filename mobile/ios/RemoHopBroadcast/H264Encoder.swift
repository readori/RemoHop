import CoreMedia
import CoreVideo
import Foundation
import VideoToolbox

/// Where the encoder's output goes (the core's provided screen).
protocol H264EncoderOutput: AnyObject {
    /// The upright screen's size (before scaling) and the rotation that
    /// made it upright changed (the device turned).
    func encoderScreen(width: Int, height: Int, rotation: Int)
    /// One access unit, Annex-B, valid only during the call; a keyframe
    /// starts with SPS and PPS. `captureMicros`: Unix microseconds.
    func encoderFrame(_ data: UnsafeRawBufferPointer, captureMicros: Int64, keyframe: Bool)
}

/// Hardware H.264 for the shared screen: VideoToolbox in real-time,
/// low-latency mode (no B-frames, no frame reordering), a keyframe when a
/// viewer asks, the bitrate the core's flow control asks for, at most 30
/// frames a second, the long edge at most 1920 (lower when the viewers
/// want less, or under memory pressure). It encodes only while the core
/// says someone watches (`start` / `stopEncoding`).
///
/// Orientation: ReplayKit hands over the screen in its portrait buffer and
/// says how the picture lies (RPVideoSampleOrientationKey). An upright
/// frame goes to the encoder as it is (VideoToolbox scales it into the
/// session's size). A turned one is first scaled into a small pool buffer,
/// then rotated into a buffer of the encoder's own pool, so the stream is
/// always upright and only changes size: the session is made again for the
/// new size, which starts with a keyframe and new parameter sets.
///
/// Memory: no frame is copied on the CPU. The encoder's output is turned
/// into Annex-B in place and handed to the core, which copies what it
/// keeps. ReplayKit's buffers are released when the encoder is done with
/// them, except the last one, kept to answer a keyframe request while the
/// screen does not change (ReplayKit sends nothing then).
final class H264Encoder {
    struct Stats {
        var received = 0, encoded = 0, dropped = 0, keyframes = 0, bytes = 0, sessions = 0, errors = 0, refined = 0
    }

    private weak var output: H264EncoderOutput?
    /// All state below is used on this queue only (encode is called with sync).
    private let queue = DispatchQueue(label: "com.remohop.broadcast.encoder", qos: .userInteractive)

    private var session: VTCompressionSession?
    private var sessionSize = FrameSize(width: 0, height: 0)
    private var transfer: VTPixelTransferSession?
    private var rotator: VTPixelRotationSession?
    private var rotatorAngle: VideoRotation = .none
    private var scaledPool: CVPixelBufferPool?
    private var scaledPoolKey = ""

    private var maxLongEdge: Int
    private var throttle: FrameThrottle
    private var bitrate: BitrateController
    private var wantKeyframe = true
    private var inFlight = 0
    /// Someone watches (the core called StartScreen and not StopScreen).
    private var active = false
    /// The long edge the viewers want (StartScreen's maxSize) and the memory cap.
    private var wantedLongEdge = 1920
    private var memoryLongEdge = 1920
    private var screen: (FrameSize, VideoRotation)?
    private var last: (buffer: CVPixelBuffer, orientation: UInt32)?
    private var lastEncodedAt = Date.distantPast
    /// When ReplayKit last delivered a frame, and the still-screen passes so far.
    private var lastReceivedAt = Date.distantPast
    private var refinePasses = 0
    private var refinePending = false
    private(set) var stats = Stats()

    /// At most this many frames wait in the encoder; more are dropped (latency and memory).
    private static let maxInFlight = 2

    /// A still screen is encoded again a few times (see refineStill): after
    /// this long without a new frame, then this often, this many times.
    static let stillAfter: TimeInterval = 0.15
    static let refineGap: TimeInterval = 0.3
    static let refineCount = 3

    static let maxFPS = 30
    static let maxLongEdge = 1920

    init(output: H264EncoderOutput, fps: Int = H264Encoder.maxFPS) {
        self.output = output
        self.maxLongEdge = Self.maxLongEdge
        throttle = FrameThrottle(fps: fps)
        bitrate = BitrateController(size: FrameSize(width: 1080, height: 1920), fps: fps)
    }

    // MARK: input

    /// A frame from ReplayKit (called on its queue). `orientation` is the
    /// RPVideoSampleOrientationKey value (CGImagePropertyOrientation raw).
    func encode(_ buffer: CVPixelBuffer, pts: CMTime, orientation: UInt32) {
        queue.sync {
            stats.received += 1
            noteScreen(buffer, orientation: orientation)
            // Kept even while nobody watches: a viewer that joins a still
            // screen gets it as its first keyframe.
            last = (buffer, orientation)
            guard active else { return }
            lastReceivedAt = Date()
            refinePasses = 0
            scheduleRefine(after: Self.stillAfter)
            guard throttle.admit(pts.seconds) else { return }
            guard inFlight < Self.maxInFlight else {
                stats.dropped += 1
                return
            }
            submit(buffer, pts: pts, orientation: orientation)
        }
    }

    /// Someone watches: encode with these limits (0: no limit), starting
    /// with a keyframe. Called again with new limits while running.
    func start(maxSize: Int, fps: Int, bitrate bps: Int64) {
        queue.async {
            self.wantedLongEdge = maxSize > 0 ? min(maxSize, Self.maxLongEdge) : Self.maxLongEdge
            self.updateLongEdge()
            let f = max(1, min(fps > 0 ? fps : Self.maxFPS, Self.maxFPS))
            if f != self.throttle.fps {
                self.throttle.setFPS(f)
                if let s = self.session { VTSessionSetProperty(s, key: kVTCompressionPropertyKey_ExpectedFrameRate, value: f as CFNumber) }
                _ = self.bitrate.resize(self.sessionSize.width > 0 ? self.sessionSize : FrameSize(width: 1080, height: 1920), fps: f)
            }
            if bps > 0, let v = self.bitrate.request(Int(clamping: bps), now: Date()) { self.applyBitrate(v) }
            if !self.active {
                self.active = true
                ExtLog.log("encoder: started for viewers (max \(maxSize), \(fps) fps, \(bps / 1000) kbit/s asked)")
            }
            self.keyframeSoon()
        }
    }

    /// Nobody watches: the session ends (its buffers go), frames are only kept.
    func stopEncoding() {
        queue.async {
            guard self.active else { return }
            self.active = false
            self.teardown()
            ExtLog.log("encoder: stopped, nobody watches")
        }
    }

    /// The upright screen size and rotation, reported when they change.
    private func noteScreen(_ buffer: CVPixelBuffer, orientation: UInt32) {
        let rotation = StreamGeometry.rotation(orientation: orientation)
        let w = CVPixelBufferGetWidth(buffer), h = CVPixelBufferGetHeight(buffer)
        let upright = rotation.swapsSides ? FrameSize(width: h, height: w) : FrameSize(width: w, height: h)
        if let s = screen, s.0 == upright, s.1 == rotation { return }
        screen = (upright, rotation)
        ExtLog.log("screen \(upright), turned \(rotation.rawValue) degrees")
        output?.encoderScreen(width: upright.width, height: upright.height, rotation: rotation.rawValue)
    }

    private func updateLongEdge() {
        let v = min(wantedLongEdge, memoryLongEdge)
        guard v != maxLongEdge else { return }
        maxLongEdge = v
        scaledPool = nil
    }

    /// A viewer needs a keyframe: the next frame is one. If the screen
    /// does not change (no frame comes), the last frame is sent again as one.
    func requestKeyframe() {
        queue.async { self.keyframeSoon() }
    }

    /// (On queue.) The next frame is a keyframe; if none comes soon (a
    /// still screen), the last frame is encoded again as one.
    private func keyframeSoon() {
        wantKeyframe = true
        let asked = Date()
        queue.asyncAfter(deadline: .now() + 0.25) { [weak self] in
            guard let self, self.active, self.wantKeyframe, self.lastEncodedAt < asked, let l = self.last, self.inFlight == 0 else { return }
            self.submit(l.buffer, pts: CMClockGetTime(CMClockGetHostTimeClock()), orientation: l.orientation)
        }
    }

    /// (On queue.) ReplayKit delivers a frame only when the screen changes,
    /// so when it stops, the last frame (encoded during the motion, at the
    /// motion's quality, or not at all when the throttle skipped it) would
    /// stay on the viewers' screens: blurry text that never sharpens. A
    /// desktop's encoder keeps getting frames and refines a still picture by
    /// itself. So once the screen has been still for `stillAfter`, the last
    /// frame is encoded again `refineCount` times; each pass spends its bits
    /// on detail, as nothing moves.
    private func scheduleRefine(after d: TimeInterval) {
        guard !refinePending else { return }
        refinePending = true
        queue.asyncAfter(deadline: .now() + d) { [weak self] in self?.refineStill() }
    }

    private func refineStill() {
        refinePending = false
        guard active, let l = last, refinePasses < Self.refineCount else { return }
        let quiet = Date().timeIntervalSince(lastReceivedAt)
        if quiet < Self.stillAfter {
            scheduleRefine(after: Self.stillAfter - quiet)
            return
        }
        if inFlight == 0 {
            submit(l.buffer, pts: CMClockGetTime(CMClockGetHostTimeClock()), orientation: l.orientation)
            refinePasses += 1
            stats.refined += 1
        }
        if refinePasses < Self.refineCount { scheduleRefine(after: Self.refineGap) }
    }

    func requestBitrate(_ bps: Int64) {
        queue.async {
            guard let v = self.bitrate.request(Int(clamping: bps), now: Date()) else { return }
            self.applyBitrate(v)
        }
    }

    /// Memory pressure: encode smaller from the next frame on.
    func setMemoryLongEdge(_ v: Int) {
        queue.async {
            self.memoryLongEdge = v
            self.updateLongEdge()
        }
    }

    /// Ends the session and lets every buffer go.
    func stop() {
        queue.sync {
            active = false
            teardown()
            last = nil
        }
    }

    func snapshot() -> (stats: Stats, size: FrameSize, bitrate: Int, fps: Int) {
        queue.sync { (stats, sessionSize, bitrate.current, throttle.fps) }
    }

    // MARK: encoding (on queue)

    private func submit(_ src: CVPixelBuffer, pts: CMTime, orientation: UInt32) {
        let rotation = StreamGeometry.rotation(orientation: orientation)
        let source = FrameSize(width: CVPixelBufferGetWidth(src), height: CVPixelBufferGetHeight(src))
        let layout = StreamGeometry.layout(source: source, rotation: rotation, maxLongEdge: maxLongEdge)
        if session == nil || layout.output != sessionSize {
            guard makeSession(layout.output) else { return }
        }
        guard let session else { return }
        let image: CVPixelBuffer
        if rotation == .none {
            image = src
        } else {
            guard let turned = rotate(src, scaled: layout.scaled, rotation: rotation, session: session) else {
                stats.errors += 1
                return
            }
            image = turned
        }
        var props: CFDictionary?
        if wantKeyframe {
            props = [kVTEncodeFrameOptionKey_ForceKeyFrame: true] as CFDictionary
            wantKeyframe = false
        }
        inFlight += 1
        lastEncodedAt = Date()
        let status = VTCompressionSessionEncodeFrame(session, imageBuffer: image, presentationTimeStamp: pts, duration: .invalid,
                                                     frameProperties: props, infoFlagsOut: nil) { [weak self] status, _, sample in
            self?.encoded(status: status, sample: sample)
        }
        if status != noErr {
            inFlight -= 1
            stats.errors += 1
            if props != nil { wantKeyframe = true }
            ExtLog.log("encoder: frame not accepted (\(status))")
            if status == kVTInvalidSessionErr { teardown() } // made again with the next frame
        }
    }

    /// Scale into the intermediate pool, then rotate into the encoder's pool.
    private func rotate(_ src: CVPixelBuffer, scaled: FrameSize, rotation: VideoRotation, session: VTCompressionSession) -> CVPixelBuffer? {
        if transfer == nil {
            var t: VTPixelTransferSession?
            guard VTPixelTransferSessionCreate(allocator: nil, pixelTransferSessionOut: &t) == noErr, let t else { return nil }
            transfer = t
        }
        if rotator == nil || rotatorAngle != rotation {
            var r: VTPixelRotationSession?
            guard VTPixelRotationSessionCreate(nil, &r) == noErr, let r else { return nil }
            let angle: CFString
            switch rotation {
            case .cw90: angle = kVTRotation_CW90
            case .cw180: angle = kVTRotation_180
            case .cw270: angle = kVTRotation_CCW90
            case .none: angle = kVTRotation_0
            }
            VTSessionSetProperty(r, key: kVTPixelRotationPropertyKey_Rotation, value: angle)
            if let old = rotator { VTPixelRotationSessionInvalidate(old) }
            rotator = r
            rotatorAngle = rotation
        }
        let format = CVPixelBufferGetPixelFormatType(src)
        let key = "\(scaled.width)x\(scaled.height)/\(format)"
        if scaledPool == nil || scaledPoolKey != key {
            scaledPool = Self.pool(scaled, format: format)
            scaledPoolKey = key
        }
        guard let scaledPool, let transfer, let rotator,
              let encoderPool = VTCompressionSessionGetPixelBufferPool(session) else { return nil }
        var mid: CVPixelBuffer?
        guard CVPixelBufferPoolCreatePixelBuffer(nil, scaledPool, &mid) == kCVReturnSuccess, let mid,
              VTPixelTransferSessionTransferImage(transfer, from: src, to: mid) == noErr else { return nil }
        var out: CVPixelBuffer?
        guard CVPixelBufferPoolCreatePixelBuffer(nil, encoderPool, &out) == kCVReturnSuccess, let out,
              VTPixelRotationSessionRotateImage(rotator, mid, out) == noErr else { return nil }
        return out
    }

    private static func pool(_ size: FrameSize, format: OSType) -> CVPixelBufferPool? {
        let poolAttrs: [CFString: Any] = [kCVPixelBufferPoolMinimumBufferCountKey: 2]
        let attrs: [CFString: Any] = [
            kCVPixelBufferPixelFormatTypeKey: format,
            kCVPixelBufferWidthKey: size.width,
            kCVPixelBufferHeightKey: size.height,
            kCVPixelBufferIOSurfacePropertiesKey: [String: Any](),
        ]
        var pool: CVPixelBufferPool?
        CVPixelBufferPoolCreate(nil, poolAttrs as CFDictionary, attrs as CFDictionary, &pool)
        return pool
    }

    private func makeSession(_ size: FrameSize) -> Bool {
        teardown()
        let attrs: [CFString: Any] = [
            kCVPixelBufferPixelFormatTypeKey: kCVPixelFormatType_420YpCbCr8BiPlanarFullRange,
            kCVPixelBufferWidthKey: size.width,
            kCVPixelBufferHeightKey: size.height,
            kCVPixelBufferIOSurfacePropertiesKey: [String: Any](),
        ]
        var s: VTCompressionSession?
        let lowLatency: [CFString: Any] = [kVTVideoEncoderSpecification_EnableLowLatencyRateControl: true]
        var status = VTCompressionSessionCreate(allocator: nil, width: Int32(size.width), height: Int32(size.height),
                                                codecType: kCMVideoCodecType_H264, encoderSpecification: lowLatency as CFDictionary,
                                                imageBufferAttributes: attrs as CFDictionary, compressedDataAllocator: nil,
                                                outputCallback: nil, refcon: nil, compressionSessionOut: &s)
        var mode = "low-latency"
        if status != noErr || s == nil {
            mode = "standard"
            status = VTCompressionSessionCreate(allocator: nil, width: Int32(size.width), height: Int32(size.height),
                                                codecType: kCMVideoCodecType_H264, encoderSpecification: nil,
                                                imageBufferAttributes: attrs as CFDictionary, compressedDataAllocator: nil,
                                                outputCallback: nil, refcon: nil, compressionSessionOut: &s)
        }
        guard status == noErr, let s else {
            stats.errors += 1
            ExtLog.log("encoder: no session for \(size) (\(status))")
            return false
        }
        let fps = throttle.fps
        _ = bitrate.resize(size, fps: fps)
        let profile = mode == "low-latency" ? kVTProfileLevel_H264_ConstrainedHigh_AutoLevel : kVTProfileLevel_H264_High_AutoLevel
        let props: [(CFString, CFTypeRef)] = [
            (kVTCompressionPropertyKey_RealTime, kCFBooleanTrue),
            (kVTCompressionPropertyKey_ProfileLevel, profile),
            (kVTCompressionPropertyKey_AllowFrameReordering, kCFBooleanFalse),
            (kVTCompressionPropertyKey_ExpectedFrameRate, fps as CFNumber),
            // Keyframes come on request; this only bounds how long a lost one hurts.
            (kVTCompressionPropertyKey_MaxKeyFrameIntervalDuration, 10 as CFNumber),
            (kVTCompressionPropertyKey_AverageBitRate, bitrate.current as CFNumber),
            (kVTCompressionPropertyKey_DataRateLimits, [bitrate.bytesPerSecondLimit, 1] as CFArray),
        ]
        for (k, v) in props {
            let st = VTSessionSetProperty(s, key: k, value: v)
            if st != noErr { ExtLog.log("encoder: \(k) not set (\(st))") }
        }
        VTCompressionSessionPrepareToEncodeFrames(s)
        session = s
        sessionSize = size
        wantKeyframe = true
        stats.sessions += 1
        ExtLog.log("encoder: \(mode) session \(size), \(fps) fps, \(bitrate.current / 1000) kbit/s")
        return true
    }

    private func applyBitrate(_ v: Int) {
        guard let s = session else { return }
        VTSessionSetProperty(s, key: kVTCompressionPropertyKey_AverageBitRate, value: v as CFNumber)
        VTSessionSetProperty(s, key: kVTCompressionPropertyKey_DataRateLimits, value: [bitrate.bytesPerSecondLimit, 1] as CFArray)
    }

    private func teardown() {
        if let s = session {
            VTCompressionSessionCompleteFrames(s, untilPresentationTimeStamp: .invalid)
            VTCompressionSessionInvalidate(s)
        }
        if let t = transfer { VTPixelTransferSessionInvalidate(t) }
        if let r = rotator { VTPixelRotationSessionInvalidate(r) }
        session = nil
        transfer = nil
        rotator = nil
        scaledPool = nil
        sessionSize = FrameSize(width: 0, height: 0)
        inFlight = 0
    }

    // MARK: output (VideoToolbox's thread)

    private func encoded(status: OSStatus, sample: CMSampleBuffer?) {
        queue.async { self.inFlight = max(0, self.inFlight - 1) }
        guard status == noErr, let sample, CMSampleBufferDataIsReady(sample),
              let block = CMSampleBufferGetDataBuffer(sample),
              let fmt = CMSampleBufferGetFormatDescription(sample) else {
            if status != noErr { queue.async { self.stats.errors += 1; self.wantKeyframe = true } }
            return
        }
        let attachments = CMSampleBufferGetSampleAttachmentsArray(sample, createIfNecessary: false) as? [[CFString: Any]]
        let keyframe = !((attachments?.first?[kCMSampleAttachmentKey_NotSync] as? Bool) ?? false)
        // ReplayKit's times are on the host clock; the core wants Unix time.
        let age = CMTimeSubtract(CMClockGetTime(CMClockGetHostTimeClock()), CMSampleBufferGetPresentationTimeStamp(sample)).seconds
        let capture = Int64((Date().timeIntervalSince1970 - max(0, age.isFinite ? age : 0)) * 1_000_000)

        var paramSets: (Data, Data)?
        if keyframe {
            guard let ps = Self.parameterSets(fmt) else { return }
            paramSets = ps
        }

        // The frame's bytes, made contiguous only if VideoToolbox split them.
        var lengthAtOffset = 0, total = 0
        var ptr: UnsafeMutablePointer<CChar>?
        var contiguous: CMBlockBuffer = block
        if !CMBlockBufferIsRangeContiguous(block, atOffset: 0, length: 0) {
            var c: CMBlockBuffer?
            guard CMBlockBufferCreateContiguous(allocator: nil, sourceBuffer: block, blockAllocator: nil, customBlockSource: nil,
                                                offsetToData: 0, dataLength: 0, flags: 0, blockBufferOut: &c) == noErr, let c else { return }
            contiguous = c
        }
        guard CMBlockBufferGetDataPointer(contiguous, atOffset: 0, lengthAtOffsetOut: &lengthAtOffset, totalLengthOut: &total,
                                          dataPointerOut: &ptr) == noErr, let ptr, lengthAtOffset == total else { return }
        let frame = UnsafeMutableRawBufferPointer(start: UnsafeMutableRawPointer(ptr), count: total)
        guard AnnexB.convertInPlace(frame) else {
            queue.async { self.stats.errors += 1 }
            return
        }
        if let ps = paramSets {
            // A keyframe carries its parameter sets in front (the only copy, once per keyframe).
            var d = AnnexB.parameterSets(sps: ps.0, pps: ps.1)
            d.append(UnsafeRawPointer(ptr).assumingMemoryBound(to: UInt8.self), count: total)
            d.withUnsafeBytes { self.output?.encoderFrame($0, captureMicros: capture, keyframe: true) }
        } else {
            output?.encoderFrame(UnsafeRawBufferPointer(frame), captureMicros: capture, keyframe: false)
        }
        queue.async {
            self.stats.encoded += 1
            self.stats.bytes += total
            if keyframe { self.stats.keyframes += 1 }
        }
    }

    /// SPS and PPS from the encoder's format description.
    private static func parameterSets(_ fmt: CMFormatDescription) -> (Data, Data)? {
        func set(_ i: Int) -> Data? {
            var p: UnsafePointer<UInt8>?
            var n = 0
            guard CMVideoFormatDescriptionGetH264ParameterSetAtIndex(fmt, parameterSetIndex: i, parameterSetPointerOut: &p,
                                                                     parameterSetSizeOut: &n, parameterSetCountOut: nil,
                                                                     nalUnitHeaderLengthOut: nil) == noErr, let p else { return nil }
            return Data(bytes: p, count: n)
        }
        guard let sps = set(0), let pps = set(1) else { return nil }
        return (sps, pps)
    }
}
