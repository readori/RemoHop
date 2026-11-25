package mcp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"os"
	"strconv"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/readori/RemoHop/internal/dial"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/svc/screenshot"
	"github.com/readori/RemoHop/internal/svc/sysinfo"
)

// defaultMaxWidth is what vision models take in without downscaling it
// themselves: larger images only cost tokens and upload time.
const defaultMaxWidth = 1568

type screenshotIn struct {
	Target    string `json:"target,omitempty" jsonschema:"Remote machine: 9-digit RemoHop ID, saved alias or host:port. Optional if the server has a default target."`
	Display   int    `json:"display,omitempty" jsonschema:"Monitor index from remohop_displays (default 0, the primary); -1 captures all monitors as one image."`
	Region    *rect  `json:"region,omitempty" jsonschema:"Capture only this rectangle of the display, in REAL display pixels (see display_width/display_height of an earlier result), to read small text or focus on one window. Clipped to the display."`
	MaxWidth  int    `json:"max_width,omitempty" jsonschema:"Downscale to at most this many pixels wide (default 1568, which suits vision models; max 8192). Raise it, or use region, to read small text."`
	Format    string `json:"format,omitempty" jsonschema:"png (default, lossless) or jpeg (smaller)."`
	Quality   int    `json:"quality,omitempty" jsonschema:"JPEG quality 1-100 (default 80)."`
	SavePath  string `json:"save_path,omitempty" jsonschema:"Optional path on THIS computer, inside the MCP server's download directory (a relative path lands there), to also save the image file to. Refuses to replace an existing file unless overwrite is true; cannot point into RemoHop's own config directory."`
	Overwrite bool   `json:"overwrite,omitempty" jsonschema:"Allow save_path to replace an existing file."`
}

// rect is a rectangle in real display pixels, origin at the display's top-left.
type rect struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

type screenshotOut struct {
	Width         int    `json:"width" jsonschema:"Width of the returned image in pixels."`
	Height        int    `json:"height" jsonschema:"Height of the returned image in pixels."`
	DisplayWidth  int    `json:"display_width" jsonschema:"Real width of the display in pixels (the image is usually a downscaled view of it)."`
	DisplayHeight int    `json:"display_height" jsonschema:"Real height of the display in pixels."`
	Shows         rect   `json:"shows" jsonschema:"The part of the display the image shows, in real display pixels (the whole display unless region was given)."`
	Format        string `json:"format"`
	MIME          string `json:"mime"`
	Bytes         int    `json:"bytes"`
	Display       int    `json:"display"`
	SavedTo       string `json:"saved_to,omitempty"`
}

type displaysIn struct {
	Target string `json:"target,omitempty" jsonschema:"Remote machine: 9-digit RemoHop ID, saved alias or host:port. Optional if the server has a default target."`
}

type displaysOut struct {
	Displays []screenshot.Display `json:"displays"`
}

type infoIn struct {
	Target string `json:"target,omitempty" jsonschema:"Remote machine: 9-digit RemoHop ID, saved alias or host:port. Optional if the server has a default target."`
}

func (s *Server) registerScreenshot() {
	addTool(s, "remohop_screenshot", "Capture the remote screen",
		"Take a screenshot of the remote machine's screen and return it as an image you can look at, plus its dimensions. "+
			"Use it to see UI state, error dialogs or what a GUI program is doing, and before and after every remohop_click / remohop_type / remohop_key to observe and verify. "+
			"Downscaled to 1568 px wide by default; pass region (real display pixels) to zoom in on a part. "+
			"The result reports the image size, the real display size and which part of the display the image shows; the pointer tools take coordinates in the image you received. "+
			"Fails with a \"no display\" error on headless machines (check remohop_info: display=false). Use remohop_displays first on multi-monitor machines.",
		readOnly(), s.screenshot)
	addTool(s, "remohop_displays", "List remote monitors",
		"List the monitors of the remote machine (index, name, position, size, which is primary) for use as the display argument of remohop_screenshot.",
		readOnly(), s.displays)
}

func (s *Server) registerSystem() {
	addTool(s, "remohop_info", "Describe a remote machine",
		"Report what the remote machine is: hostname, OS and distribution, kernel, architecture, CPU, memory, disk, uptime, load, current user, home, shell, time zone, whether it has a display, the RemoHop version, and how the connection travels (peer to peer or relayed). The os field tells you which shell remohop_exec commands must be written for (windows: PowerShell; otherwise POSIX sh). "+
			"A cheap first call to learn which commands and paths make sense there, and to confirm the connection works.",
		readOnly(), s.info)
}

func (s *Server) screenshot(ctx context.Context, in screenshotIn) (*reply[screenshotOut], error) {
	format := strings.ToLower(strings.TrimSpace(in.Format))
	switch format {
	case "", "png":
		format = screenshot.PNG
	case "jpeg", "jpg":
		format = screenshot.JPEG
	default:
		return nil, fmt.Errorf("format %q is not supported; use png or jpeg", in.Format)
	}
	if in.Display < screenshot.AllDisplays {
		return nil, errors.New("display must be -1 (all monitors) or a monitor index from remohop_displays")
	}
	if in.MaxWidth < 0 || in.MaxWidth > 8192 || in.Quality < 0 || in.Quality > 100 {
		return nil, errors.New("max_width must be 1-8192 and quality 1-100")
	}
	var region image.Rectangle
	if r := in.Region; r != nil {
		if r.X < 0 || r.Y < 0 || r.Width <= 0 || r.Height <= 0 || r.X > 1<<16 || r.Y > 1<<16 || r.Width > 1<<16 || r.Height > 1<<16 {
			return nil, errors.New("region needs x, y >= 0 and width, height > 0, in real display pixels")
		}
		region = image.Rect(r.X, r.Y, r.X+r.Width, r.Y+r.Height)
	}
	var savePath string
	if in.SavePath != "" {
		p, err := s.guardWrite(in.SavePath)
		if err != nil {
			return nil, err
		}
		if _, err := os.Lstat(p); err == nil && !in.Overwrite {
			return nil, fmt.Errorf("%s already exists; pass overwrite=true to replace it", p)
		}
		savePath = p
	}
	var (
		img  *screenshot.Image
		disp screenshot.Display
	)
	err := s.withConn(ctx, in.Target, true, func(ctx context.Context, c *peer.Conn) error {
		// The display's real size is what turns image pixels back into
		// screen positions for the input tools.
		ds, err := screenshot.Displays(ctx, c)
		if err != nil {
			return err
		}
		if disp, err = screenshot.SelectDisplay(ds, displaySpec(in.Display)); err != nil {
			return err
		}
		img, err = screenshot.Capture(ctx, c, screenshot.Options{
			Display: in.Display, Format: format, Quality: in.Quality, MaxWidth: clamp(in.MaxWidth, defaultMaxWidth, 8192), Region: region,
		})
		return err
	})
	if err != nil {
		return nil, displayErr(err)
	}
	mime := "image/png"
	if img.Format == screenshot.JPEG {
		mime = "image/jpeg"
	}
	shows := rect{Width: disp.Width, Height: disp.Height}
	if !img.Region.Empty() {
		shows = rect{X: img.Region.Min.X, Y: img.Region.Min.Y, Width: img.Region.Dx(), Height: img.Region.Dy()}
	}
	out := screenshotOut{
		Width: img.Width, Height: img.Height, DisplayWidth: disp.Width, DisplayHeight: disp.Height, Shows: shows,
		Format: img.Format, MIME: mime, Bytes: len(img.Data), Display: img.Display,
	}
	if target, err := s.resolveTarget(in.Target); err == nil {
		s.views.remember(target, view{display: in.Display, imgW: img.Width, imgH: img.Height, shows: shows})
	}
	if savePath != "" {
		if _, err := writeLocal(savePath, bytes.NewReader(img.Data), 0o600, in.Overwrite, int64(len(img.Data))); err != nil {
			return nil, fmt.Errorf("saving screenshot: %w", err)
		}
		out.SavedTo = savePath
	}
	text := fmt.Sprintf("screenshot of display %d: image %dx%d %s (%d bytes) showing %dx%d display pixels at %d,%d of a %dx%d display.\n"+
		"For remohop_click / remohop_move / remohop_scroll, give x,y as pixel positions in THIS image; they are converted to the real screen position for you.",
		out.Display, out.Width, out.Height, out.Format, out.Bytes, shows.Width, shows.Height, shows.X, shows.Y, out.DisplayWidth, out.DisplayHeight)
	if out.SavedTo != "" {
		text += ", saved to " + out.SavedTo
	}
	r := &reply[screenshotOut]{Out: out, Content: []sdk.Content{&sdk.TextContent{Text: text}}}
	if len(img.Data) > maxInlineImage {
		if out.SavedTo == "" {
			return nil, fmt.Errorf("the image is %d bytes, too large to return inline; use format jpeg, a smaller max_width, or save_path", len(img.Data))
		}
		r.Content[0].(*sdk.TextContent).Text += " (too large to return inline)"
		return r, nil
	}
	r.Content = append(r.Content, &sdk.ImageContent{Data: img.Data, MIMEType: mime})
	return r, nil
}

// displaySpec converts the tools' numeric display argument to the spec the
// services select displays by.
func displaySpec(n int) string {
	if n == screenshot.AllDisplays {
		return "all"
	}
	return strconv.Itoa(n)
}

// displayErr adds what a "no display" failure means; other errors (a
// switched-off feature, a bad index) pass through untouched.
func displayErr(err error) error {
	if strings.Contains(err.Error(), "no display available") {
		return fmt.Errorf("%w\n(the remote machine is headless, or nobody is logged in to a graphical session there)", err)
	}
	return err
}

func (s *Server) displays(ctx context.Context, in displaysIn) (*reply[displaysOut], error) {
	var list []screenshot.Display
	err := s.withConn(ctx, in.Target, true, func(ctx context.Context, c *peer.Conn) (err error) {
		list, err = screenshot.Displays(ctx, c)
		return err
	})
	if err != nil {
		return nil, displayErr(err)
	}
	if list == nil {
		list = []screenshot.Display{}
	}
	cleanStrings(&list)
	var b strings.Builder
	for _, d := range list {
		p := ""
		if d.Primary {
			p = " (primary)"
		}
		fmt.Fprintf(&b, "%d  %dx%d at %d,%d  %s%s\n", d.Index, d.Width, d.Height, d.X, d.Y, d.Name, p)
	}
	if len(list) == 0 {
		b.WriteString("no displays\n")
	}
	return textReply(displaysOut{Displays: list}, b.String()), nil
}

// infoOut is the host's description plus how this session reaches it.
type infoOut struct {
	sysinfo.Info
	ConnectionPath string `json:"connection_path,omitempty" jsonschema:"How this session reaches the machine: lan, direct or punched are peer to peer; relayed goes through the rendezvous server and is slower (avoid large transfers and expect laggy screenshots)."`
}

func (s *Server) info(ctx context.Context, in infoIn) (*reply[infoOut], error) {
	var info *sysinfo.Info
	var path string
	err := s.withConn(ctx, in.Target, true, func(ctx context.Context, c *peer.Conn) (err error) {
		path = connPath(c)
		info, err = sysinfo.Get(ctx, c)
		return err
	})
	if err != nil {
		return nil, err
	}
	cleanStrings(info)
	gib := func(n uint64) string { return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30)) }
	var b strings.Builder
	fmt.Fprintf(&b, "host: %s\nos: %s %s %s (%s), kernel %s\n", info.Hostname, info.OS, info.DistroName, info.DistroVersion, info.Arch, info.Kernel)
	fmt.Fprintf(&b, "cpu: %s, %d cores, load %.2f %.2f %.2f\n", info.CPUModel, info.CPUCores, info.LoadAverage[0], info.LoadAverage[1], info.LoadAverage[2])
	fmt.Fprintf(&b, "memory: %s available of %s\ndisk (/): %s free of %s\n", gib(info.MemAvailBytes), gib(info.MemTotalBytes), gib(info.RootFreeBytes), gib(info.RootTotalBytes))
	fmt.Fprintf(&b, "uptime: %ds\nuser: %s (uid %s), home %s, shell %s, cwd %s\n", info.UptimeSeconds, info.User, info.UID, info.Home, info.Shell, info.Cwd)
	fmt.Fprintf(&b, "time: %s (%s)\ndisplay: %t\nremohop: %s\n", info.LocalTime, info.Timezone, info.Display, info.RemoHopVersion)
	switch path {
	case "":
	case string(dial.PathRelayed):
		b.WriteString("connection: relayed through the rendezvous server (slower than a direct path; keep transfers and screenshots small)\n")
	default:
		fmt.Fprintf(&b, "connection: %s (peer to peer)\n", path)
	}
	return textReply(infoOut{Info: *info, ConnectionPath: path}, b.String()), nil
}
