import Foundation
import SwiftUI

/// The app's language, chosen in Settings: follow the system (Chinese for
/// any zh-* language, otherwise English), Simplified Chinese, or English.
enum AppLanguage: String, CaseIterable, Identifiable {
    case system, zh, en
    var id: String { rawValue }
}

/// The native screens' strings: the English text is the key, `zh` holds its
/// Simplified Chinese. An in-code table (rather than a String Catalog) so
/// the choice in Settings switches the app at once, without the system's
/// per-app language and a relaunch. The web page gets the same language:
/// ?lang= in its address, then remohopMobile.setLang when it changes.
///
/// Permission texts (Info.plist) are localized by en.lproj and
/// zh-Hans.lproj InfoPlist.strings, which also make the App Store list both
/// languages.
enum L10n {
    /// UserDefaults key of the choice ("system", "zh" or "en").
    static let key = "remohop.language"

    static var choice: AppLanguage {
        AppLanguage(rawValue: UserDefaults.standard.string(forKey: key) ?? "") ?? .system
    }

    /// "zh" or "en": the language shown now.
    static var effective: String { resolve(choice) }

    /// "zh" or "en" for a choice; `preferred` is the user's language list
    /// (the system's, or the app's own in the system Settings).
    static func resolve(_ choice: AppLanguage, preferred: [String] = Locale.preferredLanguages) -> String {
        switch choice {
        case .zh: return "zh"
        case .en: return "en"
        case .system: return (preferred.first ?? "").lowercased().hasPrefix("zh") ? "zh" : "en"
        }
    }

    static func locale(_ lang: String) -> Locale { Locale(identifier: lang == "zh" ? "zh-Hans" : "en") }

    static func text(_ english: String, lang: String = effective) -> String {
        lang == "zh" ? (zh[english] ?? english) : english
    }

    /// A pairing failure in the app's words where its code is known and the
    /// app is in Chinese; otherwise the core's own (English) message.
    static func pairError(code: String?, raw: String) -> String {
        guard effective == "zh", let code, let english = pairErrors[code] else { return raw }
        return text(english)
    }

    /// How a saved host logs in ("pairing", "key", "password") and whether it
    /// is online, as the core reports them.
    static func login(_ v: String) -> String { text(v) }
    static func presence(_ v: String) -> String { text(v.replacingOccurrences(of: "_", with: " ")) }

    static let pairErrors: [String: String] = [
        "bad_password": "Wrong password.",
        "unknown_id": "No host with that ID exists.",
        "host_offline": "That host is not online.",
        "no_path": "No direct path to the host could be found.",
        "no_rendezvous": "No rendezvous server is set: enter one above, or pair by address (host:port).",
        "host_key_changed": "The host's identity key changed since it was first seen. This could be an impersonation attempt.",
        "denied": "The host's owner denied the pairing, or nobody answered in time.",
        "locked_out": "The host has locked this device out after too many wrong passwords. Wait a while, then try again.",
        "password_required": "Enter the host's password.",
        "timeout": "Timed out.",
        "canceled": "Canceled.",
    ]

    static let zh: [String: String] = [
        // ContentView
        "RemoHop could not start: %@": "RemoHop 无法启动：%@",
        "Try again": "重试",
        // SettingsView
        "Settings": "设置",
        "Done": "完成",
        "Language": "语言",
        "Follow system": "跟随系统",
        "Applies to the app and its remote-control page at once.": "立即应用于本应用及其远程控制页面。",
        "Server key (optional)": "服务器密钥（可选）",
        "Allow relayed connections": "允许中继连接",
        "Rendezvous server": "汇合服务器",
        "Introduces this device to your hosts by their 9-digit ID. RemoHop has no default server: run your own with `remohop signal`. Without one you can connect by address (host:port). Relayed connections stay end-to-end encrypted.":
            "凭 9 位 ID 把这台设备介绍给你的被控端。RemoHop 不提供默认服务器：请用 `remohop signal` 自行搭建。没有汇合服务器时，可以通过地址（host:port）连接。中继连接同样是端到端加密的。",
        "This device": "本设备",
        "Device name shown to hosts (optional)": "向被控端显示的设备名称（可选）",
        "Save": "保存",
        "Copy key line": "复制密钥行",
        "Identity": "身份",
        "Hosts pin this key. To log in without any password, run `remohop authorize \"LINE\"` on a host with the copied line.":
            "被控端会固定这把密钥。要免密码登录，请在被控端上用复制的密钥行运行 `remohop authorize \"LINE\"`。",
        "Host ID, name or host:port": "被控端 ID、名称或 host:port",
        "Host password (used once, not stored)": "被控端密码（只用一次，不会保存）",
        "Name for this host (optional)": "给这台被控端起个名字（可选）",
        "Cancel": "取消",
        "Pair": "配对",
        "Pair with a host": "与被控端配对",
        "Log in once with the host's password; the host gives this device a token, so later connections need no password.":
            "用被控端的密码登录一次，被控端会给这台设备发一个令牌，以后连接就不再需要密码。",
        "Saved hosts": "已保存的被控端",
        "No saved hosts yet.": "还没有保存的被控端。",
        "Forget": "移除",
        "Check which are online": "检查哪些在线",
        "RemoHop talks only to the hosts you connect to and the rendezvous server you set here. Its identity, pairings and settings stay in this app's private storage. The app runs a local gateway on 127.0.0.1 that only its own page, holding a random per-launch token, can use.":
            "RemoHop 只与你连接的被控端以及你在这里设置的汇合服务器通信。它的身份、配对和设置都留在本应用的私有存储中。应用在 127.0.0.1 上运行一个本地网关，只有它自己的页面（持有每次启动时随机生成的令牌）才能使用。",
        "Settings saved.": "设置已保存。",
        "Key line copied.": "已复制密钥行。",
        "Connecting… the host may ask its owner to approve.": "正在连接…被控端可能需要其主人批准。",
        "Paired with %@ (host key %@).": "已与 %@ 配对（被控端密钥 %@）。",
        "Pairing failed.": "配对失败。",
        // saved hosts: login and presence
        "pairing": "配对",
        "key": "密钥",
        "password": "密码",
        "online": "在线",
        "offline": "离线",
        "unknown": "ID 不存在",
        "not checked": "未检查",
        "rate limited": "查询受限",
        "error": "检查失败",
        // pairing failures
        "Wrong password.": "密码错误。",
        "No host with that ID exists.": "没有这个 ID 的被控端。",
        "That host is not online.": "该被控端不在线。",
        "No direct path to the host could be found.": "找不到通往被控端的直连路径。",
        "No rendezvous server is set: enter one above, or pair by address (host:port).": "尚未设置汇合服务器：请在上方填写，或用地址（host:port）配对。",
        "The host's identity key changed since it was first seen. This could be an impersonation attempt.": "被控端的身份密钥与首次连接时不同，可能有人在冒充它。",
        "The host's owner denied the pairing, or nobody answered in time.": "被控端的主人拒绝了配对，或没有及时响应。",
        "The host has locked this device out after too many wrong passwords. Wait a while, then try again.": "密码错误次数过多，被控端已暂时拒绝这台设备。请稍后再试。",
        "Enter the host's password.": "请输入被控端的密码。",
        "Timed out.": "连接超时。",
        "Canceled.": "已取消。",
        // DownloadSaver
        "Download failed: %@": "下载失败：%@",
        "OK": "好",
    ]
}

/// `L("…")`: the English text, or its translation in the app's language.
func L(_ english: String) -> String { L10n.text(english) }

/// `L("… %@ …", value)`: the same with printf-style arguments.
func L(_ english: String, _ args: CVarArg...) -> String {
    String(format: L10n.text(english), locale: L10n.locale(L10n.effective), arguments: args)
}

extension Text {
    /// Text with inline Markdown (code spans) from an already translated
    /// string; `Text(String)` would show the backticks.
    init(markdown s: String) {
        self.init((try? AttributedString(markdown: s)) ?? AttributedString(s))
    }
}
