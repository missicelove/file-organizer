// genicon 生成程序图标（PNG），供 go-winres 嵌入 exe。用法：go run ./tools/genicon winres/icon.png
package main

import (
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
)

type rrect struct {
	x0, y0, x1, y1, r float64
	c                 color.NRGBA
}

func (q rrect) contains(x, y float64) bool {
	if x < q.x0 || x > q.x1 || y < q.y0 || y > q.y1 {
		return false
	}
	cx := math.Max(q.x0+q.r, math.Min(x, q.x1-q.r))
	cy := math.Max(q.y0+q.r, math.Min(y, q.y1-q.r))
	return (x-cx)*(x-cx)+(y-cy)*(y-cy) <= q.r*q.r
}

func main() {
	const size, ss = 256, 4
	shapes := []rrect{
		{16, 36, 116, 90, 16, color.NRGBA{0xc9, 0x86, 0x14, 255}},  // 文件夹标签
		{16, 56, 240, 218, 20, color.NRGBA{0xd9, 0x98, 0x1e, 255}}, // 文件夹后片
		{16, 92, 240, 218, 20, color.NRGBA{0xf5, 0xbd, 0x40, 255}}, // 文件夹前片
		{58, 124, 198, 140, 8, color.NRGBA{255, 255, 255, 235}},    // 整理线条
		{58, 152, 172, 168, 8, color.NRGBA{255, 255, 255, 235}},
		{58, 180, 146, 196, 8, color.NRGBA{255, 255, 255, 235}},
	}
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var r, g, b, a float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					x, y := float64(px)+(float64(sx)+.5)/ss, float64(py)+(float64(sy)+.5)/ss
					var cr, cg, cb, ca float64
					for _, s := range shapes {
						if s.contains(x, y) {
							al := float64(s.c.A) / 255
							cr = cr*(1-al) + float64(s.c.R)*al
							cg = cg*(1-al) + float64(s.c.G)*al
							cb = cb*(1-al) + float64(s.c.B)*al
							ca = ca*(1-al) + al
						}
					}
					r, g, b, a = r+cr, g+cg, b+cb, a+ca
				}
			}
			n := float64(ss * ss)
			if a > 0 {
				img.SetNRGBA(px, py, color.NRGBA{uint8(r / a), uint8(g / a), uint8(b / a), uint8(a / n * 255)})
			}
		}
	}
	f, err := os.Create(os.Args[1])
	if err != nil {
		panic(err)
	}
	defer f.Close()
	png.Encode(f, img)
}
