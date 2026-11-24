package screenshot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/wire"
)

const service = "screenshot"

const (
	// Primary is the zero-value Options.Display: the primary monitor, which
	// is always index 0 in the list Displays returns.
	Primary = 0
	// AllDisplays selects the whole virtual desktop (every monitor).
	AllDisplays = -1
)

const (
	// maxImageBytes bounds the encoded image either side will handle.
	maxImageBytes = 128 << 20
	// maxDimension bounds a plausible image edge.
	maxDimension = 1 << 16
	// captureTimeout bounds one host-side capture.
	captureTimeout = 30 * time.Second
	defaultQuality = 80
)

// Formats the service can produce.
const (
	PNG  = "png"
	JPEG = "jpeg"
)

// Options selects what to capture and how to encode it. The zero value
// captures the primary display as PNG at full size.
type Options struct {
	Display  int    // index from Displays; Primary (0) or AllDisplays (-1)
	Format   string // PNG (default) or JPEG
	Quality  int    // JPEG quality 1-100 (default 80)
	MaxWidth int    // downscale to at most this many pixels wide; 0 keeps full size
	// Region, if not empty, captures only this rectangle, in pixels of the
	// selected display (origin at its top-left). It is clipped to the
	// display; MaxWidth applies to the cropped image.
	Region image.Rectangle
}

// Image is a captured screenshot, already encoded.
type Image struct {
	Data    []byte
	Format  string
	Width   int
	Height  int
	Display int
	// Region is the captured rectangle in display pixels after clipping,
	// empty when the whole display was captured.
	Region image.Rectangle
}

// Display describes one monitor of the remote machine.
type Display struct {
	Index   int    `json:"index"`
	Name    string `json:"name,omitempty"`
	X       int    `json:"x"`
	Y       int    `json:"y"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
	Primary bool   `json:"primary"`
}

type request struct {
	Op       string    `json:"op"`
	Display  int       `json:"display"`
	Format   string    `json:"format,omitempty"`
	Quality  int       `json:"quality,omitempty"`
	MaxWidth int       `json:"max_width,omitempty"`
	Region   *rectJSON `json:"region,omitempty"`
}

// rectJSON is a rectangle on the wire: origin and size.
type rectJSON struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"width"`
	H int `json:"height"`
}

func (r rectJSON) rect() image.Rectangle { return image.Rect(r.X, r.Y, r.X+r.W, r.Y+r.H) }

func toRectJSON(r image.Rectangle) *rectJSON {
	return &rectJSON{X: r.Min.X, Y: r.Min.Y, W: r.Dx(), H: r.Dy()}
}

// validRegion bounds a requested region to plausible screen coordinates.
func validRegion(r image.Rectangle) bool {
	return r.Min.X >= 0 && r.Min.Y >= 0 && r.Dx() > 0 && r.Dy() > 0 &&
		r.Max.X <= maxDimension && r.Max.Y <= maxDimension
}

type header struct {
	Error    string    `json:"error,omitempty"`
	Width    int       `json:"width,omitempty"`
	Height   int       `json:"height,omitempty"`
	Format   string    `json:"format,omitempty"`
	Size     int       `json:"size,omitempty"`
	Display  int       `json:"display,omitempty"`
	Displays []Display `json:"displays,omitempty"`
	Region   *rectJSON `json:"region,omitempty"`
}

// Capture takes a screenshot of the remote machine.
func Capture(ctx context.Context, conn *peer.Conn, opts Options) (*Image, error) {
	format, err := normalizeFormat(opts.Format)
	if err != nil {
		return nil, err
	}
	if opts.MaxWidth < 0 || opts.MaxWidth > maxDimension {
		return nil, fmt.Errorf("screenshot: invalid max width %d", opts.MaxWidth)
	}
	if opts.Quality < 0 || opts.Quality > 100 {
		return nil, fmt.Errorf("screenshot: invalid JPEG quality %d (want 1-100)", opts.Quality)
	}
	req := request{Op: "capture", Display: opts.Display, Format: format, Quality: opts.Quality, MaxWidth: opts.MaxWidth}
	if !opts.Region.Empty() {
		if !validRegion(opts.Region) {
			return nil, fmt.Errorf("screenshot: invalid region %v", opts.Region)
		}
		req.Region = toRectJSON(opts.Region)
	}
	s, err := conn.Open(ctx, service, req)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	stop := context.AfterFunc(ctx, s.Reset)
	defer stop()

	fail := func(err error) (*Image, error) {
		if ce := ctx.Err(); ce != nil {
			return nil, ce
		}
		return nil, err
	}
	var h header
	if err := wire.ReadJSON(s, &h); err != nil {
		return fail(fmt.Errorf("screenshot: reading reply: %w", err))
	}
	if h.Error != "" {
		return nil, errors.New(h.Error)
	}
	if h.Size <= 0 || h.Size > maxImageBytes {
		return nil, fmt.Errorf("screenshot: host announced a bad image size (%d bytes)", h.Size)
	}
	if h.Format != format {
		return nil, fmt.Errorf("screenshot: host returned %q, asked for %q", h.Format, format)
	}
	data := make([]byte, h.Size)
	if _, err := io.ReadFull(s, data); err != nil {
		return fail(fmt.Errorf("screenshot: reading image: %w", err))
	}
	// Do not take the host's word for the geometry we report.
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("screenshot: host sent an undecodable image: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > maxDimension || cfg.Height > maxDimension {
		return nil, fmt.Errorf("screenshot: implausible image size %dx%d", cfg.Width, cfg.Height)
	}
	img := &Image{Data: data, Format: format, Width: cfg.Width, Height: cfg.Height, Display: h.Display}
	if req.Region != nil {
		// A host predating regions would silently send the whole display.
		if h.Region == nil {
			return nil, errors.New("screenshot: the host does not support capturing a region (update remohop on the host)")
		}
		img.Region = h.Region.rect()
		if !img.Region.In(opts.Region) || (opts.MaxWidth == 0 && (img.Region.Dx() != cfg.Width || img.Region.Dy() != cfg.Height)) {
			return nil, fmt.Errorf("screenshot: host returned region %v for requested %v", img.Region, opts.Region)
		}
	}
	return img, nil
}

// Displays lists the remote machine's monitors, primary first.
func Displays(ctx context.Context, conn *peer.Conn) ([]Display, error) {
	s, err := conn.Open(ctx, service, request{Op: "list"})
	if err != nil {
		return nil, err
	}
	defer s.Close()
	stop := context.AfterFunc(ctx, s.Reset)
	defer stop()
	var h header
	if err := wire.ReadJSON(s, &h); err != nil {
		if ce := ctx.Err(); ce != nil {
			return nil, ce
		}
		return nil, fmt.Errorf("screenshot: reading reply: %w", err)
	}
	if h.Error != "" {
		return nil, errors.New(h.Error)
	}
	if len(h.Displays) > 256 {
		return nil, errors.New("screenshot: host listed an implausible number of displays")
	}
	for i := range h.Displays {
		// The name is host-supplied text that callers print.
		h.Displays[i].Name = cli.Sanitize(h.Displays[i].Name)
	}
	return h.Displays, nil
}

func normalizeFormat(f string) (string, error) {
	switch strings.ToLower(f) {
	case "", PNG:
		return PNG, nil
	case JPEG, "jpg":
		return JPEG, nil
	}
	return "", fmt.Errorf("screenshot: unsupported format %q (want png or jpeg)", f)
}

func init() { peer.Handle(service, serve) }

var (
	grabMu sync.RWMutex
	// grabBackend is the capture backend of this host. Tests replace it with
	// setGrabber; handlers may still be winding down when they do.
	grabBackend = platformGrabber()
)

func currentGrabber() grabber {
	grabMu.RLock()
	defer grabMu.RUnlock()
	return grabBackend
}

func setGrabber(g grabber) (old grabber) {
	grabMu.Lock()
	defer grabMu.Unlock()
	old, grabBackend = grabBackend, g
	return old
}

// grabber captures the host's screen. Displays are indexed primary first;
// Grab accepts a display index or AllDisplays.
type grabber interface {
	Displays(ctx context.Context) ([]Display, error)
	Grab(ctx context.Context, display int) (image.Image, error)
}

func serve(ctx context.Context, c *peer.Conn, s peer.Stream, raw json.RawMessage) error {
	var req request
	if err := peer.DecodeReq(raw, &req); err != nil {
		return wire.WriteJSON(s, header{Error: "screenshot: bad request"})
	}
	ctx, cancel := context.WithTimeout(ctx, captureTimeout)
	defer cancel()

	switch req.Op {
	case "list":
		ds, err := currentGrabber().Displays(ctx)
		if err != nil {
			return wire.WriteJSON(s, header{Error: err.Error()})
		}
		return wire.WriteJSON(s, header{Displays: ds})
	case "capture", "":
		h, data, err := capture(ctx, req)
		if err != nil {
			return wire.WriteJSON(s, header{Error: err.Error()})
		}
		if err := wire.WriteJSON(s, h); err != nil {
			return err
		}
		_, err = s.Write(data)
		return err
	}
	return wire.WriteJSON(s, header{Error: fmt.Sprintf("screenshot: unknown operation %q", req.Op)})
}

func capture(ctx context.Context, req request) (header, []byte, error) {
	format, err := normalizeFormat(req.Format)
	if err != nil {
		return header{}, nil, err
	}
	quality := req.Quality
	if quality == 0 {
		quality = defaultQuality
	}
	if quality < 1 || quality > 100 || req.MaxWidth < 0 || req.MaxWidth > maxDimension || req.Display < AllDisplays ||
		(req.Region != nil && !validRegion(req.Region.rect())) {
		return header{}, nil, errors.New("screenshot: bad capture options")
	}
	img, err := currentGrabber().Grab(ctx, req.Display)
	if err != nil {
		return header{}, nil, err
	}
	var region *rectJSON
	if req.Region != nil {
		// Crop after grabbing: every backend captures whole displays, and
		// cropping here keeps the result pixel-exact on all of them.
		b := img.Bounds()
		r := req.Region.rect().Add(b.Min).Intersect(b)
		if r.Empty() {
			return header{}, nil, fmt.Errorf("screenshot: region %dx%d+%d+%d lies outside the display (%dx%d)",
				req.Region.W, req.Region.H, req.Region.X, req.Region.Y, b.Dx(), b.Dy())
		}
		img = crop(img, r)
		region = toRectJSON(r.Sub(b.Min))
	}
	if req.MaxWidth > 0 && img.Bounds().Dx() > req.MaxWidth {
		img = downscale(img, req.MaxWidth)
	}
	var buf bytes.Buffer
	switch format {
	case JPEG:
		err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality})
	default:
		err = png.Encode(&buf, img)
	}
	if err != nil {
		return header{}, nil, fmt.Errorf("screenshot: encoding: %w", err)
	}
	if buf.Len() > maxImageBytes {
		return header{}, nil, fmt.Errorf("screenshot: image is %d MiB, over the %d MiB limit; use a smaller max width or jpeg",
			buf.Len()>>20, maxImageBytes>>20)
	}
	b := img.Bounds()
	return header{Width: b.Dx(), Height: b.Dy(), Format: format, Size: buf.Len(), Display: req.Display, Region: region}, buf.Bytes(), nil
}

// crop returns the part r of img (r lies within its bounds).
func crop(img image.Image, r image.Rectangle) image.Image {
	if s, ok := img.(interface {
		SubImage(image.Rectangle) image.Image
	}); ok {
		return s.SubImage(r)
	}
	dst := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	draw.Draw(dst, dst.Bounds(), img, r.Min, draw.Src)
	return dst
}

// LocalDisplays lists this machine's monitors, primary first, numbered as
// the screenshot service numbers them for remote peers.
func LocalDisplays(ctx context.Context) ([]Display, error) {
	return currentGrabber().Displays(ctx)
}

// SelectDisplay picks a monitor from ds (as Displays returns them) by spec:
// "" for the primary, an index, "all" for the bounding box of every
// monitor (Index AllDisplays), or a name as Displays reports it (exact
// first, then ignoring case).
func SelectDisplay(ds []Display, spec string) (Display, error) {
	if len(ds) == 0 {
		return Display{}, errors.New("no display available on the host: no monitors reported")
	}
	spec = strings.TrimSpace(spec)
	switch spec {
	case "":
		return ds[0], nil
	case "all":
		u := image.Rect(ds[0].X, ds[0].Y, ds[0].X+ds[0].Width, ds[0].Y+ds[0].Height)
		for _, d := range ds[1:] {
			u = u.Union(image.Rect(d.X, d.Y, d.X+d.Width, d.Y+d.Height))
		}
		return Display{Index: AllDisplays, Name: "all", X: u.Min.X, Y: u.Min.Y, Width: u.Dx(), Height: u.Dy()}, nil
	}
	if n, err := strconv.Atoi(spec); err == nil {
		if n < 0 || n >= len(ds) {
			return Display{}, fmt.Errorf("no display %d (the host has %d: %s)", n, len(ds), listDisplays(ds))
		}
		return ds[n], nil
	}
	for _, d := range ds {
		if d.Name == spec {
			return d, nil
		}
	}
	for _, d := range ds {
		if strings.EqualFold(d.Name, spec) {
			return d, nil
		}
	}
	return Display{}, fmt.Errorf("no display named %q (the host has: %s)", spec, listDisplays(ds))
}

func listDisplays(ds []Display) string {
	var parts []string
	for _, d := range ds {
		p := strconv.Itoa(d.Index)
		if d.Name != "" {
			p += " (" + d.Name + ")"
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, ", ")
}

// ResolveDisplay turns a display spec (see SelectDisplay) into an index for
// Options.Display, asking the remote machine only when spec is a name.
func ResolveDisplay(ctx context.Context, conn *peer.Conn, spec string) (int, error) {
	spec = strings.TrimSpace(spec)
	switch {
	case spec == "":
		return Primary, nil
	case spec == "all":
		return AllDisplays, nil
	}
	if n, err := strconv.Atoi(spec); err == nil {
		if n < 0 {
			return 0, fmt.Errorf("screenshot: bad display %q (want an index, a name or \"all\")", spec)
		}
		return n, nil
	}
	ds, err := Displays(ctx, conn)
	if err != nil {
		return 0, err
	}
	d, err := SelectDisplay(ds, spec)
	if err != nil {
		return 0, err
	}
	return d.Index, nil
}

// orderDisplays puts the primary monitor first, the rest left to right,
// top to bottom, and numbers them. If none is flagged primary the first in
// that order is treated as primary.
func orderDisplays(ds []Display) []Display {
	sort.SliceStable(ds, func(i, j int) bool {
		if ds[i].Primary != ds[j].Primary {
			return ds[i].Primary
		}
		if ds[i].X != ds[j].X {
			return ds[i].X < ds[j].X
		}
		return ds[i].Y < ds[j].Y
	})
	ds[0].Primary = true
	for i := range ds {
		ds[i].Index = i
	}
	return ds
}
