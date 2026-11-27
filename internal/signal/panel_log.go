package signal

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/redact"
)

// The panel's health page shows the server's recent log records. A
// slog.Handler wrapper keeps the last logKeep warnings and errors, and the
// last logKeep records at info level, in memory, with addresses, IDs, keys
// and emails taken out: the panel shows no more of who uses the server
// than its other pages do.
const logKeep = 200

type logRecord struct {
	T     int64     `json:"t"` // unix milliseconds
	Level string    `json:"level"`
	Msg   string    `json:"msg"`
	Attrs []logAttr `json:"attrs,omitempty"`
}

type logAttr struct {
	K string `json:"k"`
	V string `json:"v"`
}

// logRing holds the records.
type logRing struct {
	mu   sync.Mutex
	warn *ring[logRecord]
	info *ring[logRecord]
}

func newLogRing() *logRing {
	return &logRing{warn: newRing[logRecord](logKeep), info: newRing[logRecord](logKeep)}
}

func (l *logRing) add(r logRecord, level slog.Level) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if level >= slog.LevelWarn {
		l.warn.push(r)
	} else {
		l.info.push(r)
	}
}

// records returns the warnings and errors, and the info records, newest
// last.
func (l *logRing) records() (warn, info []logRecord) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.warn.items(), l.info.items()
}

// handler returns next wrapped so that records also reach the ring.
func (l *logRing) handler(next slog.Handler) slog.Handler {
	return &ringHandler{next: next, ring: l}
}

type ringHandler struct {
	next   slog.Handler
	ring   *logRing
	attrs  []logAttr // from WithAttrs, already redacted
	prefix string    // from WithGroup
}

func (h *ringHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= slog.LevelInfo || h.next.Enabled(ctx, l)
}

func (h *ringHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level >= slog.LevelInfo {
		rec := logRecord{T: r.Time.UnixMilli(), Level: levelName(r.Level), Msg: scrubText(r.Message)}
		if r.Time.IsZero() {
			rec.T = time.Now().UnixMilli()
		}
		rec.Attrs = append(rec.Attrs, h.attrs...)
		r.Attrs(func(a slog.Attr) bool {
			rec.Attrs = appendAttr(rec.Attrs, h.prefix, a)
			return true
		})
		h.ring.add(rec, r.Level)
	}
	if h.next.Enabled(ctx, r.Level) {
		return h.next.Handle(ctx, r)
	}
	return nil
}

func (h *ringHandler) WithAttrs(as []slog.Attr) slog.Handler {
	n := *h
	n.next = h.next.WithAttrs(as)
	n.attrs = append([]logAttr(nil), h.attrs...)
	for _, a := range as {
		n.attrs = appendAttr(n.attrs, h.prefix, a)
	}
	return &n
}

func (h *ringHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	n := *h
	n.next = h.next.WithGroup(name)
	n.prefix = h.prefix + name + "."
	return &n
}

func levelName(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return "error"
	case l >= slog.LevelWarn:
		return "warn"
	default:
		return "info"
	}
}

// privateKeys are attribute names whose values identify a user, a machine
// or an address on this server; they are never shown.
var privateKeys = map[string]bool{
	"id": true, "ids": true, "addr": true, "address": true, "client": true, "host": true, "from": true, "to": true,
	"online": true, "refused": true, "key": true, "keys": true, "email": true, "ip": true, "source": true, "peer": true,
	"you": true, "device": true, "account": true, "remote": true, "reflexive": true, "name": true, "user": true,
}

func appendAttr(out []logAttr, prefix string, a slog.Attr) []logAttr {
	a.Value = a.Value.Resolve()
	if a.Value.Kind() == slog.KindGroup {
		p := prefix
		if a.Key != "" {
			p += a.Key + "."
		}
		for _, g := range a.Value.Group() {
			out = appendAttr(out, p, g)
		}
		return out
	}
	if a.Key == "" {
		return out
	}
	return append(out, logAttr{K: prefix + a.Key, V: scrubValue(a.Key, a.Value)})
}

func scrubValue(key string, v slog.Value) string {
	k := strings.ToLower(key)
	if privateKeys[k] || redact.SecretKey(k) {
		return "[hidden]"
	}
	switch v.Kind() {
	case slog.KindInt64, slog.KindUint64, slog.KindFloat64, slog.KindBool, slog.KindDuration:
		return v.String()
	case slog.KindTime:
		return v.Time().UTC().Format(time.RFC3339)
	case slog.KindAny:
		switch x := v.Any().(type) {
		case netip.Addr, netip.AddrPort, netip.Prefix, net.Addr, net.IP, *net.UDPAddr, *net.TCPAddr:
			return "[address]"
		case error:
			return scrubText(x.Error())
		case []byte:
			return "[bytes]"
		case fmt.Stringer:
			return scrubText(x.String())
		}
	}
	return scrubText(v.String())
}

var (
	// addrCandidate matches anything that may be an IP address, with or
	// without brackets, a zone and a port; each match is then parsed.
	addrCandidate = regexp.MustCompile(`\[?[0-9A-Fa-f]*[:.][0-9A-Fa-f:.]*[0-9A-Fa-f](%[0-9A-Za-z_.-]+)?\]?(:[0-9]{1,5})?`)
	hostID        = regexp.MustCompile(`\b[1-9][0-9]{8}\b`)
	emailRE       = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
)

// scrubText takes IP addresses, 9-digit host IDs, emails and secrets out of
// free text (error messages, log messages).
func scrubText(s string) string {
	s = redact.Text(s)
	s = emailRE.ReplaceAllString(s, "[email]")
	s = addrCandidate.ReplaceAllStringFunc(s, func(m string) string {
		if isAddr(m) {
			return "[address]"
		}
		return m
	})
	return hostID.ReplaceAllString(s, "[id]")
}

func isAddr(m string) bool {
	if _, err := netip.ParseAddrPort(m); err == nil {
		return true
	}
	t := strings.TrimSuffix(strings.TrimPrefix(m, "["), "]")
	if _, err := netip.ParseAddr(t); err == nil {
		return true
	}
	if h, _, err := net.SplitHostPort(m); err == nil {
		if _, err := netip.ParseAddr(h); err == nil {
			return true
		}
	}
	return false
}
