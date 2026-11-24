// Package mobile is the Go core of the RemoHop Android and iOS apps.
//
// The apps are controllers: a native shell around a WebView that shows the
// `remohop web` page. This package runs that gateway in the app's own process,
// on 127.0.0.1 with a random port and a fresh random token per start, and
// keeps every piece of state (identity key, saved peers, pairing tokens,
// settings) in a directory the app passes in: its private files directory.
// Nothing here looks at HOME.
//
// The API is shaped for gomobile: package-level functions taking and
// returning strings, booleans, integers and errors, with lists as JSON.
// gomobile cannot bind a package under internal/ (its generated wrapper
// lives outside this module tree), so mobile/remohopcore re-exports these
// functions one for one and is what `gomobile bind` is pointed at.
//
// Security: the gateway listens on loopback only, but on Android every app
// on the device can reach 127.0.0.1, so the token in the URL is the only
// thing between another app and the user's hosts. The URL is returned to
// the caller and never logged here; callers must not log it either, and
// should hand it to the WebView's initial load and nowhere else.
package mobile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/config"
	"github.com/readori/RemoHop/internal/dial"
	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/nat"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/signal"
	"github.com/readori/RemoHop/internal/web"
)

// Settings the app may read and change. Everything else in config.json is
// left alone.
var appSettings = []string{signal.SettingAddr, signal.SettingPin, signal.SettingRelay, hostd.SettingName}

var (
	mu       sync.Mutex
	dir      string
	deviceNm string
	gw       *web.Server
	gwDone   chan error

	pairMu     sync.Mutex
	pairCancel context.CancelFunc
)

// pageIdle is how long the gateway keeps a page's host sessions without a
// heartbeat. A phone pauses a backgrounded WebView, so this is much longer
// than the desktop gateway's two minutes.
const pageIdle = 30 * time.Minute

// pairTimeout bounds PairWithPassword, which may wait for the host's owner
// to approve the login.
const pairTimeout = 4 * time.Minute

// Init points the core at the app's private configuration directory and,
// optionally, a directory for temporary files (the app's cache directory;
// Android and iOS have no writable /tmp). It must be called before anything
// else and may be called again with the same directory, for example when an
// Activity is recreated.
//
// The directory is created with mode 0700 if needed. RemoHop's settings and
// rendezvous lookup read the configuration directory from REMOHOP_CONFIG_DIR,
// so Init sets that variable for this process.
func Init(configDir, tempDir string) error {
	mu.Lock()
	defer mu.Unlock()
	if configDir == "" || !filepath.IsAbs(configDir) {
		return errors.New("mobile: the configuration directory must be an absolute path")
	}
	configDir = filepath.Clean(configDir)
	if gw != nil && configDir != dir {
		return errors.New("mobile: stop the gateway before changing the configuration directory")
	}
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(configDir, 0o700); err != nil {
		return err
	}
	if err := os.Setenv("REMOHOP_CONFIG_DIR", configDir); err != nil {
		return err
	}
	if tempDir != "" {
		if !filepath.IsAbs(tempDir) {
			return errors.New("mobile: the temporary directory must be an absolute path")
		}
		if err := os.MkdirAll(tempDir, 0o700); err != nil {
			return err
		}
		if err := os.Setenv("TMPDIR", tempDir); err != nil {
			return err
		}
	}
	dir = configDir
	return nil
}

func configDir() (string, error) {
	mu.Lock()
	defer mu.Unlock()
	if dir == "" {
		return "", errors.New("mobile: Init has not been called")
	}
	return dir, nil
}

// SetAppVersion sets the version the page shows ("RemoHop <version>").
func SetAppVersion(v string) {
	web.Version = v
}

// SetDeviceName sets the label this device gives itself when it pairs with
// a host (shown in `remohop host paired` on the host), for example the phone's
// model. It is not persisted; the "name" setting, if set, wins.
func SetDeviceName(name string) {
	mu.Lock()
	defer mu.Unlock()
	deviceNm = auth.CleanText(name, auth.MaxTokenName)
}

func pairLabel() string {
	if n := hostd.DeviceName(); n != "" {
		return n
	}
	mu.Lock()
	defer mu.Unlock()
	if deviceNm != "" {
		return deviceNm
	}
	return "RemoHop mobile"
}

// quietLogger drops everything below warnings. The gateway logs no URLs or
// tokens, but a phone's log is readable by more than the user, so the core
// stays quiet by default.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// StartGateway starts the in-process web gateway on 127.0.0.1 with a random
// port and a fresh token and returns the URL to load, which contains the
// token. If the gateway is already running it returns the same URL: the
// token changes per start, not per call.
func StartGateway() (string, error) {
	d, err := configDir()
	if err != nil {
		return "", err
	}
	mu.Lock()
	defer mu.Unlock()
	if gw != nil {
		select {
		case <-gwDone:
			gw.Close()
			gw, gwDone = nil, nil
		default:
			return gw.URL(), nil
		}
	}
	srv, err := web.New(web.Config{
		Listen:    "127.0.0.1:0",
		ConfigDir: d,
		Dial:      dialGateway(d),
		Pair:      pairGateway(d),
		PageIdle:  pageIdle,
		Logger:    quietLogger(),
	})
	if err != nil {
		return "", err
	}
	done := make(chan error, 1)
	go func() {
		done <- srv.Serve(context.Background())
		close(done)
	}()
	gw, gwDone = srv, done
	return srv.URL(), nil
}

// dialGateway is the gateway's default dial plus this device's local
// addresses (see SetLocalAddresses) and the quiet logger.
func dialGateway(d string) web.DialFunc {
	return func(ctx context.Context, req web.DialRequest) (*peer.Conn, error) {
		return dial.Dial(ctx, req.Target, dial.Options{
			Password: req.Password,
			Notify:   req.Notify,
			Dir:      d,
			Locals:   localAddrs,
			Logger:   quietLogger(),
		})
	}
}

var (
	localsMu  sync.Mutex
	nativeLAN []netip.Addr
)

// SetLocalAddresses tells the core this device's own IP addresses, as a
// list separated by commas or spaces. Android 11 and later refuse apps the
// netlink requests Go's interface enumeration uses, so without this a
// phone advertises no LAN candidates and two devices on the same Wi-Fi
// cannot find the direct path between them. The Android app calls it with
// the active network's link addresses whenever the network changes.
// Loopback, link-local and unparsable entries are ignored.
func SetLocalAddresses(list string) {
	var v4, v6 []netip.Addr
	for _, f := range strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' }) {
		if i := strings.IndexByte(f, '/'); i >= 0 {
			f = f[:i] // "192.168.1.5/24" from LinkAddress.toString()
		}
		ip, err := netip.ParseAddr(f)
		if err != nil {
			continue
		}
		ip = ip.Unmap().WithZone("")
		if !ip.IsValid() || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsMulticast() {
			continue
		}
		if ip.Is4() {
			v4 = append(v4, ip)
		} else {
			v6 = append(v6, ip)
		}
	}
	all := append(v4, v6...)
	if len(all) > nat.MaxCandidates {
		all = all[:nat.MaxCandidates]
	}
	localsMu.Lock()
	nativeLAN = all
	localsMu.Unlock()
}

// localAddrs prefers Go's own enumeration and falls back to what the app
// reported.
func localAddrs() []netip.Addr {
	if a := nat.InterfaceAddrs(); len(a) > 0 {
		return a
	}
	localsMu.Lock()
	defer localsMu.Unlock()
	return append([]netip.Addr(nil), nativeLAN...)
}

// pairGateway is the page's "pair this gateway" action with this device's
// label instead of the process hostname (which is "localhost" on a phone).
func pairGateway(d string) web.PairFunc {
	return func(ctx context.Context, conn *peer.Conn, name string) error {
		info := dial.InfoOf(conn)
		if info == nil {
			return errors.New("this connection was not established by remohop and cannot be paired")
		}
		if name == "" {
			name = pairLabel()
		}
		p, err := hostd.Pair(ctx, conn, hostd.PairRequest{Name: name})
		if err != nil {
			return err
		}
		return dial.SavePairing(info, p, "", d)
	}
}

// StopGateway stops the gateway and ends every host session it holds. It
// is safe to call when nothing runs.
func StopGateway() {
	mu.Lock()
	srv := gw
	gw, gwDone = nil, nil
	mu.Unlock()
	if srv != nil {
		srv.Close()
	}
}

// GatewayRunning reports whether StartGateway's server is serving.
func GatewayRunning() bool {
	mu.Lock()
	defer mu.Unlock()
	if gw == nil {
		return false
	}
	select {
	case <-gwDone:
		return false
	default:
		return true
	}
}

// GetSetting returns one of the settings the app manages: "signal" (the
// rendezvous server, HOST[:PORT]), "signal-pin" (its public key), "relay"
// (on/off) and "name" (this device's name shown to peers). Unset values are
// returned as "".
func GetSetting(key string) (string, error) {
	if _, err := configDir(); err != nil {
		return "", err
	}
	if !allowedSetting(key) {
		return "", fmt.Errorf("mobile: %q is not a setting the app manages", key)
	}
	return config.Get(key)
}

// SetSetting validates and stores one of the settings GetSetting reads; an
// empty value removes it. There is no default rendezvous server: until the
// user sets "signal", hosts can only be reached by address (host:port).
func SetSetting(key, value string) error {
	if _, err := configDir(); err != nil {
		return err
	}
	if !allowedSetting(key) {
		return fmt.Errorf("mobile: %q is not a setting the app manages", key)
	}
	value = strings.TrimSpace(value)
	if key == signal.SettingAddr && value != "" {
		if err := checkRendezvous(value); err != nil {
			return err
		}
		// Pin and address go together: a pin for the previous server
		// would make the new one fail with a confusing key mismatch.
		old, _ := config.Get(signal.SettingAddr)
		if old != value {
			if err := config.Set(signal.SettingPin, ""); err != nil {
				return err
			}
		}
	}
	return config.Set(key, value)
}

// checkRendezvous is stricter than the CLI's check, because the value comes
// from a phone's text field: a host name or address and an optional
// numeric port.
func checkRendezvous(v string) error {
	host, port, err := signal.SplitAddr(v)
	if err != nil {
		return err
	}
	bad := fmt.Errorf("%q is not a rendezvous server address: use HOST or HOST:PORT", v)
	if host == "" || strings.ContainsAny(host, " \t/\\@?#") {
		return bad
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return bad
	}
	return nil
}

func allowedSetting(key string) bool {
	for _, k := range appSettings {
		if k == key {
			return true
		}
	}
	return false
}

// SettingsJSON returns the managed settings and this device's identity as
// one JSON object: {"signal","signal-pin","relay","name","fingerprint"}.
func SettingsJSON() (string, error) {
	d, err := configDir()
	if err != nil {
		return "", err
	}
	all, err := config.AllIn(d)
	if err != nil {
		return "", err
	}
	out := map[string]string{}
	for _, k := range appSettings {
		out[k] = all[k]
	}
	if out[signal.SettingRelay] == "" {
		out[signal.SettingRelay] = "on"
	}
	out["fingerprint"] = fingerprint(d)
	b, err := json.Marshal(out)
	return string(b), err
}

func fingerprint(d string) string {
	blob, err := os.ReadFile(filepath.Join(d, auth.IdentityFile))
	if err != nil {
		return ""
	}
	id, err := auth.ParseIdentity(blob)
	if err != nil {
		return ""
	}
	return auth.Fingerprint(id.Public())
}

// Identity returns this device's identity fingerprint, creating the
// identity key on first use.
func Identity() (string, error) {
	d, err := configDir()
	if err != nil {
		return "", err
	}
	id, err := auth.LoadIdentity(d)
	if err != nil {
		return "", err
	}
	return auth.Fingerprint(id.Public()), nil
}

// PublicKeyLine returns this device's public key as the one-line form
// `remohop authorize` takes on a host, so the device can log in by key with no
// password at all.
func PublicKeyLine() (string, error) {
	d, err := configDir()
	if err != nil {
		return "", err
	}
	id, err := auth.LoadIdentity(d)
	if err != nil {
		return "", err
	}
	return auth.FormatKeyLine(id.Public(), pairLabel()), nil
}

// DevicesJSON lists the saved hosts as a JSON array of objects with
// target, alias, login ("pairing", "key" or "password"), presence
// ("online", "offline", "unknown", "not_checked", "error", ...), error,
// last_seen and host_identity. With checkOnline it asks the configured
// rendezvous server which of them are online (bounded to 10 seconds);
// without one configured the presence is "error" with the reason.
func DevicesJSON(checkOnline bool) (string, error) {
	d, err := configDir()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	devs, err := dial.Devices(ctx, dial.DevicesOptions{Options: dial.Options{Dir: d, Logger: quietLogger()}, Offline: !checkOnline})
	if err != nil {
		return "", err
	}
	if devs == nil {
		devs = []dial.Device{}
	}
	b, err := json.Marshal(devs)
	return string(b), err
}

// PairWithPassword logs in to target (a 9-digit ID, a saved alias or
// host:port) with the host's password, asks the host for a pairing token
// and saves it, under alias if one is given, so later connections from the
// app need no password. It may wait minutes for the host's owner to approve
// the login; CancelPair stops it.
//
// gomobile hands errors to Java and Swift as text only, so the outcome is
// a JSON object either way: {"ok":true,"target","alias","host_identity",
// "new_host"} or {"ok":false,"code","error"}, where code is one of
// bad_password, unknown_id, host_offline, no_path, no_rendezvous,
// host_key_changed, denied, locked_out, password_required, timeout,
// canceled, bad_request or error. The returned error is reserved for
// calling it before Init.
//
// The password is used for this one login and not kept.
func PairWithPassword(target, password, alias string) (string, error) {
	d, err := configDir()
	if err != nil {
		return "", err
	}
	res, perr := pair(d, target, password, alias)
	if perr != nil {
		code := codeOf(perr)
		if errors.Is(perr, errBadRequest) {
			code = "bad_request"
		}
		res = map[string]any{"ok": false, "code": code, "error": cli.Sanitize(perr.Error())}
	}
	b, err := json.Marshal(res)
	return string(b), err
}

var errBadRequest = errors.New("bad request")

type badRequest struct{ msg string }

func (e *badRequest) Error() string   { return e.msg }
func (e *badRequest) Is(t error) bool { return t == errBadRequest }

func pair(d, target, password, alias string) (map[string]any, error) {
	target = cli.NormalizeTarget(strings.TrimSpace(target))
	if target == "" {
		return nil, &badRequest{"enter a 9-digit ID, a saved name, or host:port"}
	}
	if password == "" {
		return nil, &badRequest{"enter the host's password"}
	}
	alias = strings.TrimSpace(alias)
	if alias != "" {
		if err := dial.CheckAlias(alias); err != nil {
			return nil, &badRequest{err.Error()}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), pairTimeout)
	defer cancel()
	pairMu.Lock()
	if pairCancel != nil {
		pairMu.Unlock()
		return nil, &badRequest{"a pairing is already in progress"}
	}
	pairCancel = cancel
	pairMu.Unlock()
	defer func() {
		pairMu.Lock()
		pairCancel = nil
		pairMu.Unlock()
	}()

	conn, err := dial.Dial(ctx, target, dial.Options{Password: password, Dir: d, Locals: localAddrs, Logger: quietLogger()})
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	info := dial.InfoOf(conn)
	if info == nil {
		return nil, errors.New("this connection cannot be paired")
	}
	p, err := hostd.Pair(ctx, conn, hostd.PairRequest{Name: pairLabel()})
	if err != nil {
		return nil, err
	}
	if err := dial.SavePairing(info, p, alias, d); err != nil {
		return nil, fmt.Errorf("the host issued a token but it could not be saved: %w", err)
	}
	return map[string]any{
		"ok":            true,
		"target":        info.Target,
		"alias":         alias,
		"host_identity": auth.Fingerprint(info.HostKey),
		"new_host":      info.NewHost,
	}, nil
}

// CancelPair stops a PairWithPassword in progress, if any.
func CancelPair() {
	pairMu.Lock()
	defer pairMu.Unlock()
	if pairCancel != nil {
		pairCancel()
	}
}

// SetAlias names a saved host; an empty alias removes the name.
func SetAlias(target, alias string) error {
	d, err := configDir()
	if err != nil {
		return err
	}
	if alias != "" {
		if err := dial.CheckAlias(alias); err != nil {
			return err
		}
	}
	st, err := dial.OpenStore(d)
	if err != nil {
		return err
	}
	return st.SetAlias(cli.NormalizeTarget(target), alias)
}

// Forget discards what is saved for a host (pinned key, pairing token,
// alias), named by target or alias, and returns how many entries went.
func Forget(ref string) (int, error) {
	d, err := configDir()
	if err != nil {
		return 0, err
	}
	st, err := dial.OpenStore(d)
	if err != nil {
		return 0, err
	}
	return st.Forget(strings.TrimSpace(ref))
}

func codeOf(err error) string {
	switch auth.CodeOf(err) {
	case auth.CodeAuthFailed:
		return "bad_password"
	case auth.CodeLockedOut:
		return "locked_out"
	case auth.CodeDenied:
		return "denied"
	}
	switch signal.CodeOf(err) {
	case signal.CodeUnknownID:
		return "unknown_id"
	case signal.CodeOffline:
		return "host_offline"
	}
	switch {
	case errors.Is(err, signal.ErrNotConfigured):
		return "no_rendezvous"
	case errors.Is(err, dial.ErrHostKeyChanged):
		return "host_key_changed"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, nat.ErrNoPath):
		return "no_path"
	case errors.Is(err, dial.ErrNoCredential):
		return "password_required"
	}
	return "error"
}
