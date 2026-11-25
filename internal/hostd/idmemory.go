package hostd

import (
	"encoding/json"
	"path/filepath"

	"github.com/readori/RemoHop/internal/config"
)

// idsFile remembers the ID each rendezvous server gave this host, so that a
// change is noticed across restarts. IDs are the server's word: a server
// that moved an ID from one of the owner's devices to another could make a
// client that dials the ID (and pins the device the account lists under
// it) reach the wrong device. The host cannot prevent that, but it records
// every change in the audit log and warns.
const idsFile = "host-ids.json"

// noteID records id as this host's ID at server and returns the ID it had
// there before, if that was different.
func noteID(dir, server, id string) (previous string) {
	if dir == "" || server == "" || id == "" {
		return ""
	}
	p := filepath.Join(dir, idsFile)
	ids := map[string]string{}
	if b, err := config.ReadFile(p); err == nil {
		json.Unmarshal(b, &ids)
	}
	if ids[server] == id {
		return ""
	}
	previous = ids[server]
	ids[server] = id
	if b, err := json.MarshalIndent(ids, "", "  "); err == nil {
		config.WriteFileAtomic(p, append(b, '\n'), 0o600)
	}
	return previous
}
