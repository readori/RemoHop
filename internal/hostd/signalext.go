package hostd

import (
	"context"
	"encoding/json"
	"errors"
)

// ErrNotRegistered is returned by SignalExt while the host is not
// registered with its rendezvous server.
var ErrNotRegistered = errors.New("hostd: not registered with the rendezvous server")

// SignalServer is the rendezvous server the host registers with, as
// configured ("" for none).
func (h *Host) SignalServer() string { return h.cfg.Signal }

// SignalExt sends an extension request (signal.RegisterRequest's x-NAME
// types) to the host's rendezvous server, over the connection the host is
// registered on and with the host's identity key proven for it. Code built
// on the host (an extension registered with RegisterExtension) uses it to
// talk to a service on the same server without a connection of its own:
// a second connection per host would count against the server's
// per-address limit. It returns ErrNotRegistered while the host is not
// registered; callers wait for the next registration or dial on their own.
func (h *Host) SignalExt(ctx context.Context, typ string, payload any) (json.RawMessage, error) {
	h.mu.Lock()
	c := h.sigc
	h.mu.Unlock()
	if c == nil {
		return nil, ErrNotRegistered
	}
	return c.Ext(ctx, typ, h.identity, payload)
}
