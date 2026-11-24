package screenshot

import (
	"image"
	"image/draw"
)

// downscale shrinks img to width dw (keeping the aspect ratio) with an area
// average. Screens are mostly text and hard edges, which a box filter keeps
// readable without the ringing of sharper kernels, and it needs no
// dependency.
func downscale(img image.Image, dw int) *image.RGBA {
	src := toRGBA(img)
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	if dw >= sw || dw < 1 {
		return src
	}
	dh := max(1, (sh*dw+sw/2)/sw)
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))

	acc := make([]uint32, sw*4)
	for y := range dh {
		y0, y1 := y*sh/dh, (y+1)*sh/dh
		if y1 <= y0 {
			y1 = y0 + 1
		}
		clear(acc)
		for sy := y0; sy < y1; sy++ {
			row := src.Pix[(sy)*src.Stride : (sy)*src.Stride+sw*4]
			for i, v := range row {
				acc[i] += uint32(v)
			}
		}
		rows := uint32(y1 - y0)
		out := dst.Pix[y*dst.Stride:]
		for x := range dw {
			x0, x1 := x*sw/dw, (x+1)*sw/dw
			if x1 <= x0 {
				x1 = x0 + 1
			}
			n := rows * uint32(x1-x0)
			var r, g, b, a uint32
			for sx := x0; sx < x1; sx++ {
				r += acc[sx*4]
				g += acc[sx*4+1]
				b += acc[sx*4+2]
				a += acc[sx*4+3]
			}
			out[x*4] = uint8((r + n/2) / n)
			out[x*4+1] = uint8((g + n/2) / n)
			out[x*4+2] = uint8((b + n/2) / n)
			out[x*4+3] = uint8((a + n/2) / n)
		}
	}
	return dst
}

// toRGBA returns img as an *image.RGBA with its origin at (0,0).
func toRGBA(img image.Image) *image.RGBA {
	if p, ok := img.(*image.RGBA); ok && p.Rect.Min == (image.Point{}) {
		return p
	}
	b := img.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), img, b.Min, draw.Src)
	return dst
}
