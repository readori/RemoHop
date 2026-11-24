// Package config locates RemoHop's on-disk state.
package config

import (
	"os"
	"path/filepath"
)

// Dir is where configuration and credentials live: $REMOHOP_CONFIG_DIR, or
// "remohop" under the user's config directory (~/.config/remohop on Linux).
// The directory is created with mode 0700 because it holds key material.
func Dir() (string, error) {
	d := os.Getenv("REMOHOP_CONFIG_DIR")
	if d == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		d = filepath.Join(base, "remohop")
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		return "", err
	}
	// Mode bits mean nothing on Windows; there the directory's access list
	// is what keeps keys and tokens private. Best effort: loading an
	// identity re-checks and refuses to create a key in a readable place.
	MakePrivate(d)
	return d, nil
}

// SubDir returns (and creates, mode 0700) a named directory under Dir, for
// feature state such as clipboard history or spawn logs.
func SubDir(name string) (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	p := filepath.Join(d, name)
	if err := os.MkdirAll(p, 0o700); err != nil {
		return "", err
	}
	return p, nil
}
