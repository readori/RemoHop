import Foundation
import MessageUI
import Remohopcore
import SafariServices
import UIKit

/// The app side of the diagnostic log (internal/mobile/diag.go): off by
/// default, kept in the app's private storage, size-capped and redacted by
/// the core, and only ever shared by the user from Settings → Diagnostics.
enum Diag {
    struct Failure: LocalizedError {
        let message: String
        var errorDescription: String? { message }
    }

    /// Records an app event while the log is on (the core drops it otherwise).
    static func log(_ message: String) {
        RemohopcoreDiagnosticsLog("ios", message)
    }

    /// Records an event the page reported (errors, failed connections).
    static func logPage(_ message: String) {
        RemohopcoreDiagnosticsLog("web", String(message.prefix(2000)))
    }

    static var enabled: Bool { RemohopcoreDiagnosticsEnabled() }

    @MainActor
    static func setEnabled(_ on: Bool) throws {
        var err: NSError?
        if !RemohopcoreSetDiagnosticsEnabled(on, &err) { throw err ?? Failure(message: "could not change the diagnostic log") }
        if on { log("app \(AppInfo.versionLine(Bundle.main.infoDictionary)), iOS \(UIDevice.current.systemVersion), \(AppInfo.modelIdentifier())") }
    }

    static var size: Int64 { RemohopcoreDiagnosticsSize() }

    static func clear() throws {
        var err: NSError?
        if !RemohopcoreClearDiagnostics(&err) { throw err ?? Failure(message: "could not clear the diagnostic log") }
    }

    static var coreVersion: String { RemohopcoreCoreVersion() }

    /// Writes a report ("txt" or "json") into the app's temporary directory
    /// and returns its file; the previous report there is replaced.
    @MainActor
    static func export(format: String) throws -> URL {
        let info = AppInfo.reportInfo(info: Bundle.main.infoDictionary, os: UIDevice.current.systemVersion,
                                      device: AppInfo.modelIdentifier(), language: L10n.effective)
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("diagnostics", isDirectory: true)
        var err: NSError?
        let path = RemohopcoreDiagnosticsExport(info, format, dir.path, &err)
        if let err { throw err }
        return URL(fileURLWithPath: path)
    }
}

/// Presents UIKit screens (share sheet, mail composer, Safari) over
/// whatever is on screen, including the Settings sheet.
@MainActor
enum Presenter {
    static func top() -> UIViewController? {
        let scenes = UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }
        let window = scenes.flatMap(\.windows).first(where: \.isKeyWindow) ?? scenes.first?.windows.first
        var vc = window?.rootViewController
        while let next = vc?.presentedViewController { vc = next }
        return vc
    }

    static func present(_ vc: UIViewController) {
        guard let top = top() else { return }
        if let pop = vc.popoverPresentationController { // iPad
            pop.sourceView = top.view
            pop.sourceRect = CGRect(x: top.view.bounds.midX, y: top.view.bounds.midY, width: 1, height: 1)
            pop.permittedArrowDirections = []
        }
        top.present(vc, animated: true)
    }

    /// The system share sheet (Mail, Messages, AirDrop, Save to Files...).
    static func share(_ file: URL) {
        present(UIActivityViewController(activityItems: [file], applicationActivities: nil))
    }

    /// A web page in Safari's in-app view; other schemes go to the system.
    static func open(_ url: URL) {
        if url.scheme == "https" || url.scheme == "http" {
            let vc = SFSafariViewController(url: url)
            vc.dismissButtonStyle = .done
            present(vc)
        } else {
            UIApplication.shared.open(url)
        }
    }

    /// A mail to support with `file` attached; without a mail account, the
    /// mailto: link (no attachment possible) and then the share sheet.
    static func emailSupport(subject: String, body: String, file: URL) {
        if MFMailComposeViewController.canSendMail() {
            let vc = MFMailComposeViewController()
            vc.mailComposeDelegate = MailDelegate.shared
            vc.setToRecipients([AppInfo.supportEmail])
            vc.setSubject(subject)
            vc.setMessageBody(body, isHTML: false)
            if let data = try? Data(contentsOf: file) {
                vc.addAttachmentData(data, mimeType: file.pathExtension == "json" ? "application/json" : "text/plain",
                                     fileName: file.lastPathComponent)
            }
            present(vc)
        } else if let url = AppInfo.supportMailto(subject: subject, body: body) {
            UIApplication.shared.open(url, options: [:]) { opened in
                if !opened { Task { @MainActor in share(file) } }
            }
        } else {
            share(file)
        }
    }
}

final class MailDelegate: NSObject, MFMailComposeViewControllerDelegate {
    static let shared = MailDelegate()

    func mailComposeController(_ controller: MFMailComposeViewController, didFinishWith result: MFMailComposeResult, error: Error?) {
        controller.dismiss(animated: true)
    }
}
