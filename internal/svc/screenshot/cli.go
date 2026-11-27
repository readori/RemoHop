package screenshot

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/readori/RemoHop/internal/cli"
)

func init() {
	cli.Register(&cli.Command{
		Name:    "screenshot",
		Summary: "capture the remote screen to an image file",
		Remote:  true,
		Usage: `Usage: remohop <ID> screenshot [-o FILE|-] [-d N|NAME|all] [-R X,Y,W,H]
                           [--format png|jpeg] [--quality Q] [--max-width PX] [--json]
       remohop <ID> screenshot --list [--json]

Capture the remote machine's screen, for example so a multimodal model can
inspect UI state or an error dialog.

Flags:
  -o FILE            output file; "-" writes the image bytes to stdout
                     (default remohop-<target>-<YYYYmmdd-HHMMSS>.png in the
                     current directory). A .jpg/.jpeg extension selects jpeg.
  -d, --display D    monitor index or name from --list (default 0, the
                     primary); "all" captures the whole desktop
  -R, --region X,Y,W,H
                     capture only this rectangle, in pixels of the selected
                     display (origin top-left; clipped to the display). In
                     the image, pixel (0,0) is display pixel (X,Y): add X,Y
                     back before using coordinates with click/move
      --format F     png (default) or jpeg
      --quality Q    jpeg quality 1-100 (default 80)
      --max-width PX downscale on the remote machine to at most PX wide
      --list         list the remote displays (index, geometry, primary)
      --json         machine-readable output: path, width, height, format,
                     bytes, display, region (or the display list with --list)

With -o - the summary goes to stderr so stdout stays a clean image stream.

Examples:
  remohop 123456789 screenshot -o /tmp/screen.png
  remohop 123456789 screenshot --max-width 1280 --format jpeg --json
  remohop 123456789 screenshot -o - > screen.png
  remohop 123456789 screenshot --list
  remohop 123456789 screenshot -d HDMI-1 -o /tmp/hdmi.png
  remohop 123456789 screenshot -R 0,0,800,600 -o /tmp/corner.png
`,
		Run: run,
	})
}

type result struct {
	Path    string    `json:"path,omitempty"`
	Width   int       `json:"width"`
	Height  int       `json:"height"`
	Format  string    `json:"format"`
	Bytes   int       `json:"bytes"`
	Display int       `json:"display"`
	Region  *rectJSON `json:"region,omitempty"`
}

func run(ctx context.Context, env *cli.Env, args []string) int {
	fs := cli.NewFlagSet(env, "screenshot", "")
	var (
		out, display, format, region string
		quality, maxWidth            int
		list, asJSON                 bool
	)
	fs.StringVar(&out, "o", "", "")
	fs.StringVar(&display, "display", "0", "")
	fs.StringVar(&display, "d", "0", "")
	fs.StringVar(&region, "region", "", "")
	fs.StringVar(&region, "R", "", "")
	fs.StringVar(&format, "format", "", "")
	fs.IntVar(&quality, "quality", 0, "")
	fs.IntVar(&maxWidth, "max-width", 0, "")
	fs.BoolVar(&list, "list", false, "")
	fs.BoolVar(&asJSON, "json", false, "")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return cli.ExitOK
		}
		return cli.ExitUsage
	}
	if fs.NArg() > 0 {
		env.Errorf("screenshot: unexpected argument %q", fs.Arg(0))
		return cli.ExitUsage
	}
	if n, err := strconv.Atoi(display); (err == nil && n < 0) || strings.TrimSpace(display) == "" {
		env.Errorf("screenshot: bad --display %q (want an index, a name from --list, or \"all\")", display)
		return cli.ExitUsage
	}
	var rect image.Rectangle
	if region != "" {
		var err error
		if rect, err = ParseRegion(region); err != nil {
			env.Errorf("screenshot: %v", err)
			return cli.ExitUsage
		}
	}
	if format == "" && out != "" && out != "-" {
		switch strings.ToLower(filepath.Ext(out)) {
		case ".jpg", ".jpeg":
			format = JPEG
		}
	}
	f, err := normalizeFormat(format)
	if err != nil {
		env.Errorf("%v", err)
		return cli.ExitUsage
	}
	if quality < 0 || quality > 100 || maxWidth < 0 {
		env.Errorf("screenshot: --quality must be 1-100 and --max-width positive")
		return cli.ExitUsage
	}

	conn, err := env.Conn(ctx)
	if err != nil {
		env.Errorf("%v", err)
		return cli.ExitConnect
	}

	if list {
		ds, err := Displays(ctx, conn)
		if err != nil {
			env.Errorf("%v", err)
			return cli.ExitError
		}
		if asJSON {
			return printJSON(env, ds)
		}
		for _, d := range ds {
			p := ""
			if d.Primary {
				p = "  primary"
			}
			fmt.Fprintf(env.Stdout, "%d  %dx%d+%d+%d%s  %s\n", d.Index, d.Width, d.Height, d.X, d.Y, p, cli.Sanitize(d.Name))
		}
		return cli.ExitOK
	}

	disp, err := ResolveDisplay(ctx, conn, display)
	if err != nil {
		env.Errorf("%v", err)
		return cli.ExitError
	}
	img, err := Capture(ctx, conn, Options{Display: disp, Format: f, Quality: quality, MaxWidth: maxWidth, Region: rect})
	if err != nil {
		env.Errorf("%v", err)
		return cli.ExitError
	}
	res := result{Width: img.Width, Height: img.Height, Format: img.Format, Bytes: len(img.Data), Display: img.Display}
	if !img.Region.Empty() {
		res.Region = toRectJSON(img.Region)
	}

	summary := env.Stdout
	if out == "-" {
		if _, err := env.Stdout.Write(img.Data); err != nil {
			env.Errorf("screenshot: writing stdout: %v", err)
			return cli.ExitError
		}
		summary = env.Stderr
	} else {
		if out == "" {
			ext := ".png"
			if img.Format == JPEG {
				ext = ".jpg"
			}
			out = fmt.Sprintf("remohop-%s-%s%s", safeName(env.Target), time.Now().Format("20060102-150405"), ext)
		}
		if abs, err := filepath.Abs(out); err == nil {
			out = abs
		}
		if err := os.WriteFile(out, img.Data, 0o644); err != nil {
			env.Errorf("screenshot: %v", err)
			return cli.ExitError
		}
		res.Path = out
	}
	if asJSON {
		b, _ := json.Marshal(res)
		fmt.Fprintln(summary, string(b))
		return cli.ExitOK
	}
	where := ""
	if r := res.Region; r != nil {
		where = fmt.Sprintf(" region %d,%d,%d,%d", r.X, r.Y, r.W, r.H)
	}
	if res.Path != "" {
		fmt.Fprintf(summary, "%s %dx%d %s %d bytes%s\n", res.Path, res.Width, res.Height, res.Format, res.Bytes, where)
	} else {
		fmt.Fprintf(summary, "%dx%d %s %d bytes%s\n", res.Width, res.Height, res.Format, res.Bytes, where)
	}
	return cli.ExitOK
}

func printJSON(env *cli.Env, v any) int {
	enc := json.NewEncoder(env.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(v)
	return cli.ExitOK
}

// ParseRegion parses "X,Y,W,H" (pixels, W and H positive).
func ParseRegion(s string) (image.Rectangle, error) {
	parts := strings.Split(s, ",")
	if len(parts) != 4 {
		return image.Rectangle{}, fmt.Errorf("bad region %q (want X,Y,W,H)", s)
	}
	var v [4]int
	for i, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return image.Rectangle{}, fmt.Errorf("bad region %q (want X,Y,W,H)", s)
		}
		v[i] = n
	}
	r := image.Rect(v[0], v[1], v[0]+v[2], v[1]+v[3])
	if v[2] <= 0 || v[3] <= 0 || !validRegion(r) {
		return image.Rectangle{}, fmt.Errorf("bad region %q (X,Y must be >= 0, W,H > 0)", s)
	}
	return r, nil
}

// safeName makes a target usable inside a file name.
func safeName(s string) string {
	if s == "" {
		return "host"
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		}
		return '_'
	}, s)
}
