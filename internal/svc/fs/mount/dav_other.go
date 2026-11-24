//go:build !linux && !darwin && !windows

package mount

import (
	"errors"
	"runtime"
)

var errNoDavClient = errors.New("no WebDAV client is known for " + runtime.GOOS + ": use `-o nomount` and point a WebDAV client at the printed URL")

func davTarget(local string) (string, error)                        { return local, nil }
func davMounted(string) bool                                        { return false }
func davMount(string, int, string, string, Options) (string, error) { return "", errNoDavClient }
func davUnmount(string, bool) error                                 { return nil }
