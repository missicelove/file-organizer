package main

import (
	"encoding/binary"
	"image"
	"image/draw"
)

// readExif 从 JPEG 文件开头的数据中读取 EXIF 方向和内嵌缩略图。
// 相机和手机拍的照片几乎都带有约 160×120 的内嵌缩略图，直接使用它比解码整张大图快得多。
func readExif(data []byte) (orientation int, thumb []byte) {
	orientation = 1
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return
	}
	i := 2
	for i+4 <= len(data) {
		if data[i] != 0xFF {
			return
		}
		marker := data[i+1]
		if marker == 0xD8 || (marker >= 0xD0 && marker <= 0xD7) || marker == 0x01 || marker == 0xFF {
			i += 2
			continue
		}
		if marker == 0xDA || marker == 0xD9 { // 图像数据开始，后面不会再有 EXIF
			return
		}
		segLen := int(binary.BigEndian.Uint16(data[i+2:]))
		if segLen < 2 || i+2+segLen > len(data) {
			return
		}
		seg := data[i+4 : i+2+segLen]
		if marker == 0xE1 && len(seg) > 14 && string(seg[:6]) == "Exif\x00\x00" {
			return parseTIFF(seg[6:])
		}
		i += 2 + segLen
	}
	return
}

func parseTIFF(t []byte) (orientation int, thumb []byte) {
	orientation = 1
	if len(t) < 8 {
		return
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return
	}
	if bo.Uint16(t[2:]) != 42 {
		return
	}
	// readIFD 返回 tag→(类型, 值) 以及下一个 IFD 的偏移
	readIFD := func(off uint32) (map[uint16]uint32, uint32) {
		if off < 8 || int(off)+2 > len(t) {
			return nil, 0
		}
		n := int(bo.Uint16(t[off:]))
		p := int(off) + 2
		if p+n*12+4 > len(t) {
			return nil, 0
		}
		tags := map[uint16]uint32{}
		for k := 0; k < n; k++ {
			e := t[p+k*12:]
			tag, typ := bo.Uint16(e), bo.Uint16(e[2:])
			switch typ {
			case 3: // SHORT
				tags[tag] = uint32(bo.Uint16(e[8:]))
			case 4: // LONG
				tags[tag] = bo.Uint32(e[8:])
			}
		}
		return tags, bo.Uint32(t[p+n*12:])
	}
	ifd0, next := readIFD(bo.Uint32(t[4:]))
	if ifd0 == nil {
		return
	}
	if o, ok := ifd0[0x0112]; ok && o >= 1 && o <= 8 {
		orientation = int(o)
	}
	ifd1, _ := readIFD(next)
	off, ok1 := ifd1[0x0201]
	n, ok2 := ifd1[0x0202]
	if ok1 && ok2 && n > 0 && uint64(off)+uint64(n) <= uint64(len(t)) {
		b := t[off : off+n]
		if len(b) > 2 && b[0] == 0xFF && b[1] == 0xD8 {
			thumb = b
		}
	}
	return
}

// applyOrientation 按 EXIF 方向把图片转正（1 为正常，6 为需要顺时针旋转 90° 等）。
func applyOrientation(src image.Image, o int) image.Image {
	if o <= 1 || o > 8 {
		return src
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	rgba := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(rgba, rgba.Rect, src, b.Min, draw.Src)
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch o {
			case 2: // 水平翻转
				dx, dy = w-1-x, y
			case 3: // 旋转 180°
				dx, dy = w-1-x, h-1-y
			case 4: // 垂直翻转
				dx, dy = x, h-1-y
			case 5: // 转置
				dx, dy = y, x
			case 6: // 顺时针 90°
				dx, dy = h-1-y, x
			case 7: // 反转置
				dx, dy = h-1-y, w-1-x
			case 8: // 逆时针 90°
				dx, dy = y, w-1-x
			}
			i, j := rgba.PixOffset(x, y), dst.PixOffset(dx, dy)
			copy(dst.Pix[j:j+4], rgba.Pix[i:i+4])
		}
	}
	return dst
}
