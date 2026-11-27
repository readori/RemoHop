package dial

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/grant"
)

// ErrWouldReplacePairing is returned by Redeem when target has a full
// pairing saved, which the grant would replace with narrower rights.
var ErrWouldReplacePairing = errors.New("a full pairing is saved for this host")

// Redeem stores a grant code (from `remohop host grant` on the host) as the
// credential for target, replacing any saved pairing. The host binds the
// code to this machine's identity at the first login.
func Redeem(store *Store, target, code, alias string) error {
	return RedeemKeeping(store, target, code, alias, true)
}

// RedeemKeeping is Redeem that replaces a full pairing saved for target
// only if replace is set; otherwise it fails with ErrWouldReplacePairing
// and changes nothing. A grant saved earlier is always replaced.
func RedeemKeeping(store *Store, target, code, alias string, replace bool) error {
	id, secret, err := grant.ParseCode(code)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := store.Update(target, func(p *Peer) error {
		if p.Paired() && !p.Grant && !replace {
			return ErrWouldReplacePairing
		}
		p.TokenID, p.Token, p.PairedAt, p.Grant = id, secret, &now, true
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
	fs := cli.NewFlagSet(env, "peers redeem", "")
	alias := fs.String("alias", "", "")
	replace := fs.Bool("replace", false, "")
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
		env.Errorf("usage: remohop peers redeem TARGET GRANT-CODE [--alias NAME] [--replace]")
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
	if err := RedeemKeeping(store, target, pos[1], *alias, *replace); errors.Is(err, ErrWouldReplacePairing) {
		env.Errorf("peers redeem: %s already has a full pairing on this machine, and the grant would replace it with "+
			"the grant's narrower rights. To keep both, redeem the grant in a separate config directory for the agent:\n"+
			"  REMOHOP_CONFIG_DIR=~/.config/remohop-agent remohop peers redeem %s CODE\n"+
			"and start its `remohop mcp` with the same REMOHOP_CONFIG_DIR. To replace the pairing anyway, add --replace.",
			displayTarget(target), pos[0])
		return cli.ExitError
	} else if err != nil {
		env.Errorf("peers redeem: %v", err)
		return cli.ExitUsage
	}
	fmt.Fprintf(env.Stdout, "saved the grant for %s; the first login binds it to this machine.\n"+
		"Check what it allows with `remohop %s limits` (an agent: the remohop_limits tool).\n", displayTarget(target), target)
	return cli.ExitOK
}
