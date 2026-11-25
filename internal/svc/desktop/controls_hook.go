package desktop

import "time"

// adjustControls, when set, revises the controls this host cannot offer
// (platformUnsupported plus reasons) at the time a session asks, for hosts
// whose abilities depend on how they run: the Windows service with
// unattended access can send Ctrl+Alt+Del.
var adjustControls func(unsupported map[string]string)

// screenChanged, when set, reports whether the captured screen's size in
// pixels is no longer the one the session captures (a resolution change),
// so that the session restarts and captures what is there now. The run
// loop asks every screenCheckEvery. Platforms whose capture fails (and so
// restarts) on such a change leave it nil.
var screenChanged func(spec captureSpec) bool

const screenCheckEvery = 2 * time.Second
