package signal

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/config"
)

// outcomesFile is the aggregate of connection outcomes in the data
// directory.
const outcomesFile = "outcomes.json"

// Bucket upper edges in milliseconds; the last bucket is open ended.
var (
	msEdges         = []int64{250, 500, 1000, 2000, 4000, 8000}
	msLabels        = []string{"<250ms", "<500ms", "<1s", "<2s", "<4s", "<8s", ">=8s"}
	upgradedEdges   = []int64{10000, 30000, 60000, 300000}
	upgradedLabels  = []string{"<10s", "<30s", "<60s", "<300s", ">=300s"}
	unknownNATLabel = "unknown"
)

func bucketOf(path string, ms int64) (idx int) {
	edges := msEdges
	if path == PathUpgraded {
		edges = upgradedEdges
	}
	for i, e := range edges {
		if ms < e {
			return i
		}
	}
	return len(edges)
}

func bucketLabels(path string) []string {
	if path == PathUpgraded {
		return upgradedLabels
	}
	return msLabels
}

// outcomeRecord is the file format: counts only.
type outcomeRecord struct {
	Month string                      `json:"month"`
	Paths map[string]int64            `json:"paths"`
	MS    map[string][]int64          `json:"ms"`
	Pairs map[string]map[string]int64 `json:"pairs"`
}

func newOutcomeRecord(month string) outcomeRecord {
	return outcomeRecord{Month: month, Paths: map[string]int64{}, MS: map[string][]int64{}, Pairs: map[string]map[string]int64{}}
}

func (r outcomeRecord) total() (n int64) {
	for _, c := range r.Paths {
		n += c
	}
	return n
}

// outcomeStats is the server's aggregate of connection outcomes for the
// current UTC month. It keeps counts only.
type outcomeStats struct {
	mu    sync.Mutex
	now   func() time.Time
	dir   string
	log   *slog.Logger
	rec   outcomeRecord
	dirty bool
}

func readOutcomes(dir string) (outcomeRecord, error) {
	var r outcomeRecord
	b, err := os.ReadFile(filepath.Join(dir, outcomesFile))
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return r, fmt.Errorf("%s: %w", outcomesFile, err)
	}
	if r.Paths == nil {
		r.Paths = map[string]int64{}
	}
	if r.MS == nil {
		r.MS = map[string][]int64{}
	}
	if r.Pairs == nil {
		r.Pairs = map[string]map[string]int64{}
	}
	return r, nil
}

// openOutcomeStats loads the aggregate from dir. An empty dir keeps it in
// memory only; a missing file starts fresh.
func openOutcomeStats(dir string, now func() time.Time, log *slog.Logger) (*outcomeStats, error) {
	if now == nil {
		now = time.Now
	}
	o := &outcomeStats{now: now, dir: dir, log: log}
	o.rec = newOutcomeRecord(monthOf(now()))
	if dir == "" {
		return o, nil
	}
	r, err := readOutcomes(dir)
	if errors.Is(err, os.ErrNotExist) {
		return o, nil
	}
	if err != nil {
		return nil, err
	}
	if r.Month == o.rec.Month {
		o.rec = r
	} else {
		// The file holds an earlier month: report it once and start over.
		o.rec = r
		o.rollLocked()
	}
	return o, nil
}

func (o *outcomeStats) rollLocked() {
	m := monthOf(o.now())
	if m == o.rec.Month {
		return
	}
	if o.log != nil && o.rec.total() > 0 {
		args := []any{"month", o.rec.Month, "reports", o.rec.total()}
		for _, p := range sortedKeys(o.rec.Paths) {
			args = append(args, p, o.rec.Paths[p])
		}
		o.log.Info("connection outcomes: a new month begins", args...)
	}
	o.rec = newOutcomeRecord(m)
	o.dirty = true
}

func sortedKeys[V any](m map[string]V) []string {
	k := make([]string, 0, len(m))
	for s := range m {
		k = append(k, s)
	}
	sort.Strings(k)
	return k
}

// add counts one valid report.
func (o *outcomeStats) add(oc Outcome) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.rollLocked()
	r := &o.rec
	r.Paths[oc.Path]++
	if oc.Path != PathFailed || oc.MS > 0 {
		b := r.MS[oc.Path]
		if b == nil {
			b = make([]int64, len(bucketLabels(oc.Path)))
			r.MS[oc.Path] = b
		}
		b[bucketOf(oc.Path, oc.MS)]++
	}
	n, p := oc.NAT, oc.PeerNAT
	if n == "" {
		n = unknownNATLabel
	}
	if p == "" {
		p = unknownNATLabel
	}
	key := n + "/" + p
	if r.Pairs[key] == nil {
		r.Pairs[key] = map[string]int64{}
	}
	r.Pairs[key][oc.Path]++
	o.dirty = true
}

// save writes the aggregate out if it changed; a no-op without a dir.
func (o *outcomeStats) save() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.rollLocked()
	if o.dir == "" || !o.dirty {
		return nil
	}
	b, err := json.Marshal(o.rec)
	if err != nil {
		return err
	}
	if err := config.WriteFileAtomic(filepath.Join(o.dir, outcomesFile), append(b, '\n'), 0o600); err != nil {
		return err
	}
	o.dirty = false
	return nil
}

// snapshot returns a deep copy of the current month's counts.
func (o *outcomeStats) snapshot() outcomeRecord {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.rollLocked()
	return copyOutcomeRecord(o.rec)
}

func copyOutcomeRecord(r outcomeRecord) outcomeRecord {
	c := newOutcomeRecord(r.Month)
	for k, v := range r.Paths {
		c.Paths[k] = v
	}
	for k, v := range r.MS {
		c.MS[k] = append([]int64(nil), v...)
	}
	for k, v := range r.Pairs {
		m := map[string]int64{}
		for p, n := range v {
			m[p] = n
		}
		c.Pairs[k] = m
	}
	return c
}

func pct(n, total int64) float64 {
	if total == 0 {
		return 0
	}
	return float64(n) * 100 / float64(total)
}

// printOutcomeStatus reports this month's connection outcomes from the
// data directory (for `remohop signal --status`).
func printOutcomeStatus(w io.Writer, dir string, now time.Time) error {
	month := monthOf(now)
	r, err := readOutcomes(dir)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(w, "connection outcomes: (no %s in %s: no reports have been recorded with this data directory)\n", outcomesFile, dir)
		return nil
	}
	if err != nil {
		return err
	}
	if r.Month != month {
		fmt.Fprintf(w, "connection outcomes %s (UTC): 0 reports\n", month)
		fmt.Fprintf(w, "  last recorded month %s: %d reports\n", r.Month, r.total())
		return nil
	}
	// An upgrade is a second report for a connection already counted under
	// its first path, so it is left out of the shares and shown on its own.
	total := r.total() - r.Paths[PathUpgraded]
	fmt.Fprintf(w, "connection outcomes %s (UTC): %d reports\n", month, total)
	for _, p := range sortedKeys(r.Paths) {
		if p == PathUpgraded {
			continue
		}
		fmt.Fprintf(w, "  %-10s %6d  %5.1f%%\n", p, r.Paths[p], pct(r.Paths[p], total))
	}
	printBuckets := func(title, path string) {
		b := r.MS[path]
		var sum int64
		for _, c := range b {
			sum += c
		}
		if sum == 0 {
			return
		}
		var parts []string
		for i, c := range b {
			if i < len(bucketLabels(path)) {
				parts = append(parts, fmt.Sprintf("%s %.0f%%", bucketLabels(path)[i], pct(c, sum)))
			}
		}
		fmt.Fprintf(w, "  %s: %s\n", title, strings.Join(parts, ", "))
	}
	printBuckets("time to connect (punched)", PathPunched)
	if n := r.Paths[PathUpgraded]; n > 0 {
		fmt.Fprintf(w, "  upgraded to direct: %d\n", n)
		printBuckets("time to upgrade", PathUpgraded)
	}
	if len(r.Pairs) > 0 {
		fmt.Fprintln(w, "  by NAT pair (client/host): punched / relayed / failed")
		for _, k := range sortedKeys(r.Pairs) {
			m := r.Pairs[k]
			var n int64
			for p, c := range m {
				if p != PathUpgraded {
					n += c
				}
			}
			relayed := m[PathRelayed] + m[PathBridged]
			fmt.Fprintf(w, "    %-20s %5d  %5.1f%% / %5.1f%% / %5.1f%%\n", k, n,
				pct(m[PathPunched], n), pct(relayed, n), pct(m[PathFailed], n))
		}
	}
	return nil
}
