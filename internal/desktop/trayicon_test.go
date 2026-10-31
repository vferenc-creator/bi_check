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
		c := img.NRGBAAt(25, 7) // inside the status dot
		if sev == model.SevError {
			// the designed error icon: red badge in the upper right corner
			if e := img.NRGBAAt(27, 4); !(e.R > 150 && e.G < 110) {
				t.Errorf("error icon should have the red badge, got %v", e)
			}
		}
		if sev == model.SevWarning && !(c.R > 180 && c.G > 120 && c.B < 80) {
			t.Errorf("warning dot should be amber, got %v", c)
		}
		if sev == model.SevOK && !(c.G > c.R) {
			t.Errorf("ok badge should be green, got %v", c)
		}
	}
}
