package bg

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/readori/RemoHop/internal/config"
)

func TestReadyListStop(t *testing.T) {
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	if infos, err := List(); err != nil || len(infos) != 0 {
		t.Fatalf("empty list: %v %v", infos, err)
	}
	done := Ready(Info{Kind: "forward", Target: "123456789", Detail: "x"}, "ready")
	infos, err := List()
	if err != nil || len(infos) != 1 {
		t.Fatalf("list: %v %v", infos, err)
	}
	if infos[0].PID != os.Getpid() || infos[0].Kind != "forward" {
		t.Fatalf("info: %+v", infos[0])
	}
	if err := Stop(1 << 30); err != ErrNotFound {
		t.Fatalf("stop unknown: %v", err)
	}
	done()
	if infos, _ := List(); len(infos) != 0 {
		t.Fatalf("after done: %v", infos)
	}
}

// Other commands keep sidecar files in the run directory; they must not be
// mistaken for records (remohop stop --all would stop a process twice).
func TestListIgnoresSidecars(t *testing.T) {
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	done := Ready(Info{Kind: "forward", Target: "t", Detail: "d"}, "ready")
	defer done()
	dir, err := config.SubDir("run")
	if err != nil {
		t.Fatal(err)
	}
	side := fmt.Sprintf(`{"pid":%d,"kind":"forward"}`, os.Getpid())
	if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("forward-%d.json", os.Getpid())), []byte(side), 0o600); err != nil {
		t.Fatal(err)
	}
	infos, err := List()
	if err != nil || len(infos) != 1 {
		t.Fatalf("want exactly the one record, got %+v %v", infos, err)
	}
}
