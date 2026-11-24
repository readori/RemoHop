package quick

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/perm"
	"github.com/readori/RemoHop/internal/wire"
)

// A long-lived stand-in service per feature: it answers "ok" and then
// holds the stream open until the client goes away, like a desktop
// stream, a terminal or a file transfer that does not check permissions
// again by itself.
var holdFeatures = []perm.Feature{perm.Screen, perm.Input, perm.File, perm.Clip, perm.Shell, perm.Forward, perm.VPN, perm.Serial, perm.USB}

func holdService(f perm.Feature) string { return "quicktest-hold-" + string(f) }

func init() {
	hold := func(_ context.Context, _ *peer.Conn, s peer.Stream, _ json.RawMessage) error {
		if err := wire.WriteJSON(s, "ok"); err != nil {
			return err
		}
		_, err := io.Copy(io.Discard, s)
		return err
	}
	for _, f := range holdFeatures {
		peer.Handle(holdService(f), hold)
		perm.Register(holdService(f), f)
	}
}

// holdOpen opens f's stand-in service; the returned channel is closed when
// the host ends the stream.
func holdOpen(t *testing.T, conn *peer.Conn, f perm.Feature) <-chan struct{} {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := conn.Open(ctx, holdService(f), nil)
	if err != nil {
		t.Fatalf("open %s: %v", f, err)
	}
	t.Cleanup(func() { s.Reset() })
	var v string
	if err := wire.ReadJSON(s, &v); err != nil {
		t.Fatalf("open %s: %v", f, err)
	}
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		io.Copy(io.Discard, s)
	}()
	return ended
}

// Each of the three switches ends exactly what it covers on a helper who
// is already connected, at once, and nothing else; the screen stays.
func TestOptionsEndOpenStreams(t *testing.T) {
	a, h := startApp(t, AppConfig{})
	if err := a.SetOption("advanced", true); err != nil {
		t.Fatal(err)
	}
	login := dialAsync(t, h, h.Password())
	st := waitState(t, a, "a request", func(s State) bool { return len(s.Requests) == 1 })
	a.Decide(st.Requests[0].N, true)
	res := result(t, login)
	if res.err != nil {
		t.Fatal(res.err)
	}
	open := map[perm.Feature]<-chan struct{}{}
	for _, f := range holdFeatures {
		open[f] = holdOpen(t, res.conn, f)
	}
	ended := func(f perm.Feature) bool {
		select {
		case <-open[f]:
			return true
		default:
			return false
		}
	}
	for _, step := range []struct {
		option string
		ends   []perm.Feature
	}{
		{"input", []perm.Feature{perm.Input}},
		{"files", []perm.Feature{perm.File, perm.Clip}},
		{"advanced", []perm.Feature{perm.Shell, perm.Forward, perm.VPN, perm.Serial, perm.USB}},
	} {
		if err := a.SetOption(step.option, false); err != nil {
			t.Fatal(err)
		}
		for _, f := range step.ends {
			select {
			case <-open[f]:
			case <-time.After(2 * time.Second):
				t.Fatalf("switching %q off left the open %s stream running", step.option, f)
			}
		}
		// Give anything wrongly affected the same time to end.
		time.Sleep(300 * time.Millisecond)
		var gone []perm.Feature
		for _, f := range holdFeatures {
			if ended(f) && !contains(endedBy(step.option), f) {
				gone = append(gone, f)
			}
		}
		if len(gone) > 0 {
			t.Fatalf("switching %q off also ended %v", step.option, gone)
		}
	}
	if ended(perm.Screen) {
		t.Fatal("the screen stream ended although screen sharing is never switched off")
	}
	if _, err := open2(res.conn, holdService(perm.Shell)); err == nil {
		t.Fatal("a new shell stream opened with the advanced option off")
	}
}

// endedBy is what must have ended once option (and the options before it
// in TestOptionsEndOpenStreams) are off.
func endedBy(option string) []perm.Feature {
	out := []perm.Feature{perm.Input}
	if option == "input" {
		return out
	}
	out = append(out, perm.File, perm.Clip)
	if option == "files" {
		return out
	}
	return append(out, perm.Shell, perm.Forward, perm.VPN, perm.Serial, perm.USB)
}

func contains(fs []perm.Feature, f perm.Feature) bool {
	for _, x := range fs {
		if x == f {
			return true
		}
	}
	return false
}

func open2(conn *peer.Conn, service string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := conn.Open(ctx, service, nil)
	if err != nil {
		return "", err
	}
	defer s.Close()
	var v string
	if err := wire.ReadJSON(s, &v); err != nil {
		return "", fmt.Errorf("read: %w", err)
	}
	return v, nil
}
