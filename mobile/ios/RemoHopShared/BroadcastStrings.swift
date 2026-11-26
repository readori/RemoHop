import Foundation
import UserNotifications

/// The few texts the broadcast extension shows (the request notification
/// and the reason a broadcast ends), in English and Simplified Chinese.
/// The extension has no access to the app's L10n table, so they live here;
/// the language is the app's choice, mirrored in the App Group.
enum BroadcastStrings {
    /// The notification category with the Allow and Deny actions.
    static let category = "remohop.broadcast.request"
    static let allowAction = "remohop.broadcast.allow"
    static let denyAction = "remohop.broadcast.deny"
    /// userInfo key of the request's key.
    static let requestKey = "remohop.request"

    static func text(_ english: String, lang: String) -> String {
        lang == "zh" ? (zh[english] ?? english) : english
    }

    /// "Pixel 8 wants to view your screen".
    static func requestTitle(device: String, lang: String) -> String {
        let name = device.isEmpty ? text("A device", lang: lang) : device
        return String(format: text("%@ wants to view your screen", lang: lang), name)
    }

    /// New-device flag, the short scam reminder and the timeout.
    static func requestBody(firstSeen: Bool, from: String, lang: String) -> String {
        var parts: [String] = []
        parts.append(text(firstSeen ? "New device" : "Connected before", lang: lang) + (from.isEmpty ? "" : " · \(from)"))
        parts.append(text("Did someone call and ask you to share your screen? Deny: that is a scam.", lang: lang))
        parts.append(text("Refused in 1 minute if you do not answer.", lang: lang))
        return parts.joined(separator: "\n")
    }

    static let zh: [String: String] = [
        "A device": "一台设备",
        "%@ wants to view your screen": "%@ 请求查看你的屏幕",
        "New device": "新设备",
        "Connected before": "以前连接过",
        "Did someone call and ask you to share your screen? Deny: that is a scam.": "是不是有人打电话让你共享屏幕？请拒绝，这是诈骗。",
        "Refused in 1 minute if you do not answer.": "1 分钟内不回应将自动拒绝。",
        "Allow (view only)": "允许（仅查看）",
        "Deny": "拒绝",
        "RemoHop could not start sharing. Open RemoHop and try again.": "RemoHop 无法开始共享。请打开 RemoHop 再试一次。",
        "You stopped screen sharing in RemoHop.": "你已在 RemoHop 中停止屏幕共享。",
        "Open RemoHop once, then start sharing again.": "请先打开一次 RemoHop，再重新开始共享。",
        "Screen sharing stopped because of an error.": "屏幕共享因出错而停止。",
    ]

    /// The request category: Allow needs the phone unlocked (someone holding
    /// a locked phone cannot let a viewer in); dismissing the notification
    /// counts as Deny. Registered by the app at launch and by the extension
    /// when sharing starts (the same single category, in the current language).
    static func registerCategory(lang: String) {
        let allow = UNNotificationAction(identifier: allowAction, title: text("Allow (view only)", lang: lang),
                                         options: [.authenticationRequired])
        let deny = UNNotificationAction(identifier: denyAction, title: text("Deny", lang: lang), options: [.destructive])
        let cat = UNNotificationCategory(identifier: category, actions: [allow, deny], intentIdentifiers: [],
                                         options: [.customDismissAction])
        UNUserNotificationCenter.current().setNotificationCategories([cat])
    }
}
