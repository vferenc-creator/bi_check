package desktop

import (
	"image"
	"image/color"
	"image/draw"
	"math"

	xdraw "golang.org/x/image/draw"

	"bimonitor/branding"
	"bimonitor/internal/model"
)

// TrayImage renders the tray icon for the overall state: the brand icon with
// a status dot in the upper right corner (green = OK, amber = warning), or the
// designed "error" variant of the icon (red badge with "!") when something is
// missing / unreachable; none = nothing to watch. It is platform independent
// so it can be unit-tested and previewed.
func TrayImage(base image.Image, sev model.Severity, size int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	if sev == model.SevError {
		base = branding.IconAlert()
	}
	xdraw.CatmullRom.Scale(img, img.Bounds(), base, base.Bounds(), draw.Over, nil)
	var c color.NRGBA
	b := branding.Current
	switch sev {
	case model.SevOK:
		c = b.Color("ok", color.NRGBA{46, 158, 91, 255})
	case model.SevWarning:
		c = b.Color("late", color.NRGBA{217, 154, 0, 255})
	default:
		return img
	}
	// Badge: filled circle with a white ring in the upper right corner, where
	// the designed error icon has its red "!" badge.
	r := float64(size) * 0.19
	cx, cy := float64(size)*0.79, float64(size)*0.21
	ring := math.Max(1, float64(size)/16)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			d := math.Hypot(float64(x)+0.5-cx, float64(y)+0.5-cy)
			switch {
			case d <= r-ring:
				img.SetNRGBA(x, y, blendEdge(c, r-ring-d))
			case d <= r:
				img.SetNRGBA(x, y, color.NRGBA{255, 255, 255, uint8(255 * clamp01(r-d))})
			}
		}
	}
	return img
}

func clamp01(v float64) float64 { return math.Max(0, math.Min(1, v+0.5)) }

func blendEdge(c color.NRGBA, inside float64) color.NRGBA {
	a := clamp01(inside)
	return color.NRGBA{
		R: uint8(float64(c.R)*a + 255*(1-a)),
		G: uint8(float64(c.G)*a + 255*(1-a)),
		B: uint8(float64(c.B)*a + 255*(1-a)),
		A: 255,
	}
}
