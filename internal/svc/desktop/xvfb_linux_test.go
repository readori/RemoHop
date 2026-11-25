package desktop

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"

	"github.com/readori/RemoHop/internal/x11disp/xvfbtest"
)

// startXvfb runs a private X server on a free display (Xvfb picks it and
// reports it through -displayfd), points DISPLAY at it, and returns a
// connection to it.
func startXvfb(t *testing.T, w, h int) *xgb.Conn {
	t.Helper()
	if _, err := exec.LookPath("Xvfb"); err != nil {
		t.Skip("Xvfb not installed")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("Xvfb", "-displayfd", "3", "-screen", "0", fmt.Sprintf("%dx%dx24", w, h),
		"-nolisten", "tcp", "-noreset")
	cmd.ExtraFiles = []*os.File{pw}
	// Die with the test binary even if it is killed.
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pw.Close()
	var display string // known once Xvfb reports it
	t.Cleanup(func() { xvfbtest.Stop(cmd, display, nil) })
	line := make(chan string, 1)
	go func() {
		s, _ := bufio.NewReader(pr).ReadString('\n')
		line <- s
		pr.Close()
	}()
	var n string
	select {
	case n = <-line:
	case <-time.After(10 * time.Second):
		t.Fatal("Xvfb did not start")
	}
	n = strings.TrimSpace(n)
	if _, err := strconv.Atoi(n); err != nil {
		t.Fatalf("Xvfb display %q", n)
	}
	display = ":" + n
	t.Setenv("DISPLAY", display)
	t.Setenv("WAYLAND_DISPLAY", "")
	c, err := xgb.NewConnDisplay(":" + n)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

// paint fills rectangles on the root window. Colors are 0xRRGGBB (24-bit
// TrueColor).
type rect struct {
	x, y, w, h int
	color      uint32
}

func paint(t *testing.T, c *xgb.Conn, rs ...rect) {
	t.Helper()
	root := xproto.Setup(c).DefaultScreen(c).Root
	gc, err := xproto.NewGcontextId(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := xproto.CreateGCChecked(c, gc, xproto.Drawable(root), 0, nil).Check(); err != nil {
		t.Fatal(err)
	}
	defer xproto.FreeGC(c, gc)
	for _, r := range rs {
		xproto.ChangeGC(c, gc, xproto.GcForeground, []uint32{r.color})
		if err := xproto.PolyFillRectangleChecked(c, xproto.Drawable(root), gc,
			[]xproto.Rectangle{{X: int16(r.x), Y: int16(r.y), Width: uint16(r.w), Height: uint16(r.h)}}).Check(); err != nil {
			t.Fatal(err)
		}
	}
}

// writeIVF reassembles received frames into an IVF file standard tools read.
func writeIVF(t *testing.T, path string, w, h int, frames []*Frame) {
	t.Helper()
	var b bytes.Buffer
	hdr := make([]byte, 32)
	copy(hdr[0:4], "DKIF")
	binary.LittleEndian.PutUint16(hdr[6:8], 32)
	copy(hdr[8:12], "AV01")
	binary.LittleEndian.PutUint16(hdr[12:14], uint16(w))
	binary.LittleEndian.PutUint16(hdr[14:16], uint16(h))
	binary.LittleEndian.PutUint32(hdr[16:20], 1000000)
	binary.LittleEndian.PutUint32(hdr[20:24], 1)
	b.Write(hdr)
	for _, f := range frames {
		var fh [12]byte
		binary.LittleEndian.PutUint32(fh[0:4], uint32(len(f.Data)))
		binary.LittleEndian.PutUint64(fh[4:12], uint64(f.PTS.Microseconds()))
		b.Write(fh[:])
		b.Write(f.Data)
	}
	if err := os.WriteFile(path, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

// decodeLast decodes an IVF file with ffmpeg and returns the last frame as
// packed RGB, plus its dimensions as ffprobe-free parsing of the PPM header.
func decodeLast(t *testing.T, ivf string) (w, h int, rgb []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out := filepath.Join(filepath.Dir(ivf), "last.ppm")
	cmd := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-i", ivf,
		"-update", "1", "-f", "image2", "-c:v", "ppm", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg decode: %v\n%s", err, b)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(bytes.NewReader(data))
	var magic string
	var maxv int
	if _, err := fmt.Fscan(r, &magic, &w, &h, &maxv); err != nil || magic != "P6" {
		t.Fatalf("bad ppm: %v %q", err, magic)
	}
	r.ReadByte()
	rgb, _ = io.ReadAll(r)
	if len(rgb) < w*h*3 {
		t.Fatalf("short ppm")
	}
	return w, h, rgb
}

func pixel(rgb []byte, w, x, y int) (r, g, b int) {
	i := (y*w + x) * 3
	return int(rgb[i]), int(rgb[i+1]), int(rgb[i+2])
}
