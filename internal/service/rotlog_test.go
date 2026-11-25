package service

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestRotatingLog(t *testing.T) {
	p := filepath.Join(t.TempDir(), "service.log")
	os.WriteFile(p, bytes.Repeat([]byte("x"), 150), 0o600)
	l, err := openRotatingLog(p, 100)
	if err != nil {
		t.Fatal(err)
	}
	// Too big at open: moved aside.
	if b, _ := os.ReadFile(p + ".1"); len(b) != 150 {
		t.Fatalf("not rotated at open: %d bytes in .1", len(b))
	}
	line := bytes.Repeat([]byte("a"), 39)
	line = append(line, '\n')
	for i := 0; i < 3; i++ {
		if _, err := l.Write(line); err != nil {
			t.Fatal(err)
		}
	}
	// 120 bytes would not fit in 100: the third line starts a new file.
	if b, _ := os.ReadFile(p); len(b) != 40 {
		t.Errorf("current log has %d bytes, want 40", len(b))
	}
	if b, _ := os.ReadFile(p + ".1"); len(b) != 80 {
		t.Errorf("rotated log has %d bytes, want 80", len(b))
	}
	l.Close()
	if _, err := l.Write(line); err == nil {
		t.Error("write after close")
	}
}
