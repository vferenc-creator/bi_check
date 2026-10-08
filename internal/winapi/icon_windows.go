//go:build windows

package winapi

import (
	"image"
	"image/draw"
	"unsafe"

	xdraw "golang.org/x/image/draw"
)

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type iconInfo struct {
	FIcon    int32
	XHotspot uint32
	YHotspot uint32
	HbmMask  uintptr
	HbmColor uintptr
}

// IconFromImage creates an HICON of the given size from an image (scaled
// with high quality). Free with DestroyIcon.
func IconFromImage(src image.Image, size int) uintptr {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	if src.Bounds().Dx() == size && src.Bounds().Dy() == size {
		draw.Draw(img, img.Bounds(), src, src.Bounds().Min, draw.Src)
	} else {
		xdraw.CatmullRom.Scale(img, img.Bounds(), src, src.Bounds(), draw.Src, nil)
	}

	bih := bitmapInfoHeader{
		Width: int32(size), Height: -int32(size), // top-down
		Planes: 1, BitCount: 32,
	}
	bih.Size = uint32(unsafe.Sizeof(bih))
	var bits unsafe.Pointer
	hbm, _, _ := pCreateDIBSection.Call(0, uintptr(unsafe.Pointer(&bih)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if hbm == 0 {
		return 0
	}
	defer pDeleteObject.Call(hbm)
	px := unsafe.Slice((*byte)(bits), size*size*4)
	for i := 0; i < size*size; i++ {
		px[i*4+0] = img.Pix[i*4+2] // B
		px[i*4+1] = img.Pix[i*4+1] // G
		px[i*4+2] = img.Pix[i*4+0] // R
		px[i*4+3] = img.Pix[i*4+3] // A
	}
	mask, _, _ := pCreateBitmap.Call(uintptr(size), uintptr(size), 1, 1, 0)
	defer pDeleteObject.Call(mask)
	ii := iconInfo{FIcon: 1, HbmMask: mask, HbmColor: hbm}
	h, _, _ := pCreateIconIndirect.Call(uintptr(unsafe.Pointer(&ii)))
	return h
}

// DestroyIcon frees an HICON.
func DestroyIcon(h uintptr) {
	if h != 0 {
		pDestroyIcon.Call(h)
	}
}

// LoadResourceIcon loads the exe's embedded icon (resource ID 1) at a size.
func LoadResourceIcon(size int) uintptr {
	const IMAGE_ICON = 1
	h, _, _ := pLoadImageW.Call(ModuleHandle(), 1, IMAGE_ICON, uintptr(size), uintptr(size), 0)
	return h
}

// SmallIconSize is the tray/small icon size for the given DPI.
func SmallIconSize(dpi int) int {
	if pGetSystemMetricsForDpi.Find() == nil {
		r, _, _ := pGetSystemMetricsForDpi.Call(SM_CXSMICON, uintptr(dpi))
		if r > 0 {
			return int(r)
		}
	}
	r, _, _ := pGetSystemMetrics.Call(SM_CXSMICON)
	if r > 0 {
		return int(r)
	}
	return 16
}
