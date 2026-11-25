//go:build !linux && !windows && !darwin

package webview

import "fmt"

func probe() (Info, error) { return Info{}, fmt.Errorf("%w (unsupported platform)", ErrUnavailable) }

func run(Config, func(App)) error { _, err := probe(); return err }
