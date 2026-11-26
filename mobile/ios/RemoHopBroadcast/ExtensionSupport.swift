import Darwin
import Foundation
import os
import Remohopcore

/// The extension's diagnostic events, in the app's diagnostic log (the core
/// writes to `<config>/diagnostics`, shared through the App Group, so the
/// app's Settings → Diagnostic logs shows and shares them). Recorded only
/// while the user has the log on; the core redacts every line. Never pass
/// the password (temporary or the owner's own) or a gateway URL.
enum ExtLog {
    static func log(_ message: String) {
        RemohopcoreDiagnosticsLog("ios-broadcast", message)
    }

    /// The process's memory and the Go runtime's, for the log.
    static func memory(_ event: String) {
        log("memory \(event): footprint \(Memory.mb(Memory.footprint())) MB, available \(Memory.mb(Memory.available())) MB, go \(Memory.goSummary())")
    }
}

/// The extension's memory, as the system counts it against the broadcast
/// upload extension's limit (about 50 MB).
enum Memory {
    /// The physical footprint in bytes (what jetsam compares to the limit).
    static func footprint() -> UInt64 {
        var info = task_vm_info_data_t()
        var count = mach_msg_type_number_t(MemoryLayout<task_vm_info_data_t>.size / MemoryLayout<integer_t>.size)
        let kr = withUnsafeMutablePointer(to: &info) {
            $0.withMemoryRebound(to: integer_t.self, capacity: Int(count)) {
                task_info(mach_task_self_, task_flavor_t(TASK_VM_INFO), $0, &count)
            }
        }
        return kr == KERN_SUCCESS ? info.phys_footprint : 0
    }

    /// Bytes left before the system's limit (0 when it does not say).
    static func available() -> UInt64 { UInt64(os_proc_available_memory()) }

    static func mb(_ bytes: UInt64) -> String { String(format: "%.1f", Double(bytes) / 1_048_576) }

    static func footprintMB() -> Double { Double(footprint()) / 1_048_576 }
    static func availableMB() -> Double { Double(available()) / 1_048_576 }

    /// "heap 6.2 MB, total 14.0 MB, 41 goroutines, 12 GCs".
    static func goSummary() -> String {
        let s = RemohopcoreMemoryStatsJSON()
        guard let o = (try? JSONSerialization.jsonObject(with: Data(s.utf8))) as? [String: Any] else { return "?" }
        func n(_ k: String) -> UInt64 { (o[k] as? NSNumber)?.uint64Value ?? 0 }
        return "heap \(mb(n("heap"))) MB, total \(mb(n("total"))) MB, \(n("goroutines")) goroutines, \(n("gcs")) GCs"
    }
}
