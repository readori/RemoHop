package hostd

import (
	"bufio"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/config"
)

const (
	// AuditFile is the host's audit log inside the config directory: one
	// JSON object per line, mode 0600. It holds the records: accepted
	// logins, approvals, feature use, grant actions, session ends.
	AuditFile = "audit.jsonl"
	// AuditRefusedFile holds refused logins and grant refusals, which
	// anyone who can reach the host (or a granted session) can produce in
	// bulk. Kept apart so that they cannot push the records out.
	AuditRefusedFile = "audit-refused.jsonl"
	// auditMax is the size at which a log is rotated to NAME.1, NAME.1 to
	// NAME.2 and so on.
	auditMax = 1 << 20
	// auditKeep and auditRefusedKeep are how many rotated files of each
	// log are kept.
	auditKeep        = 4
	auditRefusedKeep = 1
	// auditField bounds one value in an entry.
	auditField = 200

	// Refused entries are written in full up to refusedPerKey per source
	// (or per granted session) and refusedTotal in all per
	// refusedWindow; the rest are counted, and one summary entry per
	// source carries the count ("suppressed") when the window ends.
	refusedWindow = time.Minute
	refusedPerKey = 10
	refusedTotal  = 100
	// refusedKeys bounds the sources tracked at a time; beyond it the
	// entries share one window (key refusedAll).
	refusedKeys = 100
	refusedAll  = "*"
)

// AuditEntry is one line of the audit log.
type AuditEntry struct {
	Time  time.Time `json:"time"`
	Event string    `json:"event"`
	// Fields are the event's details. Values that came from the client
	// were cleaned (auth.CleanText) when written. A summary of refused
	// entries that were not written one by one has "suppressed" (their
	// number) and "since" (the start of the window).
	Fields map[string]string `json:"fields,omitempty"`
}

type auditLog struct {
	mu   sync.Mutex
	path string // records; refused entries go to refusedPath
	// refusedPath defaults to AuditRefusedFile beside path.
	refusedPath string

	windows map[string]*refusedWindowState
	total   refusedWindowState
	now     func() time.Time // for tests
}

// refusedWindowState counts the refused entries of one key in the current
// window, and remembers the last one not written to summarise it.
type refusedWindowState struct {
	start      time.Time
	n          int
	suppressed int
	event      string
	kv         []string
}

func (a *auditLog) clock() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

func (a *auditLog) refusedFile() string {
	if a.refusedPath != "" {
		return a.refusedPath
	}
	return filepath.Join(filepath.Dir(a.path), AuditRefusedFile)
}

func (a *auditLog) entry(t time.Time, event string, kv []string) (AuditEntry, []byte) {
	e := AuditEntry{Time: t.UTC().Truncate(time.Millisecond), Event: event, Fields: map[string]string{}}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] != "" {
			e.Fields[kv[i]] = auth.CleanText(kv[i+1], auditField)
		}
	}
	line, err := json.Marshal(e)
	if err != nil {
		return e, nil
	}
	return e, append(line, '\n')
}

func (a *auditLog) dir() string { return filepath.Dir(a.path) }

// add appends a record. kv are alternating keys and values.
func (a *auditLog) add(event string, kv ...string) {
	if a == nil || a.path == "" {
		return
	}
	e, line := a.entry(a.clock(), event, kv)
	a.mu.Lock()
	appendRotated(a.path, auditKeep, line)
	a.mu.Unlock()
	if line != nil {
		notifyAudit(a.dir(), false, e)
	}
}

// addRefused appends a refused login or a grant refusal, rate-limited per
// key (the source address or the granted session) and in all. kv are
// alternating keys and values.
func (a *auditLog) addRefused(key, event string, kv ...string) {
	if a == nil || a.path == "" {
		return
	}
	now := a.clock()
	a.mu.Lock()
	written := a.flushLocked(now, false)
	defer func() {
		a.mu.Unlock()
		notifyAudit(a.dir(), true, written...)
	}()
	if a.windows == nil {
		a.windows = map[string]*refusedWindowState{}
	}
	w := a.windows[key]
	if w == nil {
		if len(a.windows) >= refusedKeys {
			key = refusedAll
			w = a.windows[key]
		}
		if w == nil {
			w = &refusedWindowState{start: now}
			a.windows[key] = w
		}
	}
	if a.total.start.IsZero() {
		a.total.start = now
	}
	w.n++
	if w.n <= refusedPerKey {
		a.total.n++ // counts entries written in full
	}
	if w.n > refusedPerKey || a.total.n > refusedTotal {
		w.suppressed++
		w.event, w.kv = event, kv
		if key == refusedAll {
			w.kv = []string{"from", "many sources"}
		}
		return
	}
	e, line := a.entry(now, event, kv)
	appendRotated(a.refusedFile(), auditRefusedKeep, line)
	if line != nil {
		written = append(written, e)
	}
}

// flush writes the summaries of windows that have ended.
func (a *auditLog) flush() {
	if a == nil || a.path == "" {
		return
	}
	a.mu.Lock()
	written := a.flushLocked(a.clock(), false)
	a.mu.Unlock()
	notifyAudit(a.dir(), true, written...)
}

// close writes every pending summary.
func (a *auditLog) close() {
	if a == nil || a.path == "" {
		return
	}
	a.mu.Lock()
	written := a.flushLocked(a.clock(), true)
	a.mu.Unlock()
	notifyAudit(a.dir(), true, written...)
}

// flushLocked writes the summaries of ended windows and returns them.
func (a *auditLog) flushLocked(now time.Time, all bool) (written []AuditEntry) {
	for key, w := range a.windows {
		if !all && now.Sub(w.start) < refusedWindow {
			continue
		}
		if w.suppressed > 0 {
			kv := append(slices.Clip(w.kv), "suppressed", strconv.Itoa(w.suppressed), "since", w.start.UTC().Format(time.RFC3339))
			e, line := a.entry(now, w.event, kv)
			appendRotated(a.refusedFile(), auditRefusedKeep, line)
			if line != nil {
				written = append(written, e)
			}
		}
		delete(a.windows, key)
	}
	if all || now.Sub(a.total.start) >= refusedWindow {
		a.total = refusedWindowState{}
	}
	return written
}

// appendRotated appends line to path, first rotating path when it has
// grown past auditMax, keeping keep older files.
func appendRotated(path string, keep int, line []byte) {
	if line == nil {
		return
	}
	if st, err := os.Stat(path); err == nil && st.Size() > auditMax {
		for i := keep - 1; i >= 1; i-- {
			from := fmt.Sprintf("%s.%d", path, i)
			if _, err := os.Stat(from); err == nil {
				config.Rename(from, fmt.Sprintf("%s.%d", path, i+1))
			}
		}
		config.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	f.Write(line)
	f.Close()
}

// grantKey is the rate-limit key of a granted client's refusals.
func grantKey(grant string, clientKey ed25519.PublicKey) string {
	return "grant:" + grant + ":" + auth.Fingerprint(clientKey)
}

// sourceKey is the rate-limit key of a client address: the IP, and for
// IPv6 its /64, which one machine can fill with addresses.
func sourceKey(remote netip.AddrPort) string {
	ip := remote.Addr().Unmap()
	if ip.Is6() {
		if p, err := ip.Prefix(64); err == nil {
			return p.String()
		}
	}
	return ip.String()
}

// ReadAudit returns the last n entries of the audit logs in dir (the
// records and the refused entries, with their rotated files), oldest first;
// n <= 0 means all that are kept.
func ReadAudit(dir string, n int) ([]AuditEntry, error) {
	var names []string
	for i := auditRefusedKeep; i >= 1; i-- {
		names = append(names, fmt.Sprintf("%s.%d", AuditRefusedFile, i))
	}
	names = append(names, AuditRefusedFile)
	for i := auditKeep; i >= 1; i-- {
		names = append(names, fmt.Sprintf("%s.%d", AuditFile, i))
	}
	names = append(names, AuditFile)
	var out []AuditEntry
	for _, name := range names {
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
	// Each file is in time order; merge them.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	if n > 0 && len(out) > n {
		out = out[len(out)-n:]
	}
	return out, nil
}
