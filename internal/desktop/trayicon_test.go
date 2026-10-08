package desktop

import (
	"testing"

	"bimonitor/branding"
	"bimonitor/internal/model"
)

func TestTrayImageBadge(t *testing.T) {
	base := branding.Icon()
	for _, sev := range []model.Severity{model.SevNone, model.SevOK, model.SevWarning, model.SevError} {
		img := TrayImage(base, sev, 32)
		if img.Bounds().Dx() != 32 {
			t.Fatal("size")
		}
		c := img.NRGBAAt(26, 26) // inside the badge
		if sev == model.SevError && !(c.R > 180 && c.G < 120) {
			t.Errorf("error badge should be red, got %v", c)
		}
		if sev == model.SevOK && !(c.G > c.R) {
			t.Errorf("ok badge should be green, got %v", c)
		}
	}
}
