package mobile

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"strings"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/config"
	"github.com/readori/RemoHop/internal/dial"
	"github.com/readori/RemoHop/internal/nat"
	"github.com/readori/RemoHop/internal/signal"
)

// The apps show every failure in their own words, chosen by a stable code;
// the core's English text is only for the diagnostic log. These are the
// codes PairWithPassword and SetSettingJSON report (see their comments).

// codeOf classifies an error from a dial or a pairing.
func codeOf(err error) string {
	switch auth.CodeOf(err) {
	case auth.CodeAuthFailed:
		return "bad_password"
	case auth.CodeLockedOut:
		return "locked_out"
	case auth.CodeDenied:
		return "denied"
	case auth.CodeBusy:
		return "host_busy"
	case auth.CodeUnknownToken:
		return "pairing_rejected"
	case auth.CodeUnknownKey:
		return "key_rejected"
	case auth.CodeNoPassword:
		return "password_login_disabled"
	}
	switch signal.CodeOf(err) {
	case signal.CodeUnknownID:
		return "unknown_id"
	case signal.CodeOffline:
		return "host_offline"
	case signal.CodeNoAnswer, signal.CodeDeclined:
		return "host_unreachable"
	case signal.CodeRateLimited:
		return "rendezvous_rate_limited"
	case signal.CodeIsHost, signal.CodeWrongProtocol:
		return signal.CodeOf(err) // "signal_is_host", "signal_wrong_protocol"
	}
	msg := err.Error()
	switch {
	case errors.Is(err, signal.ErrNotConfigured):
		return "no_rendezvous"
	case errors.Is(err, dial.ErrHostKeyChanged):
		return "host_key_changed"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, nat.ErrNoPath):
		return "no_path"
	case errors.Is(err, dial.ErrNoCredential):
		return "password_required"
	// The dialer has no sentinel for these; its wording is stable.
	case strings.HasPrefix(msg, "rendezvous server") && strings.Contains(msg, "unreachable"):
		return "rendezvous_unreachable"
	case strings.HasPrefix(msg, "no remohop host answered"):
		return "host_unreachable"
	case isDNS(err) || strings.HasPrefix(msg, "cannot resolve"):
		return "cannot_resolve"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	}
	return "error"
}

func isDNS(err error) bool {
	var d *net.DNSError
	return errors.As(err, &d)
}

var errAlreadyPairing = errors.New("a pairing is already in progress")

// requestError is a request the core turns down before trying anything,
// with its code.
type requestError struct {
	code string
	msg  string
}

func (e *requestError) Error() string { return e.msg }

// checkTarget turns down a target that is none of the three forms before
// the network is touched: the dialer's own message names a CLI command.
func checkTarget(d, target string) error {
	if cli.IsID(target) {
		return nil
	}
	if host, port, err := net.SplitHostPort(target); err == nil && host != "" && port != "" {
		return nil
	}
	if st, err := dial.OpenStore(d); err == nil {
		if _, ok, err := st.ByAlias(target); err == nil && ok {
			return nil
		}
	}
	return &requestError{"bad_target", "unknown target " + strconv.Quote(target) + ": expected a 9-digit ID, a saved name or HOST:PORT"}
}

// SetSettingJSON is SetSetting with an outcome the apps can show in their
// own language: {"ok":true} or {"ok":false,"code","error"}, where code is
//
//	bad_address  "signal" is not HOST or HOST:PORT
//	bad_key      "signal-pin" is not a server public key
//	bad_name     "name" is too long or has control characters
//	bad_value    "relay" or "outcomes" is not on or off
//	error        anything else (the text says what)
//
// The returned error is reserved for calling it before Init.
func SetSettingJSON(key, value string) (string, error) {
	if _, err := configDir(); err != nil {
		return "", err
	}
	res := map[string]any{"ok": true}
	if err := SetSetting(key, value); err != nil {
		res = map[string]any{"ok": false, "code": settingCode(key, value, err), "error": cli.Sanitize(err.Error())}
	}
	b, err := json.Marshal(res)
	return string(b), err
}

// settingCode says whether err was the value being refused, and for which
// setting, or something else (a file that could not be written).
func settingCode(key, value string, err error) string {
	if !allowedSetting(key) {
		return "error"
	}
	value = strings.TrimSpace(value)
	invalid := false
	if key == signal.SettingAddr && value != "" && checkRendezvous(value) != nil {
		invalid = true
	} else if s, ok := config.LookupSetting(key); ok && value != "" && s.Validate != nil && s.Validate(value) != nil {
		invalid = true
	}
	if !invalid {
		return "error"
	}
	switch key {
	case signal.SettingAddr:
		return "bad_address"
	case signal.SettingPin:
		return "bad_key"
	case signal.SettingRelay, dial.SettingOutcomes:
		return "bad_value"
	default:
		return "bad_name"
	}
}

// PendingConnections counts connection attempts in the page that have not
// finished: dialing, or waiting for the host's owner to approve. The apps
// keep the process alive for them as for an open session (the user often
// switches to a chat to say "please press Allow" exactly then).
func PendingConnections() int {
	if srv := current(); srv != nil {
		return srv.PendingAttempts()
	}
	return 0
}
