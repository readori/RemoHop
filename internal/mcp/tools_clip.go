package mcp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/dial"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/svc/clip"
)

const (
	defaultClipBytes = 64 << 10
	maxClipBytes     = 1 << 20
	maxInlineImage   = 8 << 20
	defaultHistory   = 20
	maxHistory       = 200
	maxHistoryMD     = 256 << 10
)

type clipGetIn struct {
	Target    string `json:"target,omitempty" jsonschema:"Remote machine: 9-digit RemoHop ID, saved alias or host:port. Optional if the server has a default target."`
	MaxBytes  int    `json:"max_bytes,omitempty" jsonschema:"Cap returned text at this many bytes (default 65536, max 1048576). Does not apply to images."`
	SavePath  string `json:"save_path,omitempty" jsonschema:"Optional absolute path on THIS computer to also save the clipboard content to (text file or PNG). Refuses to replace an existing file unless overwrite is true; cannot point into RemoHop's own config directory."`
	Overwrite bool   `json:"overwrite,omitempty" jsonschema:"Allow save_path to replace an existing file."`
}

type clipGetOut struct {
	Kind      string `json:"kind" jsonschema:"text, image or empty."`
	MIME      string `json:"mime,omitempty"`
	Text      string `json:"text,omitempty"`
	Bytes     int    `json:"bytes" jsonschema:"Size of the clipboard content in bytes."`
	Truncated bool   `json:"truncated,omitempty"`
	SavedTo   string `json:"saved_to,omitempty"`
	Backend   string `json:"backend,omitempty" jsonschema:"Clipboard backend on the remote machine; \"memory\" means a headless host with a virtual clipboard that no desktop application shares."`
}

type clipSetIn struct {
	Target string `json:"target,omitempty" jsonschema:"Remote machine: 9-digit RemoHop ID, saved alias or host:port. Optional if the server has a default target."`
	Text   string `json:"text" jsonschema:"Text to place on the remote clipboard (non-empty, max 1 MiB here)."`
}

type clipSetOut struct {
	Bytes   int    `json:"bytes"`
	Backend string `json:"backend,omitempty" jsonschema:"Clipboard backend on the remote machine; \"memory\" means a headless host with a virtual clipboard that no desktop application shares."`
}

type clipHistoryIn struct {
	Target string `json:"target,omitempty" jsonschema:"Only entries for this remote machine (ID, alias or host:port; matched under any of its saved names). Omit for all machines."`
	Limit  int    `json:"limit,omitempty" jsonschema:"Newest N entries (default 20, max 200)."`
	Since  string `json:"since,omitempty" jsonschema:"Only entries at or after this time: an RFC 3339 timestamp (2026-05-01T09:00:00Z) or a duration ago (30m, 2h)."`
	Peer   string `json:"peer,omitempty" jsonschema:"Only entries for this peer, as named in the history headings (usually the 9-digit ID). Omit for all peers."`
}

type clipHistoryOut struct {
	Entries   int  `json:"entries"`
	Truncated bool `json:"truncated,omitempty"`
}

func (s *Server) registerClipboard() {
	addTool(s, "remohop_clipboard_get", "Read the remote clipboard",
		"Read the current clipboard of the remote machine. Text is returned as text; an image is returned as an image content block you can look at. "+
			"The item is also recorded in this computer's clipboard history (remohop_clipboard_history). "+
			"An empty clipboard is reported as kind \"empty\", not as an error. On a headless host the clipboard is a virtual one that only RemoHop shares (backend \"memory\").",
		readOnly(), s.clipGet)
	addTool(s, "remohop_clipboard_set", "Write text to the remote clipboard",
		"Replace the clipboard of the remote machine with the given text, for example to hand the person a snippet to paste there. Overwrites whatever was on the clipboard.",
		destructive(true), s.clipSet)
	addTool(s, "remohop_clipboard_history", "Export clipboard history as Markdown",
		"Export this computer's record of clipboard items that passed through RemoHop (gets, sets, pushes, pulls and syncs) as Markdown, oldest first: "+
			"one heading per entry with time, direction and peer, text in fenced code blocks, images as file paths. Does not contact the remote machine.",
		localReadOnly(), s.clipHistory)
}

func (s *Server) clipBackend(ctx context.Context, target string, c *peer.Conn) string {
	info, err := clip.RemoteInfo(ctx, c)
	if err != nil {
		s.log.Debug("clipboard backend lookup failed", "target", target, "err", err)
		return ""
	}
	return clean(info.Backend)
}

func (s *Server) clipGet(ctx context.Context, in clipGetIn) (*reply[clipGetOut], error) {
	// Validate the destination before touching the remote machine.
	var savePath string
	if in.SavePath != "" {
		p, err := guardLocal(in.SavePath)
		if err != nil {
			return nil, err
		}
		if _, err := os.Lstat(p); err == nil && !in.Overwrite {
			return nil, fmt.Errorf("%s already exists; pass overwrite=true to replace it", p)
		}
		savePath = p
	}
	var (
		c       clip.Content
		backend string
		empty   bool
	)
	err := s.withConn(ctx, in.Target, true, func(ctx context.Context, conn *peer.Conn) error {
		var err error
		c, err = clip.GetWith(ctx, conn, s.clipOptions(in.Target))
		if errors.Is(err, clip.ErrEmpty) {
			empty, err = true, nil
		}
		if err == nil {
			backend = s.clipBackend(ctx, in.Target, conn)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	if empty {
		return textReply(clipGetOut{Kind: "empty", Backend: backend}, "the remote clipboard is empty"), nil
	}
	out := clipGetOut{Kind: string(c.Kind), MIME: c.MIME, Bytes: len(c.Data), Backend: backend}
	if savePath != "" {
		if _, err := writeLocal(savePath, bytes.NewReader(c.Data), 0o600, in.Overwrite, int64(len(c.Data))); err != nil {
			return nil, fmt.Errorf("saving clipboard: %w", err)
		}
		out.SavedTo = savePath
	}
	if c.Kind == clip.KindImage {
		if len(c.Data) > maxInlineImage && out.SavedTo == "" {
			return nil, fmt.Errorf("the clipboard image is %d bytes, too large to return inline; call again with save_path to save it on this computer", len(c.Data))
		}
		r := &reply[clipGetOut]{Out: out}
		r.Content = append(r.Content, &sdk.TextContent{Text: fmt.Sprintf("remote clipboard image (%s, %d bytes)", c.MIME, len(c.Data))})
		if len(c.Data) <= maxInlineImage {
			r.Content = append(r.Content, &sdk.ImageContent{Data: c.Data, MIMEType: "image/png"})
		}
		return r, nil
	}
	limit := clamp(in.MaxBytes, defaultClipBytes, maxClipBytes)
	text := c.String()
	if len(text) > limit {
		text, out.Truncated = truncUTF8(text, limit), true
	}
	out.Text = clean(text)
	msg := out.Text
	if out.Truncated {
		msg += fmt.Sprintf("\n[truncated: showing %d of %d bytes]", len(text), len(c.Data))
	}
	return textReply(out, msg), nil
}

func (s *Server) clipSet(ctx context.Context, in clipSetIn) (*reply[clipSetOut], error) {
	if in.Text == "" {
		return nil, errors.New("text is empty")
	}
	if len(in.Text) > maxClipBytes {
		return nil, fmt.Errorf("text is %d bytes; the limit is %d", len(in.Text), maxClipBytes)
	}
	if !utf8.ValidString(in.Text) {
		return nil, errors.New("text is not valid UTF-8")
	}
	var backend string
	err := s.withConn(ctx, in.Target, true, func(ctx context.Context, conn *peer.Conn) error {
		if err := clip.SetWith(ctx, conn, clip.Text(in.Text), s.clipOptions(in.Target)); err != nil {
			return err
		}
		backend = s.clipBackend(ctx, in.Target, conn)
		return nil
	})
	if err != nil {
		return nil, err
	}
	msg := fmt.Sprintf("remote clipboard set (%d bytes)", len(in.Text))
	if backend == "memory" {
		msg += "; note: that host has no desktop clipboard, so only RemoHop clients can read it back"
	}
	return textReply(clipSetOut{Bytes: len(in.Text), Backend: backend}, msg), nil
}

func (s *Server) clipHistory(_ context.Context, in clipHistoryIn) (*reply[clipHistoryOut], error) {
	f := clip.Filter{Peer: strings.TrimSpace(in.Peer)}
	names := peerNames(in.Target)
	if in.Since != "" {
		t, err := parseSince(in.Since, time.Now())
		if err != nil {
			return nil, err
		}
		f.Since = t
	}
	h, err := clip.OpenHistory("")
	if err != nil {
		return nil, fmt.Errorf("opening clipboard history: %w", err)
	}
	entries, err := h.List(f)
	if err != nil {
		return nil, fmt.Errorf("reading clipboard history: %w", err)
	}
	if names != nil {
		kept := entries[:0]
		for _, e := range entries {
			if names[e.Peer] {
				kept = append(kept, e)
			}
		}
		entries = kept
	}
	if limit := clamp(in.Limit, defaultHistory, maxHistory); len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	md := clean(clip.RenderMarkdown(entries))
	out := clipHistoryOut{Entries: len(entries)}
	if len(md) > maxHistoryMD {
		md, out.Truncated = truncUTF8(md, maxHistoryMD)+"\n\n[truncated; use a smaller limit or since]\n", true
	}
	if len(entries) == 0 {
		md = "clipboard history is empty for this filter"
	}
	return textReply(out, md), nil
}

// peerNames returns every name history entries for target may be recorded
// under: the CLI uses the target as typed (possibly an alias) while the
// session-based calls use the canonical ID. nil means no target filter.
func peerNames(target string) map[string]bool {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil
	}
	names := map[string]bool{target: true, cli.NormalizeTarget(target): true}
	if store, err := dial.OpenStore(""); err == nil {
		if peers, err := store.List(); err == nil {
			for _, p := range peers {
				if names[p.Target] || (p.Alias != "" && names[p.Alias]) {
					names[p.Target] = true
					if p.Alias != "" {
						names[p.Alias] = true
					}
				}
			}
		}
	}
	return names
}

// parseSince accepts an RFC 3339 time or a Go duration meaning "that long ago".
func parseSince(v string, now time.Time) (time.Time, error) {
	v = strings.TrimSpace(v)
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	if d, err := time.ParseDuration(v); err == nil && d >= 0 {
		return now.Add(-d), nil
	}
	return time.Time{}, fmt.Errorf("since %q is neither an RFC 3339 time (2026-05-01T09:00:00Z) nor a duration ago (30m, 2h)", v)
}

// clipOptions records clipboard history under the name the user addressed the
// machine by (an alias, usually), which is what `remohop <target> clip history`
// looks for, rather than under the session's resolved ID.
func (s *Server) clipOptions(targetArg string) clip.CallOptions {
	name, err := s.resolveTarget(targetArg)
	if err != nil {
		return clip.CallOptions{}
	}
	return clip.CallOptions{Peer: name}
}
