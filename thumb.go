package main

import (
	"bytes"
	"container/list"
	"errors"
	"image"
	"image/color"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"os"
	"sort"
	"sync"
	"time"

	_ "golang.org/x/image/bmp"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

// 程序自己能解码的图片格式；其他格式（视频、PDF、HEIC 等）在 Windows 上交给系统缩略图。
var goThumbExts = map[string]bool{
	"jpg": true, "jpeg": true, "jfif": true, "png": true, "gif": true,
	"bmp": true, "webp": true, "tif": true, "tiff": true,
}

var errNoThumb = errors.New("没有缩略图")

// thumbExtList 返回界面应当请求缩略图的扩展名。
func thumbExtList() []string {
	m := map[string]bool{}
	for e := range goThumbExts {
		m[e] = true
	}
	for _, e := range platformThumbExts() {
		m[e] = true
	}
	out := make([]string, 0, len(m))
	for e := range m {
		out = append(out, e)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------- 生成

const maxThumbPixels = 120_000_000 // 超大图片（如全景拼接图）不解码，避免占用过多内存

var thumbSem = make(chan struct{}, 4)

// makeThumb 生成适合放进 size×size 方框的 JPEG 缩略图。
func makeThumb(path, ext string, size int) ([]byte, error) {
	thumbSem <- struct{}{}
	defer func() { <-thumbSem }()
	var img image.Image
	var err error = errNoThumb
	if goThumbExts[ext] {
		img, err = decodeThumb(path, ext, size)
	}
	if img == nil {
		if pimg, perr := platformThumbTimeout(path, size); perr == nil {
			img, err = fit(pimg, size), nil
		}
	}
	if img == nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, flatten(img), &jpeg.Options{Quality: 82}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// platformThumbTimeout 防止个别损坏文件让系统缩略图接口长时间卡住。
func platformThumbTimeout(path string, size int) (image.Image, error) {
	type result struct {
		img image.Image
		err error
	}
	ch := make(chan result, 1)
	go func() {
		img, err := platformThumb(path, size)
		ch <- result{img, err}
	}()
	select {
	case r := <-ch:
		return r.img, r.err
	case <-time.After(8 * time.Second):
		return nil, errNoThumb
	}
}

func decodeThumb(path, ext string, size int) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	orient := 1
	if ext == "jpg" || ext == "jpeg" || ext == "jfif" {
		head := make([]byte, 192<<10)
		n, _ := io.ReadFull(f, head)
		var exifThumb []byte
		orient, exifThumb = readExif(head[:n])
		// 内嵌缩略图够大就直接用（列表里的小图总是够用）
		if exifThumb != nil {
			if t, err := jpeg.Decode(bytes.NewReader(exifThumb)); err == nil {
				b := t.Bounds()
				if max(b.Dx(), b.Dy()) >= size*3/4 {
					return applyOrientation(fit(t, size), orient), nil
				}
			}
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
	}
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return nil, err
	}
	if cfg.Width*cfg.Height > maxThumbPixels {
		return nil, errNoThumb
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	src, _, err := image.Decode(f)
	if err != nil {
		return nil, err
	}
	return applyOrientation(fit(src, size), orient), nil
}

// fit 把图片等比缩小到 size×size 以内（不放大）。
func fit(src image.Image, size int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return src
	}
	if w <= size && h <= size {
		return src
	}
	nw, nh := size, size
	if w >= h {
		nh = max(1, h*size/w)
	} else {
		nw = max(1, w*size/h)
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	kernel := draw.CatmullRom
	if w*h > 4_000_000 {
		kernel = draw.BiLinear // 大图用更快的算法
	}
	kernel.Scale(dst, dst.Rect, src, b, draw.Src, nil)
	return dst
}

// flatten 把带透明度的图片铺在白底上，以便保存为 JPEG。
func flatten(src image.Image) image.Image {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Rect, &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	draw.Draw(dst, dst.Rect, src, b.Min, draw.Over)
	return dst
}

// ---------------------------------------------------------------- 缓存

type thumbCache struct {
	mu    sync.Mutex
	ll    *list.List
	m     map[string]*list.Element
	bytes int
	max   int
}

type thumbEntry struct {
	key  string
	data []byte // nil 表示该文件没有缩略图（避免反复尝试）
}

func newThumbCache(maxBytes int) *thumbCache {
	return &thumbCache{ll: list.New(), m: map[string]*list.Element{}, max: maxBytes}
}

func (c *thumbCache) get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.m[key]; ok {
		c.ll.MoveToFront(e)
		return e.Value.(*thumbEntry).data, true
	}
	return nil, false
}

func (c *thumbCache) put(key string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.m[key]; ok {
		c.bytes -= len(e.Value.(*thumbEntry).data)
		c.ll.Remove(e)
	}
	c.m[key] = c.ll.PushFront(&thumbEntry{key, data})
	c.bytes += len(data) + len(key) + 64
	for c.bytes > c.max && c.ll.Len() > 1 {
		e := c.ll.Back()
		te := e.Value.(*thumbEntry)
		c.bytes -= len(te.data) + len(te.key) + 64
		delete(c.m, te.key)
		c.ll.Remove(e)
	}
}
