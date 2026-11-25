package desktop

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/peer/peertest"
)

// TestWinDesktopE2ELockScreen locks the workstation during a session (the
// viewer's Lock button) and checks that the session survives the lock
// screen, saying why there is no picture, instead of ending with "screen
// capture/encoding failed". It cannot unlock again, so it runs only on CI
// runners (GITHUB_ACTIONS) or with REMOHOP_LOCK_E2E=1, after the other
// desktop tests.
func TestWinDesktopE2ELockScreen(t *testing.T) {
	winDesktopE2E(t)
	if os.Getenv("GITHUB_ACTIONS") != "true" && os.Getenv("REMOHOP_LOCK_E2E") != "1" {
		t.Skip("locks this workstation: set REMOHOP_LOCK_E2E=1 (CI runners run it)")
	}
	client, _ := peertest.Pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	sess, err := Open(ctx, client, Options{FPS: 15, Codec: codecH264})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	h := sess.Hello()
	frames := 0
	for frames < 10 {
		ev, err := sess.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if ev.Frame != nil {
			frames++
		}
	}
	t.Logf("before locking: encoder %s, secure desktop %q", h.Encoder, secureDesktop())
	lctx, lcancel := context.WithTimeout(ctx, 20*time.Second)
	err = sess.Lock(lctx)
	lcancel()
	if err != nil {
		t.Skipf("RESULT RUNNER LIMIT: cannot lock the workstation here: %v", err)
	}
	var paused string
	var after int
	end := time.Now().Add(25 * time.Second)
	for time.Now().Before(end) {
		ev, err := sess.Next(ctx)
		if err != nil {
			t.Fatalf("RESULT the session ended on the lock screen: %v (secure desktop %q)", err, secureDesktop())
		}
		if ev.Stats != nil && ev.Stats.Paused != "" {
			paused = ev.Stats.Paused
		}
		if ev.Frame != nil {
			after++
		}
	}
	t.Logf("RESULT lock screen: session alive after 25 s, secure desktop %q, paused %q, %d frames after locking", secureDesktop(), paused, after)
	if paused == "" && after < 10 {
		t.Errorf("on the lock screen the session neither streams nor says why")
	}
}
