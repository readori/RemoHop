package update

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// VerifyBundle checks a downloaded RemoHop.app before it replaces the
// installed one: a valid, complete code signature (codesign --verify
// --deep --strict) by the project's Apple team. The SHA256SUMS signature
// already vouches for the archive; this keeps the bundle that macOS
// runs to what Gatekeeper would accept.
func VerifyBundle(app string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "/usr/bin/codesign", "--verify", "--deep", "--strict", app).CombinedOutput(); err != nil {
		return fmt.Errorf("code signature: %v: %s", err, strings.TrimSpace(string(out)))
	}
	out, err := exec.CommandContext(ctx, "/usr/bin/codesign", "-dv", "--verbose=2", app).CombinedOutput()
	if err != nil {
		return fmt.Errorf("code signature: %v", err)
	}
	if !strings.Contains(string(out), "\nTeamIdentifier="+AppleTeamID+"\n") {
		return fmt.Errorf("code signature: not signed by team %s", AppleTeamID)
	}
	return nil
}
