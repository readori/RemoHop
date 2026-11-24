package mesh

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/readori/RemoHop/internal/config"
)

// StatusFile is where the host's mesh reports its state, for `remohop net
// status` and `remohop net peers`.
const StatusFile = "net-status.json"

// Status is the mesh on this device.
type Status struct {
	PID     int       `json:"pid"`
	Up      bool      `json:"up"`
	Error   string    `json:"error,omitempty"`
	Updated time.Time `json:"updated"`

	Device   string `json:"device,omitempty"`
	Name     string `json:"name,omitempty"`
	FQDN     string `json:"fqdn,omitempty"`
	Identity string `json:"identity,omitempty"`
	Addr4    string `json:"addr4,omitempty"`
	Addr6    string `json:"addr6,omitempty"`
	Prefix4  string `json:"prefix4,omitempty"`
	Prefix6  string `json:"prefix6,omitempty"`
	DNS      string `json:"dns,omitempty"`

	Exit         bool     `json:"exit,omitempty"`
	Routes       []string `json:"routes,omitempty"`
	UseExit      string   `json:"use_exit,omitempty"`
	ExitActive   bool     `json:"exit_active,omitempty"`
	AcceptRoutes bool     `json:"accept_routes,omitempty"`
	Learned      []string `json:"learned_routes,omitempty"`

	// PolicyVersion is 0 and PolicyRules -1 without a policy (everything
	// allowed).
	PolicyVersion int64  `json:"policy_version"`
	PolicyRules   int    `json:"policy_rules"`
	PolicySigner  string `json:"policy_signer,omitempty"`

	Notes []string     `json:"notes,omitempty"`
	Peers []PeerStatus `json:"peers,omitempty"`
}

// PeerStatus is another device of the mesh.
type PeerStatus struct {
	Name     string `json:"name"`
	FQDN     string `json:"fqdn"`
	Identity string `json:"identity"`
	ID       string `json:"id,omitempty"`
	Addr4    string `json:"addr4"`
	Addr6    string `json:"addr6,omitempty"`
	Online   bool   `json:"online"`
	// Link is "up", "connecting" or empty (none: links come up when
	// traffic needs them).
	Link       string    `json:"link,omitempty"`
	Path       string    `json:"path,omitempty"`
	Remote     string    `json:"remote,omitempty"`
	Since      time.Time `json:"since,omitzero"`
	OpenedHere bool      `json:"opened_here,omitempty"`
	Tx         int64     `json:"tx_packets,omitempty"`
	Rx         int64     `json:"rx_packets,omitempty"`
	Dropped    int64     `json:"dropped,omitempty"`
	Exit       bool      `json:"exit_node,omitempty"`
	Routes     []string  `json:"routes,omitempty"`
	LastError  string    `json:"last_error,omitempty"`
}

func (n *Node) writeStatus(errText string) {
	st := n.snapshot(errText)
	writeStatusFile(n.dir, st)
}

func writeStatusFile(dir string, st Status) {
	st.PID = os.Getpid()
	st.Updated = time.Now().UTC().Truncate(time.Second)
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return
	}
	config.WriteFileAtomic(filepath.Join(dir, StatusFile), append(b, '\n'), 0o600)
}

func writeErrorStatus(dir string, err error) {
	writeStatusFile(dir, Status{Error: err.Error(), PolicyRules: -1})
}

func removeStatus(dir string) { os.Remove(filepath.Join(dir, StatusFile)) }

func statusExists(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, StatusFile))
	return err == nil
}

// ReadStatus returns the mesh status the host process last wrote, or nil
// when the mesh is not running (no file, or its process is gone).
func ReadStatus(dir string) (*Status, error) {
	b, err := config.ReadFile(filepath.Join(dir, StatusFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var st Status
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, err
	}
	if st.PID <= 0 || !processAlive(st.PID) {
		return nil, nil
	}
	return &st, nil
}
