// Package branding holds everything that identifies the product visually:
// name, company, colors and the application icon. To rebrand, replace
// icon.png (square, at least 256×256) and edit brand.json, then rebuild with
// the build script (it regenerates the embedded exe resources as well).
package branding

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	_ "image/png"
	"sort"
	"strconv"
	"strings"
)

//go:embed brand.json
var brandJSON []byte

//go:embed icon.png
var IconPNG []byte

// Brand describes the product identity.
type Brand struct {
	AppName    string            `json:"appName"`
	ShortName  string            `json:"shortName"`
	Company    string            `json:"company"`
	AppID      string            `json:"appId"`
	DataFolder string            `json:"dataFolder"`
	Copyright  string            `json:"copyright"`
	Colors     map[string]string `json:"colors"`
}

// Current is the brand compiled into the binary.
var Current = mustLoad(brandJSON)

func mustLoad(b []byte) Brand {
	var br Brand
	if err := json.Unmarshal(b, &br); err != nil {
		panic("branding/brand.json: " + err.Error())
	}
	return br
}

// Icon decodes the embedded application icon.
func Icon() image.Image {
	img, _, err := image.Decode(bytes.NewReader(IconPNG))
	if err != nil {
		panic("branding/icon.png: " + err.Error())
	}
	return img
}

// CSSVariables renders the brand colors as CSS custom properties
// (--primary, --accent, ...) so the web UI never hard-codes a color.
func (b Brand) CSSVariables() string {
	keys := make([]string, 0, len(b.Colors))
	for k := range b.Colors {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	sb.WriteString(":root{")
	for _, k := range keys {
		fmt.Fprintf(&sb, "--%s:%s;", kebab(k), b.Colors[k])
	}
	sb.WriteString("}")
	return sb.String()
}

// Color returns a named brand color, or fallback when missing or invalid.
func (b Brand) Color(name string, fallback color.NRGBA) color.NRGBA {
	if c, ok := ParseHex(b.Colors[name]); ok {
		return c
	}
	return fallback
}

// ParseHex parses #RRGGBB.
func ParseHex(s string) (color.NRGBA, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "#")
	if len(s) != 6 {
		return color.NRGBA{}, false
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return color.NRGBA{}, false
	}
	return color.NRGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 255}, true
}

func kebab(s string) string {
	var sb strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				sb.WriteByte('-')
			}
			r += 'a' - 'A'
		}
		sb.WriteRune(r)
	}
	return sb.String()
}
