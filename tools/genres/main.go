// genres generates the Windows resources (icon, manifest, version info) that
// get linked into the exe, from branding/brand.json and branding/icon.png.
//
//	go run ./tools/genres -version 1.2.3
//
// Output: cmd/bimonitor/rsrc_windows_amd64.syso (picked up by go build).
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/tc-hib/winres"
	"github.com/tc-hib/winres/version"

	"bimonitor/branding"
)

func main() {
	ver := flag.String("version", "0.0.0", "product version (x.y.z)")
	out := flag.String("out", "cmd/bimonitor/rsrc_windows_amd64.syso", "output .syso")
	flag.Parse()

	b := branding.Current
	rs := &winres.ResourceSet{}

	icon, err := winres.NewIconFromResizedImage(branding.Icon(), []int{256, 64, 48, 40, 32, 24, 20, 16})
	if err != nil {
		log.Fatal(err)
	}
	// Resource ID 1 is what LoadIcon(hInstance, MAKEINTRESOURCE(1)) uses at runtime.
	if err := rs.SetIcon(winres.ID(1), icon); err != nil {
		log.Fatal(err)
	}

	rs.SetManifest(winres.AppManifest{
		Description:         b.AppName,
		Compatibility:       winres.Win10AndAbove,
		ExecutionLevel:      winres.AsInvoker,
		DPIAwareness:        winres.DPIPerMonitorV2,
		UseCommonControlsV6: true,
		LongPathAware:       true,
	})

	v := strings.TrimPrefix(*ver, "v")
	num := numericVersion(v)
	vi := version.Info{}
	vi.SetFileVersion(num)
	vi.SetProductVersion(num)
	vi.Timestamp = time.Now()
	const hu = 0x040E // hu-HU
	for k, val := range map[string]string{
		"CompanyName":      b.Company,
		"FileDescription":  b.AppName,
		"ProductName":      b.AppName,
		"InternalName":     "bimonitor",
		"OriginalFilename": "BIMonitor.exe",
		"LegalCopyright":   fmt.Sprintf("%s %d", b.Copyright, time.Now().Year()),
		"FileVersion":      v,
		"ProductVersion":   v,
	} {
		if err := vi.Set(hu, k, val); err != nil {
			log.Fatal(err)
		}
	}
	rs.SetVersionInfo(vi)

	f, err := os.Create(*out)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	if err := rs.WriteObject(f, winres.ArchAMD64); err != nil {
		log.Fatal(err)
	}
}

// numericVersion turns "1.2.3-beta+abc" into "1.2.3.0".
func numericVersion(v string) string {
	if i := strings.IndexAny(v, "-+ "); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	for len(parts) < 4 {
		parts = append(parts, "0")
	}
	return strings.Join(parts[:4], ".")
}
