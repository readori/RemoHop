// Package wintest has the helpers of RemoHop's Windows end-to-end tests (the
// REMOHOP_INPUT_E2E and REMOHOP_DESKTOP_E2E tests): a real top-level window to
// capture or type into, started in a helper PowerShell process so that
// everything the tests see goes through the real window system, and
// diagnostics about the desktop the test runs on. It is only built on
// Windows and only imported by tests.
package wintest
