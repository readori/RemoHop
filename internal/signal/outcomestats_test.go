package signal

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

func fixedNow(t *time.Time) func() time.Time { return func() time.Time { return *t } }

func TestOutcomeStatsBuckets(t *testing.T) {
	now := time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC)
	o, _ := openOutcomeStats("", fixedNow(&now), nil)
	for _, ms := range []int64{0, 249, 250, 499, 500, 999, 1000, 1999, 2000, 3999, 4000, 7999, 8000, 99999} {
		o.add(Outcome{Path: PathPunched, MS: ms})
	}
	o.add(Outcome{Path: PathRelayed, MS: 10})
	for _, ms := range []int64{9999, 10000, 29999, 30000, 59999, 60000, 299999, 300000} {
		o.add(Outcome{Path: PathUpgraded, MS: ms})
	}
	s := o.snapshot()
	if s.Paths[PathPunched] != 14 || s.Paths[PathRelayed] != 1 || s.Paths[PathUpgraded] != 8 {
		t.Fatalf("paths: %v", s.Paths)
	}
	want := []int64{2, 2, 2, 2, 2, 2, 2}
	for i, w := range want {
		if s.MS[PathPunched][i] != w {
			t.Fatalf("punched buckets: %v", s.MS[PathPunched])
		}
	}
	for i, w := range []int64{1, 2, 2, 2, 1} {
		if s.MS[PathUpgraded][i] != w {
			t.Fatalf("upgraded buckets: %v", s.MS[PathUpgraded])
		}
	}
}

func TestOutcomeStatsPairs(t *testing.T) {
	o, _ := openOutcomeStats("", nil, nil)
	o.add(Outcome{Path: PathPunched, NAT: "cone", PeerNAT: "symmetric"})
	o.add(Outcome{Path: PathRelayed, NAT: "cone", PeerNAT: "symmetric"})
	o.add(Outcome{Path: PathFailed})
	s := o.snapshot()
	if s.Pairs["cone/symmetric"][PathPunched] != 1 || s.Pairs["cone/symmetric"][PathRelayed] != 1 || s.Pairs["unknown/unknown"][PathFailed] != 1 {
		t.Fatalf("pairs: %v", s.Pairs)
	}
}

func TestOutcomeStatsRollover(t *testing.T) {
	now := time.Date(2026, 3, 31, 23, 0, 0, 0, time.UTC)
	o, _ := openOutcomeStats("", fixedNow(&now), nil)
	o.add(Outcome{Path: PathDirect, MS: 5})
	now = now.Add(2 * time.Hour)
	o.add(Outcome{Path: PathRelayed, MS: 5})
	s := o.snapshot()
	if s.Month != "2026-04" || s.Paths[PathDirect] != 0 || s.Paths[PathRelayed] != 1 {
		t.Fatalf("after roll-over: %+v", s)
	}
}

func TestOutcomeStatsSaveReopen(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC)
	o, err := openOutcomeStats(dir, fixedNow(&now), nil)
	if err != nil {
		t.Fatal(err)
	}
	o.add(Outcome{Path: PathPunched, MS: 700, NAT: "cone", PeerNAT: "cone"})
	if err := o.save(); err != nil {
		t.Fatal(err)
	}
	o2, err := openOutcomeStats(dir, fixedNow(&now), nil)
	if err != nil {
		t.Fatal(err)
	}
	s := o2.snapshot()
	if s.Paths[PathPunched] != 1 || s.MS[PathPunched][2] != 1 || s.Pairs["cone/cone"][PathPunched] != 1 {
		t.Fatalf("reopened: %+v", s)
	}
	// A new month ignores the old file.
	now = now.AddDate(0, 1, 0)
	o3, _ := openOutcomeStats(dir, fixedNow(&now), nil)
	if o3.snapshot().total() != 0 {
		t.Fatal("old month loaded")
	}
}

func TestOutcomeStatsConcurrent(t *testing.T) {
	o, _ := openOutcomeStats(t.TempDir(), nil, nil)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 100 {
				o.add(Outcome{Path: PathPunched, MS: 300})
				o.save()
				o.snapshot()
			}
		})
	}
	wg.Wait()
	if n := o.snapshot().Paths[PathPunched]; n != 800 {
		t.Fatalf("count %d", n)
	}
}

func TestPrintOutcomeStatus(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC)
	var b bytes.Buffer
	if err := printOutcomeStatus(&b, dir, now); err != nil || !strings.Contains(b.String(), "no outcomes.json") {
		t.Fatalf("no file: %v %q", err, b.String())
	}
	o, _ := openOutcomeStats(dir, fixedNow(&now), nil)
	for range 3 {
		o.add(Outcome{Path: PathPunched, MS: 300, NAT: "cone", PeerNAT: "cone"})
	}
	o.add(Outcome{Path: PathRelayed, MS: 3000, NAT: "cone", PeerNAT: "cone"})
	o.add(Outcome{Path: PathUpgraded, MS: 20000})
	o.save()
	b.Reset()
	if err := printOutcomeStatus(&b, dir, now); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, w := range []string{"connection outcomes 2026-03 (UTC): 5 reports", "punched", "60.0%", "<500ms 100%", "upgraded to direct: 1", "cone/cone"} {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q in:\n%s", w, out)
		}
	}
	t.Log("\n" + out)
}

func TestOutcomeEndToEnd(t *testing.T) {
	tn := newTestNet(t, Limits{})
	ctx := testCtx(t)
	c := tn.dial(tn.socket(tn.net.Host("192.0.2.9")), Options{})
	for _, o := range []Outcome{
		{Path: PathPunched, MS: 900, NAT: "cone", PeerNAT: "cone"},
		{Path: PathPunched, MS: 1500, NAT: "cone", PeerNAT: "cone"},
		{Path: PathFailed, MS: 8000},
	} {
		if err := c.ReportOutcome(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	s := tn.srv.outcomes.snapshot()
	if s.Paths[PathPunched] != 2 || s.Paths[PathFailed] != 1 || s.Pairs["cone/cone"][PathPunched] != 2 {
		t.Fatalf("aggregate: %+v", s)
	}
}
