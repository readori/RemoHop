package quick

import (
	"fmt"
	"strings"
)

// Text shown outside the page: native dialogs and the terminal. The page
// has its own table (assets/app.js).
var messages = map[string]map[string]string{
	"en": {
		"ask_title":     "RemoHop: connection request",
		"ask_text":      "%s wants to connect to this computer.\n\nDevice key: %s\nAddress: %s\n\nAllow only if you are expecting this person right now.",
		"ask_action":    "%s, which is already connected, asks for your permission to:\n\n%s\n\nDevice key: %s",
		"ask_someone":   "A device",
		"ask_question":  "Allow this connection?",
		"allow":         "Allow",
		"deny":          "Deny",
		"error_title":   "RemoHop",
		"no_window":     "RemoHop could not open its window, so sharing was not started.\n\nNo web browser could be opened on this computer.",
		"start_failed":  "RemoHop could not start.\n\n%s",
		"redirect_text": "Opening RemoHop…",
	},
	"zh": {
		"ask_title":     "RemoHop：连接请求",
		"ask_text":      "%s 请求连接这台电脑。\n\n设备密钥：%s\n地址：%s\n\n只有当你此刻正在等这个人连接时才允许。",
		"ask_action":    "已连接的 %s 请求你许可以下操作：\n\n%s\n\n设备密钥：%s",
		"ask_someone":   "一台设备",
		"ask_question":  "允许这次连接吗？",
		"allow":         "允许",
		"deny":          "拒绝",
		"error_title":   "RemoHop",
		"no_window":     "RemoHop 无法打开窗口，因此没有开始共享。\n\n这台电脑上没有可以打开的网页浏览器。",
		"start_failed":  "RemoHop 无法启动。\n\n%s",
		"redirect_text": "正在打开 RemoHop…",
	},
}

func msg(lang, key string, args ...any) string {
	s, ok := messages[lang][key]
	if !ok {
		s = messages["en"][key]
	}
	if len(args) > 0 {
		return fmt.Sprintf(s, args...)
	}
	return s
}

// normLang reduces a locale name ("zh_CN.UTF-8", "zh-Hans-CN", "en-US") to
// a language the UI has, or "".
func normLang(locale string) string {
	l := strings.ToLower(strings.TrimSpace(locale))
	switch {
	case strings.HasPrefix(l, "zh"):
		return "zh"
	case strings.HasPrefix(l, "en"):
		return "en"
	}
	return ""
}

// askText is the native dialog's question about r.
func askText(lang string, r Request) string {
	who := r.Name
	if who == "" {
		who = r.Device
	}
	if who == "" {
		who = msg(lang, "ask_someone")
	} else {
		who = "“" + who + "”"
	}
	if r.Action != "" {
		return msg(lang, "ask_action", who, r.Action, r.Identity)
	}
	return msg(lang, "ask_text", who, r.Identity, r.From)
}
