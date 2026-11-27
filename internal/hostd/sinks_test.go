package hostd

import (
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/grant"
)

// withSinks clears the registered sinks for one test and restores them.
func withSinks(t *testing.T) {
	t.Helper()
	sinks.mu.Lock()
	audit, recs := sinks.audit, sinks.recordings
	sinks.audit, sinks.recordings = nil, nil
	sinks.mu.Unlock()
	t.Cleanup(func() {
		sinks.mu.Lock()
		sinks.audit, sinks.recordings = audit, recs
		sinks.mu.Unlock()
	})
}

type seen struct {
	dir     string
	e       AuditEntry
	refused bool
}

func TestAuditSink(t *testing.T) {
	withSinks(t)
	var mu sync.Mutex
	var got []seen
	RegisterAuditSink(func(dir string, e AuditEntry, refused bool) {
		mu.Lock()
		got = append(got, seen{dir, e, refused})
		mu.Unlock()
	})

	dir := t.TempDir()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a := &auditLog{path: filepath.Join(dir, AuditFile), now: func() time.Time { return now }}
	a.add("login", "result", "accepted", "name", "alice\x1b[31m")
	for i := range refusedPerKey + 5 {
		a.addRefused("192.0.2.7", "login", "result", "refused", "from", fmt.Sprintf("192.0.2.7:%d", 4000+i))
	}
	now = now.Add(refusedWindow)
	a.flush()

	if len(got) != 1+refusedPerKey+1 {
		t.Fatalf("sink saw %d entries, want %d (1 record, %d refused in full, 1 summary)", len(got), 2+refusedPerKey, refusedPerKey)
	}
	if g := got[0]; g.dir != dir || g.refused || g.e.Event != "login" || g.e.Fields["name"] != cleaned("alice\x1b[31m") {
		t.Errorf("first entry: %+v", g)
	}
	for _, g := range got[1 : 1+refusedPerKey] {
		if !g.refused || g.e.Fields["suppressed"] != "" {
			t.Errorf("refused entry: %+v", g)
		}
	}
	if last := got[len(got)-1]; !last.refused || last.e.Fields["suppressed"] != "5" {
		t.Errorf("summary: %+v", last)
	}

	// The sink sees exactly what the files hold.
	entries, err := ReadAudit(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	var fromFiles, fromSink []string
	for _, e := range entries {
		fromFiles = append(fromFiles, e.Event+e.Fields["from"]+e.Fields["suppressed"]+e.Fields["name"])
	}
	for _, g := range got {
		fromSink = append(fromSink, g.e.Event+g.e.Fields["from"]+g.e.Fields["suppressed"]+g.e.Fields["name"])
	}
	slices.Sort(fromFiles)
	slices.Sort(fromSink)
	if !slices.Equal(fromFiles, fromSink) {
		t.Errorf("files %q\nsink  %q", fromFiles, fromSink)
	}
}

// cleaned is what the audit log makes of a client-supplied value.
func cleaned(s string) string {
	a := &auditLog{}
	e, _ := a.entry(time.Now(), "x", []string{"v", s})
	return e.Fields["v"]
}

func TestRecordingSink(t *testing.T) {
	withSinks(t)
	if hasRecordingSinks() {
		t.Fatal("sinks left from another test")
	}
	var got []Recording
	RegisterRecordingSink(func(r Recording) { got = append(got, r) })

	dir := t.TempDir()
	path := filepath.Join(dir, RecordingsDir, "g", "x.cast")
	c, err := grant.NewCast(path, 80, 24, "g: echo hi")
	if err != nil {
		t.Fatal(err)
	}
	r := &sinkRecorder{Recorder: c, info: Recording{Dir: dir, Path: path, Grant: "g", Kind: "exec", Title: "echo hi"}}
	r.Output([]byte("hi\r\n"))
	r.Close("exit 0")
	r.Close("exit 0") // twice: one notification
	if len(got) != 1 {
		t.Fatalf("%d notifications", len(got))
	}
	if g := got[0]; g.Path != path || g.Grant != "g" || g.Kind != "exec" || g.Exit != "exit 0" || g.Dir != dir {
		t.Errorf("recording: %+v", g)
	}
}
