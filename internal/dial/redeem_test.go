package dial

import (
	"bytes"
	"strings"
	"testing"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/grant"
)

// TestRedeemKeepsPairing: `peers redeem` does not silently replace a full
// pairing with a grant's narrower rights; --replace does, and a grant
// replaces an earlier grant. The grant shows in `peers`.
func TestRedeemKeepsPairing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("REMOHOP_CONFIG_DIR", dir)
	store := mustStore(t, dir)
	const target = "123456789"
	if err := store.Update(target, func(p *Peer) error {
		p.TokenID, p.Token = "pairing", []byte("full pairing token")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	code := grant.EncodeCode("0a1b", bytes.Repeat([]byte{7}, 32))
	run := func(args ...string) (int, string, string) {
		env, out, errb := cliEnv("", "", Options{})
		c := cli.Lookup("peers").Run(testCtx(t), env, args)
		return c, out.String(), errb.String()
	}
	c, _, errOut := run("redeem", target, code)
	if c != cli.ExitError || !strings.Contains(errOut, "full pairing") || !strings.Contains(errOut, "REMOHOP_CONFIG_DIR=~/.config/remohop-agent") ||
		!strings.Contains(errOut, "--replace") {
		t.Fatalf("redeem over a pairing: %d %q", c, errOut)
	}
	if p, _, _ := store.Get(target); p.TokenID != "pairing" || p.Grant {
		t.Fatalf("the pairing was changed: %+v", p)
	}
	if c, _, errOut := run("redeem", target, code, "--replace"); c != cli.ExitOK {
		t.Fatalf("redeem --replace: %d %q", c, errOut)
	}
	if p, _, _ := store.Get(target); p.TokenID != "0a1b" || !p.Grant {
		t.Fatalf("after --replace: %+v", p)
	}
	other := grant.EncodeCode("0c0d", bytes.Repeat([]byte{8}, 32))
	if c, _, errOut := run("redeem", target, other); c != cli.ExitOK {
		t.Fatalf("a grant over a grant: %d %q", c, errOut)
	}
	if c, out, _ := run(); c != cli.ExitOK || !strings.Contains(out, "GRANT") || !strings.Contains(out, "yes") {
		t.Fatalf("peers: %d\n%s", c, out)
	}
	if c, out, _ := run("--json"); c != cli.ExitOK || !strings.Contains(out, `"grant": true`) {
		t.Fatalf("peers --json: %d\n%s", c, out)
	}
}
