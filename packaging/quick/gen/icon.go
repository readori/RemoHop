package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
)

// The icon is drawn here rather than kept as image files: a cobalt rounded
// square with the RemoHop mark (two screen corners and an arrow between
// them) in round-capped strokes, the same mark as the mobile apps, the
// website and internal/quick/assets/icon.svg. Coordinates are on a 64-unit
// grid, matching the SVG's viewBox.

var (
	blue  = color.NRGBA{0x25, 0x46, 0xd8, 0xff} // #2546D8
	paper = color.NRGBA{0xf7, 0xf8, 0xff, 0xff} // #F7F8FF
)

// markStroke is the stroke width of the mark; strokes have round caps and
// joins, so a point is on the mark when it is within half of it of one of
// the segments.
const markStroke = 5.5

// markPaths are the mark's polylines (the SVG path
// "M13 31V17H34 M30 47H51V33 M24 32H42 M35 25L42 32L35 39").
var markPaths = [][][2]float64{
	{{13, 31}, {13, 17}, {34, 17}},
	{{30, 47}, {51, 47}, {51, 33}},
	{{24, 32}, {42, 32}},
	{{35, 25}, {42, 32}, {35, 39}},
}

func inRect(x, y, x0, y0, x1, y1 float64) bool { return x >= x0 && x < x1 && y >= y0 && y < y1 }

func inRoundRect(x, y, x0, y0, x1, y1, r float64) bool {
	if !inRect(x, y, x0, y0, x1, y1) {
		return false
	}
	cx := math.Max(x0+r, math.Min(x, x1-r))
	cy := math.Max(y0+r, math.Min(y, y1-r))
	return (x-cx)*(x-cx)+(y-cy)*(y-cy) <= r*r
}

// segDist2 is the squared distance from (x, y) to the segment a-b.
func segDist2(x, y float64, a, b [2]float64) float64 {
	dx, dy := b[0]-a[0], b[1]-a[1]
	t := ((x-a[0])*dx + (y-a[1])*dy) / (dx*dx + dy*dy)
	t = math.Max(0, math.Min(1, t))
	px, py := a[0]+t*dx-x, a[1]+t*dy-y
	return px*px + py*py
}

func onMark(x, y float64) bool {
	const r2 = markStroke * markStroke / 4
	for _, p := range markPaths {
		for i := 1; i < len(p); i++ {
			if segDist2(x, y, p[i-1], p[i]) <= r2 {
				return true
			}
		}
	}
	return false
}

// sample returns the colour at a point of the 64-unit design.
func sample(x, y float64) (color.NRGBA, bool) {
	if !inRoundRect(x, y, 0, 0, 64, 64, 13) {
		return color.NRGBA{}, false
	}
	if onMark(x, y) {
		return paper, true
	}
	return blue, true
}

// render draws the icon size pixels square. margin is the fraction of the
// canvas left empty on each side (macOS icons sit inside their canvas;
// Windows icons fill it).
func render(size int, margin float64) *image.NRGBA {
	const ss = 4 // samples per pixel side
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	scale := 64 / (float64(size) * (1 - 2*margin))
	off := float64(size) * margin
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var r, g, b, a float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					x := (float64(px) + (float64(sx)+0.5)/ss - off) * scale
					y := (float64(py) + (float64(sy)+0.5)/ss - off) * scale
					if c, ok := sample(x, y); ok {
						r, g, b, a = r+float64(c.R), g+float64(c.G), b+float64(c.B), a+1
					}
				}
			}
			if a > 0 {
				img.SetNRGBA(px, py, color.NRGBA{uint8(r/a + 0.5), uint8(g/a + 0.5), uint8(b/a + 0.5), uint8(a*255/(ss*ss) + 0.5)})
			}
		}
	}
	return img
}

func pngBytes(img image.Image) []byte {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// dibBytes encodes img as the device-independent bitmap an icon resource
// holds: a BITMAPINFOHEADER whose height counts the colour rows and the
// mask rows, 32-bit BGRA rows bottom-up, then a 1-bit AND mask.
func dibBytes(img *image.NRGBA) []byte {
	n := img.Bounds().Dx()
	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, struct {
		Size          uint32
		Width, Height int32
		Planes, Bits  uint16
		Compression   uint32
		SizeImage     uint32
		XPPM, YPPM    int32
		Used, Imp     uint32
	}{Size: 40, Width: int32(n), Height: int32(2 * n), Planes: 1, Bits: 32, SizeImage: uint32(4 * n * n)})
	for y := n - 1; y >= 0; y-- {
		for x := 0; x < n; x++ {
			c := img.NRGBAAt(x, y)
			buf.Write([]byte{c.B, c.G, c.R, c.A})
		}
	}
	rowBytes := (n + 31) / 32 * 4
	for y := n - 1; y >= 0; y-- {
		row := make([]byte, rowBytes)
		for x := 0; x < n; x++ {
			if img.NRGBAAt(x, y).A == 0 {
				row[x/8] |= 0x80 >> (x % 8)
			}
		}
		buf.Write(row)
	}
	return buf.Bytes()
}

// iconImage is one size of a Windows icon.
type iconImage struct {
	size int
	data []byte // DIB below 256 pixels, PNG at 256
}

// windowsIcon renders the sizes Windows asks for.
func windowsIcon() []iconImage {
	var out []iconImage
	for _, n := range []int{16, 20, 24, 32, 40, 48, 64, 256} {
		img := render(n, 0)
		if n == 256 {
			out = append(out, iconImage{n, pngBytes(img)})
		} else {
			out = append(out, iconImage{n, dibBytes(img)})
		}
	}
	return out
}

// dirByte is how an icon directory writes a width or height: 0 means 256.
func dirByte(n int) uint8 {
	if n >= 256 {
		return 0
	}
	return uint8(n)
}

// icoFile is a .ico file with the given images.
func icoFile(images []iconImage) []byte {
	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, [3]uint16{0, 1, uint16(len(images))})
	offset := 6 + 16*len(images)
	for _, im := range images {
		buf.Write([]byte{dirByte(im.size), dirByte(im.size), 0, 0})
		binary.Write(&buf, binary.LittleEndian, [2]uint16{1, 32})
		binary.Write(&buf, binary.LittleEndian, [2]uint32{uint32(len(im.data)), uint32(offset)})
		offset += len(im.data)
	}
	for _, im := range images {
		buf.Write(im.data)
	}
	return buf.Bytes()
}

// groupIcon is the RT_GROUP_ICON resource naming images stored as RT_ICON
// resources firstID, firstID+1, ...
func groupIcon(images []iconImage, firstID int) []byte {
	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, [3]uint16{0, 1, uint16(len(images))})
	for i, im := range images {
		buf.Write([]byte{dirByte(im.size), dirByte(im.size), 0, 0})
		binary.Write(&buf, binary.LittleEndian, [2]uint16{1, 32})
		binary.Write(&buf, binary.LittleEndian, uint32(len(im.data)))
		binary.Write(&buf, binary.LittleEndian, uint16(firstID+i))
	}
	return buf.Bytes()
}

// icnsFile is a macOS .icns file. Every entry is a PNG; the types are the
// ones that take PNG data on all supported macOS versions.
func icnsFile() []byte {
	const margin = 0.1 // the artwork fills about 80% of a macOS icon's canvas
	entries := []struct {
		typ  string
		size int
	}{
		{"ic11", 32}, {"ic12", 64}, {"ic07", 128}, {"ic13", 256}, {"ic08", 256}, {"ic14", 512}, {"ic09", 512}, {"ic10", 1024},
	}
	rendered := map[int][]byte{}
	var body bytes.Buffer
	for _, e := range entries {
		data, ok := rendered[e.size]
		if !ok {
			data = pngBytes(render(e.size, margin))
			rendered[e.size] = data
		}
		body.WriteString(e.typ)
		binary.Write(&body, binary.BigEndian, uint32(8+len(data)))
		body.Write(data)
	}
	var out bytes.Buffer
	out.WriteString("icns")
	binary.Write(&out, binary.BigEndian, uint32(8+body.Len()))
	out.Write(body.Bytes())
	return out.Bytes()
}
