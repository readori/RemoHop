package mcp

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/readori/RemoHop/internal/dial"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/svc/fs/rfs"
	"github.com/readori/RemoHop/internal/svc/fs/xfer"
	"github.com/readori/RemoHop/internal/svc/wake"
)

const maxSendItems = 64

type sendIn struct {
	Target     string   `json:"target,omitempty" jsonschema:"Remote machine: 9-digit RemoHop ID, saved alias or host:port. Optional if the server has a default target."`
	LocalPaths []string `json:"local_paths" jsonschema:"Absolute paths of files (or, with recursive, directories) on THIS computer."`
	Recursive  bool     `json:"recursive,omitempty" jsonschema:"Allow directories and send them with their contents."`
}

type sentItem struct {
	Source string `json:"source"`
	Path   string `json:"path" jsonschema:"Final path on the remote machine."`
	Bytes  int64  `json:"bytes"`
	Dir    bool   `json:"dir,omitempty"`
}

type sendOut struct {
	Sent []sentItem `json:"sent"`
}

type wakeIn struct {
	Target string `json:"target,omitempty" jsonschema:"The always-on remote machine that should send the packet on its LAN: 9-digit RemoHop ID, saved alias or host:port. Optional if the server has a default target."`
	MAC    string `json:"mac" jsonschema:"MAC address of the sleeping computer (aa:bb:cc:dd:ee:ff), or a name saved on this computer with remohop wake --save."`
	Port   int    `json:"port,omitempty" jsonschema:"UDP port (default 9; some machines use 7)."`
}

type wakeOut struct {
	MAC     string   `json:"mac"`
	Port    int      `json:"port"`
	SentTo  []string `json:"sent_to" jsonschema:"Broadcast destinations the remote machine sent the packet to."`
	Relayer string   `json:"via" jsonschema:"The remote machine that sent it."`
}

func (s *Server) registerExtras() {
	addTool(s, "remohop_send", "Send files to the remote user's Downloads folder",
		"Deliver files from THIS computer into the remote user's Downloads folder without choosing a remote path. Nothing is ever overwritten: a taken name becomes \"name (1).ext\". "+
			"Returns the final remote paths. Use it to hand a person a file; use remohop_upload when the file must land at a specific path. Up to 512 MiB in total per call.",
		additive(), s.send)
	addTool(s, "remohop_wake", "Wake a sleeping computer near a remote machine",
		"Ask the remote machine (a small always-on box running remohop host) to broadcast a Wake-on-LAN magic packet on its own local network, to wake a sleeping computer next to it. "+
			"The packet is fire-and-forget: success means it was sent, not that the computer woke; give it up to a minute, then try to reach it. The sleeping computer needs Wake-on-LAN enabled and usually a wired connection. Hosts limit wake requests per minute.",
		additive(), s.wake)
}

func (s *Server) send(ctx context.Context, in sendIn) (*reply[sendOut], error) {
	if len(in.LocalPaths) == 0 {
		return nil, errors.New("local_paths is empty")
	}
	if len(in.LocalPaths) > maxSendItems {
		return nil, fmt.Errorf("at most %d paths per call", maxSendItems)
	}
	var srcs []string
	var total int64
	for _, arg := range in.LocalPaths {
		p, err := guardLocal(arg)
		if err != nil {
			return nil, err
		}
		st, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("send: %w", err)
		}
		if st.IsDir() && !in.Recursive {
			return nil, fmt.Errorf("%s is a directory; pass recursive=true to send it", p)
		}
		n, err := treeSize(p, maxTransferBytes-total)
		if err != nil {
			return nil, err
		}
		total += n
		srcs = append(srcs, p)
	}
	var sent []xfer.Sent
	err := s.withFS(ctx, in.Target, false, func(ctx context.Context, c *rfs.Client) (err error) {
		sent, err = xfer.Send(ctx, c, srcs, xfer.SendOptions{Recursive: in.Recursive})
		return err
	})
	out := sendOut{Sent: []sentItem{}}
	var b strings.Builder
	for _, it := range sent {
		si := sentItem{Source: it.Source, Path: clean(it.Path), Bytes: it.Bytes, Dir: it.Dir}
		out.Sent = append(out.Sent, si)
		fmt.Fprintf(&b, "%s -> %s (%d bytes)\n", si.Source, si.Path, si.Bytes)
	}
	if err != nil {
		// Some items may have gone through; say which, so they are not
		// sent again under a second name.
		if len(sent) > 0 {
			return nil, fmt.Errorf("%w\ndelivered before the failure:\n%s", err, b.String())
		}
		return nil, err
	}
	return textReply(out, b.String()), nil
}

// treeSize adds up the regular files under p, failing once the total passes
// limit or a symlink leads into RemoHop's config directory.
func treeSize(p string, limit int64) (int64, error) {
	var total int64
	err := filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if _, err := guardLocal(path); err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		if total > limit {
			return fmt.Errorf("the files exceed the %d byte limit per call (use `remohop <ID> cp -r` or `remohop <ID> sync` from a terminal)", int64(maxTransferBytes))
		}
		return nil
	})
	return total, err
}

func (s *Server) wake(ctx context.Context, in wakeIn) (*reply[wakeOut], error) {
	if in.Port < 0 || in.Port > 65535 {
		return nil, errors.New("port must be 1-65535")
	}
	mac := strings.TrimSpace(in.MAC)
	if _, err := wake.ParseMAC(mac); err != nil {
		// Not an address: maybe a saved name.
		store, serr := wake.OpenStore()
		if serr != nil {
			return nil, fmt.Errorf("%q is not a MAC address: %w", in.MAC, err)
		}
		saved, ok, serr := store.Lookup(mac)
		if serr != nil || !ok {
			return nil, fmt.Errorf("%q is neither a MAC address (aa:bb:cc:dd:ee:ff) nor a name saved with `remohop wake --save NAME MAC`", in.MAC)
		}
		mac = saved
	}
	target, err := s.resolveTarget(in.Target)
	if err != nil {
		return nil, err
	}
	var res *wake.Result
	err = s.withConn(ctx, in.Target, false, func(ctx context.Context, c *peer.Conn) (err error) {
		res, err = wake.Remote(ctx, c, mac, wake.Options{Port: in.Port})
		return err
	})
	if err != nil {
		return nil, err
	}
	out := wakeOut{MAC: res.MAC, Port: res.Port, SentTo: res.Targets, Relayer: target}
	if out.SentTo == nil {
		out.SentTo = []string{}
	}
	cleanStrings(&out)
	return textReply(out, fmt.Sprintf("%s sent a Wake-on-LAN packet for %s to %s (port %d). It is not confirmed that the computer woke: wait up to a minute, then try to reach it.",
		target, out.MAC, strings.Join(out.SentTo, ", "), out.Port)), nil
}

// connPath says how a session's packets travel ("lan", "direct", "punched"
// or "relayed"), or "" for sessions not made by the dialer.
func connPath(c *peer.Conn) string {
	if info := dial.InfoOf(c); info != nil {
		return string(info.Path)
	}
	return ""
}
