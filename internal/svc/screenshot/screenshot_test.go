package screenshot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/peer/peertest"
	"github.com/readori/RemoHop/internal/wire"
)

// fakeGrabber serves two monitors: a 200x100 primary and a 100x100 second.
type fakeGrabber struct{ err error }

func (f fakeGrabber) Displays(context.Context) ([]Display, error) {
	if f.err != nil {
		return nil, f.err
	}
	return []Display{
		{Index: 0, Name: "left", Width: 200, Height: 100, Primary: true},
		{Index: 1, Name: "right", X: 200, Width: 100, Height: 100},
	}, nil
}

func (f fakeGrabber) Grab(_ context.Context, display int) (image.Image, error) {
	if f.err != nil {
		return nil, f.err
	}
	w := map[int]int{0: 200, 1: 100, AllDisplays: 300}[display]
	if w == 0 {
		return nil, errors.New("no such display")
	}
	img := image.NewRGBA(image.Rect(0, 0, w, 100))
	for y := range 100 {
		for x := range w {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), uint8(display + 1), 255})
		}
	}
	return img, nil
}

func useGrabber(t *testing.T, g grabber) {
	t.Helper()
	old := setGrabber(g)
	t.Cleanup(func() { setGrabber(old) })
}

func ctxT(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestCaptureOptions(t *testing.T) {
	useGrabber(t, fakeGrabber{})
	client, _ := peertest.Pair(t)
	ctx := ctxT(t)

	img, err := Capture(ctx, client, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if img.Format != PNG || img.Width != 200 || img.Height != 100 {
		t.Fatalf("got %s %dx%d", img.Format, img.Width, img.Height)
	}
	dec, err := png.Decode(bytes.NewReader(img.Data))
	if err != nil {
		t.Fatal(err)
	}
	if c := color.RGBAModel.Convert(dec.At(50, 20)).(color.RGBA); c != (color.RGBA{50, 20, 1, 255}) {
		t.Fatalf("pixel %v", c)
	}

	img, err = Capture(ctx, client, Options{Display: 1})
	if err != nil || img.Width != 100 || img.Display != 1 {
		t.Fatalf("display 1: %+v %v", img, err)
	}
	img, err = Capture(ctx, client, Options{Display: AllDisplays})
	if err != nil || img.Width != 300 {
		t.Fatalf("all: %+v %v", img, err)
	}

	img, err = Capture(ctx, client, Options{Format: "jpg", Quality: 50, MaxWidth: 100})
	if err != nil {
		t.Fatal(err)
	}
	if img.Format != JPEG || img.Width != 100 || img.Height != 50 {
		t.Fatalf("jpeg+resize: %s %dx%d", img.Format, img.Width, img.Height)
	}
	if _, err := jpeg.Decode(bytes.NewReader(img.Data)); err != nil {
		t.Fatal(err)
	}
	// MaxWidth larger than the image leaves it alone.
	img, err = Capture(ctx, client, Options{MaxWidth: 5000})
	if err != nil || img.Width != 200 {
		t.Fatalf("large max width: %+v %v", img, err)
	}

	for _, bad := range []Options{{Format: "gif"}, {Quality: 101}, {MaxWidth: -1}} {
		if _, err := Capture(ctx, client, bad); err == nil {
			t.Errorf("%+v should be rejected", bad)
		}
	}
	if _, err := Capture(ctx, client, Options{Display: 7}); err == nil || !strings.Contains(err.Error(), "no such display") {
		t.Fatalf("bad display: %v", err)
	}

	ds, err := Displays(ctx, client)
	if err != nil || len(ds) != 2 || !ds[0].Primary || ds[1].X != 200 {
		t.Fatalf("displays %+v %v", ds, err)
	}
}

func TestHostRejectsBadRequests(t *testing.T) {
	useGrabber(t, fakeGrabber{})
	client, _ := peertest.Pair(t)
	ctx := ctxT(t)
	for _, req := range []request{
		{Op: "capture", Format: "bmp"},
		{Op: "capture", Quality: 500},
		{Op: "capture", MaxWidth: 1 << 30},
		{Op: "capture", Display: -5},
		{Op: "launch"},
	} {
		s, err := client.Open(ctx, service, req)
		if err != nil {
			t.Fatal(err)
		}
		var h header
		if err := wire.ReadJSON(s, &h); err != nil || h.Error == "" {
			t.Errorf("%+v: header %+v err %v", req, h, err)
		}
		s.Close()
	}
}

func TestGrabErrorIsReported(t *testing.T) {
	useGrabber(t, fakeGrabber{err: errors.New("no display available on the host: nothing there")})
	client, _ := peertest.Pair(t)
	if _, err := Capture(ctxT(t), client, Options{}); err == nil || !strings.Contains(err.Error(), "no display available on the host") {
		t.Fatalf("err %v", err)
	}
	if _, err := Displays(ctxT(t), client); err == nil {
		t.Fatal("Displays should fail too")
	}
}

func TestDownscale(t *testing.T) {
	// 4x4 black/white columns average to mid grey at half width.
	src := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := range 4 {
		for x := range 4 {
			v := uint8(0)
			if x%2 == 1 {
				v = 255
			}
			src.Set(x, y, color.RGBA{v, v, v, 255})
		}
	}
	dst := downscale(src, 2)
	if dst.Bounds().Dx() != 2 || dst.Bounds().Dy() != 2 {
		t.Fatalf("bounds %v", dst.Bounds())
	}
	if c := dst.RGBAAt(0, 0); c.R != 128 || c.A != 255 {
		t.Fatalf("pixel %v", c)
	}
	// Uniform colours stay exact for non-integer ratios, and tiny heights stay >= 1.
	flat := image.NewRGBA(image.Rect(0, 0, 1000, 3))
	for i := 0; i < len(flat.Pix); i += 4 {
		flat.Pix[i], flat.Pix[i+1], flat.Pix[i+2], flat.Pix[i+3] = 10, 200, 33, 255
	}
	d := downscale(flat, 333)
	if d.Bounds().Dx() != 333 || d.Bounds().Dy() < 1 {
		t.Fatalf("bounds %v", d.Bounds())
	}
	if c := d.RGBAAt(332, 0); c != (color.RGBA{10, 200, 33, 255}) {
		t.Fatalf("pixel %v", c)
	}
	// A non-zero origin and a non-RGBA type are handled.
	g := image.NewGray(image.Rect(10, 10, 14, 14))
	if down := downscale(g, 2); down.Bounds().Dx() != 2 {
		t.Fatalf("gray bounds %v", down.Bounds())
	}
}

func TestOrderDisplays(t *testing.T) {
	ds := orderDisplays([]Display{{X: 1920}, {X: 0}, {X: 3840, Primary: true}})
	if ds[0].X != 3840 || ds[1].X != 0 || ds[2].X != 1920 || ds[0].Index != 0 || ds[2].Index != 2 {
		t.Fatalf("%+v", ds)
	}
	ds = orderDisplays([]Display{{X: 10}, {X: 0}})
	if ds[0].X != 0 || !ds[0].Primary {
		t.Fatalf("no primary flagged: %+v", ds)
	}
}

func newEnv(client *peer.Conn, stdout, stderr *bytes.Buffer) *cli.Env {
	return &cli.Env{
		Target: "test", Stdin: strings.NewReader(""), Stdout: stdout, Stderr: stderr,
		DialFunc: func(context.Context, *cli.Env) (*peer.Conn, error) { return client, nil },
	}
}

func runCLI(t *testing.T, client *peer.Conn, args ...string) (code int, stdout, stderr *bytes.Buffer) {
	t.Helper()
	stdout, stderr = &bytes.Buffer{}, &bytes.Buffer{}
	code = cli.Lookup("screenshot").Run(ctxT(t), newEnv(client, stdout, stderr), args)
	return
}

func TestCLI(t *testing.T) {
	useGrabber(t, fakeGrabber{})
	client, _ := peertest.Pair(t)
	dir := t.TempDir()

	// Explicit file with --json.
	out := filepath.Join(dir, "screen.png")
	code, so, se := runCLI(t, client, "-o", out, "--json")
	if code != cli.ExitOK {
		t.Fatalf("exit %d: %s", code, se)
	}
	var res result
	if err := json.Unmarshal(so.Bytes(), &res); err != nil {
		t.Fatalf("json %q: %v", so, err)
	}
	if res.Path != out || res.Width != 200 || res.Height != 100 || res.Format != "png" || res.Bytes == 0 {
		t.Fatalf("result %+v", res)
	}
	data, err := os.ReadFile(out)
	if err != nil || len(data) != res.Bytes {
		t.Fatalf("file: %v (%d bytes, want %d)", err, len(data), res.Bytes)
	}

	// Default name in the cwd: remohop-<target>-<time>.png.
	t.Chdir(dir)
	code, so, se = runCLI(t, client)
	if code != cli.ExitOK {
		t.Fatalf("exit %d: %s", code, se)
	}
	m, _ := filepath.Glob(filepath.Join(dir, "remohop-test-*.png"))
	if len(m) != 1 || !regexp.MustCompile(`remohop-test-\d{8}-\d{6}\.png$`).MatchString(m[0]) {
		t.Fatalf("default file %v", m)
	}
	if !strings.Contains(so.String(), "200x100 png") {
		t.Fatalf("summary %q", so)
	}

	// Extension picks jpeg; --max-width downsizes.
	out = filepath.Join(dir, "small.jpg")
	if code, _, se = runCLI(t, client, "-o", out, "--max-width", "50", "--quality", "60"); code != cli.ExitOK {
		t.Fatalf("exit %d: %s", code, se)
	}
	data, _ = os.ReadFile(out)
	if cfg, err := jpeg.DecodeConfig(bytes.NewReader(data)); err != nil || cfg.Width != 50 {
		t.Fatalf("jpeg %v %v", cfg, err)
	}

	// -o - streams bytes on stdout and the summary on stderr.
	code, so, se = runCLI(t, client, "-o", "-", "--display", "all")
	if code != cli.ExitOK {
		t.Fatalf("exit %d: %s", code, se)
	}
	if cfg, err := png.DecodeConfig(bytes.NewReader(so.Bytes())); err != nil || cfg.Width != 300 {
		t.Fatalf("stdout image %v %v", cfg, err)
	}
	if !strings.Contains(se.String(), "300x100") {
		t.Fatalf("stderr %q", se)
	}

	// --list, text and JSON.
	code, so, _ = runCLI(t, client, "--list")
	if code != cli.ExitOK || !strings.Contains(so.String(), "0  200x100+0+0  primary  left") || !strings.Contains(so.String(), "1  100x100+200+0  right") {
		t.Fatalf("list %q", so)
	}
	code, so, _ = runCLI(t, client, "--list", "--json")
	var ds []Display
	if code != cli.ExitOK || json.Unmarshal(so.Bytes(), &ds) != nil || len(ds) != 2 {
		t.Fatalf("list json %q", so)
	}

	for _, args := range [][]string{{"--display", "-1"}, {"-d", ""}, {"-R", "1,2,3"}, {"-R", "0,0,0,5"}, {"-R", "-1,0,5,5"}, {"--format", "gif"}, {"--quality", "200"}, {"extra"}, {"--bogus"}} {
		if code, _, _ := runCLI(t, client, args...); code != cli.ExitUsage {
			t.Errorf("%v: exit %d, want usage", args, code)
		}
	}
	if code, _, se := runCLI(t, client, "--display", "9"); code != cli.ExitError || !strings.Contains(se.String(), "no such display") {
		t.Fatalf("bad display: exit %d %q", code, se)
	}

	// Displays by name (as --list prints them, case-insensitive as a
	// fallback), and an unknown name lists what exists.
	for _, name := range []string{"right", "RIGHT"} {
		code, so, se = runCLI(t, client, "-d", name, "-o", "-")
		if code != cli.ExitOK {
			t.Fatalf("-d %s: exit %d %s", name, code, se)
		}
		if cfg, err := png.DecodeConfig(bytes.NewReader(so.Bytes())); err != nil || cfg.Width != 100 {
			t.Fatalf("-d %s: image %v %v", name, cfg, err)
		}
	}
	if code, _, se := runCLI(t, client, "--display", "middle"); code != cli.ExitError ||
		!strings.Contains(se.String(), `no display named "middle"`) || !strings.Contains(se.String(), "1 (right)") {
		t.Fatalf("unknown name: exit %d %q", code, se)
	}

	// -R crops to a region of the selected display, reported in --json.
	code, so, se = runCLI(t, client, "-d", "right", "-R", "10,20,30,40", "-o", filepath.Join(dir, "r.png"), "--json")
	if code != cli.ExitOK {
		t.Fatalf("-R: exit %d %s", code, se)
	}
	res = result{}
	if err := json.Unmarshal(so.Bytes(), &res); err != nil || res.Width != 30 || res.Height != 40 || res.Display != 1 ||
		res.Region == nil || *res.Region != (rectJSON{10, 20, 30, 40}) {
		t.Fatalf("-R result %s (%v)", so, err)
	}
	data, _ = os.ReadFile(res.Path)
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if got := color.NRGBAModel.Convert(img.At(0, 0)); got != (color.NRGBA{10, 20, 2, 255}) {
		t.Fatalf("-R pixel (0,0) = %v", got)
	}
}

func TestCaptureRegion(t *testing.T) {
	useGrabber(t, fakeGrabber{})
	client, _ := peertest.Pair(t)
	ctx := ctxT(t)

	// Pixel-exact: the fake encodes (x, y, display+1) in every pixel.
	img, err := Capture(ctx, client, Options{Region: image.Rect(5, 7, 25, 17)})
	if err != nil {
		t.Fatal(err)
	}
	if img.Width != 20 || img.Height != 10 || img.Region != image.Rect(5, 7, 25, 17) {
		t.Fatalf("region capture %dx%d %v", img.Width, img.Height, img.Region)
	}
	dec, _ := png.Decode(bytes.NewReader(img.Data))
	for _, p := range []image.Point{{0, 0}, {19, 9}, {7, 3}} {
		want := color.NRGBA{uint8(5 + p.X), uint8(7 + p.Y), 1, 255}
		if got := color.NRGBAModel.Convert(dec.At(p.X, p.Y)); got != want {
			t.Errorf("pixel %v = %v, want %v", p, got, want)
		}
	}

	// Clipped at the display edge; the clipped rectangle is reported.
	img, err = Capture(ctx, client, Options{Display: 1, Region: image.Rect(90, 90, 150, 150)})
	if err != nil || img.Width != 10 || img.Height != 10 || img.Region != image.Rect(90, 90, 100, 100) {
		t.Fatalf("clipped: %+v %v", img, err)
	}
	// Downscaling applies to the crop.
	img, err = Capture(ctx, client, Options{Region: image.Rect(0, 0, 100, 50), MaxWidth: 50})
	if err != nil || img.Width != 50 || img.Height != 25 {
		t.Fatalf("crop+downscale: %+v %v", img, err)
	}
	// Entirely outside the display, or malformed.
	if _, err := Capture(ctx, client, Options{Region: image.Rect(500, 500, 510, 510)}); err == nil || !strings.Contains(err.Error(), "outside the display") {
		t.Fatalf("outside: %v", err)
	}
	if _, err := Capture(ctx, client, Options{Region: image.Rect(-5, 0, 5, 5)}); err == nil {
		t.Fatal("negative region accepted")
	}
}

func TestSelectDisplay(t *testing.T) {
	ds, _ := fakeGrabber{}.Displays(context.Background())
	for spec, want := range map[string]int{"": 0, "0": 0, "1": 1, "left": 0, "right": 1, "Right": 1, " right ": 1} {
		d, err := SelectDisplay(ds, spec)
		if err != nil || d.Index != want {
			t.Errorf("%q: %+v %v", spec, d, err)
		}
	}
	all, err := SelectDisplay(ds, "all")
	if err != nil || all.Index != AllDisplays || all.X != 0 || all.Width != 300 || all.Height != 100 {
		t.Fatalf("all: %+v %v", all, err)
	}
	for _, bad := range []string{"2", "-1", "center"} {
		if _, err := SelectDisplay(ds, bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := SelectDisplay(nil, ""); err == nil || !strings.Contains(err.Error(), "no display available") {
		t.Fatalf("empty list: %v", err)
	}
}
