import UIKit

/// Saves a gateway download where the user chooses. The page hands over the
/// download URL (a single-use ticket valid for 30 seconds), so the file is
/// fetched at once into a temporary directory and offered in the share
/// sheet ("Save to Files", AirDrop, other apps). The copy is removed when
/// the sheet closes.
@MainActor
enum DownloadSaver {
    static func save(url: URL, name: String, from view: UIView) {
        let config = URLSessionConfiguration.ephemeral
        config.connectionProxyDictionary = [:] // loopback only, never through a proxy
        config.timeoutIntervalForRequest = 30
        let session = URLSession(configuration: config)
        Task {
            defer { session.finishTasksAndInvalidate() }
            do {
                let (tmp, response) = try await session.download(from: url)
                guard let http = response as? HTTPURLResponse, http.statusCode == 200 else {
                    try? FileManager.default.removeItem(at: tmp)
                    throw Core.Failure(message: "HTTP \((response as? HTTPURLResponse)?.statusCode ?? 0)")
                }
                let dir = FileManager.default.temporaryDirectory.appendingPathComponent("downloads-" + UUID().uuidString, isDirectory: true)
                try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
                let file = dir.appendingPathComponent(name)
                try FileManager.default.moveItem(at: tmp, to: file)
                share(file, dir: dir, from: view)
            } catch {
                alert(L("Download failed: %@", error.localizedDescription), from: view)
            }
        }
    }

    private static func share(_ file: URL, dir: URL, from view: UIView) {
        let sheet = UIActivityViewController(activityItems: [file], applicationActivities: nil)
        sheet.completionWithItemsHandler = { _, _, _, _ in try? FileManager.default.removeItem(at: dir) }
        if let pop = sheet.popoverPresentationController { // iPad
            pop.sourceView = view
            pop.sourceRect = CGRect(x: view.bounds.midX, y: view.bounds.midY, width: 1, height: 1)
            pop.permittedArrowDirections = []
        }
        top(from: view)?.present(sheet, animated: true)
    }

    private static func alert(_ text: String, from view: UIView) {
        let a = UIAlertController(title: nil, message: text, preferredStyle: .alert)
        a.addAction(UIAlertAction(title: L("OK"), style: .default))
        top(from: view)?.present(a, animated: true)
    }

    private static func top(from view: UIView) -> UIViewController? {
        var vc = view.window?.rootViewController
        while let next = vc?.presentedViewController { vc = next }
        return vc
    }
}
