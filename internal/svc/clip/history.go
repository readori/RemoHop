package clip

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/config"
)

// Entry is one history record.
type Entry struct {
	Time time.Time `json:"time"`
	// Dir is DirPush (local -> remote) or DirPull (remote -> local).
	Dir Direction `json:"dir"`
	// Via is the operation: sync, get, set, push or pull.
	Via  string `json:"via"`
	Peer string `json:"peer"`
	Kind Kind   `json:"kind"`
	Size int    `json:"size"`
	Hash string `json:"hash"`
	// Text holds the text of text entries (possibly cut, see Truncated).
	Text      string `json:"text,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	// File is the PNG file of image entries: relative to the history
	// directory on disk, absolute in entries returned by List.
	File string `json:"file,omitempty"`
	// ID identifies the entry across listings (derived from its time, peer,
	// direction and content).
	ID string `json:"id,omitempty"`
	// Source is where the content came from: SourceLocal for this machine's
	// clipboard, otherwise the peer's name.
	Source string `json:"source,omitempty"`
	// SentTo lists the peers the content was sent to.
	SentTo []string `json:"sent_to,omitempty"`
	// N is the entry's number in the listing it came from (1 = newest). It
	// is not stored.
	N int `json:"n,omitempty"`
}

// SourceLocal is Entry.Source for content that originated on this machine.
const SourceLocal = "local"

// complete fills in the fields older history files lack (ID, Source,
// SentTo), which is the whole migration: old files load as if they had been
// written by this version and are rewritten in the new form by the next
// change. hostSide says whose point of view "local" is: on the client a push
// leaves this machine, on the host it arrives.
func (e *Entry) complete(hostSide bool) {
	if e.ID == "" {
		sum := sha256.Sum256(fmt.Appendf(nil, "%d|%s|%s|%s", e.Time.UnixNano(), e.Peer, e.Dir, e.Hash))
		e.ID = hex.EncodeToString(sum[:6])
	}
	if e.Source != "" {
		return
	}
	if outgoing := (e.Dir == DirPush) != hostSide; outgoing {
		e.Source = SourceLocal
		if len(e.SentTo) == 0 && e.Peer != "" {
			e.SentTo = []string{e.Peer}
		}
	} else {
		e.Source = e.Peer
	}
}

// History is the client-side record of everything that passed through the
// clipboard service. Clipboards carry secrets, so the directory is 0700 and
// every file 0600. One history is global: every entry names its peer, and
// List filters by it. It is safe for use by several processes (a background
// sync and one-shot commands) through a lock file.
type History struct {
	dir string

	// Limits; zero means the default.
	MaxEntries int   // per peer, default 1000
	MaxBytes   int64 // per peer, default 64 MiB of text plus image data
	MaxText    int   // text kept per entry, default 1 MiB

	// hostSide marks the host daemon's own history (see Entry.complete).
	hostSide bool

	mu sync.Mutex
}

const (
	defaultMaxEntries = 1000
	defaultMaxBytes   = 64 << 20
	defaultMaxText    = 1 << 20
	historyFile       = "history.jsonl"
	imageDir          = "images"
)

// OpenHistory opens (creating if needed) the history in dir; an empty dir
// means the "clip" directory under the RemoHop config directory.
func OpenHistory(dir string) (*History, error) {
	if dir == "" {
		d, err := config.SubDir("clip")
		if err != nil {
			return nil, err
		}
		dir = d
	}
	if err := os.MkdirAll(filepath.Join(dir, imageDir), 0o700); err != nil {
		return nil, err
	}
	return &History{dir: dir}, nil
}

func defaultHistory() *History {
	h, err := OpenHistory("")
	if err != nil {
		slog.Warn("clip: cannot open history", "err", err)
		return nil
	}
	return h
}

// Dir is the history directory.
func (h *History) Dir() string { return h.dir }

func (h *History) limits() (int, int64, int) {
	n, b, t := h.MaxEntries, h.MaxBytes, h.MaxText
	if n <= 0 {
		n = defaultMaxEntries
	}
	if b <= 0 {
		b = defaultMaxBytes
	}
	if t <= 0 {
		t = defaultMaxText
	}
	return n, b, t
}

// lock takes the cross-process lock: an OS file lock (flock, LockFileEx)
// that the system releases if the holder dies. A create-and-delete lock
// file does not work on Windows, where a file that was just deleted can
// refuse to be created again for a moment ("access denied").
func (h *History) lock() (unlock func(), err error) {
	unlock, err = config.Lock(filepath.Join(h.dir, historyFile))
	if err != nil {
		return nil, fmt.Errorf("clipboard history: %w", err)
	}
	return unlock, nil
}

// Add records c. Peer, Dir and Via come from e; the other fields are filled
// in. A record identical to the previous one (same peer, direction and
// content) is skipped and reported as not recorded.
func (h *History) Add(e Entry, c Content) (recorded bool, err error) {
	c = normalize(c)
	if c.Validate() != nil {
		return false, nil
	}
	_, _, maxText := h.limits()
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	e.Time = e.Time.UTC()
	e.Kind, e.Size, e.Hash = c.Kind, len(c.Data), c.Hash()
	e.ID, e.N = "", 0
	e.complete(h.hostSide)
	switch c.Kind {
	case KindText:
		text := c.Data
		if len(text) > maxText {
			text = text[:maxText]
			for len(text) > 0 && !utf8.Valid(text) {
				text = text[:len(text)-1]
			}
			e.Truncated = true
		}
		e.Text = string(text)
	case KindImage:
		e.File = filepath.Join(imageDir, e.Hash+".png")
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	unlock, err := h.lock()
	if err != nil {
		return false, err
	}
	defer unlock()

	entries, err := h.load()
	if err != nil {
		return false, err
	}
	if n := len(entries); n > 0 {
		last := entries[n-1]
		if last.Peer == e.Peer && last.Dir == e.Dir && last.Hash == e.Hash {
			return false, nil
		}
	}
	if e.File != "" {
		if err := h.writeImage(e.File, c.Data); err != nil {
			return false, err
		}
	}
	entries = append(entries, e)
	if kept := h.prune(entries); len(kept) != len(entries) {
		return true, h.save(kept)
	}
	line, err := json.Marshal(e)
	if err != nil {
		return false, err
	}
	f, err := os.OpenFile(filepath.Join(h.dir, historyFile), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return false, err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err == nil, err
}

func (h *History) writeImage(rel string, data []byte) error {
	p := filepath.Join(h.dir, rel)
	if st, err := os.Stat(p); err == nil && st.Size() == int64(len(data)) {
		return nil // same hash, same content
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// prune enforces the limits. MaxEntries and MaxBytes apply per peer, so one
// peer flooding the clipboard can only evict its own history; a generous
// overall ceiling (totalFactor times the per-peer limits) bounds the whole
// store. The newest entry of every peer is always kept.
func (h *History) prune(entries []Entry) []Entry {
	maxEntries, maxBytes, _ := h.limits()
	type usage struct {
		n     int
		bytes int64
		files map[string]bool
	}
	per := map[string]*usage{}
	keep := make([]bool, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		u := per[e.Peer]
		if u == nil {
			u = &usage{files: map[string]bool{}}
			per[e.Peer] = u
		}
		cost := int64(len(e.Text))
		if e.File != "" && !u.files[e.File] {
			cost += imageSize(e) // an image shared by several entries is stored once
		}
		if u.n > 0 && (u.n+1 > maxEntries || u.bytes+cost > maxBytes) {
			continue
		}
		u.n++
		u.bytes += cost
		if e.File != "" {
			u.files[e.File] = true
		}
		keep[i] = true
	}
	var out []Entry
	for i, e := range entries {
		if keep[i] {
			out = append(out, e)
		}
	}
	// Overall ceiling, oldest first, never the last entry.
	var total int64
	files := map[string]bool{}
	for _, e := range out {
		total += int64(len(e.Text))
		if e.File != "" && !files[e.File] {
			files[e.File] = true
			total += imageSize(e)
		}
	}
	for len(out) > 1 && (len(out) > maxEntries*totalFactor || total > maxBytes*totalFactor) {
		total -= int64(len(out[0].Text))
		out = out[1:]
	}
	return out
}

// totalFactor scales the per-peer limits to the whole-store ceiling.
const totalFactor = 8

func imageSize(e Entry) int64 {
	if e.File == "" {
		return 0
	}
	return int64(e.Size)
}

// load reads every entry, skipping lines that do not parse.
func (h *History) load() ([]Entry, error) {
	f, err := os.Open(filepath.Join(h.dir, historyFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Entry
	r := bufio.NewReaderSize(f, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var e Entry
			if json.Unmarshal(line, &e) == nil && e.Hash != "" {
				e.N = 0
				e.complete(h.hostSide)
				out = append(out, e)
			}
		}
		if err != nil {
			break
		}
	}
	return out, nil
}

// save atomically rewrites the history with entries and removes image files
// nothing references any more.
func (h *History) save(entries []Entry) error {
	var buf bytes.Buffer
	keep := map[string]bool{}
	for _, e := range entries {
		line, err := json.Marshal(e)
		if err != nil {
			return err
		}
		buf.Write(line)
		buf.WriteByte('\n')
		if e.File != "" {
			keep[filepath.Base(e.File)] = true
		}
	}
	p := filepath.Join(h.dir, historyFile)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		return err
	}
	ents, _ := os.ReadDir(filepath.Join(h.dir, imageDir))
	for _, de := range ents {
		if !keep[de.Name()] {
			os.Remove(filepath.Join(h.dir, imageDir, de.Name()))
		}
	}
	return nil
}

// Filter selects history entries.
type Filter struct {
	Peer  string    // only this peer; empty means every peer
	Since time.Time // only entries at or after this time
	Limit int       // only the newest Limit entries; 0 means all
}

// List returns the matching entries, oldest first, each with its number N
// (1 = newest in the peer scope). Image paths are absolute.
func (h *History) List(f Filter) ([]Entry, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	all, err := h.load()
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, e := range all {
		if f.Peer != "" && e.Peer != f.Peer {
			continue
		}
		if e.File != "" {
			e.File = filepath.Join(h.dir, e.File)
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	// Numbers count back from the newest entry of the peer scope, before the
	// Since/Limit cuts, so the same entry has the same number in every view
	// of that scope.
	for i := range out {
		out[i].N = len(out) - i
	}
	if !f.Since.IsZero() {
		for len(out) > 0 && out[0].Time.Before(f.Since) {
			out = out[1:]
		}
	}
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[len(out)-f.Limit:]
	}
	return out, nil
}

// Clear deletes the entries of peer (every entry if peer is empty) and any
// image files that become unreferenced. It returns how many entries went.
func (h *History) Clear(peer string) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	unlock, err := h.lock()
	if err != nil {
		return 0, err
	}
	defer unlock()
	all, err := h.load()
	if err != nil {
		return 0, err
	}
	var kept []Entry
	for _, e := range all {
		if peer != "" && e.Peer != peer {
			kept = append(kept, e)
		}
	}
	return len(all) - len(kept), h.save(kept)
}

// Remove deletes the entries with the given IDs and returns them (with
// absolute image paths as they were); image files nothing references any
// more are deleted too.
func (h *History) Remove(ids ...string) ([]Entry, error) {
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var removed []Entry
	err := h.rewrite(func(all []Entry) []Entry {
		var kept []Entry
		for _, e := range all {
			if want[e.ID] {
				removed = append(removed, e)
			} else {
				kept = append(kept, e)
			}
		}
		return kept
	})
	return removed, err
}

// MarkSent notes on entry id that its content was also sent to peer.
func (h *History) MarkSent(id, peer string) error {
	return h.rewrite(func(all []Entry) []Entry {
		for i := range all {
			if all[i].ID == id && !slices.Contains(all[i].SentTo, peer) {
				all[i].SentTo = append(all[i].SentTo, peer)
			}
		}
		return all
	})
}

// rewrite loads, transforms and saves the history under the lock.
func (h *History) rewrite(f func([]Entry) []Entry) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	unlock, err := h.lock()
	if err != nil {
		return err
	}
	defer unlock()
	all, err := h.load()
	if err != nil {
		return err
	}
	return h.save(f(all))
}

// Content loads the content of an entry back, from its text or image file.
func (e Entry) Content() (Content, error) {
	switch e.Kind {
	case KindText:
		if e.Truncated {
			return Content{}, errors.New("history text was truncated")
		}
		return Text(e.Text), nil
	case KindImage:
		b, err := os.ReadFile(e.File)
		if err != nil {
			return Content{}, err
		}
		return PNG(b)
	}
	return Content{}, fmt.Errorf("unknown kind %q", e.Kind)
}

// RenderMarkdown formats entries (oldest first) as Markdown meant to be read
// by an LLM: one heading per entry with time, direction and peer, text in
// fenced code blocks whose fence is longer than any backtick run inside the
// text (so the content cannot terminate it early), and images as file
// references.
func RenderMarkdown(entries []Entry) string {
	s, _ := RenderMarkdownLimit(entries, 0)
	return s
}

// DefaultMarkdownBytes is the default size cap of a Markdown export.
const DefaultMarkdownBytes = 2 << 20

// RenderMarkdownLimit is RenderMarkdown with a size cap (0 means none).
// When the entries do not fit, the oldest are left out; if even the newest
// one does not fit, its text is cut. The export then ends with a line saying
// so, and shown reports how many entries made it.
func RenderMarkdownLimit(entries []Entry, maxBytes int) (md string, shown int) {
	const header = "# Clipboard history\n\n%d %s, oldest first. Times are UTC. \"pull\" means copied from the remote machine to the local one, \"push\" the other way.\n"
	parts := make([]string, len(entries))
	first := 0
	if maxBytes > 0 {
		budget := maxBytes - len(header) - 160 // header and truncation note
		first = len(entries)
		for i := len(entries) - 1; i >= 0; i-- {
			parts[i] = renderEntry(entries[i])
			if len(parts[i]) > budget {
				if i == len(entries)-1 {
					// Not even the newest fits: show the start of its text.
					e := entries[i]
					over := len(parts[i]) - budget + 80
					if e.Kind == KindText && over < len(e.Text) {
						e.Text = strings.ToValidUTF8(e.Text[:len(e.Text)-over], "")
						e.Truncated = true
						parts[i] = renderEntry(e)
						first = i
					}
				}
				break
			}
			budget -= len(parts[i])
			first = i
		}
	} else {
		for i, e := range entries {
			parts[i] = renderEntry(e)
		}
	}
	shown = len(entries) - first
	var b strings.Builder
	fmt.Fprintf(&b, header, shown, plural(shown, "entry", "entries"))
	for _, p := range parts[first:] {
		b.WriteString(p)
	}
	if first > 0 {
		fmt.Fprintf(&b, "\n(output truncated at %d bytes: the %d oldest of %d entries were left out)\n", maxBytes, first, len(entries))
	}
	return b.String(), shown
}

func renderEntry(e Entry) string {
	var b strings.Builder
	dir := "pull from"
	if e.Dir == DirPush {
		dir = "push to"
	}
	num := ""
	if e.N > 0 {
		num = fmt.Sprintf(" (#%d)", e.N)
	}
	fmt.Fprintf(&b, "\n## %s - %s %s%s\n\n", e.Time.UTC().Format(time.RFC3339), dir, meta(e.Peer), num)
	fmt.Fprintf(&b, "- kind: %s, %d bytes, via %s, sha256 %s", meta(string(e.Kind)), e.Size, meta(e.Via), meta(shortHash(e.Hash)))
	if e.Source != "" {
		fmt.Fprintf(&b, ", source %s", meta(e.Source))
	}
	if len(e.SentTo) > 0 {
		fmt.Fprintf(&b, ", sent to %s", meta(strings.Join(e.SentTo, ", ")))
	}
	b.WriteString("\n\n")
	switch e.Kind {
	case KindImage:
		fmt.Fprintf(&b, "![clipboard image](%s)\n\nFile: `%s` (image/png)\n", meta(e.File), meta(e.File))
	default:
		fence := fenceFor(e.Text)
		b.WriteString(fence + "text\n")
		b.WriteString(e.Text)
		if !strings.HasSuffix(e.Text, "\n") {
			b.WriteByte('\n')
		}
		b.WriteString(fence + "\n")
		if e.Truncated {
			fmt.Fprintf(&b, "\n(truncated: the first %d of %d bytes are shown)\n", len(e.Text), e.Size)
		}
	}
	return b.String()
}

func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// meta makes a metadata string safe for a single Markdown line: control
// characters (newlines, escapes) are replaced and the length is bounded.
func meta(s string) string {
	s = cli.Sanitize(s)
	s = strings.NewReplacer("\n", " ", "\r", " ", "`", "'").Replace(s)
	if r := []rune(s); len(r) > 80 {
		s = string(r[:80]) + "..."
	}
	return s
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// fenceFor returns a backtick fence longer than any backtick run in s.
func fenceFor(s string) string {
	longest, run := 0, 0
	for i := 0; i < len(s); i++ {
		if s[i] == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	return strings.Repeat("`", max(3, longest+1))
}
