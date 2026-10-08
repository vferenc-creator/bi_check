// Package web contains the user interface (plain HTML/CSS/JS, no build step)
// and assembles it into a single self-contained page.
package web

import (
	_ "embed"
	"encoding/base64"
	"strings"

	"bimonitor/branding"
)

//go:embed index.html
var indexHTML string

//go:embed styles.css
var stylesCSS string

//go:embed app.js
var appJS string

// Page returns the complete UI as one HTML document (CSS, JS and the icon
// inlined), suitable for WebView2's NavigateToString or a dev HTTP server.
func Page() string {
	b := branding.Current
	icon := "data:image/png;base64," + base64.StdEncoding.EncodeToString(branding.IconPNG)
	r := strings.NewReplacer(
		"{{APPNAME}}", htmlEscape(b.AppName),
		"{{COMPANY}}", htmlEscape(b.Company),
		"{{ICON}}", icon,
		"{{BRANDVARS}}", b.CSSVariables(),
		"{{STYLES}}", stylesCSS,
		"{{SCRIPT}}", appJS,
	)
	return r.Replace(indexHTML)
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}
