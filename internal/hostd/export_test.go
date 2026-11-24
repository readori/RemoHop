package hostd

// Test hooks for the external test package.

// SilenceNotifications stops approval requests from showing desktop
// notifications on the machine running the tests.
func SilenceNotifications() { notifyDesktop = nil }

// EnforceGrants runs the periodic grant check now.
func (h *Host) EnforceGrants() int { return h.enforceGrants() }
