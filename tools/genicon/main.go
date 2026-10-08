// genicon draws the application icon (branding/icon.png): the Energofish
// mark – a black "e" with the orange fin – on a white rounded square.
// It is a vector redraw of the logo; if you have the original artwork as a
// square PNG, simply overwrite branding/icon.png and skip this tool.
//
//	go run ./tools/genicon
package main

import (
	"image"
	"image/color"
	"image/png"
	"log"
	"os"

	"golang.org/x/image/vector"

	"bimonitor/branding"
)

const size = 256

// Logo coordinates are in the original artwork's space (≈ 880×560 for the
// mark); tr maps them onto the icon canvas.
const (
	srcX0, srcY0 = 55.0, 10.0
	srcW         = 800.0
	scale        = 228.0 / srcW
	offX         = (size - srcW*scale) / 2
	offY         = 47.0
)

func tr(x, y float32) (float32, float32) {
	return offX + (x-srcX0)*scale, offY + (y-srcY0)*scale
}

type pen struct{ r *vector.Rasterizer }

func (p pen) M(x, y float32) { p.r.MoveTo(tr(x, y)) }
func (p pen) C(x1, y1, x2, y2, x3, y3 float32) {
	ax, ay := tr(x1, y1)
	bx, by := tr(x2, y2)
	cx, cy := tr(x3, y3)
	p.r.CubeTo(ax, ay, bx, by, cx, cy)
}

func main() {
	b := branding.Current
	black := b.Color("primary", color.NRGBA{30, 27, 31, 255})
	orange := b.Color("accent", color.NRGBA{242, 165, 49, 255})
	bg := b.Color("iconBackground", color.NRGBA{255, 255, 255, 255})

	dst := image.NewNRGBA(image.Rect(0, 0, size, size))

	// Background tile.
	tile := vector.NewRasterizer(size, size)
	roundRect(tile, 6, 6, size-6, size-6, 52)
	tile.Draw(dst, dst.Bounds(), image.NewUniform(bg), image.Point{})

	// Orange fin.
	fin := vector.NewRasterizer(size, size)
	f := pen{fin}
	f.M(60, 15)
	f.C(250, 20, 420, 50, 512, 95)
	f.C(380, 100, 230, 140, 130, 195)
	f.C(125, 130, 100, 70, 60, 15)
	fin.ClosePath()
	fin.Draw(dst, dst.Bounds(), image.NewUniform(orange), image.Point{})

	// Black "e" (outer outline incl. the horizontal slot).
	e := vector.NewRasterizer(size, size)
	p := pen{e}
	p.M(795, 395)
	p.C(860, 345, 870, 240, 790, 175)
	p.C(715, 112, 560, 100, 430, 138)
	p.C(255, 190, 85, 290, 68, 400)
	p.C(55, 505, 185, 562, 335, 557)
	p.C(455, 553, 560, 532, 600, 503)
	p.C(618, 490, 605, 468, 572, 469)
	p.C(480, 472, 385, 478, 348, 468)
	p.C(305, 457, 303, 420, 347, 412)
	p.C(460, 402, 650, 405, 795, 395)
	e.ClosePath()
	e.Draw(dst, dst.Bounds(), image.NewUniform(black), image.Point{})

	// Counter (the eye of the "e"), punched out with the background color.
	hole := vector.NewRasterizer(size, size)
	h := pen{hole}
	h.M(326, 318)
	h.C(365, 252, 465, 190, 560, 184)
	h.C(645, 180, 638, 250, 622, 282)
	h.C(600, 322, 520, 338, 430, 336)
	h.C(362, 335, 318, 333, 326, 318)
	hole.ClosePath()
	hole.Draw(dst, dst.Bounds(), image.NewUniform(bg), image.Point{})

	out, err := os.Create("branding/icon.png")
	if err != nil {
		log.Fatal(err)
	}
	defer out.Close()
	if err := png.Encode(out, dst); err != nil {
		log.Fatal(err)
	}
}

func roundRect(r *vector.Rasterizer, x0, y0, x1, y1, rad float32) {
	const k = 0.5523
	r.MoveTo(x0+rad, y0)
	r.LineTo(x1-rad, y0)
	r.CubeTo(x1-rad+rad*k, y0, x1, y0+rad-rad*k, x1, y0+rad)
	r.LineTo(x1, y1-rad)
	r.CubeTo(x1, y1-rad+rad*k, x1-rad+rad*k, y1, x1-rad, y1)
	r.LineTo(x0+rad, y1)
	r.CubeTo(x0+rad-rad*k, y1, x0, y1-rad+rad*k, x0, y1-rad)
	r.LineTo(x0, y0+rad)
	r.CubeTo(x0, y0+rad-rad*k, x0+rad-rad*k, y0, x0+rad, y0)
	r.ClosePath()
}
