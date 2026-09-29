//go:build windows

package main

import (
	"image"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 通过 Windows 外壳（与资源管理器相同的机制）获取缩略图，支持视频、PDF、HEIC 等格式。

var (
	modShell32                      = windows.NewLazySystemDLL("shell32.dll")
	modGdi32                        = windows.NewLazySystemDLL("gdi32.dll")
	modUser32                       = windows.NewLazySystemDLL("user32.dll")
	procSHCreateItemFromParsingName = modShell32.NewProc("SHCreateItemFromParsingName")
	procGetObjectW                  = modGdi32.NewProc("GetObjectW")
	procGetDIBits                   = modGdi32.NewProc("GetDIBits")
	procDeleteObject                = modGdi32.NewProc("DeleteObject")
	procGetDC                       = modUser32.NewProc("GetDC")
	procReleaseDC                   = modUser32.NewProc("ReleaseDC")
)

// IID_IShellItemImageFactory {BCC18B79-BA16-442F-80C4-8A59C30C463B}
var iidShellItemImageFactory = windows.GUID{Data1: 0xbcc18b79, Data2: 0xba16, Data3: 0x442f, Data4: [8]byte{0x80, 0xc4, 0x8a, 0x59, 0xc3, 0x0c, 0x46, 0x3b}}

type imageFactoryVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
	GetImage       uintptr
}

type imageFactory struct{ vtbl *imageFactoryVtbl }

const (
	siigbfBiggerSizeOK  = 0x1
	siigbfThumbnailOnly = 0x8 // 只要真正的缩略图，不要文件类型图标
)

type winBitmap struct {
	Type, Width, Height, WidthBytes int32
	Planes, BitsPixel               uint16
	Bits                            uintptr
}

type bitmapInfoHeader struct {
	Size                         uint32
	Width, Height                int32
	Planes, BitCount             uint16
	Compression, SizeImage       uint32
	XPelsPerMeter, YPelsPerMeter int32
	ClrUsed, ClrImportant        uint32
}

func platformThumbExts() []string {
	var out []string
	for _, c := range []string{"image", "video"} {
		out = append(out, categoryInfo(c).Exts...)
	}
	return append(out, "pdf", "psd", "ai")
}

func platformThumb(path string, size int) (image.Image, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED); err == nil || err == syscall.Errno(1) {
		defer windows.CoUninitialize()
	}
	p16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	var f *imageFactory
	hr, _, _ := procSHCreateItemFromParsingName.Call(uintptr(unsafe.Pointer(p16)), 0,
		uintptr(unsafe.Pointer(&iidShellItemImageFactory)), uintptr(unsafe.Pointer(&f)))
	if int32(hr) != 0 || f == nil {
		return nil, errNoThumb
	}
	defer syscall.SyscallN(f.vtbl.Release, uintptr(unsafe.Pointer(f)))
	var hbmp uintptr
	sz := uintptr(uint32(size)) | uintptr(uint32(size))<<32 // SIZE 结构按值传递
	hr, _, _ = syscall.SyscallN(f.vtbl.GetImage, uintptr(unsafe.Pointer(f)), sz,
		siigbfThumbnailOnly|siigbfBiggerSizeOK, uintptr(unsafe.Pointer(&hbmp)))
	if int32(hr) != 0 || hbmp == 0 {
		return nil, errNoThumb
	}
	defer procDeleteObject.Call(hbmp)
	return hbitmapToImage(hbmp)
}

func hbitmapToImage(hbmp uintptr) (image.Image, error) {
	var bm winBitmap
	if r, _, _ := procGetObjectW.Call(hbmp, unsafe.Sizeof(bm), uintptr(unsafe.Pointer(&bm))); r == 0 {
		return nil, errNoThumb
	}
	w, h := int(bm.Width), int(bm.Height)
	if h < 0 {
		h = -h
	}
	if w <= 0 || h <= 0 || w > 4096 || h > 4096 {
		return nil, errNoThumb
	}
	var bi struct {
		hdr    bitmapInfoHeader
		colors [256]uint32
	}
	bi.hdr = bitmapInfoHeader{Width: int32(w), Height: -int32(h), Planes: 1, BitCount: 32} // 负高度 = 自上而下
	bi.hdr.Size = uint32(unsafe.Sizeof(bi.hdr))
	buf := make([]byte, w*h*4)
	hdc, _, _ := procGetDC.Call(0)
	defer procReleaseDC.Call(0, hdc)
	if n, _, _ := procGetDIBits.Call(hdc, hbmp, 0, uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&bi)), 0); n == 0 {
		return nil, errNoThumb
	}
	hasAlpha := false
	for i := 3; i < len(buf); i += 4 {
		if buf[i] != 0 {
			hasAlpha = true
			break
		}
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(buf); i += 4 {
		a := buf[i+3]
		if !hasAlpha {
			a = 255
		}
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = buf[i+2], buf[i+1], buf[i], a // BGRA → RGBA
	}
	return img, nil
}
