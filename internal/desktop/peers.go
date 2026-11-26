package desktop

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/dial"
	"github.com/readori/RemoHop/internal/svc/wake"
)

// Peer is an address-book entry: a host this computer connected to or
// paired with, or a device of its account.
type Peer struct {
	Target   string     `json:"target"`
	Alias    string     `json:"alias,omitempty"`
	Name     string     `json:"name,omitempty"`
	Platform string     `json:"platform,omitempty"`
	Account  bool       `json:"account,omitempty"`
	Login    string     `json:"login"`
	Presence string     `json:"presence"`
	LastSeen *time.Time `json:"last_seen,omitempty"`
	// Wake: a Wake-on-LAN address is saved for it.
	Wake bool `json:"wake,omitempty"`
}

// Peers lists the address book. Presence comes from the rendezvous server
// and is asked for at most every 30 seconds unless refresh is set.
func (a *App) Peers(ctx context.Context, refresh bool) ([]Peer, string) {
	a.peersMu.Lock()
	defer a.peersMu.Unlock()
	// A change to the saved hosts (a computer just connected to in a tab,
	// a password saved, a pairing made) shows at once, without waiting for
	// the presence refresh.
	var mtime time.Time
	if dir := a.dirOrDefault(); dir != "" {
		if fi, err := os.Stat(filepath.Join(dir, dial.PeersFile)); err == nil {
			mtime = fi.ModTime()
		}
	}
	if !refresh && a.peers != nil && time.Since(a.peersAt) < maxPeersRefresh && mtime.Equal(a.peersMtime) {
		return a.peers, a.peersErr
	}
	a.peersMtime = mtime
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	devs, err := dial.Devices(ctx, dial.DevicesOptions{Options: dial.Options{Dir: a.cfg.Dir, Signal: a.cfg.Signal}, Offline: a.cfg.NoSignal})
	if err != nil {
		a.peersErr = err.Error()
		if a.peers == nil {
			a.peers = []Peer{}
		}
		return a.peers, a.peersErr
	}
	macs := map[string]bool{}
	if st, err := wake.OpenStore(); err == nil {
		if list, err := st.List(); err == nil {
			for _, e := range list {
				macs[e.Name] = true
			}
		}
	}
	out := make([]Peer, 0, len(devs))
	for _, d := range devs {
		if d.ThisDevice || d.Target == "" {
			continue
		}
		p := Peer{Target: d.Target, Alias: clean(d.Alias), Name: clean(d.Name), Platform: clean(d.Platform), Account: d.Account,
			Login: d.Login, Presence: d.Presence, LastSeen: d.LastSeen}
		p.Wake = macs[wakeName(d.Target, d.Alias)]
		out = append(out, p)
	}
	a.peers, a.peersAt, a.peersErr = out, time.Now(), ""
	return out, ""
}

// wakeName is the name a peer's Wake-on-LAN address is saved under in the
// shared wake store (`remohop wake NAME` works with it too): its alias, or
// its ID.
func wakeName(target, alias string) string {
	if alias != "" && wake.ValidName(alias) == nil {
		return alias
	}
	if wake.ValidName(target) == nil {
		return target
	}
	return ""
}

func (a *App) invalidatePeers() {
	a.peersMu.Lock()
	a.peersAt = time.Time{}
	a.peersMu.Unlock()
	a.wake()
}

func (a *App) peer(target string) (Peer, bool) {
	peers, _ := a.Peers(context.Background(), false)
	for _, p := range peers {
		if p.Target == target {
			return p, true
		}
	}
	return Peer{}, false
}

// SetAlias names a saved host ("" removes the name).
func (a *App) SetAlias(target, alias string) error {
	target = cli.NormalizeTarget(strings.TrimSpace(target))
	alias = strings.TrimSpace(alias)
	if alias != "" {
		if err := dial.CheckAlias(alias); err != nil {
			return Errorf("bad_alias", err.Error())
		}
	}
	st, err := dial.OpenStore(a.cfg.Dir)
	if err != nil {
		return err
	}
	if _, ok, err := st.Get(target); err != nil {
		return err
	} else if !ok {
		return Errorf("gone", "no saved host with that target")
	}
	if err := st.SetAlias(target, alias); err != nil {
		return err
	}
	a.invalidatePeers()
	return nil
}

// Forget removes a saved host (and its pairing).
func (a *App) Forget(target string) error {
	st, err := dial.OpenStore(a.cfg.Dir)
	if err != nil {
		return err
	}
	if _, err := st.Forget(cli.NormalizeTarget(strings.TrimSpace(target))); err != nil {
		return err
	}
	a.invalidatePeers()
	return nil
}

// SavedPassword is the password saved for target (the console saves one
// when the user asks it to remember a host that does not pair), for the
// page to show; with forget it is removed instead.
func (a *App) SavedPassword(target string, forget bool) (string, error) {
	st, err := dial.OpenStore(a.cfg.Dir)
	if err != nil {
		return "", err
	}
	target = cli.NormalizeTarget(strings.TrimSpace(target))
	if forget {
		if err := st.SetPassword(target, ""); err != nil {
			return "", err
		}
		a.invalidatePeers()
		return "", nil
	}
	p, ok, err := st.Get(target)
	if err != nil {
		return "", err
	}
	if !ok || !p.SavedPassword() {
		return "", Errorf("gone", "no saved password for that computer")
	}
	return p.Password, nil
}

// SetWake saves (or with mac "" removes) the Wake-on-LAN address of a
// saved host.
func (a *App) SetWake(target, mac string) error {
	p, ok := a.peer(cli.NormalizeTarget(strings.TrimSpace(target)))
	if !ok {
		return Errorf("gone", "no saved host with that target")
	}
	name := wakeName(p.Target, p.Alias)
	if name == "" {
		return Errorf("wake_name", "give this host a short name first")
	}
	st, err := wake.OpenStore()
	if err != nil {
		return err
	}
	mac = strings.TrimSpace(mac)
	if mac == "" {
		_, err = st.Forget(name)
	} else if _, perr := wake.ParseMAC(mac); perr != nil {
		return Errorf("bad_mac", perr.Error())
	} else {
		err = st.Save(name, mac)
	}
	if err != nil {
		return err
	}
	a.invalidatePeers()
	return nil
}

// Wake sends the Wake-on-LAN packet for a saved host from this computer
// (it reaches machines on the same network).
func (a *App) Wake(ctx context.Context, target string) error {
	p, ok := a.peer(cli.NormalizeTarget(strings.TrimSpace(target)))
	if !ok {
		return Errorf("gone", "no saved host with that target")
	}
	st, err := wake.OpenStore()
	if err != nil {
		return err
	}
	mac, found, err := st.Lookup(wakeName(p.Target, p.Alias))
	if err != nil {
		return err
	}
	if !found {
		return Errorf("no_mac", "no Wake-on-LAN address is saved for this host")
	}
	_, err = wake.Send(ctx, mac, wake.Options{})
	return err
}

// dirOrDefault is the config directory the saved hosts live in.
func (a *App) dirOrDefault() string {
	if a.cfg.Dir != "" {
		return a.cfg.Dir
	}
	return a.dir
}
