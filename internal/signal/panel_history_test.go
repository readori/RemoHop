package signal

import (
	"bytes"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRing(t *testing.T) {
	r := newRing[int](3)
	if r.last() != nil || len(r.items()) != 0 {
		t.Fatal("empty ring has entries")
	}
	for i := 1; i <= 5; i++ {
		r.push(i)
	}
	if got := r.items(); !reflect.DeepEqual(got, []int{3, 4, 5}) || *r.last() != 5 || r.len() != 3 {
		t.Fatalf("items %v, last %d", got, *r.last())
	}
	*r.last() = 50
	if got := r.items(); got[2] != 50 {
		t.Fatalf("last() does not point into the ring: %v", got)
	}
}

func TestHistoryResolutions(t *testing.T) {
	h := newHistory(filepath.Join(t.TempDir(), historyFile))
	base := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	for i := range 30 {
		h.add(sample{T: base.Add(time.Duration(i) * time.Minute).Unix(), Hosts: i, Conns: 2 * i,
			C: map[string]int64{"lookups": 1, "relay_bytes": 1000}})
	}
	// A second sample for the same minute is merged.
	h.add(sample{T: base.Add(29 * time.Minute).Unix(), C: map[string]int64{"lookups": 1}})
	// One for an earlier minute (the clock went back) is dropped from the
	// minute series.
	h.add(sample{T: base.Add(5 * time.Minute).Unix(), Hosts: 99})

	step, fine := h.series("24h")
	if step != time.Minute || len(fine) != 30 {
		t.Fatalf("24h: step %v, %d points", step, len(fine))
	}
	if fine[29].C["lookups"] != 2 || fine[29].Hosts != 29 {
		t.Errorf("merged minute %+v", fine[29])
	}
	step, coarse := h.series("7d")
	if step != 15*time.Minute || len(coarse) != 2 {
		t.Fatalf("7d: step %v, %d points", step, len(coarse))
	}
	// Counters add up, gauges keep the peak of the quarter hour.
	if coarse[0].C["lookups"] != 15 || coarse[0].Hosts != 14 || coarse[0].Conns != 28 {
		t.Errorf("first quarter hour %+v", coarse[0])
	}
	if coarse[1].C["relay_bytes"] != 15000 || coarse[1].Hosts != 29 {
		t.Errorf("second quarter hour %+v", coarse[1])
	}
	if d := h.day("2026-11-27"); d["lookups"] != 31 || d["relay_bytes"] != 30000 {
		t.Errorf("day totals %v", d)
	}
	// Series are copies: changing one leaves the history alone.
	fine[0].C["lookups"] = 1e6
	if _, again := h.series("24h"); again[0].C["lookups"] != 1 {
		t.Error("series shares its maps with the history")
	}
}

func TestHistoryKeepsItsWindow(t *testing.T) {
	h := newHistory(filepath.Join(t.TempDir(), historyFile))
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	n := 8 * 24 * 60 // eight days of minutes
	for i := range n {
		h.add(sample{T: base.Add(time.Duration(i) * time.Minute).Unix(), Hosts: 1})
	}
	_, fine := h.series("24h")
	_, coarse := h.series("7d")
	if len(fine) != fineKeep || len(coarse) != coarseKeep {
		t.Fatalf("%d minutes and %d quarter hours, want %d and %d", len(fine), len(coarse), fineKeep, coarseKeep)
	}
	last := base.Add(time.Duration(n-1) * time.Minute)
	if fine[len(fine)-1].T != last.Unix() || fine[0].T != last.Add(-(fineKeep-1)*time.Minute).Unix() {
		t.Errorf("minutes from %v to %v", time.Unix(fine[0].T, 0).UTC(), time.Unix(fine[len(fine)-1].T, 0).UTC())
	}
}

func TestHistoryPersists(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().Truncate(time.Minute)
	h := newHistory(filepath.Join(dir, historyFile))
	// One sample too old to keep, then two recent ones.
	h.add(sample{T: now.Add(-25 * time.Hour).Unix(), Hosts: 7, C: map[string]int64{"registrations": 3}})
	h.add(sample{T: now.Add(-2 * time.Minute).Unix(), Hosts: 4, Relays: 1, C: map[string]int64{"registrations": 1}})
	h.add(sample{T: now.Add(-time.Minute).Unix(), Hosts: 5, WS: 2})
	if err := h.save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, historyFile)); err != nil {
		t.Fatal(err)
	}
	// No temporary files left behind: the write is atomic.
	if m, _ := filepath.Glob(filepath.Join(dir, historyFile+".tmp*")); len(m) > 0 {
		t.Fatalf("temporary files left: %v", m)
	}
	h2, err := loadHistory(dir, now)
	if err != nil {
		t.Fatal(err)
	}
	_, fine := h2.series("24h")
	if len(fine) != 2 || fine[0].Hosts != 4 || fine[1].WS != 2 || fine[0].C["registrations"] != 1 {
		t.Fatalf("reloaded minutes %+v", fine)
	}
	_, coarse := h2.series("7d")
	if len(coarse) < 2 || coarse[0].Hosts != 7 {
		t.Fatalf("reloaded quarter hours %+v", coarse)
	}
	// Saving an unchanged history writes nothing.
	os.Remove(filepath.Join(dir, historyFile))
	if err := h2.save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, historyFile)); !os.IsNotExist(err) {
		t.Fatal("an unchanged history was written")
	}

	// A damaged file is set aside and the panel starts afresh.
	os.WriteFile(filepath.Join(dir, historyFile), []byte("{not json"), 0o600)
	h3, err := loadHistory(dir, now)
	if h3 == nil || err == nil {
		t.Fatalf("damaged file: %v %v", h3, err)
	}
	if _, err := os.Stat(filepath.Join(dir, historyFile+".bad")); err != nil {
		t.Fatal("the damaged file was not set aside")
	}
}

func TestHistoryDays(t *testing.T) {
	h := newHistory(filepath.Join(t.TempDir(), historyFile))
	for _, d := range []time.Time{
		time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC),
		time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC),
		time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC),
		time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC),
	} {
		h.add(sample{T: d.Unix(), C: map[string]int64{"relay_bytes": 5}})
	}
	days, totals := h.dayList()
	if !reflect.DeepEqual(days, []string{"2026-10-15", "2026-11-02", "2026-11-03"}) || totals["2026-11-03"]["relay_bytes"] != 5 {
		t.Fatalf("days %v %v", days, totals)
	}
}

func TestPanelSampleCountsDeltas(t *testing.T) {
	p := newTestPanel(t, PanelConfig{})
	p.srv.stats.add(stLookups)
	p.srv.stats.add(stLookups)
	now := time.Now()
	p.sample(now)
	p.srv.stats.add(stLookups)
	p.sample(now.Add(time.Minute))
	_, fine := p.hist.series("24h")
	if len(fine) != 2 || fine[0].C["lookups"] != 2 || fine[1].C["lookups"] != 1 {
		t.Fatalf("samples %+v", fine)
	}
	// Today is what was sampled plus what was not yet.
	p.srv.stats.add(stLookups)
	if n := p.today(now)["lookups"]; n != 4 {
		t.Fatalf("today: %d lookups, want 4", n)
	}
}

func TestLogRingScrubs(t *testing.T) {
	var out bytes.Buffer
	logs := newLogRing()
	log := slog.New(logs.handler(slog.NewTextHandler(&out, nil))).With("component", "relay")
	log.Info("host online", "id", "412345678", "addr", netip.MustParseAddrPort("203.0.113.5:4000"), "new", true)
	log.Warn("relay: refused a session, every port is in use", "client", "198.51.100.3:9")
	log.Error("cannot open a socket", "err", "listen udp [2001:db8::1]:40000: address already in use; asked by 512345678",
		"count", 123456789)
	log.WithGroup("acct").Info("login", "email", "someone@example.com", "detail", "mail to other@example.com failed")
	log.Debug("introduced", "id", "412345678") // below info: not kept
	warn, info := logs.records()
	if len(warn) != 2 || len(info) != 2 {
		t.Fatalf("%d warnings, %d info", len(warn), len(info))
	}
	all := ""
	for _, r := range append(warn, info...) {
		all += r.Msg + " "
		for _, a := range r.Attrs {
			all += a.K + "=" + a.V + " "
		}
	}
	for _, leak := range []string{"412345678", "203.0.113.5", "198.51.100.3", "2001:db8::1", "512345678", "someone@example.com", "other@example.com"} {
		if strings.Contains(all, leak) {
			t.Errorf("the ring keeps %q: %s", leak, all)
		}
	}
	for _, keep := range []string{"host online", "component=relay", "new=true", "count=123456789", "address already in use", "acct.detail="} {
		if !strings.Contains(all, keep) {
			t.Errorf("the ring lost %q: %s", keep, all)
		}
	}
	if warn[1].Level != "error" || warn[0].Level != "warn" {
		t.Errorf("levels %s %s", warn[0].Level, warn[1].Level)
	}
	// The real log is untouched.
	if !strings.Contains(out.String(), "203.0.113.5") {
		t.Error("the wrapped handler did not get the record")
	}
	for range logKeep + 10 {
		log.Warn("again")
	}
	if warn, _ := logs.records(); len(warn) != logKeep {
		t.Errorf("%d warnings kept, want %d", len(warn), logKeep)
	}
}
