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
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
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

// The gateway runs in persistent (app) mode: a host session lasts until the
// user disconnects it, and one that drops (the app was suspended, the
// network changed) is established again by itself; see internal/web's
// keep.go and redialGateway below.

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
// stays quiet by default. While the user has the diagnostic log on, every
// record also goes, redacted, into that private log (see diag.go).
func quietLogger() *slog.Logger {
	return slog.New(newCoreHandler())
}

// gwLife serialises starting, checking, pausing and stopping the gateway
// (mu only guards the variables, so other calls are not held up by the
// health check).
var gwLife sync.Mutex

// healthTimeout bounds EnsureGateway's request to its own listener; on a
// phone that answers in a millisecond.
const healthTimeout = 1500 * time.Millisecond

// StartGateway starts the in-process web gateway on 127.0.0.1 with a random
// port and a fresh token and returns the URL to load, which contains the
// token. If the gateway is already running it returns its URL: the token
// changes per start, not per call. A listener that is paused or known to
// have failed is opened again first; EnsureGateway also checks one that
// looks fine.
func StartGateway() (string, error) {
	gwLife.Lock()
	defer gwLife.Unlock()
	if srv := current(); srv != nil {
		if srv.Listening() || srv.Rebind() == nil {
			return srv.URL(), nil
		}
		stopLocked()
	}
	return startLocked()
}

// EnsureGateway is what the apps call whenever they come to the foreground,
// and when the page reports that it cannot reach the gateway. It returns
// the URL of a gateway that has just answered a request from inside this
// process:
//
//   - nothing runs: it starts one (new port and token), like StartGateway;
//   - the listener answers: the same URL;
//   - the listener is paused (PauseGateway) or dead: iOS takes a suspended
//     app's listening socket away, and Go's accept loop does not notice
//     (see internal/web/listener.go). It listens again, on the same port
//     if it can, which keeps the URL, the token, the page and every host
//     session. If the port is gone it moves to a new port with a new token
//     (the sessions stay in the gateway, but the page must load the new URL
//     and starts a new browser session);
//   - that fails too: it stops the gateway and starts a new one.
//
// It also checks the host sessions in the background (CheckSessions): one
// whose connection died while the app was away is re-established now.
//
// The caller compares the URL with the one its web view has and loads it
// only if it changed.
func EnsureGateway() (string, error) {
	gwLife.Lock()
	defer gwLife.Unlock()
	if srv := current(); srv != nil {
		if srv.Listening() && healthy(srv) == nil {
			go srv.CheckSessions()
			return srv.URL(), nil
		}
		if err := srv.Rebind(); err == nil && healthy(srv) == nil {
			go srv.CheckSessions()
			return srv.URL(), nil
		}
		stopLocked()
	}
	return startLocked()
}

// PauseGateway closes the gateway's listening socket and keeps everything
// else (the server, its token, the browser and host sessions). The iOS app
// calls it when it goes to the background, as Apple's TN2277 asks of
// listening sockets: then the system has nothing to take away from the
// suspended app, the port is released cleanly, and EnsureGateway opens it
// again on return with the same URL. Safe to call when nothing runs.
func PauseGateway() {
	gwLife.Lock()
	defer gwLife.Unlock()
	if srv := current(); srv != nil {
		srv.PauseListening()
	}
}

// BreakGatewayListenerForTest breaks the running gateway's listener the way
// iOS does to a suspended app's socket, for tests of EnsureGateway: the
// socket goes away underneath the server. With stall the server cannot tell
// (GatewayRunning stays true, connections are refused: iOS's case); without,
// it notices. It is harmless in production (the next EnsureGateway repairs
// it) but nothing but tests should call it.
func BreakGatewayListenerForTest(stall bool) {
	gwLife.Lock()
	defer gwLife.Unlock()
	if srv := current(); srv != nil {
		srv.BreakListenerForTest(stall)
	}
}

// CheckSessions pings every host session and re-establishes the ones that
// do not answer, at once. The apps call it when the network changes (and
// EnsureGateway does on every return to the foreground).
func CheckSessions() {
	if srv := current(); srv != nil {
		srv.CheckSessions()
	}
}

// SessionsJSON lists the gateway's host sessions as a JSON array of
// {"key","target","state",...} (state: connected, reconnecting,
// waiting_approval, failed), "[]" when there are none. The Android app
// polls it to keep its foreground service and notification while a
// session exists.
func SessionsJSON() (string, error) {
	srv := current()
	if srv == nil {
		return "[]", nil
	}
	b, err := json.Marshal(srv.Sessions())
	return string(b), err
}

// DisconnectAll ends every host session, as Disconnect in the page does
// (the Android notification's Disconnect).
func DisconnectAll() {
	if srv := current(); srv != nil {
		srv.DisconnectAll()
	}
}

// DropSessionsForTest closes every host session's connection as a network
// outage would; the sessions reconnect by themselves. Tests only.
func DropSessionsForTest() {
	if srv := current(); srv != nil {
		srv.DropSessionsForTest()
	}
}

func healthy(srv *web.Server) error {
	ctx, cancel := context.WithTimeout(context.Background(), healthTimeout)
	defer cancel()
	return srv.Healthy(ctx)
}

// current returns the gateway if its Serve still runs.
func current() *web.Server {
	mu.Lock()
	defer mu.Unlock()
	if gw == nil {
		return nil
	}
	select {
	case <-gwDone:
		return nil
	default:
		return gw
	}
}

// startLocked starts a gateway unless one serves; gwLife must be held.
func startLocked() (string, error) {
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
		Listen:     "127.0.0.1:0",
		ConfigDir:  d,
		Dial:       dialGateway(d),
		Pair:       pairGateway(d),
		Persistent: true,
		Redial:     redialGateway(d),
		Logger:     quietLogger(),
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
	changed := !slices.Equal(nativeLAN, all)
	nativeLAN = all
	localsMu.Unlock()
	if changed {
		// The network changed: sessions on the old one are probably dead.
		go CheckSessions()
	}
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

// redialGateway decides how a host session logs in again after its
// connection dropped, without the user:
//
//   - a session that logged in with a pairing token or this device's key
//     does so again (the saved credential);
//   - a session that logged in with a password, to a host this device has
//     no saved pairing for, asks the host right away for a pairing token
//     and keeps it in memory only, for this session (a one-time password
//     is spent by the first login, so the password could not do it). The
//     token is not saved: it is gone when the user disconnects or the app
//     ends. The host lists it like any pairing of this device (`remohop
//     host paired`, under the device's name) and replaces it when the
//     device pairs again;
//   - where the host refuses to pair (quick-support hosts, grants, hosts
//     run with --no-pairing), the password is kept in memory instead. If
//     that host asks its owner to approve every login, every reconnection
//     needs approval again, and the page says so.
//
// The password is never written anywhere; see docs/security.md.
func redialGateway(d string) web.RedialFunc {
	return func(ctx context.Context, conn *peer.Conn, req web.DialRequest) (web.Redialer, error) {
		info := dial.InfoOf(conn)
		if info == nil {
			return nil, errors.New("this connection was not established by remohop")
		}
		target, password := info.Target, ""
		var mem *hostd.Pairing
		if info.Auth == auth.KindPassword && !savedPairing(d, target) {
			pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			p, err := hostd.Pair(pctx, conn, hostd.PairRequest{Name: pairLabel()})
			cancel()
			if err == nil {
				mem = p
			} else {
				password = req.Password
			}
		}
		return func(ctx context.Context, notify func(string)) (*peer.Conn, error) {
			return dial.Dial(ctx, target, dial.Options{
				Password: password,
				Pairing:  mem,
				Notify:   notify,
				Dir:      d,
				Locals:   localAddrs,
				Logger:   quietLogger(),
			})
		}, nil
	}
}

// savedPairing reports whether this device has a pairing saved for target.
func savedPairing(d, target string) bool {
	st, err := dial.OpenStore(d)
	if err != nil {
		return false
	}
	p, _, err := st.Get(target)
	return err == nil && p.Paired()
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
	gwLife.Lock()
	defer gwLife.Unlock()
	stopLocked()
}

func stopLocked() {
	mu.Lock()
	srv := gw
	gw, gwDone = nil, nil
	mu.Unlock()
	if srv != nil {
		srv.Close()
	}
}

// GatewayRunning reports whether the gateway runs and its listener is open
// and has not failed as far as the server can tell. It does no I/O, so it
// cannot see a listener that iOS took away while the app was suspended;
// EnsureGateway checks for real.
func GatewayRunning() bool {
	srv := current()
	return srv != nil && srv.Listening()
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
// canceled, signal_is_host (the rendezvous setting names a RemoHop host),
// signal_wrong_protocol (it names something else), bad_request or error. The returned error is reserved for
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
// alias), named by target or alias, and returns how many entries went. Its
// open sessions end too.
func Forget(ref string) (int, error) {
	d, err := configDir()
	if err != nil {
		return 0, err
	}
	st, err := dial.OpenStore(d)
	if err != nil {
		return 0, err
	}
	ref = strings.TrimSpace(ref)
	target := cli.NormalizeTarget(ref)
	if p, ok, err := st.ByAlias(ref); err == nil && ok {
		target = p.Target
	}
	n, err := st.Forget(ref)
	if err == nil {
		// Forgetting a host also ends the sessions to it: they would
		// otherwise reconnect with credentials the user just deleted.
		if srv := current(); srv != nil {
			srv.DisconnectTarget(target)
		}
	}
	return n, err
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
	case signal.CodeIsHost, signal.CodeWrongProtocol:
		return signal.CodeOf(err) // "signal_is_host", "signal_wrong_protocol"
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

// checkTimeout bounds CheckRendezvous.
const checkTimeout = 10 * time.Second

// CheckRendezvous connects to a rendezvous server the way a connection by
// ID does, and reports what answered, for the Settings screen to show when
// the user saves the server. addr is HOST[:PORT]; empty checks the
// configured server. The configured key pin is used when addr is the
// configured server (a new address has no pin yet: SetSetting drops the
// old one when the address changes).
//
// The outcome is a JSON object either way:
//
//	{"ok":true,"code":"ok","server":"HOST:PORT","key":"<server key>"}
//	{"ok":false,"code":"...","error":"..."}
//
// where key is the server's public key in the form the "signal-pin"
// setting takes, and code is one of
//
//	ok                     a RemoHop rendezvous server answered
//	no_rendezvous          addr is empty and none is configured
//	bad_address            addr is not HOST or HOST:PORT
//	unreachable            no answer (wrong address or port, UDP blocked, down)
//	signal_is_host         the address is a RemoHop host, not a rendezvous
//	                       server (connect to it by address instead)
//	signal_wrong_protocol  something else answered
//	key_mismatch           the server's key is not the pinned one
//	error                  anything else
//
// The returned error is reserved for calling it before Init.
func CheckRendezvous(addr string) (string, error) {
	if _, err := configDir(); err != nil {
		return "", err
	}
	res := checkRendezvousServer(strings.TrimSpace(addr))
	b, err := json.Marshal(res)
	return string(b), err
}

func checkRendezvousServer(addr string) map[string]any {
	fail := func(code string, err error) map[string]any {
		return map[string]any{"ok": false, "code": code, "error": cli.Sanitize(err.Error())}
	}
	configured, opts, err := signal.Resolve("")
	if err != nil && !errors.Is(err, signal.ErrNotConfigured) {
		return fail("error", err)
	}
	switch {
	case addr == "" && configured == "":
		return fail("no_rendezvous", signal.ErrNotConfigured)
	case addr == "":
		addr = configured
	case addr != configured:
		if err := checkRendezvous(addr); err != nil {
			return fail("bad_address", err)
		}
		opts = signal.Options{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
	defer cancel()
	c, sock, err := signal.Connect(ctx, addr, opts, func() (*nat.Socket, error) { return nat.Listen("") }, quietLogger())
	if err != nil {
		switch {
		case signal.IsWrongServer(err):
			return fail(signal.CodeOf(err), err)
		case errors.Is(err, signal.ErrPinMismatch):
			return fail("key_mismatch", err)
		case errors.Is(err, context.DeadlineExceeded), isTimeout(err):
			return fail("unreachable", fmt.Errorf("no answer from the rendezvous server %s: check the address and port, and that UDP gets out of this network", addr))
		}
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) {
			return fail("unreachable", err)
		}
		return fail("error", err)
	}
	key := auth.EncodeKey(c.ServerKey())
	c.Close()
	sock.Close()
	return map[string]any{"ok": true, "code": "ok", "server": addr, "key": key}
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
