package signal

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/readori/RemoHop/internal/config"
	"github.com/readori/RemoHop/internal/nat"
	"github.com/readori/RemoHop/internal/wsdgram"
)

// Transports: how a peer reaches the rendezvous server.
const (
	// TransportAuto tries UDP and, if that gets no answer within
	// FallbackAfter and a websocket bridge is configured, the bridge.
	TransportAuto = "auto"
	TransportUDP  = "udp"
	TransportWS   = "ws"
)

// FallbackAfter is how long a UDP attempt may take before auto falls back
// to the websocket bridge. A rendezvous handshake normally completes in
// one or two round trips.
var FallbackAfter = 4 * time.Second

// Config keys for the websocket bridge.
const (
	SettingWS               = "signal-ws"
	SettingWSAccessID       = "signal-ws-access-id"
	SettingWSAccessSecretFn = "signal-ws-access-secret-file"
	SettingTransport        = "signal-transport"
)

// DefaultWS is the websocket bridge of the build-time default server
// (internal/dial.DefaultSignalWS); empty in source.
var DefaultWS string

func init() {
	config.RegisterSetting(config.Setting{
		Key: SettingWS,
		Help: "the rendezvous server's websocket bridge, wss://HOST[/PATH], used when UDP to the server is blocked " +
			"(only HTTPS gets out); see `remohop signal --help`",
		Validate: func(v string) error {
			_, err := wsdgram.ParseURL(v)
			return err
		},
	})
	config.RegisterSetting(config.Setting{
		Key:  SettingWSAccessID,
		Help: "Cloudflare Access service-token client ID for the websocket bridge (the secret goes in signal-ws-access-secret-file or REMOHOP_SIGNAL_WS_ACCESS_SECRET)",
		Validate: func(v string) error {
			if strings.ContainsAny(v, "\r\n\t ") || len(v) > 256 {
				return errors.New("malformed client ID")
			}
			return nil
		},
	})
	config.RegisterSetting(config.Setting{
		Key:  SettingWSAccessSecretFn,
		Help: "file holding the Cloudflare Access service-token client secret for the websocket bridge",
	})
	config.RegisterSetting(config.Setting{
		Key:     SettingTransport,
		Default: TransportAuto,
		Help:    "how to reach the rendezvous server: auto (UDP, else the websocket bridge), udp, or ws; REMOHOP_TRANSPORT overrides it",
		Validate: func(v string) error {
			_, err := parseTransport(v)
			return err
		},
	})
}

func parseTransport(v string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", TransportAuto:
		return TransportAuto, nil
	case TransportUDP:
		return TransportUDP, nil
	case TransportWS, "websocket", "wss":
		return TransportWS, nil
	}
	return "", fmt.Errorf("transport %q: use auto, udp or ws", v)
}

// bridgeSettings fills the websocket fields of opts from the environment
// and settings. useConfigWS says whether the configured signal-ws belongs
// to the server being used (it does not when --signal names another one).
func bridgeSettings(opts *Options, settings map[string]string, useConfigWS bool, defaultWS string) error {
	mode := os.Getenv("REMOHOP_TRANSPORT")
	if mode == "" {
		mode = settings[SettingTransport]
	}
	var err error
	if opts.Transport, err = parseTransport(mode); err != nil {
		return fmt.Errorf("REMOHOP_TRANSPORT/%s: %w", SettingTransport, err)
	}
	switch {
	case os.Getenv("REMOHOP_SIGNAL_WS") != "":
		opts.WS = os.Getenv("REMOHOP_SIGNAL_WS")
	case useConfigWS && settings[SettingWS] != "":
		opts.WS = settings[SettingWS]
	case useConfigWS:
		opts.WS = defaultWS
	}
	if opts.WS == "" {
		if opts.Transport == TransportWS {
			return errors.New("transport ws needs the server's websocket bridge: `remohop config set signal-ws wss://HOST/remohop/v1/udp` (or REMOHOP_SIGNAL_WS)")
		}
		return nil
	}
	u, err := wsdgram.ParseURL(opts.WS)
	if err != nil {
		return err
	}
	id := os.Getenv("REMOHOP_SIGNAL_WS_ACCESS_ID")
	if id == "" {
		id = settings[SettingWSAccessID]
	}
	secret := os.Getenv("REMOHOP_SIGNAL_WS_ACCESS_SECRET")
	if secret == "" && settings[SettingWSAccessSecretFn] != "" {
		b, err := os.ReadFile(settings[SettingWSAccessSecretFn])
		if err != nil {
			return fmt.Errorf("%s: %w", SettingWSAccessSecretFn, err)
		}
		secret = strings.TrimSpace(string(b))
	}
	switch {
	case id != "" && secret == "":
		return errors.New("signal-ws-access-id is set but there is no secret: put it in a file named by signal-ws-access-secret-file, or in REMOHOP_SIGNAL_WS_ACCESS_SECRET")
	case id == "" && secret != "":
		return errors.New("a websocket access secret is set but no signal-ws-access-id")
	case id != "":
		if strings.ContainsAny(secret, "\r\n") || strings.ContainsAny(id, "\r\n") {
			return errors.New("malformed websocket access token")
		}
		if u.Scheme != "wss" {
			return errors.New("a websocket access token is only sent over wss://, not " + u.Scheme + "://")
		}
		opts.WSHeader = http.Header{"Cf-Access-Client-Id": {id}, "Cf-Access-Client-Secret": {secret}}
	}
	return nil
}

// UseUDP reports whether UDP is to be tried.
func (o Options) UseUDP() bool { return o.WS == "" || o.Transport != TransportWS }

// UseWS reports whether the websocket bridge is to be used, at once or as
// the fallback.
func (o Options) UseWS() bool { return o.WS != "" && o.Transport != TransportUDP }

// UDPWait is how long a UDP attempt may take, out of full: FallbackAfter
// when the bridge is the fallback.
func (o Options) UDPWait(full time.Duration) time.Duration {
	if o.UseWS() {
		return min(full, FallbackAfter)
	}
	return full
}

// NewBridge prepares the client end of the websocket bridge (it connects
// on first use).
func (o Options) NewBridge(log *slog.Logger) (*wsdgram.Conn, error) {
	return wsdgram.New(wsdgram.Options{URL: o.WS, Header: o.WSHeader, Logger: log})
}

// Display names the server for messages: addr, or the bridge when there
// is no UDP address.
func Display(addr string, o Options) string {
	if addr == "" {
		if u, err := wsdgram.ParseURL(o.WS); err == nil {
			return u.String()
		}
	}
	return addr
}

// DialBridge connects to the rendezvous server through its websocket
// bridge, from sock, a socket made on wc.
func DialBridge(ctx context.Context, sock *nat.Socket, wc *wsdgram.Conn, opts Options) (*Client, error) {
	if err := wc.Connect(ctx); err != nil {
		return nil, fmt.Errorf("rendezvous server's websocket bridge %s unreachable: %w", wc.URL(), err)
	}
	return Dial(ctx, sock, wc.ServerAddr().String(), opts)
}

// Connect dials the rendezvous server as opts say: over UDP from a socket
// that newUDP opens and, when that is not possible or gets no answer
// within FallbackAfter and a bridge is configured, through the bridge. It
// returns the client and the socket it runs on; the caller closes both,
// the client first.
func Connect(ctx context.Context, addr string, opts Options, newUDP func() (*nat.Socket, error), log *slog.Logger) (*Client, *nat.Socket, error) {
	var udpErr error
	var locals []netip.Addr // of the UDP socket, for the UDP-blocked hint
	if opts.UseUDP() && addr != "" {
		sock, err := newUDP()
		if err != nil {
			return nil, nil, err
		}
		locals = sock.Locals()
		if opts.UseWS() && UDPBlocked(opts.HintDir, addr, locals) {
			sock.Close()
			udpErr = errors.New("UDP to it was blocked from this network moments ago")
			if log != nil {
				log.Debug("rendezvous server was not reachable over UDP from this network moments ago; going to its websocket bridge")
			}
		} else {
			dctx, cancel := context.WithTimeout(ctx, opts.UDPWait(time.Hour))
			c, err := Dial(dctx, sock, addr, opts)
			cancel()
			if err == nil {
				return c, sock, nil
			}
			sock.Close()
			if !opts.UseWS() || ctx.Err() != nil || IsWrongServer(err) {
				return nil, nil, err
			}
			udpErr = err
			if log != nil {
				log.Debug("rendezvous server not reachable over UDP; trying its websocket bridge", "err", err)
			}
		}
	}
	if !opts.UseWS() {
		return nil, nil, ErrNotConfigured
	}
	wc, err := opts.NewBridge(log)
	if err != nil {
		return nil, nil, err
	}
	sock, err := nat.NewSocket(wc)
	if err != nil {
		wc.Close()
		return nil, nil, err
	}
	c, err := DialBridge(ctx, sock, wc, opts)
	if err != nil {
		sock.Close()
		SetUDPBlocked(opts.HintDir, addr, locals, false) // perhaps UDP works again
		if udpErr != nil {
			return nil, nil, fmt.Errorf("%v; %w", udpErr, err)
		}
		return nil, nil, err
	}
	if udpErr != nil {
		SetUDPBlocked(opts.HintDir, addr, locals, true)
	}
	return c, sock, nil
}
