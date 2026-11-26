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

    /// An English text whose Chinese depends on where it is shown: looked
    /// up as "context|English" first (the own password's label is 固定密码,
    /// the choice in Password settings 我自己的固定密码).
    static func text(_ english: String, context: String, lang: String = effective) -> String {
        lang == "zh" ? (zh[context + "|" + english] ?? zh[english] ?? english) : english
    }

    /// A pairing failure in the app's words, in either language (a code the
    /// app does not know yet gets a general sentence). The core's own text
    /// is English and may name CLI commands: it goes to the diagnostic log
    /// only.
    static func pairError(code: String?) -> String {
        text(pairErrors[code ?? ""] ?? pairFailed)
    }

    static let pairFailed = "Pairing did not work. Turn on Diagnostic logs in Settings to see why."

    /// Codes a fix in Settings → Rendezvous server cures: the pair screen offers "Set up".
    static let rendezvousProblems: Set<String> = ["no_rendezvous", "rendezvous_unreachable", "signal_is_host", "signal_wrong_protocol"]

    /// A setting the core refused (SetSettingJSON's code), in the app's words.
    static func settingError(code: String?) -> String {
        text(settingErrors[code ?? ""] ?? "The setting could not be saved.")
    }

    static let settingErrors: [String: String] = [
        "bad_address": "Enter the server's name or IP address, optionally with a port, for example rendezvous.example.net:21116.",
        "bad_key": "This is not a server key. Paste the key the server's administrator gave you.",
        "bad_name": "Use at most 64 characters, without line breaks.",
        "bad_value": "Use at most 64 characters, without line breaks.",
        "error": "The setting could not be saved.",
    ]

    /// How a saved host logs in ("pairing", "key", "password") and whether it
    /// is online, as the core reports them.
    static func login(_ v: String) -> String { text(v) }
    static func presence(_ v: String) -> String { text(v.replacingOccurrences(of: "_", with: " ")) }

    static let pairErrors: [String: String] = [
        "bad_password": "Wrong password.",
        "unknown_id": "No host with that ID exists.",
        "host_offline": "That host is not online.",
        "no_path": "No direct path to the host could be found.",
        "no_rendezvous": "IDs are found through a rendezvous server, and none is set up yet. Set one up, or pair by the host's address (host:port) instead.",
        "host_key_changed": "The host's identity key changed since it was first seen. This could be an impersonation attempt.",
        "denied": "The host's owner denied the pairing, or nobody answered in time.",
        "locked_out": "The host has locked this device out after too many wrong passwords. Wait a while, then try again.",
        "password_required": "Enter the host's password.",
        "timeout": "Timed out.",
        "canceled": "Canceled.",
        "target_required": "Enter the host's ID or address.",
        "bad_target": "That is not a 9-digit ID, a saved name or an address such as 192.168.1.20:21118.",
        "bad_alias": "Start with a letter; then use letters, digits, single spaces, ., _ or - (up to 63 characters).",
        "already_pairing": "A pairing is already in progress. Wait for it to finish or cancel it.",
        "host_unreachable": "The host did not answer. Check the ID or address, and that RemoHop is running on it.",
        "cannot_resolve": "That address could not be found. Check its spelling.",
        "rendezvous_unreachable": "The rendezvous server set in Settings cannot be reached. Check its address and the network, or pair by address (host:port).",
        "rendezvous_rate_limited": "The rendezvous server is limiting requests from this device. Try again in a moment.",
        "signal_is_host": "The rendezvous server in Settings is a RemoHop host, not a rendezvous server. Pair with it by its address (host:port), and fix the setting.",
        "signal_wrong_protocol": "The address in Settings → Rendezvous server is not a RemoHop rendezvous server.",
        "host_busy": "The host is busy with other logins. Try again in a moment.",
        "key_rejected": "The host does not accept this device's key. Pair with its password.",
        "pairing_rejected": "The host no longer accepts this device's pairing. Pair again with its password.",
        "password_login_disabled": "This host does not accept password logins.",
    ]

    static let zh: [String: String] = [
        // ContentView
        "RemoHop could not start: %@": "RemoHop 无法启动：%@",
        "Try again": "重试",
        "The remote-control page could not be loaded.": "远程控制页面无法加载。",
        "Loading": "正在加载",
        // SettingsView
        "Settings": "设置",
        "Done": "完成",
        "Language": "语言",
        "Follow system": "跟随系统",
        "Applies to the app and its remote-control page at once.": "立即应用于本应用及其远程控制页面。",
        "Server key (optional)": "服务器密钥（可选）",
        "Allow relayed connections": "允许中继连接",
        "Rendezvous server": "汇合服务器",
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
        "RemoHop talks only to the hosts you connect to, the devices you let connect to this one, and the rendezvous server you set here. Its identity, pairings and settings stay in this app's private storage. The app runs a local gateway on 127.0.0.1 that only its own page, holding a random per-launch token, can use.":
            "RemoHop 只与你连接的被控端、你允许连接这台设备的设备，以及你在这里设置的汇合服务器通信。它的身份、配对和设置都留在本应用的私有存储中。应用在 127.0.0.1 上运行一个本地网关，只有它自己的页面（持有每次启动时随机生成的令牌）才能使用。",
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
        "IDs are found through a rendezvous server, and none is set up yet. Set one up, or pair by the host's address (host:port) instead.": "ID 要通过汇合服务器查找，而目前还没有设置。请先设置，或改用被控端的地址（host:port）配对。",
        "Enter the host's ID or address.": "请输入被控端的 ID 或地址。",
        "That is not a 9-digit ID, a saved name or an address such as 192.168.1.20:21118.": "这不是 9 位 ID、已保存的名称，也不是像 192.168.1.20:21118 这样的地址。",
        "A pairing is already in progress. Wait for it to finish or cancel it.": "已有一个配对正在进行。请等待它完成或取消它。",
        "The host did not answer. Check the ID or address, and that RemoHop is running on it.": "被控端没有应答。请检查 ID 或地址，并确认它上面正在运行 RemoHop。",
        "That address could not be found. Check its spelling.": "找不到这个地址，请检查拼写。",
        "The rendezvous server set in Settings cannot be reached. Check its address and the network, or pair by address (host:port).": "无法连接设置中的汇合服务器。请检查它的地址和网络，或用地址（host:port）配对。",
        "The rendezvous server is limiting requests from this device. Try again in a moment.": "汇合服务器正在限制本设备的请求，请稍后再试。",
        "The rendezvous server in Settings is a RemoHop host, not a rendezvous server. Pair with it by its address (host:port), and fix the setting.": "设置中填写的汇合服务器是一台 RemoHop 被控端，不是汇合服务器。请直接用它的地址（host:port）配对，并修改该设置。",
        "The address in Settings → Rendezvous server is not a RemoHop rendezvous server.": "“设置 → 汇合服务器”中填写的不是 RemoHop 汇合服务器。",
        "The host is busy with other logins. Try again in a moment.": "被控端正忙于处理其他登录，请稍后再试。",
        "The host does not accept this device's key. Pair with its password.": "被控端不接受本设备的密钥，请用它的密码配对。",
        "The host no longer accepts this device's pairing. Pair again with its password.": "被控端已不再接受本设备的配对，请用它的密码重新配对。",
        "This host does not accept password logins.": "这台被控端不接受密码登录。",
        "Pairing did not work. Turn on Diagnostic logs in Settings to see why.": "配对未成功。可以在设置中开启诊断日志查看原因。",
        "Set up": "去设置",
        "Enter the server's name or IP address, optionally with a port, for example rendezvous.example.net:21116.": "请填写服务器的域名或 IP 地址，可以带端口，例如 rendezvous.example.net:21116。",
        "This is not a server key. Paste the key the server's administrator gave you.": "这不是服务器密钥。请粘贴服务器管理员给你的密钥。",
        "Use at most 64 characters, without line breaks.": "最多 64 个字符，不能换行。",
        "The setting could not be saved.": "无法保存该设置。",
        "Downloading %@…": "正在下载 %@…",
        "Downloading %@ · %d%%": "正在下载 %@ · %d%%",
        "Download canceled.": "已取消下载。",
        "Another download is still running. Wait for it, or cancel it.": "另一个下载仍在进行。请等待它完成，或取消它。",
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
        "A rendezvous server is a small meeting point on the internet: it introduces this device to your computers by their 9-digit ID and helps the two find a direct path. RemoHop has no default server; enter the address of the one you or your administrator run. Without one you can still connect by address (host:port), for example to a computer on the same network.": "汇合服务器是互联网上的一个小“碰头点”：它凭 9 位 ID 把本设备介绍给你的电脑，并帮两者找到直连路径。RemoHop 不提供默认服务器，请填写你或管理员搭建的服务器地址。没有汇合服务器时，仍可以通过地址（host:port）连接，例如同一网络中的电脑。",
        "The server's public key, if you want this device to accept only that server. Leave it empty to trust the address.": "服务器的公钥，用于让本设备只接受这台服务器。留空则信任该地址。",
        "Relay": "中继",
        "When no direct path works, the connection goes through the server. It stays end-to-end encrypted, but is slower.": "无法直连时，连接经由服务器中转。仍然端到端加密，只是速度较慢。",
        "For administrators": "给管理员",
        "Ask whoever set up RemoHop on your computers for the server's address and key. To run your own, see \"Rendezvous server\" on the RemoHop website.": "请向为你的电脑安装 RemoHop 的人索取服务器地址和密钥。如果想自己搭建，请参阅 RemoHop 网站上的“汇合服务器”。",
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
        "The host's 9-digit ID (needs a rendezvous server, set in Settings), a saved name, or its address such as 192.168.1.20:21118 (no server needed).": "被控端的 9 位 ID（需要在设置中填写汇合服务器）、已保存的名称，或它的地址，例如 192.168.1.20:21118（不需要服务器）。",
        "Log size": "日志大小",
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
        "Only the hosts you connect to, the devices you let connect to this one, and the rendezvous server you set. No Readori server is involved, and nothing is sent to us.": "只连接你要控制的被控端、你允许连接这台设备的设备，以及你设置的汇合服务器。不经过 Readori 的服务器，也不会向我们发送任何数据。",
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
        // saved host page, rendezvous key hint
        "Paired · connects without a password": "已配对 · 免密码连接",
        "Authorised by key · connects without a password": "已用密钥授权 · 免密码连接",
        "Recent · needs the password": "最近连接 · 需要密码",
        "Paired: connects without a password": "已配对：连接时无需密码",
        "Authorised by key: connects without a password": "已用密钥授权：连接时无需密码",
        "Recent: needs the password each time": "最近连接：每次都需要密码",
        "Status": "状态",
        "ID": "ID",
        "Last connected": "上次连接",
        "Online status": "在线状态",
        "Connect": "连接",
        "Copy": "复制",
        "Forget this computer": "移除这台电脑",
        "Forget %@?": "移除 %@？",
        "Its saved pairing and key are deleted from this device, and any open session to it ends. To connect again you will need its password.": "本设备上保存的配对信息和密钥会被删除，与它的连接也会断开。以后再连接时需要输入它的密码。",
        "Start with a letter; then use letters, digits, single spaces, ., _ or - (up to 63 characters).": "请以字母或汉字开头，只能包含字母、汉字、数字、单个空格以及 . _ -，最多 63 个字符。",
        "Shown instead of the ID or address, and usable in place of it when you connect. Leave it empty to show the ID or address.": "显示在 ID 或地址的位置，连接时也可以用它代替 ID 或地址。留空则显示 ID 或地址。",
        "Removes the pairing and the pinned key from this device. The computer then needs its password again, and a changed key is no longer noticed.": "从本设备删除配对信息和固定的密钥。之后连接这台电脑需要再次输入密码，密钥变化也不会再被发现。",
        "Hosts this device has connected to or paired with. Tap one to rename it, connect to it or forget it; pull down to check which are online.": "本设备连接或配对过的被控端。轻点一台可重命名、连接或移除；下拉可检查哪些在线。",
        "No server key set": "未设置服务器密钥",
        "Without the key, this device trusts whichever server answers at this address the first time it connects (trust on first use). Paste the key your server's administrator gave you, or use the one this server presented after comparing it.": "没有密钥时，本设备第一次连接会信任在这个地址上应答的服务器（首次使用即信任）。请粘贴服务器管理员给你的密钥，或在核对后使用这台服务器提供的密钥。",
        "Use this key": "使用此密钥",
        "Privacy questions": "隐私问题",
        // Saved password (a host that does not pair)
        "Password saved · connects without a password": "已保存密码 · 免密码连接",
        "Password saved: connects without typing a password": "已保存密码：连接时无需输入密码",
        "Saved password": "保存的密码",
        "Show password": "显示密码",
        "Hide password": "隐藏密码",
        "Delete saved password": "删除保存的密码",
        "Delete the saved password?": "删除保存的密码？",
        "%@ stays in Saved hosts. To connect to it again you will need its password.": "%@ 仍会保留在已保存的被控端中。以后连接它时需要再次输入密码。",
        "The saved password could not be read.": "无法读取保存的密码。",
        "The saved password could not be deleted.": "无法删除保存的密码。",
        "This computer does not pair, so when you chose to remember it, RemoHop saved the password you typed. It stays on this device only and is used when you connect. Delete it and the computer needs its password again.":
            "这台电脑不接受配对，所以你选择记住它时，RemoHop 保存了你输入的密码。密码只保存在本设备上，连接时自动使用。删除后，连接这台电脑需要再次输入密码。",
        // Share this screen (broadcast extension)
        "Share this screen": "共享此屏幕",
        "Not sharing": "未在共享",
        "Starting…": "正在启动…",
        "Sharing": "正在共享",
        "Stopped because of an error": "因出错而停止",
        "Not available in this build": "此版本构建不支持",
        "Start sharing": "开始共享",
        "Stop sharing": "停止共享",
        "Password": "密码",
        "Notifications are off: answer requests here.": "通知已关闭：请在这里回应连接请求。",
        "Open Settings": "打开设置",
        "Requests": "连接请求",
        "Viewers": "正在查看",
        "A device": "一台设备",
        "Disconnect": "断开",
        "Disconnect all": "全部断开",
        "New device": "新设备",
        "Connected before": "以前连接过",
        "Refused in %d s": "%d 秒后自动拒绝",
        "Did someone call and ask you to share your screen? Deny: that is a scam.": "是不是有人打电话让你共享屏幕？请拒绝，这是诈骗。",
        "Deny": "拒绝",
        "Allow (view only)": "允许（仅查看）",
        "View only": "仅查看",
        "Viewers see this screen and nothing else. They cannot tap, type or open anything on this device, and no sound is shared.": "对方只能看到这个屏幕，不能在这台设备上点按、输入或打开任何东西，也不会共享声音。",
        "Start and stop": "开始和停止",
        "Sharing starts only when you tap Start sharing and then Start Broadcast. Stop it any time from the red status bar or Control Center, or with Stop sharing here.": "只有在你轻点“开始共享”、再轻点“开始直播”后才会开始共享。你可以随时通过红色状态栏或控制中心停止，也可以在这里轻点“停止共享”。",
        "Who can connect": "谁能连接",
        "Someone with the ID and the password shown here. You are asked every time, in a notification and on this page; a request you do not answer is refused after a minute. The password is new each time sharing starts, unless you set your own in Password settings.": "知道这里显示的 ID 和密码的人。每次连接都会先询问你（通过通知，也显示在本页）；一分钟内不回应的请求会被自动拒绝。每次开始共享时密码都会更换，除非你在“密码设置”中设了自己的固定密码。",
        "Coming back": "重新连接",
        "A viewer you allowed that drops out, for example because its app went to the background, comes back for 10 minutes without asking you again. Disconnecting it or stopping sharing ends that.": "你允许过的观看设备如果中途断开（比如它的 App 切到了后台），10 分钟内重新连接不会再问你。你断开它或停止共享后，就需要重新批准。",
        "What viewers see": "对方能看到什么",
        "Everything on the screen while sharing, including notifications that appear. Close anything private first.": "共享期间屏幕上的一切，包括弹出的通知。请先关闭涉及隐私的内容。",
        "When it ends": "何时结束",
        "When you stop it, when the phone restarts, or when the system ends it, for example when memory runs short. Viewers are disconnected at once.": "你停止共享、手机重启，或系统结束共享（例如内存不足）时。对方会立即断开。",
        "Scams": "防范诈骗",
        "Did someone call you saying they are from support, your bank or the police, and ask you to install this or share your screen? Do not share. That is a scam.": "是不是有人打电话自称客服、银行或警察，让你安装这个软件或共享屏幕？请不要共享，这是诈骗。",
        // Share this screen: Password settings
        "Password settings": "密码设置",
        "Random": "随机密码",
        "My own": "固定密码",
        "Password for incoming connections": "连接密码",
        "A new random password": "随机密码",
        "My own password": "我自己的固定密码",
        "Made each time sharing starts.": "每次开始共享时自动生成。",
        "Stays the same until you change it; at least 8 characters, with letters and digits.": "一直不变，直到你修改；至少 8 位，包含字母和数字。",
        "While sharing": "共享时",
        "A change applies at once, also while sharing; viewers already connected stay connected.": "修改立即生效，正在共享时也一样；已经连接的人不会被断开。",
        "Either way, every connection still asks you first.": "无论哪种，每次有人连接都会先问你。",
        "Repeat password": "再输入一次",
        "The two passwords do not match.": "两次输入的密码不一致。",
        "Use at least 8 characters, with at least two of: letters, digits, other characters.": "至少 8 位，且包含字母、数字、其他字符中的两种。",
        "The password could not be saved.": "无法保存密码。",
        "label|My own password": "固定密码",
    ]

    /// The English keys of the screen-sharing page, for the test that each
    /// has a translation.
    static let shareStrings: [String] = [
        "Share this screen",
        "Not sharing",
        "Starting…",
        "Sharing",
        "Stopped because of an error",
        "Not available in this build",
        "Start sharing",
        "Stop sharing",
        "Password",
        "Notifications are off: answer requests here.",
        "Open Settings",
        "Requests",
        "Viewers",
        "A device",
        "Disconnect",
        "Disconnect all",
        "New device",
        "Connected before",
        "Refused in %d s",
        "Did someone call and ask you to share your screen? Deny: that is a scam.",
        "Deny",
        "Allow (view only)",
        "View only",
        "Viewers see this screen and nothing else. They cannot tap, type or open anything on this device, and no sound is shared.",
        "Start and stop",
        "Sharing starts only when you tap Start sharing and then Start Broadcast. Stop it any time from the red status bar or Control Center, or with Stop sharing here.",
        "Who can connect",
        "Someone with the ID and the password shown here. You are asked every time, in a notification and on this page; a request you do not answer is refused after a minute. The password is new each time sharing starts, unless you set your own in Password settings.",
        "Coming back",
        "A viewer you allowed that drops out, for example because its app went to the background, comes back for 10 minutes without asking you again. Disconnecting it or stopping sharing ends that.",
        "What viewers see",
        "Everything on the screen while sharing, including notifications that appear. Close anything private first.",
        "When it ends",
        "When you stop it, when the phone restarts, or when the system ends it, for example when memory runs short. Viewers are disconnected at once.",
        "Scams",
        "Did someone call you saying they are from support, your bank or the police, and ask you to install this or share your screen? Do not share. That is a scam.",
        "Address",
        "ID",
        "Copy",
        "Status",
        "Password settings",
        "Random",
        "My own",
        "Password for incoming connections",
        "A new random password",
        "My own password",
        "Made each time sharing starts.",
        "Stays the same until you change it; at least 8 characters, with letters and digits.",
        "While sharing",
        "A change applies at once, also while sharing; viewers already connected stay connected.",
        "Either way, every connection still asks you first.",
        "Repeat password",
        "The two passwords do not match.",
        "Use at least 8 characters, with at least two of: letters, digits, other characters.",
        "The password could not be saved.",
        "Save",
        "Cancel",
        "Show password",
        "Hide password",
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
        "A rendezvous server is a small meeting point on the internet: it introduces this device to your computers by their 9-digit ID and helps the two find a direct path. RemoHop has no default server; enter the address of the one you or your administrator run. Without one you can still connect by address (host:port), for example to a computer on the same network.",
        "The server's public key, if you want this device to accept only that server. Leave it empty to trust the address.",
        "Relay",
        "When no direct path works, the connection goes through the server. It stays end-to-end encrypted, but is slower.",
        "For administrators",
        "Ask whoever set up RemoHop on your computers for the server's address and key. To run your own, see \"Rendezvous server\" on the RemoHop website.",
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
        "The host's 9-digit ID (needs a rendezvous server, set in Settings), a saved name, or its address such as 192.168.1.20:21118 (no server needed).",
        "Log size",
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
        "Only the hosts you connect to, the devices you let connect to this one, and the rendezvous server you set. No Readori server is involved, and nothing is sent to us.",
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
        "Paired · connects without a password",
        "Authorised by key · connects without a password",
        "Recent · needs the password",
        "Paired: connects without a password",
        "Authorised by key: connects without a password",
        "Recent: needs the password each time",
        "Status",
        "ID",
        "Last connected",
        "Online status",
        "Connect",
        "Copy",
        "Forget this computer",
        "Forget %@?",
        "Its saved pairing and key are deleted from this device, and any open session to it ends. To connect again you will need its password.",
        "Start with a letter; then use letters, digits, single spaces, ., _ or - (up to 63 characters).",
        "Shown instead of the ID or address, and usable in place of it when you connect. Leave it empty to show the ID or address.",
        "Removes the pairing and the pinned key from this device. The computer then needs its password again, and a changed key is no longer noticed.",
        "Hosts this device has connected to or paired with. Tap one to rename it, connect to it or forget it; pull down to check which are online.",
        "No server key set",
        "Without the key, this device trusts whichever server answers at this address the first time it connects (trust on first use). Paste the key your server's administrator gave you, or use the one this server presented after comparing it.",
        "Use this key",
        "Privacy questions",
        "Set up",
        "Downloading %@…",
        "Downloading %@ · %d%%",
        "Download canceled.",
        "Another download is still running. Wait for it, or cancel it.",
        "Password saved · connects without a password",
        "Password saved: connects without typing a password",
        "Saved password",
        "Show password",
        "Hide password",
        "Delete saved password",
        "Delete the saved password?",
        "%@ stays in Saved hosts. To connect to it again you will need its password.",
        "The saved password could not be read.",
        "The saved password could not be deleted.",
        "This computer does not pair, so when you chose to remember it, RemoHop saved the password you typed. It stays on this device only and is used when you connect. Delete it and the computer needs its password again.",
    ]
}

/// `L("…")`: the English text, or its translation in the app's language.
func L(_ english: String) -> String { L10n.text(english) }

/// `L("…", context: "…")`: the same, for a text translated differently in that place.
func L(_ english: String, context: String) -> String { L10n.text(english, context: context) }

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
