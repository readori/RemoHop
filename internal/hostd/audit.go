package hostd

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/config"
)

const (
	// AuditFile is the host's audit log inside the config directory: one
	// JSON object per line, mode 0600.
	AuditFile = "audit.jsonl"
	// auditMax is the size at which the log is rotated to AuditFile+".1",
	// replacing the previous rotation.
	auditMax = 1 << 20
	// auditField bounds one value in an entry.
	auditField = 200
)

// AuditEntry is one line of the audit log.
type AuditEntry struct {
	Time  time.Time `json:"time"`
	Event string    `json:"event"`
	// Fields are the event's details. Values that came from the client
	// were cleaned (auth.CleanText) when written.
	Fields map[string]string `json:"fields,omitempty"`
}

type auditLog struct {
	mu   sync.Mutex
	path string
}

// add appends an entry. kv are alternating keys and values.
func (a *auditLog) add(event string, kv ...string) {
	if a == nil || a.path == "" {
		return
	}
	e := AuditEntry{Time: time.Now().UTC().Truncate(time.Millisecond), Event: event, Fields: map[string]string{}}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] != "" {
			e.Fields[kv[i]] = auth.CleanText(kv[i+1], auditField)
		}
	}
	line, err := json.Marshal(e)
	if err != nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if st, err := os.Stat(a.path); err == nil && st.Size() > auditMax {
		config.Rename(a.path, a.path+".1")
	}
	f, err := os.OpenFile(a.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	f.Write(append(line, '\n'))
	f.Close()
}

// ReadAudit returns the last n entries of the audit log in dir, oldest
// first; n <= 0 means all that are kept.
func ReadAudit(dir string, n int) ([]AuditEntry, error) {
	var out []AuditEntry
	for _, name := range []string{AuditFile + ".1", AuditFile} {
		f, err := os.Open(filepath.Join(dir, name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 64<<10)
		for sc.Scan() {
			var e AuditEntry
			if json.Unmarshal(sc.Bytes(), &e) == nil && e.Event != "" {
				out = append(out, e)
			}
		}
		f.Close()
	}
	if n > 0 && len(out) > n {
		out = out[len(out)-n:]
	}
	return out, nil
}
