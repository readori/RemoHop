// Package redact removes secrets from text that leaves the device in a
// diagnostic report (the mobile apps' and the desktop app's): passwords,
// tokens, keys. Host IDs, names and addresses stay, because following a
// connection needs them.
package redact

import (
	"regexp"
	"strings"
)

var secretWords = []string{"password", "passwd", "passphrase", "secret", "token", "ticket", "csrf", "cookie",
	"authorization", "privatekey", "private_key", "private-key", "otp"}

// SecretKey reports whether an attribute named k holds a secret.
func SecretKey(k string) bool {
	k = strings.ToLower(k)
	if k == "pw" || k == "pass" || k == "sid" || k == "t" {
		return true
	}
	for _, w := range secretWords {
		if strings.Contains(k, w) {
			return true
		}
	}
	return false
}

var redactions = []struct {
	re   *regexp.Regexp
	with string
}{
	// PEM private keys (and anything armoured as one).
	{regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----[\s\S]*?(-----END [A-Z0-9 ]*PRIVATE KEY-----|$)`), "[private key redacted]"},
	// The gateway's access token is the first path element of its URL.
	{regexp.MustCompile(`(?i)(\b(?:https?|wss?)://[^/\s"'<>]+/)[A-Za-z0-9_~-]{12,}`), "${1}[token]"},
	// Secret query parameters.
	{regexp.MustCompile(`(?i)([?&](?:t|token|ticket|password|pass|pw|secret|csrf|sid|key|otp)=)[^&\s"'<>]+`), "${1}[redacted]"},
	// Authorization headers.
	{regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}`), "${1} [redacted]"},
	// key=value and "key": "value" pairs whose key names a secret.
	{regexp.MustCompile(`(?i)("?\b[a-z0-9_.-]*(?:password|passwd|passphrase|secret|token|ticket|csrf|cookie|private[_-]?key|otp)[a-z0-9_.-]*"?\s*[=:]\s*)("(?:[^"\\]|\\.)*"|'[^']*'|[^\s,;&}\]]+)`), "${1}[redacted]"},
	{regexp.MustCompile(`(?i)(\b(?:pw|pass|sid|x-remohop-session|x-remohop-csrf)\s*[=:]\s*)("(?:[^"\\]|\\.)*"|[^\s,;&}\]]+)`), "${1}[redacted]"},
	// Long opaque strings (tokens, keys) that slipped through: 32 or more
	// base64/hex characters in one run. Fingerprints (groups of four hex
	// digits with colons), IDs and addresses are shorter runs.
	{regexp.MustCompile(`[A-Za-z0-9+/_-]{32,}={0,2}`), "[redacted]"},
}

// Text removes secrets from s: gateway tokens in URLs, secret query
// parameters and key=value pairs, authorization headers, PEM private keys
// and long opaque tokens. Host IDs, host names and IP addresses are kept.
func Text(s string) string {
	for _, r := range redactions {
		s = r.re.ReplaceAllString(s, r.with)
	}
	return s
}
