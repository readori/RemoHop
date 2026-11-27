package vpn

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/config"
)

// A tunnel changes things outside this process: firewall rules, forwarding
// switches, routes that do not belong to the tunnel interface. Every such
// change is written to a state file together with the command that undoes
// it, before it is made. Undoing happens when the tunnel ends in any
// orderly way; if the process is killed instead, the file survives, and
// the next remohop that starts with enough privilege finds it, sees that its
// owner is gone and runs the undo steps then. Things that vanish with the
// interface itself (its addresses, the routes through it) are not
// recorded: the kernel removes the interface when its creator dies.
type state struct {
	mu   sync.Mutex
	path string

	PID   int        `json:"pid"`
	Role  string     `json:"role"` // "host", "client" or "host-shared"
	Dev   string     `json:"dev,omitempty"`
	Steps []undoStep `json:"undo"`
}

// undoStep reverses one change: a command to run, or a file under
// /proc/sys to set back.
type undoStep struct {
	Cmd    []string `json:"cmd,omitempty"`
	Sysctl string   `json:"sysctl,omitempty"`
	Value  string   `json:"value,omitempty"`
	// Remove is a file to delete; only those in removableFiles are.
	Remove string `json:"remove,omitempty"`
	// NRPT is the comment of Windows name resolution policy rules to
	// remove (validNRPTComment).
	NRPT string `json:"nrpt,omitempty"`
}

// stateDir is where state files live: commands a later root process may
// run, so the directory is private to this user on every OS.
func stateDir() (string, error) {
	d, err := config.SubDir("vpn")
	if err != nil {
		return "", err
	}
	if err := config.MakePrivate(d); err != nil {
		return "", err
	}
	return d, nil
}

func newState(role, name string) (*state, error) {
	dir, err := stateDir()
	if err != nil {
		return nil, err
	}
	s := &state{path: filepath.Join(dir, fmt.Sprintf("%s-%d-%s.json", role, os.Getpid(), name)), PID: os.Getpid(), Role: role, Dev: name}
	return s, s.save()
}

func (s *state) save() error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return config.WriteFileAtomic(s.path, b, 0o600)
}

// record notes how to undo a change that is about to be made.
func (s *state) record(step undoStep) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Steps = append(s.Steps, step)
	return s.save()
}

// undo reverses every recorded change, newest first, and removes the file.
// Steps that fail (the rule is already gone, say) are skipped: undo must
// be safe to run twice.
func (s *state) undo() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.Steps) - 1; i >= 0; i-- {
		st := s.Steps[i]
		switch {
		case len(st.Cmd) > 0 && allowedUndo(st.Cmd[0]):
			run(st.Cmd...)
		case st.Sysctl != "" && strings.HasPrefix(filepath.Clean(st.Sysctl), "/proc/sys/net/"):
			os.WriteFile(st.Sysctl, []byte(st.Value), 0o644)
		case st.Remove != "" && removableFiles[st.Remove]:
			os.Remove(st.Remove)
		case st.NRPT != "" && validNRPTComment(st.NRPT):
			run(NRPTRemoveCmd(st.NRPT)...)
		}
	}
	s.Steps = nil
	os.Remove(s.path)
}

// allowedUndo limits what a state file can make a later (root) process
// run to the tools this package itself uses. The file lives in a directory
// only its owner can write, so this is belt and braces.
func allowedUndo(tool string) bool {
	switch tool {
	case "ip", "iptables", "nft", "route", "resolvectl", "netsh", "pnputil":
		return true
	}
	return false
}

// Recover undoes what tunnels of dead processes left behind and reports
// how many state files it handled. It needs the privilege the tunnel had;
// without it nothing is touched and stale is the number still waiting.
func Recover() (cleaned, stale int) {
	dir, err := stateDir()
	if err != nil {
		return 0, 0
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		s := &state{}
		if json.Unmarshal(b, s) != nil || s.PID <= 0 {
			os.Remove(p)
			continue
		}
		if s.PID == os.Getpid() || processAlive(s.PID) {
			continue
		}
		if sys.privileged() != nil {
			stale++
			continue
		}
		s.path = p
		s.undo()
		cleaned++
	}
	return cleaned, stale
}

func init() {
	// The promised clean-up after a crash, on the next start of any remohop
	// command. It costs one directory listing when there is nothing to do.
	if d, err := configDirNoCreate(); err == nil {
		if ents, err := os.ReadDir(filepath.Join(d, "vpn")); err == nil && len(ents) > 0 {
			Recover()
		}
	}
}

// configDirNoCreate locates the config directory without creating it, so
// that merely starting remohop leaves no trace.
func configDirNoCreate() (string, error) {
	if d := os.Getenv("REMOHOP_CONFIG_DIR"); d != "" {
		return d, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "remohop"), nil
}

// run executes a system tool and returns its combined output on failure.
func run(argv ...string) error {
	_, err := output(argv...)
	return err
}

// commandTimeout bounds a system tool. PowerShell gets longer: its first
// start on a machine, loading the DnsClient module for the NRPT cmdlets,
// took more than 10 seconds on a CI runner.
func commandTimeout(tool string) time.Duration {
	if i := strings.LastIndexAny(tool, `/\`); i >= 0 {
		tool = tool[i+1:]
	}
	if strings.EqualFold(tool, "powershell.exe") {
		return 60 * time.Second
	}
	return 10 * time.Second
}

func output(argv ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout(argv[0]))
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	out, err := runPrivileged(cmd)
	if err != nil {
		return string(out), fmt.Errorf("%s: %w: %s", strings.Join(argv, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
