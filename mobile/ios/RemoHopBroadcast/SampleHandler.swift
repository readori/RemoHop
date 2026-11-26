import CoreMedia
import ReplayKit

/// RemoHop's broadcast upload extension: view-only screen sharing.
///
/// The user starts it from the app's broadcast button (or Control Center)
/// and stops it from the red status bar, Control Center or the app. The
/// host runs here, not in the app, because the app may be suspended while
/// the screen is shared: the Go core (linked into this extension) runs an
/// in-process host with the quick-support policy (every login asks the
/// owner, a temporary password, no pairing, view only), and its screen is
/// the H.264 stream encoded from ReplayKit's frames. This process has a
/// hard memory limit of about 50 MB; see BroadcastHost and H264Encoder.
final class SampleHandler: RPBroadcastSampleHandler {
    private var host: BroadcastHost?
    private let lock = NSLock()

    override func broadcastStarted(withSetupInfo setupInfo: [String: NSObject]?) {
        let host = BroadcastHost()
        host.onFinish = { [weak self] message in self?.finish(message) }
        do {
            try host.start()
            lock.lock()
            self.host = host
            lock.unlock()
        } catch {
            let code = (error as? HostCoreError)?.code ?? "init"
            let message: String
            switch code {
            case "open_app": message = host.text("Open RemoHop once, then start sharing again.")
            default: message = host.text("RemoHop could not start sharing. Open RemoHop and try again.")
            }
            finishBroadcastWithError(Self.error(message))
        }
    }

    override func broadcastPaused() { current()?.setPaused(true) }
    override func broadcastResumed() { current()?.setPaused(false) }

    override func broadcastFinished() {
        take()?.stop(reason: "stopped by the user")
    }

    override func processSampleBuffer(_ sampleBuffer: CMSampleBuffer, with sampleBufferType: RPSampleBufferType) {
        // Video only: no app or microphone audio is shared.
        guard sampleBufferType == .video, let host = current(), let encoder = host.encoder,
              let pixels = CMSampleBufferGetImageBuffer(sampleBuffer) else { return }
        let orientation = (CMGetAttachment(sampleBuffer, key: RPVideoSampleOrientationKey as CFString, attachmentModeOut: nil) as? NSNumber)?.uint32Value ?? 1
        encoder.encode(pixels, pts: CMSampleBufferGetPresentationTimeStamp(sampleBuffer), orientation: orientation)
    }

    /// Ends the broadcast from this side (the app's Stop, an error) with a message the system shows.
    private func finish(_ message: String) {
        take()?.stop(reason: message)
        finishBroadcastWithError(Self.error(message))
    }

    private func current() -> BroadcastHost? {
        lock.lock()
        defer { lock.unlock() }
        return host
    }

    private func take() -> BroadcastHost? {
        lock.lock()
        defer { lock.unlock() }
        let h = host
        host = nil
        return h
    }

    private static func error(_ message: String) -> NSError {
        NSError(domain: "com.remohop.app.broadcast", code: 1, userInfo: [NSLocalizedDescriptionKey: message])
    }
}
