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
        // Settings screens (SettingsView, SettingsScreens, Diagnostics)
        "Connection": "连接",
        "Device name": "设备名称",
        "Identity key": "身份密钥",
        "Hosts": "被控端",
        "General": "通用",
        "Support": "支持",
        "Diagnostic logs": "诊断日志",
        "On": "开",
        "Off": "关",
        "Privacy": "隐私",
        "About RemoHop": "关于 RemoHop",
        "Not set": "未设置",
        "Problem": "有问题",
        "Paired with %@.": "已与 %@ 配对。",
        "About this screen": "关于此页",
        "Saved": "已保存",
        "Server": "服务器",
        "Address": "地址",
        "Server key": "服务器密钥",
        "Optional": "可选",
        "Test connection": "测试连接",
        "Relay when there is no direct path": "无法直连时使用中继",
        "Finds your computers by their 9-digit ID. Not needed when you connect by address (host:port). RemoHop has no default server: use your own.": "凭 9 位 ID 找到你的电脑。用地址（host:port）连接时不需要。RemoHop 不提供默认服务器，请使用你自己的。",
        "The server's public key, if you want this device to accept only that server. Leave it empty to trust the address.": "服务器的公钥，用于让本设备只接受这台服务器。留空则信任该地址。",
        "Relay": "中继",
        "When no direct path works, the connection goes through the server. It stays end-to-end encrypted, but is slower.": "无法直连时，连接经由服务器中转。仍然端到端加密，只是速度较慢。",
        "For administrators": "给管理员",
        "Run a server with `remohop signal`; it prints its address and key.": "用 `remohop signal` 运行服务器，它会显示地址和密钥。",
        "Working": "正常",
        "This is a host, not a server": "这是被控端，不是服务器",
        "Unreachable": "无法连接",
        "Key does not match": "密钥不匹配",
        "The name hosts show for this device when it pairs with them. Empty: the device model.": "与被控端配对时，被控端显示的本设备名称。留空则使用设备型号。",
        "Fingerprint": "指纹",
        "This device's own key, created on first use and kept only here. Hosts remember it, so they can tell it is the same device next time.": "本设备自己的密钥，首次使用时生成，只保存在这里。被控端会记住它，下次就能认出是同一台设备。",
        "Key line": "密钥行",
        "Lets this device log in to a host without any password. On the host, run `remohop authorize \"LINE\"` with the copied line.": "让本设备无需任何密码即可登录被控端。在被控端上用复制的密钥行运行 `remohop authorize \"LINE\"`。",
        "Hosts this device has connected to or paired with, with their pinned keys and pairing tokens. Swipe left to rename or forget one; pull down to check which are online.": "本设备连接或配对过的被控端，以及它们的固定密钥和配对令牌。向左滑动可重命名或移除；下拉可检查哪些在线。",
        "Rename": "重命名",
        "Name": "名称",
        "Host ID or address": "被控端 ID 或地址",
        "Host password": "被控端密码",
        "Name (optional)": "名称（可选）",
        "Log in once with the host's password. The host gives this device a token, so later connections need no password. The password is used once and not stored.": "用被控端的密码登录一次，被控端会给本设备发一个令牌，以后连接无需密码。密码只用这一次，不会保存。",
        "The host's 9-digit ID (needs a rendezvous server), a saved name, or its address such as 192.168.1.20:21118.": "被控端的 9 位 ID（需要汇合服务器）、已保存的名称，或它的地址，例如 192.168.1.20:21118。",
        "Log size": "日志大小",
        "File format": "文件格式",
        "Share diagnostic logs": "分享诊断日志",
        "Email support": "发邮件给支持团队",
        "Clear logs": "清除日志",
        "What is recorded": "记录什么",
        "While this is on, the app keeps a log of what it does (connection steps, errors, app events) on this device, up to about 1 MB. It is off by default, and turning it off deletes the log.": "开启后，应用会在本设备上记录它的运行情况（连接步骤、错误、应用事件），最多约 1 MB。默认关闭，关闭时会删除日志。",
        "What is removed": "去除什么",
        "Passwords, access tokens, pairing tokens and keys are removed before anything is written. Host IDs, host names and IP addresses stay in, because support needs them to follow a connection.": "写入之前会去除密码、访问令牌、配对令牌和密钥。被控端 ID、主机名和 IP 地址会保留，因为支持团队需要它们来分析连接。",
        "Who sees it": "谁能看到",
        "Nobody, unless you share it: the log is never uploaded. Share it or email it to support yourself when something does not work.": "除非你主动分享，否则没有人能看到：日志从不上传。遇到问题时，你可以自己分享或通过邮件发给支持团队。",
        "RemoHop diagnostic logs": "RemoHop 诊断日志",
        "Please describe what happened (what you did, what you expected, what you saw):": "请描述发生了什么（你做了什么、预期是什么、实际看到了什么）：",
        "No analytics, ads or tracking": "没有统计分析、广告或跟踪",
        "Your keys, pairings and settings stay on this device": "密钥、配对和设置只保存在本设备上",
        "Sessions are end-to-end encrypted": "会话端到端加密",
        "Diagnostic logs stay here unless you share them": "诊断日志留在本设备，除非你主动分享",
        "Privacy policy": "隐私政策",
        "What the app talks to": "应用会连接谁",
        "Only the hosts you connect to and the rendezvous server you set. No Readori server is involved, and nothing is sent to us.": "只连接你要控制的被控端，以及你设置的汇合服务器。不经过 Readori 的服务器，也不会向我们发送任何数据。",
        "The local gateway": "本地网关",
        "The app runs its remote-control page on a gateway inside the app itself (127.0.0.1). Only the app's own page, holding a random token made at each launch, can use it.": "应用的远程控制页面运行在应用内部的网关上（127.0.0.1）。只有应用自己的页面（持有每次启动时随机生成的令牌）才能使用它。",
        "Off by default. When you turn them on they stay on this device, with passwords and tokens removed, and leave it only when you share them.": "默认关闭。开启后日志只保存在本设备上，并去除密码和令牌；只有你分享时才会离开本设备。",
        "Version %@ (%@)": "版本 %@（%@）",
        "Version": "版本",
        "Build": "构建号",
        "Core": "核心",
        "Website": "网站",
        "Source code": "源代码",
        "License: AGPL-3.0": "许可证：AGPL-3.0",
        "Open-source notices": "开源声明",
        "Contact": "联系我们",
        "Telegram": "Telegram",
        "Terms of use": "使用条款",
        "Third-party licenses": "第三方许可证",
        "Not a rendezvous server": "不是汇合服务器",
    ]

    /// The English keys of the settings screens, for the test that each has
    /// a translation.
    static let settingsStrings: [String] = [
        "Connection",
        "Device name",
        "Identity key",
        "Hosts",
        "General",
        "Support",
        "Diagnostic logs",
        "On",
        "Off",
        "Privacy",
        "About RemoHop",
        "Not set",
        "OK",
        "Problem",
        "Paired with %@.",
        "About this screen",
        "Saved",
        "Server",
        "Address",
        "Server key",
        "Optional",
        "Test connection",
        "Relay when there is no direct path",
        "Finds your computers by their 9-digit ID. Not needed when you connect by address (host:port). RemoHop has no default server: use your own.",
        "The server's public key, if you want this device to accept only that server. Leave it empty to trust the address.",
        "Relay",
        "When no direct path works, the connection goes through the server. It stays end-to-end encrypted, but is slower.",
        "For administrators",
        "Run a server with `remohop signal`; it prints its address and key.",
        "Working",
        "This is a host, not a server",
        "Unreachable",
        "Key does not match",
        "The name hosts show for this device when it pairs with them. Empty: the device model.",
        "Fingerprint",
        "This device's own key, created on first use and kept only here. Hosts remember it, so they can tell it is the same device next time.",
        "Key line",
        "Lets this device log in to a host without any password. On the host, run `remohop authorize \"LINE\"` with the copied line.",
        "Hosts this device has connected to or paired with, with their pinned keys and pairing tokens. Swipe left to rename or forget one; pull down to check which are online.",
        "Rename",
        "Name",
        "Save",
        "Host ID or address",
        "Host password",
        "Name (optional)",
        "Log in once with the host's password. The host gives this device a token, so later connections need no password. The password is used once and not stored.",
        "The host's 9-digit ID (needs a rendezvous server), a saved name, or its address such as 192.168.1.20:21118.",
        "Log size",
        "File format",
        "Share diagnostic logs",
        "Email support",
        "Clear logs",
        "What is recorded",
        "While this is on, the app keeps a log of what it does (connection steps, errors, app events) on this device, up to about 1 MB. It is off by default, and turning it off deletes the log.",
        "What is removed",
        "Passwords, access tokens, pairing tokens and keys are removed before anything is written. Host IDs, host names and IP addresses stay in, because support needs them to follow a connection.",
        "Who sees it",
        "Nobody, unless you share it: the log is never uploaded. Share it or email it to support yourself when something does not work.",
        "RemoHop diagnostic logs",
        "Please describe what happened (what you did, what you expected, what you saw):",
        "No analytics, ads or tracking",
        "Your keys, pairings and settings stay on this device",
        "Sessions are end-to-end encrypted",
        "Diagnostic logs stay here unless you share them",
        "Privacy policy",
        "What the app talks to",
        "Only the hosts you connect to and the rendezvous server you set. No Readori server is involved, and nothing is sent to us.",
        "The local gateway",
        "The app runs its remote-control page on a gateway inside the app itself (127.0.0.1). Only the app's own page, holding a random token made at each launch, can use it.",
        "Off by default. When you turn them on they stay on this device, with passwords and tokens removed, and leave it only when you share them.",
        "Version %@ (%@)",
        "Version",
        "Build",
        "Core",
        "Website",
        "Source code",
        "License: AGPL-3.0",
        "Open-source notices",
        "Contact",
        "Telegram",
        "Terms of use",
        "Third-party licenses",
        "Not a rendezvous server",
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
