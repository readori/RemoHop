package hostd

import (
	"errors"
	"os"
	"time"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/config"
	"github.com/readori/RemoHop/internal/svc/shell"
)

// Config keys for the device name and the shell keep-alive.
const (
	SettingName     = "name"
	SettingTermKeep = "term-keep"
)

func init() {
	config.RegisterSetting(config.Setting{
		Key:  SettingName,
		Help: "this device's name, shown to peers: in `ping`, in a host's approval prompts and `remohop status` (default: none)",
		Validate: func(v string) error {
			if c := auth.CleanText(v, auth.MaxTokenName); c != v {
				return errors.New("use at most 64 bytes of plain text, no control characters")
			}
			return nil
		},
	})
	config.RegisterSetting(config.Setting{
		Key:     SettingTermKeep,
		Default: "30m",
		Help:    "how long a host keeps a detached interactive shell before ending it, e.g. 2h (read when `remohop host` starts; REMOHOP_SHELL_KEEPALIVE wins)",
		Validate: func(v string) error {
			d, err := time.ParseDuration(v)
			if err != nil || d <= 0 || d > 30*24*time.Hour {
				return errors.New("a duration between 1s and 720h, like 30m or 2h")
			}
			return nil
		},
	})
}

// DeviceName is this machine's device name from the config file, cleaned.
func DeviceName() string {
	v, _ := config.Get(SettingName)
	return auth.CleanText(v, auth.MaxTokenName)
}

// applyTermKeep sets the shell keep-alive from the config file, unless the
// environment already chose one (the shell package reads it at start).
func applyTermKeep(values map[string]string) {
	if os.Getenv("REMOHOP_SHELL_KEEPALIVE") != "" {
		return
	}
	if d, err := time.ParseDuration(values[SettingTermKeep]); err == nil && d > 0 {
		shell.KeepAlive = d
	}
}
