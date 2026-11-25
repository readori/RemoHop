package signal

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/netip"
	"path/filepath"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/config"
)

// Where UDP to the rendezvous server is blocked, every dial in auto mode
// waited FallbackAfter (4 s) before trying the websocket bridge: each
// command, each reconnect attempt, each presence check. A UDP-blocked hint
// remembers, for a few minutes, that from this network (the machine's
// addresses) UDP got no answer from this server and the bridge worked; the
// next dials go to the bridge at once. Moving to another network, or the
// hint expiring, brings UDP back.
const (
	wsHintsFile = "ws-hints.json"
	wsHintTTL   = 5 * time.Minute
	maxWSHints  = 32
)

var (
	wsHintsMu sync.Mutex
	// wsHintsMem holds the hints of callers without a configuration
	// directory, for the life of the process.
	wsHintsMem = map[string]time.Time{}
)

func wsHintKey(server string, locals []netip.Addr) string {
	sum := sha256.Sum256([]byte(server + "\x00" + netPrint(locals)))
	return hex.EncodeToString(sum[:12])
}

func readWSHints(dir string) map[string]time.Time {
	if dir == "" {
		return wsHintsMem
	}
	hints := map[string]time.Time{}
	if b, err := config.ReadFile(filepath.Join(dir, wsHintsFile)); err == nil {
		json.Unmarshal(b, &hints)
	}
	return hints
}

// UDPBlocked reports whether a dial to server from a socket with these
// local addresses should go to the websocket bridge at once. dir is the
// configuration directory, or "" to remember in memory only.
func UDPBlocked(dir, server string, locals []netip.Addr) bool {
	if server == "" {
		return false
	}
	wsHintsMu.Lock()
	defer wsHintsMu.Unlock()
	exp, ok := readWSHints(dir)[wsHintKey(server, locals)]
	now := time.Now()
	return ok && now.Before(exp) && exp.Sub(now) <= wsHintTTL
}

// SetUDPBlocked records (or, with blocked false, forgets) that UDP to
// server did not get through from this network while the bridge did.
// Failures to write are ignored: a hint is only an optimisation.
func SetUDPBlocked(dir, server string, locals []netip.Addr, blocked bool) {
	if server == "" {
		return
	}
	wsHintsMu.Lock()
	defer wsHintsMu.Unlock()
	hints := readWSHints(dir)
	key := wsHintKey(server, locals)
	now := time.Now()
	_, had := hints[key]
	for k, exp := range hints {
		if !now.Before(exp) {
			delete(hints, k)
		}
	}
	switch {
	case blocked:
		hints[key] = now.Add(wsHintTTL)
		for k := range hints {
			if len(hints) <= maxWSHints {
				break
			}
			if k != key {
				delete(hints, k)
			}
		}
	case had:
		delete(hints, key)
	default:
		return
	}
	if dir == "" {
		return
	}
	if b, err := json.Marshal(hints); err == nil {
		config.WriteFileAtomic(filepath.Join(dir, wsHintsFile), b, 0o600)
	}
}
