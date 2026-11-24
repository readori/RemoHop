package clip

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// errUnavailable marks a backend whose helper cannot reach a display server;
// detection treats it as "no system clipboard here".
var errUnavailable = errors.New("clipboard helper cannot reach a display")

func cantConnect(stderr string) bool {
	s := strings.ToLower(stderr)
	return strings.Contains(s, "can't open display") || strings.Contains(s, "cannot open display") ||
		strings.Contains(s, "failed to connect") || strings.Contains(s, "compositor doesn't support") ||
		strings.Contains(s, "no wayland") || strings.Contains(s, "wayland_display")
}

// x11Board drives the CLIPBOARD selection through xclip or xsel.
type x11Board struct {
	tool string // "xclip" or "xsel"
	h    helper
}

func (b *x11Board) Name() string { return "x11/" + b.tool }

func (b *x11Board) Watch(ctx context.Context) <-chan Content {
	return pollWatch(ctx, b, pollInterval)
}

func (b *x11Board) Read(ctx context.Context) (Content, error) {
	if b.tool == "xsel" {
		return b.readXsel(ctx)
	}
	out, stderr, err := b.h.output(ctx, nil, "xclip", "-selection", "clipboard", "-o", "-t", "TARGETS")
	if err != nil {
		if errors.Is(err, ErrTooLarge) {
			return Content{}, err
		}
		if cantConnect(stderr) {
			return Content{}, fmt.Errorf("%w: %s", errUnavailable, stderr)
		}
		// With no selection owner xclip fails with "target TARGETS not
		// available", which just means the clipboard is empty.
		return Content{}, ErrEmpty
	}
	targets := strings.Fields(string(out))
	has := func(t string) bool {
		for _, x := range targets {
			if strings.EqualFold(x, t) {
				return true
			}
		}
		return false
	}
	if has("image/png") {
		return b.get(ctx, "image/png", KindImage)
	}
	for _, t := range []string{"UTF8_STRING", "text/plain;charset=utf-8", "text/plain"} {
		if has(t) {
			return b.get(ctx, t, KindText)
		}
	}
	if has("STRING") {
		c, err := b.get(ctx, "STRING", KindText)
		if err == nil && !utf8.Valid(c.Data) {
			// STRING is ISO 8859-1 by definition.
			r := make([]rune, len(c.Data))
			for i, x := range c.Data {
				r[i] = rune(x)
			}
			c = Text(string(r))
		}
		return c, err
	}
	return Content{}, ErrEmpty
}

func (b *x11Board) get(ctx context.Context, target string, kind Kind) (Content, error) {
	out, _, err := b.h.output(ctx, nil, "xclip", "-selection", "clipboard", "-o", "-t", target)
	if err != nil {
		if errors.Is(err, ErrTooLarge) {
			return Content{}, err
		}
		return Content{}, ErrEmpty // the owner went away between the two calls
	}
	return finishRead(kind, out)
}

func (b *x11Board) readXsel(ctx context.Context) (Content, error) {
	out, stderr, err := b.h.output(ctx, nil, "xsel", "--clipboard", "--output")
	if err != nil {
		if errors.Is(err, ErrTooLarge) {
			return Content{}, err
		}
		if cantConnect(stderr) {
			return Content{}, fmt.Errorf("%w: %s", errUnavailable, stderr)
		}
		return Content{}, ErrEmpty
	}
	return finishRead(KindText, out)
}

func (b *x11Board) Write(ctx context.Context, c Content) error {
	c = normalize(c)
	if err := c.Validate(); err != nil {
		return err
	}
	var err error
	switch {
	case b.tool == "xsel" && c.Kind == KindImage:
		return errors.New("xsel cannot hold images; install xclip")
	case b.tool == "xsel":
		err = b.h.own(ctx, c.Data, "xsel", "--clipboard", "--input")
	case c.Kind == KindImage:
		err = b.h.own(ctx, c.Data, "xclip", "-selection", "clipboard", "-i", "-t", "image/png")
	default:
		err = b.h.own(ctx, c.Data, "xclip", "-selection", "clipboard", "-i", "-t", "UTF8_STRING")
	}
	if err != nil {
		return err
	}
	return confirmWrite(ctx, b, c)
}

// confirmWrite waits until b reads back c. The selection-owning helpers
// (xclip, xsel, wl-copy) fork, and the foreground process exits before the
// background one has taken the selection over. Under load that takes a
// moment: a read straight after would still see the previous content, and
// two writes in quick succession could even be taken over in the wrong
// order, so the older content wins. Waiting here makes Write mean "the
// clipboard now holds c" and keeps successive writes ordered.
func confirmWrite(ctx context.Context, b Board, c Content) error {
	want := c.Hash()
	deadline := time.Now().Add(confirmTimeout)
	for {
		if got, err := b.Read(ctx); err == nil && got.Hash() == want {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s: the clipboard did not take the new content within %s", b.Name(), confirmTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// confirmTimeout bounds confirmWrite.
const confirmTimeout = 5 * time.Second

// finishRead turns helper output into validated content; clipboards that
// carry unusable data read as empty rather than failing the whole sync.
func finishRead(kind Kind, data []byte) (Content, error) {
	if kind == KindText && bytes.IndexByte(data, 0) >= 0 {
		return Content{}, ErrEmpty
	}
	c := normalize(Content{Kind: kind, Data: data})
	if err := c.Validate(); err != nil {
		if errors.Is(err, ErrTooLarge) {
			return Content{}, err
		}
		return Content{}, ErrEmpty
	}
	return c, nil
}

// waylandBoard drives wl-clipboard.
type waylandBoard struct{ h helper }

func (b *waylandBoard) Name() string { return "wayland/wl-clipboard" }

func (b *waylandBoard) Watch(ctx context.Context) <-chan Content {
	return pollWatch(ctx, b, pollInterval)
}

func (b *waylandBoard) Read(ctx context.Context) (Content, error) {
	out, stderr, err := b.h.output(ctx, nil, "wl-paste", "--list-types")
	if err != nil {
		if cantConnect(stderr) {
			return Content{}, fmt.Errorf("%w: %s", errUnavailable, stderr)
		}
		return Content{}, ErrEmpty
	}
	types := strings.Fields(string(out))
	pick := func(match func(string) bool) string {
		for _, t := range types {
			if match(t) {
				return t
			}
		}
		return ""
	}
	if pick(func(t string) bool { return t == "image/png" }) != "" {
		out, _, err := b.h.output(ctx, nil, "wl-paste", "--no-newline", "--type", "image/png")
		if err != nil {
			return Content{}, ErrEmpty
		}
		return finishRead(KindImage, out)
	}
	t := pick(func(t string) bool { return t == "text/plain;charset=utf-8" })
	if t == "" {
		t = pick(func(t string) bool { return t == "UTF8_STRING" || t == "text/plain" })
	}
	if t == "" {
		return Content{}, ErrEmpty
	}
	out, _, err = b.h.output(ctx, nil, "wl-paste", "--no-newline", "--type", t)
	if err != nil {
		return Content{}, ErrEmpty
	}
	return finishRead(KindText, out)
}

func (b *waylandBoard) Write(ctx context.Context, c Content) error {
	c = normalize(c)
	if err := c.Validate(); err != nil {
		return err
	}
	mime := "text/plain;charset=utf-8"
	if c.Kind == KindImage {
		mime = "image/png"
	}
	if err := b.h.own(ctx, c.Data, "wl-copy", "--type", mime); err != nil {
		return err
	}
	return confirmWrite(ctx, b, c)
}
