import UIKit

/// Saves a gateway download where the user chooses. The page hands over the
/// download URL (a single-use ticket valid for 30 seconds), so the transfer
/// starts at once, with its progress and a Cancel button at the bottom of
/// the screen; the file then goes to the share sheet ("Save to Files",
/// AirDrop, other apps). The copy is removed when the sheet closes. One
/// download at a time: a second tap while one runs says so instead of
/// starting another transfer.
@MainActor
enum DownloadSaver {
    private static var job: DownloadJob?

    static func save(url: URL, name: String, from view: UIView) {
        if job != nil {
            alert(L("Another download is still running. Wait for it, or cancel it."), from: view)
            return
        }
        let hud = DownloadHUD(name: name)
        let j = DownloadJob(name: name, progress: { done, total in
            Task { @MainActor in hud.update(done: done, total: total) }
        }, finished: { result in
            Task { @MainActor in
                job = nil
                hud.removeFromSuperview()
                switch result {
                case .success(let (file, dir)): share(file, dir: dir, from: view)
                case .failure(let error):
                    if (error as? URLError)?.code == .cancelled { return } // the user's Cancel
                    alert(L("Download failed: %@", error.localizedDescription), from: view)
                }
            }
        })
        job = j
        hud.onCancel = { j.cancel() }
        hud.show(in: view.window ?? view)
        j.start(url)
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

/// One transfer, reporting its progress; the file is moved out of the
/// session's temporary location as soon as it is complete.
final class DownloadJob: NSObject, URLSessionDownloadDelegate, @unchecked Sendable {
    private let name: String
    private let progress: @Sendable (Int64, Int64) -> Void
    private let finished: @Sendable (Result<(URL, URL), Error>) -> Void
    private var session: URLSession?
    private let lock = NSLock()
    private var done = false

    init(name: String, progress: @escaping @Sendable (Int64, Int64) -> Void, finished: @escaping @Sendable (Result<(URL, URL), Error>) -> Void) {
        self.name = name
        self.progress = progress
        self.finished = finished
    }

    func start(_ url: URL) {
        let config = URLSessionConfiguration.ephemeral
        config.connectionProxyDictionary = [:] // loopback only, never through a proxy
        config.timeoutIntervalForRequest = 60
        let s = URLSession(configuration: config, delegate: self, delegateQueue: nil)
        session = s
        s.downloadTask(with: url).resume()
    }

    func cancel() { session?.invalidateAndCancel() }

    private func finish(_ r: Result<(URL, URL), Error>) {
        lock.lock()
        let first = !done
        done = true
        lock.unlock()
        if first { finished(r) }
    }

    func urlSession(_ session: URLSession, downloadTask: URLSessionDownloadTask, didWriteData bytesWritten: Int64,
                    totalBytesWritten: Int64, totalBytesExpectedToWrite: Int64) {
        progress(totalBytesWritten, totalBytesExpectedToWrite)
    }

    func urlSession(_ session: URLSession, downloadTask: URLSessionDownloadTask, didFinishDownloadingTo location: URL) {
        let status = (downloadTask.response as? HTTPURLResponse)?.statusCode ?? 0
        guard status == 200 else {
            finish(.failure(Core.Failure(message: "HTTP \(status)")))
            return
        }
        // The file at location is removed when this returns: move it now.
        do {
            let dir = FileManager.default.temporaryDirectory.appendingPathComponent("downloads-" + UUID().uuidString, isDirectory: true)
            try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
            let file = dir.appendingPathComponent(name)
            try FileManager.default.moveItem(at: location, to: file)
            finish(.success((file, dir)))
        } catch {
            finish(.failure(error))
        }
    }

    func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: Error?) {
        if let error { finish(.failure(error)) }
        session.finishTasksAndInvalidate()
    }
}

/// The transfer's name, a progress bar and Cancel, above the bottom edge.
@MainActor
final class DownloadHUD: UIView {
    var onCancel: (() -> Void)?
    private let name: String
    private let label = UILabel()
    private let bar = UIProgressView(progressViewStyle: .default)

    init(name: String) {
        self.name = name
        super.init(frame: .zero)
        backgroundColor = UIColor(white: 0.12, alpha: 0.94)
        layer.cornerRadius = 14
        translatesAutoresizingMaskIntoConstraints = false
        label.textColor = .white
        label.font = .preferredFont(forTextStyle: .subheadline)
        label.adjustsFontForContentSizeCategory = true
        label.numberOfLines = 2
        label.text = L("Downloading %@…", name)
        let cancel = UIButton(type: .system)
        cancel.setTitle(L("Cancel"), for: .normal)
        cancel.addAction(UIAction { [weak self] _ in self?.onCancel?() }, for: .touchUpInside)
        let row = UIStackView(arrangedSubviews: [label, cancel])
        row.spacing = 8
        row.alignment = .center
        label.setContentCompressionResistancePriority(.defaultLow, for: .horizontal)
        cancel.setContentHuggingPriority(.required, for: .horizontal)
        let col = UIStackView(arrangedSubviews: [row, bar])
        col.axis = .vertical
        col.spacing = 8
        col.translatesAutoresizingMaskIntoConstraints = false
        addSubview(col)
        NSLayoutConstraint.activate([
            col.leadingAnchor.constraint(equalTo: leadingAnchor, constant: 16),
            col.trailingAnchor.constraint(equalTo: trailingAnchor, constant: -12),
            col.topAnchor.constraint(equalTo: topAnchor, constant: 10),
            col.bottomAnchor.constraint(equalTo: bottomAnchor, constant: -12),
        ])
        isAccessibilityElement = false
        accessibilityViewIsModal = false
    }

    required init?(coder: NSCoder) { fatalError("not used") }

    func show(in host: UIView) {
        host.addSubview(self)
        let g = host.safeAreaLayoutGuide
        NSLayoutConstraint.activate([
            leadingAnchor.constraint(equalTo: g.leadingAnchor, constant: 12),
            trailingAnchor.constraint(equalTo: g.trailingAnchor, constant: -12),
            bottomAnchor.constraint(equalTo: g.bottomAnchor, constant: -12),
        ])
        UIAccessibility.post(notification: .announcement, argument: label.text)
    }

    func update(done: Int64, total: Int64) {
        if total > 0 {
            let pct = Int(min(100, max(0, done * 100 / total)))
            bar.setProgress(Float(done) / Float(total), animated: true)
            label.text = L("Downloading %@ · %d%%", name, pct)
        } else {
            label.text = L("Downloading %@…", name) + " " + ByteCountFormatter.string(fromByteCount: done, countStyle: .file)
        }
    }
}
