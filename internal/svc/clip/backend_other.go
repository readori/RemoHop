package clip

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode/utf16"
)

// macBoard uses pbcopy/pbpaste for text and osascript for PNG images. The
// image path has not been verified on real macOS from the development
// environment.
type macBoard struct{ h helper }

func (b *macBoard) Name() string { return "macos" }

func (b *macBoard) Watch(ctx context.Context) <-chan Content {
	return pollWatch(ctx, b, pollInterval)
}

func (b *macBoard) Read(ctx context.Context) (Content, error) {
	out, _, err := b.h.output(ctx, nil, "pbpaste", "-Prefer", "txt")
	if err != nil && errors.Is(err, ErrTooLarge) {
		return Content{}, err
	}
	if err == nil && len(out) > 0 {
		return finishRead(KindText, out)
	}
	// No text: look for an image. `the clipboard as «class PNGf»` prints a
	// literal like «data PNGf89504E47...».
	info, _, err := b.h.output(ctx, nil, "osascript", "-e", "clipboard info")
	if err != nil || !strings.Contains(string(info), "PNGf") {
		return Content{}, ErrEmpty
	}
	lit, _, err := b.h.output(ctx, nil, "osascript", "-e", "the clipboard as «class PNGf»")
	if err != nil {
		return Content{}, ErrEmpty
	}
	s := strings.TrimSpace(string(lit))
	s = strings.TrimSuffix(strings.TrimPrefix(s, "«data PNGf"), "»")
	data, err := hex.DecodeString(s)
	if err != nil {
		return Content{}, ErrEmpty
	}
	return finishRead(KindImage, data)
}

func (b *macBoard) Write(ctx context.Context, c Content) error {
	c = normalize(c)
	if err := c.Validate(); err != nil {
		return err
	}
	if c.Kind == KindText {
		_, stderr, err := b.h.output(ctx, c.Data, "pbcopy")
		if err != nil {
			return fmt.Errorf("pbcopy: %w %s", err, stderr)
		}
		return nil
	}
	f, err := os.CreateTemp("", "remohop-clip-*.png")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(c.Data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	script := fmt.Sprintf(`set the clipboard to (read (POSIX file %q) as «class PNGf»)`, f.Name())
	if _, stderr, err := b.h.output(ctx, nil, "osascript", "-e", script); err != nil {
		return fmt.Errorf("osascript: %w %s", err, stderr)
	}
	return nil
}

// winBoard drives the clipboard through PowerShell (text and PNG images).
// Data crosses the process boundary as base64 so encodings cannot corrupt
// it. Not verified on real Windows from the development environment.
type winBoard struct {
	h   helper
	exe string
}

func (b *winBoard) Name() string { return "windows/powershell" }

func (b *winBoard) Watch(ctx context.Context) <-chan Content {
	return pollWatch(ctx, b, pollInterval)
}

// psArgs wraps script as an -EncodedCommand (UTF-16LE base64), which avoids
// all quoting problems and leaves stdin free for data. -Sta is required by
// System.Windows.Forms.Clipboard.
func psArgs(script string) []string {
	u := utf16.Encode([]rune("$ErrorActionPreference='Stop';Add-Type -AssemblyName System.Windows.Forms;Add-Type -AssemblyName System.Drawing;" + script))
	raw := make([]byte, 0, len(u)*2)
	for _, x := range u {
		raw = append(raw, byte(x), byte(x>>8))
	}
	return []string{"-NoProfile", "-NonInteractive", "-Sta", "-EncodedCommand", base64.StdEncoding.EncodeToString(raw)}
}

const psRead = `
if ([Windows.Forms.Clipboard]::ContainsImage()) {
  $i=[Windows.Forms.Clipboard]::GetImage(); $m=New-Object IO.MemoryStream
  $i.Save($m,[Drawing.Imaging.ImageFormat]::Png)
  [Console]::Out.Write('I'+[Convert]::ToBase64String($m.ToArray()))
} elseif ([Windows.Forms.Clipboard]::ContainsText()) {
  [Console]::Out.Write('T'+[Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes([Windows.Forms.Clipboard]::GetText())))
}`

const psWriteText = `$b=[Convert]::FromBase64String([Console]::In.ReadToEnd().Trim());[Windows.Forms.Clipboard]::SetText([Text.Encoding]::UTF8.GetString($b))`

const psWriteImage = `$b=[Convert]::FromBase64String([Console]::In.ReadToEnd().Trim());$m=New-Object IO.MemoryStream(,$b);[Windows.Forms.Clipboard]::SetImage([Drawing.Image]::FromStream($m))`

func (b *winBoard) Read(ctx context.Context) (Content, error) {
	out, stderr, err := b.h.output(ctx, nil, b.exe, psArgs(psRead)...)
	if err != nil {
		if errors.Is(err, ErrTooLarge) {
			return Content{}, err
		}
		return Content{}, fmt.Errorf("powershell: %w %s", err, stderr)
	}
	out = bytes.TrimSpace(out)
	if len(out) < 2 {
		return Content{}, ErrEmpty
	}
	data, err := base64.StdEncoding.DecodeString(string(out[1:]))
	if err != nil {
		return Content{}, ErrEmpty
	}
	if out[0] == 'I' {
		return finishRead(KindImage, data)
	}
	return finishRead(KindText, data)
}

func (b *winBoard) Write(ctx context.Context, c Content) error {
	c = normalize(c)
	if err := c.Validate(); err != nil {
		return err
	}
	script := psWriteText
	if c.Kind == KindImage {
		script = psWriteImage
	}
	enc := []byte(base64.StdEncoding.EncodeToString(c.Data))
	if _, stderr, err := b.h.output(ctx, enc, b.exe, psArgs(script)...); err != nil {
		return fmt.Errorf("powershell: %w %s", err, stderr)
	}
	return nil
}
