package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Many writers doing read-modify-write under Lock lose no update, whether
// they are goroutines here or (the same code path) separate processes.
func TestLockSerialisesWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "counter")
	writers, rounds := 16, 20
	if runtime.GOOS == "windows" {
		// Synced writes are slow on Windows runners: 320 of them in a row,
		// with readers forcing rename retries, can outlast one writer's
		// lockWait. Fewer still exercise the same contention.
		writers, rounds = 8, 10
	}
	var wg sync.WaitGroup
	errs := make(chan error, writers*rounds)
	for w := 0; w < writers; w++ {
		wg.Go(func() {
			for r := 0; r < rounds; r++ {
				unlock, err := Lock(path)
				if err != nil {
					errs <- err
					return
				}
				n := 0
				if b, err := os.ReadFile(path); err == nil {
					n, _ = strconv.Atoi(strings.TrimSpace(string(b)))
				}
				err = WriteFileAtomic(path, []byte(strconv.Itoa(n+1)), 0o600)
				unlock()
				if err != nil {
					errs <- err
					return
				}
			}
		})
	}
	// Readers that do not take the lock must never see a partial file, and
	// must not make a writer fail.
	stop := make(chan struct{})
	var readers sync.WaitGroup
	for i := 0; i < 4; i++ {
		readers.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				time.Sleep(time.Millisecond)
				if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
					if _, err := strconv.Atoi(string(b)); err != nil {
						errs <- err
						return
					}
				}
			}
		})
	}
	wg.Wait()
	close(stop)
	readers.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != strconv.Itoa(writers*rounds) {
		t.Fatalf("counter is %s after %d locked increments", b, writers*rounds)
	}
	// The lock can be taken again at once, and releasing twice is harmless.
	unlock, err := Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	unlock()
	if unlock, err = Lock(path); err != nil {
		t.Fatal(err)
	}
	unlock()
	// No temporary files are left behind.
	ents, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Fatalf("left behind %s", e.Name())
		}
	}
}

func TestMakePrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := MakePrivate(dir); err != nil {
		t.Fatalf("MakePrivate: %v", err)
	}
	// Files created afterwards, by any of the module's writers, are
	// private too.
	file := filepath.Join(dir, "secret")
	if err := WriteFileAtomic(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "sub")
	os.Mkdir(sub, 0o700)
	for _, p := range []string{dir, file, sub} {
		private, err := IsPrivate(p)
		if err != nil || !private {
			t.Fatalf("%s is not private (%v)", p, err)
		}
	}
	if _, err := IsPrivate(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("IsPrivate of a missing file reported no error")
	}
}
