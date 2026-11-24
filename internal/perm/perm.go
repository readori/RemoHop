// Package perm is the table that says which permission feature each host
// service belongs to. A host owner switches features on and off
// (`remohop config feature NAME on|off`); the host daemon refuses streams for
// services whose feature is off, and for services that are in no feature.
//
// It is a leaf package so that service packages can register themselves
// from init() without importing the host daemon.
package perm

import (
	"errors"
	"sort"
	"sync"
)

// Feature names a group of services a host owner allows or forbids as one.
type Feature string

const (
	Shell   Feature = "shell"   // run commands: shell, exec, spawn, jobs
	File    Feature = "file"    // read and write files: fs, transfers, mounts
	Forward Feature = "forward" // port forwarding in both directions
	Clip    Feature = "clip"    // clipboard
	Screen  Feature = "screen"  // see the screen: screenshots, desktop viewing
	Input   Feature = "input"   // control keyboard and mouse
	Info    Feature = "info"    // system information
	VPN     Feature = "vpn"     // IP tunnel to this machine and its networks
	Serial  Feature = "serial"  // serial ports of this machine
	USB     Feature = "usb"     // export USB devices of this machine
	// Always is for services every authenticated client may use (ping,
	// pairing). It cannot be switched off.
	Always Feature = "always"
)

var (
	mu       sync.RWMutex
	services = map[string]Feature{
		"shell": Shell, "exec": Shell, "spawn": Shell, "jobs": Shell,
		"fs": File, "fs.get": File, "fs.put": File,
		"forward": Forward, "forward.listen": Forward,
		"clip":       Clip,
		"screenshot": Screen, "desktop": Screen,
		"input":   Input,
		"sysinfo": Info,
		"ping":    Always, "pair": Always,
	}
)

// Features lists the features a host owner can switch, in display order.
func Features() []Feature {
	return []Feature{Shell, File, Forward, Clip, Screen, Input, Info, VPN, Serial, USB}
}

// Valid reports whether f is a switchable feature.
func Valid(f Feature) bool {
	for _, k := range Features() {
		if k == f {
			return true
		}
	}
	return false
}

// Register assigns a service to a feature. Service packages call it from
// init() for services not in the built-in table; a service that is never
// registered is refused by hosts. It panics on an unknown feature or on an
// attempt to move a service to a different feature, both programming errors.
func Register(service string, f Feature) {
	if f != Always && !Valid(f) {
		panic("perm: unknown feature " + string(f))
	}
	mu.Lock()
	defer mu.Unlock()
	if old, ok := services[service]; ok && old != f {
		panic("perm: service " + service + " is already in feature " + string(old))
	}
	services[service] = f
}

// Of returns the feature a service belongs to.
func Of(service string) (Feature, bool) {
	mu.RLock()
	defer mu.RUnlock()
	f, ok := services[service]
	return f, ok
}

// Services lists the services of a feature, sorted.
func Services(f Feature) []string {
	mu.RLock()
	defer mu.RUnlock()
	var out []string
	for s, sf := range services {
		if sf == f {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// WithdrawnError says that the host's owner switched Feature off (or a
// grant stopped allowing it) while a stream of it was open. The host
// daemon cancels the context of such a stream's handler with it as the
// cause, so a service can tell the client why it ends: see Withdrawn.
type WithdrawnError struct{ Feature Feature }

func (e *WithdrawnError) Error() string {
	return "the host's owner switched feature \"" + string(e.Feature) + "\" off for this session"
}

// Withdrawn reports whether err, typically context.Cause of a handler's
// context, is a WithdrawnError, and returns it.
func Withdrawn(err error) (*WithdrawnError, bool) {
	var w *WithdrawnError
	if errors.As(err, &w) {
		return w, true
	}
	return nil, false
}
