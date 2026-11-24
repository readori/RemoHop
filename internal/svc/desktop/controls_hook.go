package desktop

// adjustControls, when set, revises the controls this host cannot offer
// (platformUnsupported plus reasons) at the time a session asks, for hosts
// whose abilities depend on how they run: the Windows service with
// unattended access can send Ctrl+Alt+Del.
var adjustControls func(unsupported map[string]string)
