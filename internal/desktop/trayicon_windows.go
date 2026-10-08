//go:build windows

package desktop

import (
	"image"

	"bimonitor/internal/model"
	"bimonitor/internal/winapi"
)

func trayIcon(base image.Image, sev model.Severity, dpi int) uintptr {
	size := winapi.SmallIconSize(dpi)
	return winapi.IconFromImage(TrayImage(base, sev, size*2), size)
}
