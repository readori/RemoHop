package dial

import (
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/grant"
)

// Redeem stores a grant code (from `remohop host grant` on the host) as the
// credential for target, replacing any saved pairing. The host binds the
// code to this machine's identity at the first login.
func Redeem(store *Store, target, code, alias string) error {
	id, secret, err := grant.ParseCode(code)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := store.Update(target, func(p *Peer) error {
		p.TokenID, p.Token, p.PairedAt = id, secret, &now
		return nil
	}); err != nil {
		return err
	}
	if alias != "" {
		return store.SetAlias(target, alias)
	}
	return nil
}

func runPeersRedeem(env *cli.Env, store *Store, args []string) int {
	fs := flag.NewFlagSet("peers redeem", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	alias := fs.String("alias", "", "")
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return cli.ExitUsage
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
	if len(pos) != 2 {
		env.Errorf("usage: remohop peers redeem TARGET GRANT-CODE [--alias NAME]")
		return cli.ExitUsage
	}
	target := cli.NormalizeTarget(pos[0])
	if !cli.IsID(target) {
		if p, ok, _ := store.ByAlias(target); ok {
			target = p.Target
		} else if !strings.Contains(target, ":") {
			env.Errorf("peers: %q is not a 9-digit ID, a HOST:PORT or a saved alias", pos[0])
			return cli.ExitUsage
		}
	}
	if err := Redeem(store, target, pos[1], *alias); err != nil {
		env.Errorf("peers redeem: %v", err)
		return cli.ExitUsage
	}
	fmt.Fprintf(env.Stdout, "saved the grant for %s; the first login binds it to this machine.\n"+
		"Check what it allows with `remohop %s limits` (an agent: the remohop_limits tool).\n", displayTarget(target), target)
	return cli.ExitOK
}
