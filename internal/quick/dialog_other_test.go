//go:build !windows && !darwin

package quick

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeTools puts scripts named after dialog tools first on PATH; each
// records its arguments and exits with the given status.
func fakeTools(t *testing.T, tools map[string]int) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	for name, code := range tools {
		script := "#!/bin/sh\necho " + name + " \"$@\" >> " + log + "\nexit " + strconv.Itoa(code) + "\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	t.Setenv("DISPLAY", ":99")
	return log
}

// TestNativeAskFallbacks: without zenity (KDE, XFCE), the request is still
// asked in a dialog: kdialog, else xmessage, with Deny as the safe answer.
func TestNativeAskFallbacks(t *testing.T) {
	r := Request{Name: "helper", Identity: "SHA256:abc", From: "192.0.2.1"}
	ctx := context.Background()

	log := fakeTools(t, map[string]int{"kdialog": 0})
	if allow, ok := nativeAsk(ctx, "en", r, 5*time.Second); !allow || !ok {
		t.Fatalf("kdialog Allow: %v %v", allow, ok)
	}
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), "--warningyesno") || !strings.Contains(string(b), "Allow") {
		t.Fatalf("kdialog call: %s", b)
	}

	fakeTools(t, map[string]int{"kdialog": 1})
	if allow, ok := nativeAsk(ctx, "en", r, 5*time.Second); allow || !ok {
		t.Fatalf("kdialog Deny: %v %v", allow, ok)
	}

	log = fakeTools(t, map[string]int{"xmessage": 11})
	if allow, ok := nativeAsk(ctx, "en", r, 5*time.Second); !allow || !ok {
		t.Fatalf("xmessage Allow: %v %v", allow, ok)
	}
	b, _ = os.ReadFile(log)
	if !strings.Contains(string(b), "-default Deny") {
		t.Fatalf("xmessage call: %s", b)
	}
	fakeTools(t, map[string]int{"xmessage": 0}) // timed out
	if _, ok := nativeAsk(ctx, "en", r, 5*time.Second); ok {
		t.Fatal("an xmessage timeout counted as an answer")
	}

	// A zenity that cannot open its window (any other status) leaves it to the next tool.
	fakeTools(t, map[string]int{"zenity": 99, "xmessage": 10})
	if allow, ok := nativeAsk(ctx, "en", r, 5*time.Second); allow || !ok {
		t.Fatalf("zenity failing, xmessage Deny: %v %v", allow, ok)
	}

	// Nothing at all: no answer, and a notification points at the page.
	log = fakeTools(t, map[string]int{"notify-send": 0})
	if _, ok := nativeAsk(ctx, "en", r, 5*time.Second); ok {
		t.Fatal("answered with no dialog tool")
	}
	if b, _ := os.ReadFile(log); !strings.Contains(string(b), "notify-send") {
		t.Fatal("no notification")
	}
}
