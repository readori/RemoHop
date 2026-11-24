//go:build !windows

package bg

import (
	"os"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The test binary doubles as the background command: Start re-executes it,
// and the child lands here instead of running the tests.
func TestMain(m *testing.M) {
	if InChild() {
		switch os.Getenv("BG_TEST_MODE") {
		case "fail":
			os.Stderr.WriteString("could not bind port\n")
			os.Exit(3)
		case "hang":
			time.Sleep(time.Minute)
			os.Exit(0)
		}
		done := Ready(Info{Kind: "test", Target: "t", Detail: "d"}, "child ready")
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM)
		<-ch
		done()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestStartDetachesAndStops(t *testing.T) {
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	msg, pid, err := Start(10 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if msg != "child ready" {
		t.Fatalf("msg = %q", msg)
	}
	if pid <= 0 || pid == os.Getpid() || !alive(pid) {
		t.Fatalf("bad child pid %d", pid)
	}
	infos, err := List()
	if err != nil || len(infos) != 1 || infos[0].PID != pid || infos[0].Kind != "test" {
		t.Fatalf("list: %+v %v", infos, err)
	}
	if err := Stop(pid); err != nil {
		t.Fatal(err)
	}
	if infos, _ := List(); len(infos) != 0 {
		t.Fatalf("record left behind: %+v", infos)
	}
}

func TestStartReportsChildFailure(t *testing.T) {
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	t.Setenv("BG_TEST_MODE", "fail")
	_, _, err := Start(10 * time.Second)
	if err == nil || !strings.Contains(err.Error(), "could not bind port") {
		t.Fatalf("want the child's stderr in the error, got %v", err)
	}
}

func TestStartTimesOut(t *testing.T) {
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	t.Setenv("BG_TEST_MODE", "hang")
	_, _, err := Start(300 * time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("want timeout, got %v", err)
	}
}
