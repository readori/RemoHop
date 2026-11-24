package hostd

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/config"
	"github.com/readori/RemoHop/internal/perm"
)

// statusFile records the running host in the config directory, for
// `remohop host status` and to stop a second host from starting on the same
// identity (the two would keep taking the ID away from each other).
const statusFile = "host.json"

// Status describes a host process.
type Status struct {
	PID int `json:"pid"`
	// ID is the 9-digit ID, empty until the first registration.
	ID string `json:"id,omitempty"`
	// Name is this machine's device name (`remohop config name`).
	Name string `json:"name,omitempty"`
	// Online reports whether the host is currently connected to the
	// rendezvous server.
	Online bool `json:"online"`
	// Password is the temporary password, formatted; empty when only a
	// fixed password is accepted. A fixed password is never reported.
	Password string `json:"password,omitempty"`
	// PasswordMode is "temporary", "one-time", "fixed" or "fixed+one-time".
	PasswordMode string `json:"password_mode"`
	// Temporary is true when Password is set.
	Temporary bool     `json:"temporary_password"`
	Listen    string   `json:"listen"`
	Direct    []string `json:"direct,omitempty"`
	Identity  string   `json:"identity"`
	Signal    string   `json:"signal,omitempty"`
	// Transport is how the host is registered: "udp", or "websocket"
	// through the rendezvous server's websocket bridge (clients then
	// arrive relayed); empty while offline.
	Transport string `json:"transport,omitempty"`
	NAT       string `json:"nat,omitempty"`
	// FeaturesOff lists the features the owner has switched off.
	FeaturesOff []string `json:"features_off,omitempty"`
	// Ask is "off", "on" (password logins need approval) or "all"
	// (paired clients too).
	Ask      string          `json:"ask"`
	Pending  int             `json:"pending_approvals"`
	Sessions int             `json:"sessions"`
	Clients  []SessionStatus `json:"clients,omitempty"`
	Started  time.Time       `json:"started"`
}

// SessionStatus describes one live session.
type SessionStatus struct {
	Peer     string    `json:"peer"`             // pairing name or key fingerprint
	Device   string    `json:"device,omitempty"` // the name the client gave itself
	Identity string    `json:"identity"`
	From     string    `json:"from"`
	Path     string    `json:"path"` // lan, direct, punched, relayed or "relayed (websocket)"
	Auth     auth.Kind `json:"auth"`
	Since    time.Time `json:"since"`
	// Grant is the grant the session stands on; empty for full access.
	Grant string `json:"grant,omitempty"`
}

// OnChange registers fn to be called (from other goroutines) whenever the
// host's registration state or password changes.
func (h *Host) OnChange(fn func()) {
	h.mu.Lock()
	h.onChange = fn
	h.mu.Unlock()
}

// Status reports the host's current state.
func (h *Host) Status() Status {
	pol := h.policy()
	password, mode := h.TempPassword()
	h.mu.Lock()
	defer h.mu.Unlock()
	st := Status{
		PID:          os.Getpid(),
		ID:           h.id,
		Name:         DeviceName(),
		Online:       h.online,
		Password:     password,
		PasswordMode: mode,
		Temporary:    password != "",
		Listen:       h.sock.LocalAddr().String(),
		Identity:     auth.Fingerprint(h.identity.Public()),
		Signal:       h.cfg.Signal,
		Transport:    h.transportLocked(),
		NAT:          string(h.natType),
		Ask:          "off",
		Pending:      len(h.approvals),
		Sessions:     len(h.sessions),
		Started:      h.started,
	}
	if pol.ask {
		st.Ask = "on"
		if pol.askPaired {
			st.Ask = "all"
		}
	}
	for _, f := range perm.Features() {
		if pol.off[f] {
			st.FeaturesOff = append(st.FeaturesOff, string(f))
		}
	}
	for _, ap := range h.sock.Candidates() {
		st.Direct = append(st.Direct, ap.String())
	}
	for s := range h.sessions {
		peer := s.Auth.TokenName
		if peer == "" {
			peer = "key " + auth.Fingerprint(s.Auth.ClientKey)
		}
		st.Clients = append(st.Clients, SessionStatus{Peer: peer, Identity: auth.Fingerprint(s.Auth.ClientKey),
			Device: s.Auth.ClientName, From: s.Remote.String(), Path: s.Path, Auth: s.Auth.Kind, Since: s.Started, Grant: s.grant})
	}
	sort.Slice(st.Clients, func(i, j int) bool { return st.Clients[i].Since.Before(st.Clients[j].Since) })
	return st
}

// transportLocked names how the host is registered; h.mu is held.
func (h *Host) transportLocked() string {
	switch {
	case !h.online:
		return ""
	case h.viaWS:
		return "websocket"
	}
	return "udp"
}

// writeStatus stores st (mode 0600: it contains the password) in dir.
func writeStatus(dir string, st Status) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return config.WriteFileAtomic(filepath.Join(dir, statusFile), append(b, '\n'), 0o600)
}

// ReadStatus returns the status of the host running on dir's identity, or
// (nil, nil) if none is running. A file left behind by a host that died is
// removed.
func ReadStatus(dir string) (*Status, error) {
	p := filepath.Join(dir, statusFile)
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var st Status
	if json.Unmarshal(b, &st) != nil || st.PID <= 0 || !processAlive(st.PID) {
		os.Remove(p)
		return nil, nil
	}
	return &st, nil
}
