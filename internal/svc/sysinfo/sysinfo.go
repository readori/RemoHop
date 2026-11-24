package sysinfo

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"runtime"
	"time"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/wire"
)

const serviceName = "sysinfo"

func init() {
	peer.Handle(serviceName, handle)
}

// Info describes the machine a host runs on. Fields the platform cannot
// supply are left zero rather than guessed: the document has the same shape
// everywhere so agents can parse it without per-OS special cases.
type Info struct {
	Hostname       string     `json:"hostname"`
	OS             string     `json:"os"`
	Arch           string     `json:"arch"`
	DistroName     string     `json:"distro_name"`
	DistroVersion  string     `json:"distro_version"`
	Kernel         string     `json:"kernel"`
	CPUModel       string     `json:"cpu_model"`
	CPUCores       int        `json:"cpu_cores"`
	MemTotalBytes  uint64     `json:"mem_total_bytes"`
	MemAvailBytes  uint64     `json:"mem_available_bytes"`
	UptimeSeconds  int64      `json:"uptime_seconds"`
	LoadAverage    [3]float64 `json:"load_average"`
	User           string     `json:"user"`
	UID            string     `json:"uid"`
	Home           string     `json:"home"`
	Shell          string     `json:"shell"`
	Cwd            string     `json:"cwd"`
	LocalTime      string     `json:"local_time"`
	Timezone       string     `json:"timezone"`
	RootTotalBytes uint64     `json:"root_total_bytes"`
	RootFreeBytes  uint64     `json:"root_free_bytes"`
	Display        bool       `json:"display"`
	RemoHopVersion string     `json:"remohop_version"`
}

// Get asks the remote host for its description.
func Get(ctx context.Context, conn *peer.Conn) (*Info, error) {
	s, err := conn.Open(ctx, serviceName, nil)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	// The reply is read with blocking I/O, so cancellation has to abort the
	// stream rather than wait for the host.
	stop := context.AfterFunc(ctx, s.Reset)
	defer stop()

	var info Info
	if err := wire.ReadJSON(s, &info); err != nil {
		if ce := ctx.Err(); ce != nil {
			return nil, ce
		}
		return nil, fmt.Errorf("sysinfo: %w", err)
	}
	return &info, nil
}

func handle(ctx context.Context, c *peer.Conn, s peer.Stream, req json.RawMessage) error {
	return wire.WriteJSON(s, collect())
}

// collect gathers the description of the machine this process runs on.
// Nothing here fails: every source that cannot be read leaves its fields zero.
func collect() *Info {
	host, _ := os.Hostname()
	cwd, _ := os.Getwd()
	now := time.Now()
	zone, _ := now.Zone()

	info := &Info{
		Hostname:       host,
		OS:             runtime.GOOS,
		Arch:           runtime.GOARCH,
		CPUCores:       runtime.NumCPU(),
		Shell:          os.Getenv("SHELL"),
		Cwd:            cwd,
		LocalTime:      now.Format(time.RFC3339),
		Timezone:       zone,
		RemoHopVersion: cli.Version,
	}
	if u, err := user.Current(); err == nil {
		info.User = u.Username
		info.UID = u.Uid
		info.Home = u.HomeDir
	}
	platform(info)
	return info
}
