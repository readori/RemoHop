package signal

import (
	"errors"
	"os"
	"strings"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/config"
)

// Config keys.
const (
	SettingAddr = "signal"
	SettingPin  = "signal-pin"
	// SettingRelay is whether this machine accepts a relayed connection
	// when no direct one is possible. REMOHOP_RELAY=0 overrides it to off.
	SettingRelay = "relay"
	// SettingNone records that the user chose to use no rendezvous server
	// (computers are reached by address), so that the desktop app and
	// quick support do not ask again.
	SettingNone = "signal-none"
)

// OfficialAddr is the public rendezvous server that the desktop app's and
// quick support's first-run choice offers. Nothing uses it unless the user
// picks it: there is still no built-in default (DefaultAddr).
const OfficialAddr = "signal.remohop.com"

// DefaultAddr and DefaultPin are the rendezvous server (and optionally its
// key) used when neither a flag, the environment nor the config file names
// one. They are empty in source: RemoHop does not ship with a built-in
// server. A distribution that runs one sets them at build time (through
// internal/dial.DefaultSignal, see there).
var (
	DefaultAddr string
	DefaultPin  string
)

// ErrNotConfigured is returned by Resolve when no rendezvous server is set.
// Its text lists the ways to go on, one per line.
var ErrNotConfigured = errors.New("no rendezvous server configured. Choose one:\n" + Choices("  ") +
	"For one command only, pass --signal HOST[:PORT] or set REMOHOP_SIGNAL.")

// Choices lists, one per line and each line starting with indent, the
// ways to go on without a rendezvous server: the official one (which the
// desktop app and quick support offer; nothing uses it unless chosen),
// one's own, or none at all on the same network.
func Choices(indent string) string {
	return indent + "official server: remohop config set signal " + OfficialAddr + "  (as offered by the desktop app and Quick Support)\n" +
		indent + "your own server: remohop config set signal HOST[:PORT]  (start one with `remohop signal`)\n" +
		indent + "same network:    remohop HOST:PORT <command>  (no server needed; `remohop lan` lists the hosts)\n"
}

func init() {
	config.RegisterSetting(config.Setting{
		Key:  SettingAddr,
		Help: "rendezvous server address, HOST[:PORT] (default port 21116)",
		Validate: func(v string) error {
			_, _, err := SplitAddr(v)
			return err
		},
	})
	config.RegisterSetting(config.Setting{Key: SettingNone, Validate: config.ValidateOnOff,
		Help: "on: the user chose to connect by address only, without a rendezvous server (the desktop app and quick support then do not ask for one)"})
	config.RegisterSetting(config.Setting{Key: SettingRelay, Default: "on", Validate: config.ValidateOnOff,
		Help: "accept a relayed (still end-to-end encrypted) connection when no direct one is possible and the rendezvous server offers relaying"})
	config.RegisterSetting(config.Setting{
		Key:  SettingPin,
		Help: "public key of the configured rendezvous server, as printed by `remohop signal`; connections to a server with another key are refused",
		Validate: func(v string) error {
			_, err := auth.DecodeKey(v)
			return err
		},
	})
}

// Resolve picks the rendezvous server to use: override (the --signal flag
// or REMOHOP_SIGNAL) if set, else the config file, else the build-time default.
// A pin is returned only together with the address it was configured for,
// and likewise the websocket bridge (signal-ws), unless REMOHOP_SIGNAL_WS names
// one. A config file with a bridge but no address gives addr "" and a
// bridge-only Options.
func Resolve(override string) (addr string, opts Options, err error) {
	settings, err := config.All()
	if err != nil {
		if override == "" {
			return "", Options{}, err
		}
		settings = map[string]string{}
	}
	switch {
	case override != "":
		addr = override
		err = bridgeSettings(&opts, settings, false, "")
	case settings[SettingAddr] != "":
		addr, opts.Pin = settings[SettingAddr], settings[SettingPin]
		err = bridgeSettings(&opts, settings, true, "")
	case settings[SettingWS] != "":
		opts.Pin = settings[SettingPin]
		err = bridgeSettings(&opts, settings, true, "")
		opts.Transport = TransportWS
	case DefaultAddr != "":
		addr, opts.Pin = DefaultAddr, DefaultPin
		err = bridgeSettings(&opts, settings, true, DefaultWS)
	case os.Getenv("REMOHOP_SIGNAL_WS") != "":
		err = bridgeSettings(&opts, settings, false, "")
		opts.Transport = TransportWS
	default:
		return "", Options{}, ErrNotConfigured
	}
	if err != nil {
		return "", Options{}, err
	}
	return addr, opts, nil
}

// RelayAllowed reports whether this machine accepts relayed connections:
// the "relay" setting in dir's config file (default on), unless REMOHOP_RELAY
// is set to 0, off, no or false.
func RelayAllowed(dir string) bool {
	switch strings.ToLower(os.Getenv("REMOHOP_RELAY")) {
	case "0", "off", "no", "false":
		return false
	}
	var values map[string]string
	if dir == "" {
		values, _ = config.All()
	} else {
		values, _ = config.AllIn(dir)
	}
	return config.Bool(values[SettingRelay], true)
}
