package hostd

// Test hooks for the external test package.

// SilenceNotifications stops approval requests from showing desktop
// notifications on the machine running the tests.
func SilenceNotifications() { notifyDesktop = nil }

// EnforceGrants runs the periodic grant check now.
func (h *Host) EnforceGrants() int { return h.enforceGrants() }

// SetSessionBuiltHook makes f run in every new session between building its
// connection and serving it, and returns a function that removes it.
func SetSessionBuiltHook(f func()) (restore func()) {
	testHookSessionBuilt = f
	return func() { testHookSessionBuilt = nil }
}

// ApprovalAnswer applies a line typed at the host's terminal (cmd.go).
func ApprovalAnswer(h *Host, text string, shown int) string { return approvalAnswer(h, text, shown) }
