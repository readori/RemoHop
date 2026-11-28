package desktop

import (
	"context"
	"time"

	"github.com/readori/RemoHop/internal/dial"
)

// LANPeer is a computer that answered discovery on the local network
// (internal/lan). What it says about itself is a hint: connecting to Addr
// checks its key like any connection.
type LANPeer struct {
	Name        string `json:"name"`
	Addr        string `json:"addr"`
	ID          string `json:"id,omitempty"`
	Fingerprint string `json:"fingerprint"`
	// Saved is how the address book has it (an alias, an ID or an
	// address), if it has it with this key; Paired, if a pairing is saved.
	Saved  string `json:"saved,omitempty"`
	Paired bool   `json:"paired,omitempty"`
	// Conflict is the saved computer (an ID or address) it claims to be
	// although its key is not the one saved for it.
	Conflict string `json:"conflict,omitempty"`
}

const (
	// lanFresh is how long a look at the network is shown before the
	// next one: the page asks every few seconds while it shows the list.
	lanFresh = 3 * time.Second
	// lanWait is how long one look listens for answers.
	lanWait = 1500 * time.Millisecond
)

// LAN lists the computers on the local network, this one left out. One
// look at a time: callers that come while it runs get its result.
func (a *App) LAN(ctx context.Context) ([]LANPeer, error) {
	a.lanMu.Lock()
	defer a.lanMu.Unlock()
	if a.lan != nil && time.Since(a.lanAt) < lanFresh {
		return a.lan, nil
	}
	browse := a.cfg.LANBrowse
	if browse == nil {
		browse = func(ctx context.Context, dir string) ([]dial.LANHost, error) {
			return dial.DiscoverLAN(ctx, dir, lanWait)
		}
	}
	hosts, err := browse(ctx, a.cfg.Dir)
	if err != nil {
		return nil, err
	}
	out := make([]LANPeer, 0, len(hosts))
	for _, h := range hosts {
		if h.Self {
			continue
		}
		p := LANPeer{Name: clean(h.Name), Addr: h.Addr.String(), ID: h.ID, Fingerprint: h.Fingerprint, Conflict: h.Conflict}
		for _, s := range h.Saved {
			if p.Saved == "" {
				p.Saved = clean(s.Alias)
				if p.Saved == "" {
					p.Saved = s.Target
				}
			}
			p.Paired = p.Paired || s.Paired
		}
		out = append(out, p)
	}
	a.lan, a.lanAt = out, time.Now()
	return out, nil
}
