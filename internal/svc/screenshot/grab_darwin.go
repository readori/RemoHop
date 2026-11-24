//go:build darwin

package screenshot

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/png" // decoder for screencapture's output
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type darwinGrabber struct{}

func platformGrabber() grabber { return darwinGrabber{} }

// Grab runs screencapture. Display indexes map to its 1-based -D option;
// AllDisplays is its default of capturing every display into one image.
func (darwinGrabber) Grab(ctx context.Context, display int) (image.Image, error) {
	dir, err := os.MkdirTemp("", "remohop-shot-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, "shot.png")
	args := []string{"-x", "-t", "png"}
	if display > 0 {
		args = append(args, "-D", strconv.Itoa(display+1))
	} else if display == Primary {
		args = append(args, "-D", "1")
	}
	args = append(args, file)
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "screencapture", args...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("screenshot: screencapture failed (is Screen Recording permission granted, and is a user logged in?): %s", msg)
	}
	f, err := os.Open(file)
	if err != nil {
		return nil, fmt.Errorf("screenshot: screencapture wrote no file: %w", err)
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	return img, err
}

// Displays reports the primary display only: the size is learned from a
// capture, as macOS offers no cheap dependency-free enumeration.
func (g darwinGrabber) Displays(ctx context.Context) ([]Display, error) {
	img, err := g.Grab(ctx, Primary)
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	return []Display{{Name: "main", Width: b.Dx(), Height: b.Dy(), Primary: true}}, nil
}
