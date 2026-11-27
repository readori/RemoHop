package hostd

import (
	"path/filepath"
	"slices"
	"sync"

	"github.com/readori/RemoHop/internal/grant"
)

// Sinks let another package see what the host records, to ship it off the
// machine (syslog, a central log server, the team server of RemoHop Teams).
// The host's own files stay the record: a sink sees each entry after it
// was written locally, and nothing a sink does changes what was written.
//
// A sink is called on the goroutine that recorded the entry, which may be
// handling a login: it must not block. Copy what it needs and hand it to
// its own goroutine; drop rather than wait when that falls behind.

// AuditSink receives one audit entry. dir is the config directory of the
// host that wrote it (one process can run several hosts, in tests). refused
// is true for entries of the refused log (AuditRefusedFile), including the
// summaries of suppressed ones.
type AuditSink func(dir string, e AuditEntry, refused bool)

// Recording describes a finished session recording (an asciicast v2 file).
type Recording struct {
	Dir   string // the host's config directory
	Path  string // the file, absolute; it is complete and closed
	Grant string // the grant the session stood on
	Kind  string // "terminal" or "exec"
	Title string
	Exit  string // how the session ended, as written into the recording
}

// RecordingSink is told about each recording when it is closed.
type RecordingSink func(Recording)

var sinks struct {
	mu         sync.Mutex
	audit      []AuditSink
	recordings []RecordingSink
}

// RegisterAuditSink adds an audit sink. Call it from init().
func RegisterAuditSink(fn AuditSink) {
	sinks.mu.Lock()
	defer sinks.mu.Unlock()
	sinks.audit = append(sinks.audit, fn)
}

// RegisterRecordingSink adds a recording sink. Call it from init().
func RegisterRecordingSink(fn RecordingSink) {
	sinks.mu.Lock()
	defer sinks.mu.Unlock()
	sinks.recordings = append(sinks.recordings, fn)
}

func notifyAudit(dir string, refused bool, entries ...AuditEntry) {
	sinks.mu.Lock()
	fns := slices.Clone(sinks.audit)
	sinks.mu.Unlock()
	for _, e := range entries {
		for _, fn := range fns {
			fn(dir, e, refused)
		}
	}
}

func hasRecordingSinks() bool {
	sinks.mu.Lock()
	defer sinks.mu.Unlock()
	return len(sinks.recordings) > 0
}

func notifyRecording(r Recording) {
	sinks.mu.Lock()
	fns := slices.Clone(sinks.recordings)
	sinks.mu.Unlock()
	for _, fn := range fns {
		fn(r)
	}
}

// sinkRecorder tells the recording sinks when its recording is closed.
type sinkRecorder struct {
	grant.Recorder
	info Recording
	once sync.Once
}

func (r *sinkRecorder) Close(exit string) {
	r.Recorder.Close(exit)
	r.once.Do(func() {
		info := r.info
		info.Exit = exit
		if abs, err := filepath.Abs(info.Path); err == nil {
			info.Path = abs
		}
		notifyRecording(info)
	})
}
