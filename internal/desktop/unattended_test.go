package desktop

import (
	"context"
	"runtime"
	"testing"

	"github.com/readori/RemoHop/internal/config"
	"github.com/readori/RemoHop/internal/hostd"
)

// On macOS and Linux the unattended password is in the user's own
// settings, and "Show" reads it from there without asking anyone.
func TestUnattendedReveal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("asks for an administrator on Windows")
	}
	app, _ := startApp(t, "")
	if pw, err := app.UnattendedReveal(context.Background()); err != nil || pw != "" {
		t.Fatalf("before a password is set: %q, %v", pw, err)
	}
	if err := config.Set(hostd.SettingPassword, "Strong-Pass-2026!"); err != nil {
		t.Fatal(err)
	}
	if pw, err := app.UnattendedReveal(context.Background()); err != nil || pw != "Strong-Pass-2026!" {
		t.Fatalf("revealed %q, %v", pw, err)
	}
}
