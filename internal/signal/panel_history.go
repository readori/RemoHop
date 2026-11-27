package signal

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/config"
)

// The panel's history: a sample every minute, kept for 24 hours at that
// resolution and for 7 days at 15 minutes, plus per-day totals for this
// month and the last. It lives in memory and is written to
// DATA/panel-history.json (atomically, every few minutes and on exit), so
// a restart keeps it. Like the rest of the panel it holds counts only.
const (
	historyFile    = "panel-history.json"
	historyVersion = 1
	fineStep       = time.Minute
	fineKeep       = 24 * 60
	coarseStep     = 15 * time.Minute
	coarseKeep     = 7 * 24 * 4
	historySave    = 5 * time.Minute
)

// sample is the server over one interval: the gauges when it was taken
// (for a 15-minute point, their highest value in it) and how much each
// counter of serverStats grew (non-zero ones only).
type sample struct {
	T      int64            `json:"t"` // unix seconds: the minute (or quarter hour) it belongs to
	Hosts  int              `json:"hosts"`
	Conns  int              `json:"conns"`
	WS     int              `json:"ws"`
	Relays int              `json:"relays"`
	C      map[string]int64 `json:"c,omitempty"`
}

// merge adds o into s: counters add up, gauges keep the higher value.
func (s *sample) merge(o sample) {
	s.Hosts, s.Conns, s.WS, s.Relays = max(s.Hosts, o.Hosts), max(s.Conns, o.Conns), max(s.WS, o.WS), max(s.Relays, o.Relays)
	for k, v := range o.C {
		if s.C == nil {
			s.C = map[string]int64{}
		}
		s.C[k] += v
	}
}

func (s sample) clone() sample {
	s.C = maps.Clone(s.C)
	return s
}

// ring is a fixed-size buffer that drops its oldest entry when full.
type ring[T any] struct {
	buf   []T
	start int
	n     int
}

func newRing[T any](size int) *ring[T] { return &ring[T]{buf: make([]T, size)} }

func (r *ring[T]) push(v T) {
	if r.n < len(r.buf) {
		r.buf[(r.start+r.n)%len(r.buf)] = v
		r.n++
		return
	}
	r.buf[r.start] = v
	r.start = (r.start + 1) % len(r.buf)
}

// last is the newest entry, or nil.
func (r *ring[T]) last() *T {
	if r.n == 0 {
		return nil
	}
	return &r.buf[(r.start+r.n-1)%len(r.buf)]
}

// items returns the entries, oldest first.
func (r *ring[T]) items() []T {
	out := make([]T, 0, r.n)
	for i := range r.n {
		out = append(out, r.buf[(r.start+i)%len(r.buf)])
	}
	return out
}

func (r *ring[T]) len() int { return r.n }

// history holds the samples. Its methods are safe for concurrent use.
type history struct {
	path string

	mu     sync.Mutex
	fine   *ring[sample]
	coarse *ring[sample]
	days   map[string]map[string]int64 // UTC date → counter totals
	dirty  bool
}

type historyRecord struct {
	V      int                         `json:"v"`
	Fine   []sample                    `json:"fine"`
	Coarse []sample                    `json:"coarse"`
	Days   map[string]map[string]int64 `json:"days,omitempty"`
}

func newHistory(path string) *history {
	return &history{path: path, fine: newRing[sample](fineKeep), coarse: newRing[sample](coarseKeep), days: map[string]map[string]int64{}}
}

// loadHistory reads the history file in dir, dropping what is older than
// it keeps. A missing file is an empty history; a damaged one is set aside
// (renamed *.bad) so that the panel still starts.
func loadHistory(dir string, now time.Time) (*history, error) {
	h := newHistory(filepath.Join(dir, historyFile))
	b, err := os.ReadFile(h.path)
	if errors.Is(err, os.ErrNotExist) {
		return h, nil
	}
	if err != nil {
		return nil, err
	}
	var rec historyRecord
	if err := json.Unmarshal(b, &rec); err != nil || rec.V != historyVersion {
		os.Rename(h.path, h.path+".bad")
		return h, fmt.Errorf("%s: unreadable, set aside as %s.bad and starting afresh", historyFile, historyFile)
	}
	for _, s := range rec.Fine {
		if now.Sub(time.Unix(s.T, 0)) < fineKeep*fineStep {
			h.pushFine(s)
		}
	}
	for _, s := range rec.Coarse {
		if now.Sub(time.Unix(s.T, 0)) < coarseKeep*coarseStep {
			h.pushCoarse(s)
		}
	}
	for d, c := range rec.Days {
		h.days[d] = c
	}
	h.pruneDays(now)
	return h, nil
}

// pushFine and pushCoarse append in time order: a sample for the newest
// slot is merged into it, one older than the newest is dropped (the clock
// went back).
func (h *history) pushFine(s sample) {
	if l := h.fine.last(); l != nil && s.T <= l.T {
		if s.T == l.T {
			l.merge(s)
		}
		return
	}
	h.fine.push(s.clone())
}

func (h *history) pushCoarse(s sample) {
	if l := h.coarse.last(); l != nil && s.T <= l.T {
		if s.T == l.T {
			l.merge(s)
		}
		return
	}
	h.coarse.push(s.clone())
}

func dayOf(t time.Time) string { return t.UTC().Format("2006-01-02") }

// add records one minute's sample.
func (h *history) add(s sample) {
	h.mu.Lock()
	defer h.mu.Unlock()
	t := time.Unix(s.T, 0)
	s.T = t.Truncate(fineStep).Unix()
	h.pushFine(s)
	q := s.clone()
	q.T = t.Truncate(coarseStep).Unix()
	h.pushCoarse(q)
	if len(s.C) > 0 {
		d := h.days[dayOf(t)]
		if d == nil {
			d = map[string]int64{}
			h.days[dayOf(t)] = d
		}
		for k, v := range s.C {
			d[k] += v
		}
		h.pruneDays(t)
	}
	h.dirty = true
}

// pruneDays keeps the days of this month and the last.
func (h *history) pruneDays(now time.Time) {
	now = now.UTC()
	this := now.Format("2006-01")
	prev := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0).Format("2006-01")
	for d := range h.days {
		if m := d[:min(7, len(d))]; m != this && m != prev {
			delete(h.days, d)
		}
	}
}

// series returns the samples of a range ("24h" or "7d") and their step.
func (h *history) series(rng string) (time.Duration, []sample) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if rng == "7d" {
		return coarseStep, cloneSamples(h.coarse.items())
	}
	return fineStep, cloneSamples(h.fine.items())
}

func cloneSamples(in []sample) []sample {
	for i := range in {
		in[i] = in[i].clone()
	}
	return in
}

// day returns the counter totals of one UTC date (a copy).
func (h *history) day(d string) map[string]int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return maps.Clone(h.days[d])
}

// dayList returns the recorded days, sorted, with their totals.
func (h *history) dayList() (days []string, totals map[string]map[string]int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	totals = map[string]map[string]int64{}
	for d, c := range h.days {
		days = append(days, d)
		totals[d] = maps.Clone(c)
	}
	slices.Sort(days)
	return days, totals
}

// save writes the history out if it changed.
func (h *history) save() error {
	h.mu.Lock()
	if !h.dirty {
		h.mu.Unlock()
		return nil
	}
	rec := historyRecord{V: historyVersion, Fine: h.fine.items(), Coarse: h.coarse.items(), Days: h.days}
	b, err := json.Marshal(rec)
	h.dirty = false
	h.mu.Unlock()
	if err != nil {
		return err
	}
	if err := config.WriteFileAtomic(h.path, append(b, '\n'), 0o600); err != nil {
		h.mu.Lock()
		h.dirty = true
		h.mu.Unlock()
		return err
	}
	return nil
}
