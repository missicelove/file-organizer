package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ------------------------------------------------------------ 最近新建的文件夹

func TestQuickFolders(t *testing.T) {
	root := makeMessyTree(t)
	a := scanApp(t, root, true)
	quickFile := filepath.Join(filepath.Dir(a.hist.logPath), "最近文件夹.json")

	r1, _ := a.Mkdir(MkdirReq{Parent: a.tree.root.ID, Name: "归类-图片"})
	a.Mkdir(MkdirReq{Parent: a.tree.root.ID, Name: "归类-文档"})
	got := a.quick.list()
	if len(got) != 2 || filepath.Base(got[0]) != "归类-文档" || filepath.Base(got[1]) != "归类-图片" {
		t.Fatalf("quick after mkdir: %v", got)
	}
	// 重命名、移动文件夹后路径随之更新
	if _, err := a.Rename(RenameReq{Item: Ref{D: r1.NewID}, NewName: "照片归档"}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(a.quick.list()[1], "照片归档") {
		t.Errorf("rename not tracked: %v", a.quick.list())
	}
	a.Move(MoveReq{Items: []Ref{{D: r1.NewID}}, Target: a.dirByPath(t, "工作").ID})
	want := filepath.Join(root, "工作", "照片归档")
	if a.quick.list()[1] != want {
		t.Errorf("move not tracked: %v", a.quick.list())
	}
	// 持久化：重新加载后内容一致
	if q2 := loadQuick(quickFile); strings.Join(q2.list(), "|") != strings.Join(a.quick.list(), "|") {
		t.Errorf("not persisted: %v vs %v", q2.list(), a.quick.list())
	}
	// 撤销移动 → 路径恢复；撤销重命名 → 名称恢复；撤销新建 → 从列表中移除
	a.Undo()
	if a.quick.list()[1] != filepath.Join(root, "照片归档") {
		t.Errorf("undo move not tracked: %v", a.quick.list())
	}
	a.Undo()
	a.Undo() // 撤销“新建 归类-文档”
	if l := a.quick.list(); len(l) != 1 || filepath.Base(l[0]) != "归类-图片" {
		t.Errorf("after undo mkdir: %v", l)
	}
	// 手动添加已有文件夹、去重、移除
	a.quick.add(filepath.Join(root, "下载"))
	a.quick.add(filepath.Join(root, "下载"))
	if l := a.quick.list(); len(l) != 2 || filepath.Base(l[0]) != "下载" {
		t.Errorf("add existing: %v", l)
	}
	a.quick.remove(filepath.Join(root, "下载"))
	if len(a.quick.list()) != 1 {
		t.Errorf("remove: %v", a.quick.list())
	}
}

func TestQuickPathHelpers(t *testing.T) {
	sep := string(os.PathSeparator)
	base := filepath.Join(sep+"a", "照片")
	cases := []struct {
		p    string
		want bool
	}{
		{base, true},
		{filepath.Join(base, "2020"), true},
		{base + "2", false}, // 名称前缀相同但不是子文件夹
		{filepath.Join(sep+"a", "其他"), false},
	}
	for _, c := range cases {
		if got := isUnder(c.p, base); got != c.want {
			t.Errorf("isUnder(%q) = %v", c.p, got)
		}
	}
	q := &QuickStore{paths: []string{filepath.Join(base, "2020"), base + "2", base}}
	q.rename(base, filepath.Join(sep+"b", "相册"))
	want := []string{filepath.Join(sep+"b", "相册", "2020"), base + "2", filepath.Join(sep+"b", "相册")}
	if strings.Join(q.paths, "|") != strings.Join(want, "|") {
		t.Errorf("rename: %v", q.paths)
	}
	for i := 0; i < 30; i++ {
		q.add(fmt.Sprintf("%s%d", sep, i))
	}
	if len(q.paths) != maxQuick {
		t.Errorf("cap: %d", len(q.paths))
	}
}

// ------------------------------------------------------------ 删除（移到回收站）

// useFakeTrash 让删除操作把文件移到测试用的临时“回收站”，不会碰到真正的回收站。
func useFakeTrash(t *testing.T) string {
	bin := t.TempDir()
	old := trashFunc
	trashFunc = func(paths []string) error {
		for i, p := range paths {
			if err := os.Rename(p, filepath.Join(bin, fmt.Sprintf("%d-%s", i, filepath.Base(p)))); err != nil {
				return err
			}
		}
		return nil
	}
	t.Cleanup(func() { trashFunc = old })
	return bin
}

func TestDelete(t *testing.T) {
	bin := useFakeTrash(t)
	root := makeMessyTree(t)
	a := scanApp(t, root, true)
	a.Mkdir(MkdirReq{Parent: a.dirByPath(t, "工作").ID, Name: "旧资料"})
	undoBefore := len(a.hist.list())
	projA := a.dirByPath(t, "工作/项目A")

	// 文件夹 + 其中的文件 + 另一个文件；内部文件随文件夹一起删除
	res, err := a.Delete(DeleteReq{Items: []Ref{{D: projA.ID}, fileRef(t, a, "工作/项目A/报价单.xlsx"), fileRef(t, a, "下载/setup.exe")}})
	if err != nil || res.Done != 2 || len(res.Errors) != 0 {
		t.Fatalf("delete: %+v %v", res, err)
	}
	mustNotExist(t, filepath.Join(root, "工作", "项目A"))
	mustNotExist(t, filepath.Join(root, "下载", "setup.exe"))
	entries, _ := os.ReadDir(bin)
	if len(entries) != 2 {
		t.Errorf("fake bin has %d entries", len(entries))
	}
	assertTreeMatchesDisk(t, a)
	if len(a.hist.list()) != undoBefore {
		t.Error("delete must not enter the undo list")
	}
	log, _ := os.ReadFile(a.hist.logPath)
	if !strings.Contains(string(log), "移到回收站") {
		t.Error("delete not logged")
	}

	// 删除“最近新建的文件夹”的上级 → 记录被移除
	res, _ = a.Delete(DeleteReq{Items: []Ref{{D: a.dirByPath(t, "工作").ID}}})
	if res.Done != 1 || len(a.quick.list()) != 0 {
		t.Errorf("quick not cleaned: %+v %v", res, a.quick.list())
	}
	// 不能删除根目录
	res, _ = a.Delete(DeleteReq{Items: []Ref{{D: a.tree.root.ID}}})
	if res.Done != 0 || len(res.Errors) != 1 {
		t.Errorf("root delete: %+v", res)
	}
	// 按查询删除（例如“全部视频”）
	res, _ = a.Delete(DeleteReq{Query: &Query{Dir: a.tree.root.ID, Recursive: true, Cat: "video"}})
	if res.Done != 1 {
		t.Errorf("query delete: %+v", res)
	}
	assertTreeMatchesDisk(t, a)
}

func TestDeleteFailureKeepsTree(t *testing.T) {
	root := makeMessyTree(t)
	a := scanApp(t, root, true)
	old := trashFunc
	trashFunc = func([]string) error { return errors.New("模拟失败") }
	defer func() { trashFunc = old }()
	res, err := a.Delete(DeleteReq{Items: []Ref{fileRef(t, a, "杂项/a.txt")}})
	if err != nil || res.Done != 0 || len(res.Errors) != 1 || !strings.Contains(res.Errors[0], "模拟失败") {
		t.Fatalf("%+v %v", res, err)
	}
	mustExist(t, filepath.Join(root, "杂项", "a.txt"))
	assertTreeMatchesDisk(t, a)
}

// ------------------------------------------------------------ 缩略图

func solidImage(w, h int, c color.Color) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	return img
}

func encodeJPEG(t *testing.T, img image.Image) []byte {
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// withExif 在 JPEG 中插入 EXIF：方向标记和（可选的）内嵌缩略图。
func withExif(main []byte, orientation uint16, thumb []byte) []byte {
	le := binary.LittleEndian
	var tf bytes.Buffer
	w := func(v any) { binary.Write(&tf, le, v) }
	tf.WriteString("II")
	w(uint16(42))
	w(uint32(8))
	w(uint16(1)) // IFD0：1 项
	w(uint16(0x0112))
	w(uint16(3))
	w(uint32(1))
	w(orientation)
	w(uint16(0))
	if thumb == nil {
		w(uint32(0))
	} else {
		ifd1 := uint32(8 + 2 + 12 + 4)
		w(ifd1)
		w(uint16(2)) // IFD1：2 项
		w(uint16(0x0201))
		w(uint16(4))
		w(uint32(1))
		w(ifd1 + 2 + 2*12 + 4)
		w(uint16(0x0202))
		w(uint16(4))
		w(uint32(1))
		w(uint32(len(thumb)))
		w(uint32(0))
		tf.Write(thumb)
	}
	app1 := append([]byte("Exif\x00\x00"), tf.Bytes()...)
	seg := []byte{0xFF, 0xE1, byte((len(app1) + 2) >> 8), byte(len(app1) + 2)}
	out := append([]byte{}, main[:2]...)
	out = append(out, seg...)
	out = append(out, app1...)
	return append(out, main[2:]...)
}

func decodeThumbResult(t *testing.T, data []byte) image.Image {
	t.Helper()
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("thumb is not a jpeg: %v", err)
	}
	return img
}

func near(c color.Color, r, g, b uint8) bool {
	cr, cg, cb, _ := c.RGBA()
	d := func(x uint32, y uint8) bool { v := int(x>>8) - int(y); return v > -40 && v < 40 }
	return d(cr, r) && d(cg, g) && d(cb, b)
}

func TestThumbPNGAndTransparency(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "红色.png")
	f, _ := os.Create(p)
	png.Encode(f, solidImage(400, 200, color.RGBA{220, 20, 20, 255}))
	f.Close()
	data, err := makeThumb(p, "png", 64)
	if err != nil {
		t.Fatal(err)
	}
	img := decodeThumbResult(t, data)
	if b := img.Bounds(); b.Dx() != 64 || b.Dy() != 32 {
		t.Errorf("size %v", b)
	}
	if !near(img.At(32, 16), 220, 20, 20) {
		t.Errorf("color %v", img.At(32, 16))
	}
	// 透明图片铺白底
	p2 := filepath.Join(dir, "透明.png")
	f, _ = os.Create(p2)
	png.Encode(f, image.NewNRGBA(image.Rect(0, 0, 50, 50)))
	f.Close()
	data, _ = makeThumb(p2, "png", 64)
	if img := decodeThumbResult(t, data); !near(img.At(25, 25), 255, 255, 255) || img.Bounds().Dx() != 50 {
		t.Errorf("transparent: %v %v", img.Bounds(), img.At(25, 25))
	}
	// 不是图片的文件
	p3 := filepath.Join(dir, "假图片.jpg")
	os.WriteFile(p3, []byte("not an image"), 0o644)
	if _, err := makeThumb(p3, "jpg", 64); err == nil {
		t.Error("expected error for broken image")
	}
}

func TestThumbExifEmbeddedAndOrientation(t *testing.T) {
	dir := t.TempDir()
	red := encodeJPEG(t, solidImage(400, 200, color.RGBA{220, 20, 20, 255}))
	blueThumb := encodeJPEG(t, solidImage(160, 80, color.RGBA{20, 20, 220, 255}))

	// 带内嵌缩略图 + 方向 6（需顺时针转 90°）：应使用内嵌缩略图（蓝色）并转成竖图
	p := filepath.Join(dir, "相机照片.jpg")
	os.WriteFile(p, withExif(red, 6, blueThumb), 0o644)
	if o, th := readExif(withExif(red, 6, blueThumb)); o != 6 || th == nil {
		t.Fatalf("readExif = %d, %v", o, th != nil)
	}
	img := decodeThumbResult(t, mustThumb(t, p, "jpg", 64))
	if b := img.Bounds(); b.Dx() != 32 || b.Dy() != 64 {
		t.Errorf("rotated size %v", b)
	}
	if !near(img.At(16, 32), 20, 20, 220) {
		t.Errorf("embedded thumb not used: %v", img.At(16, 32))
	}
	// 要求的尺寸比内嵌缩略图大很多时，解码原图
	img = decodeThumbResult(t, mustThumb(t, p, "jpg", 300))
	if !near(img.At(10, 10), 220, 20, 20) {
		t.Errorf("large thumb should use main image: %v", img.At(10, 10))
	}

	// 没有内嵌缩略图，方向 6：左红右绿的横图转正后应为上红下绿的竖图
	half := image.NewRGBA(image.Rect(0, 0, 400, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 400; x++ {
			if x < 200 {
				half.Set(x, y, color.RGBA{220, 20, 20, 255})
			} else {
				half.Set(x, y, color.RGBA{20, 200, 20, 255})
			}
		}
	}
	p2 := filepath.Join(dir, "竖拍.jpg")
	os.WriteFile(p2, withExif(encodeJPEG(t, half), 6, nil), 0o644)
	img = decodeThumbResult(t, mustThumb(t, p2, "jpg", 64))
	if b := img.Bounds(); b.Dx() != 32 || b.Dy() != 64 {
		t.Errorf("size %v", b)
	}
	if !near(img.At(16, 5), 220, 20, 20) || !near(img.At(16, 58), 20, 200, 20) {
		t.Errorf("orientation wrong: top %v bottom %v", img.At(16, 5), img.At(16, 58))
	}
}

func mustThumb(t *testing.T, p, ext string, size int) []byte {
	t.Helper()
	data, err := makeThumb(p, ext, size)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestApplyOrientationAll(t *testing.T) {
	// 2×1 图片：左红右蓝。检查 8 种方向下红色像素最终的位置。
	src := image.NewRGBA(image.Rect(0, 0, 2, 1))
	src.Set(0, 0, color.RGBA{255, 0, 0, 255})
	src.Set(1, 0, color.RGBA{0, 0, 255, 255})
	wantRed := map[int]image.Point{1: {0, 0}, 2: {1, 0}, 3: {1, 0}, 4: {0, 0}, 5: {0, 0}, 6: {0, 0}, 7: {0, 1}, 8: {0, 1}}
	for o, p := range wantRed {
		out := applyOrientation(src, o)
		if !near(out.At(p.X, p.Y), 255, 0, 0) {
			t.Errorf("orientation %d: red not at %v (bounds %v)", o, p, out.Bounds())
		}
	}
}

func TestReadExifNeverPanics(t *testing.T) {
	good := withExif(encodeJPEG(t, solidImage(20, 10, color.White)), 3, encodeJPEG(t, solidImage(8, 4, color.Black)))
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 3000; i++ {
		b := append([]byte{}, good[:rng.Intn(len(good))]...)
		for k := 0; k < 3 && len(b) > 0; k++ {
			b[rng.Intn(len(b))] = byte(rng.Intn(256))
		}
		readExif(b) // 截断或损坏的数据不能导致崩溃
	}
}

func TestThumbCacheEviction(t *testing.T) {
	c := newThumbCache(1000)
	for i := 0; i < 20; i++ {
		c.put(fmt.Sprint(i), make([]byte, 100))
	}
	if c.bytes > 1000 {
		t.Errorf("cache over budget: %d", c.bytes)
	}
	if _, ok := c.get("19"); !ok {
		t.Error("newest entry evicted")
	}
	if _, ok := c.get("0"); ok {
		t.Error("oldest entry kept")
	}
	c.put("none", nil)
	if d, ok := c.get("none"); !ok || d != nil {
		t.Error("negative entry")
	}
}

func TestThumbAPI(t *testing.T) {
	c, _ := startTestServer(t)
	dir := t.TempDir()
	f, _ := os.Create(filepath.Join(dir, "图 1.png"))
	png.Encode(f, solidImage(300, 300, color.RGBA{0, 128, 255, 255}))
	f.Close()
	os.WriteFile(filepath.Join(dir, "说明.txt"), []byte("hi"), 0o644)
	st := c.scan(dir)
	get := func(name, token string) (int, string, []byte) {
		u := fmt.Sprintf("%s/api/thumb?d=%d&n=%s&s=96&t=%s", c.base, st.Root.ID, urlQuery(name), token)
		resp, err := http.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header.Get("Content-Type"), b
	}
	code, ct, body := get("图 1.png", c.token)
	if code != 200 || ct != "image/jpeg" {
		t.Fatalf("thumb: %d %s", code, ct)
	}
	if img := decodeThumbResult(t, body); img.Bounds().Dx() != 96 {
		t.Errorf("size %v", img.Bounds())
	}
	if code, _, _ := get("图 1.png", "wrong"); code != 403 {
		t.Errorf("wrong token: %d", code)
	}
	if code, _, _ := get("说明.txt", c.token); code != 404 {
		t.Errorf("text file: %d", code)
	}
	if code, _, _ := get("不存在.png", c.token); code != 404 {
		t.Errorf("missing: %d", code)
	}
	var st2 map[string]any
	c.do("GET", "/api/state", nil, &st2)
	if exts, _ := st2["thumbExts"].([]any); len(exts) < 9 {
		t.Errorf("thumbExts: %v", st2["thumbExts"])
	}
}

func TestQuickAndDeleteAPI(t *testing.T) {
	useFakeTrash(t)
	c, _ := startTestServer(t)
	root := makeMessyTree(t)
	st := c.scan(root)
	var res OpResult
	c.do("POST", "/api/mkdir", MkdirReq{Parent: st.Root.ID, Name: "新归类"}, &res)
	var quick []struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	c.do("GET", "/api/quick", nil, &quick)
	if len(quick) != 1 || quick[0].Name != "新归类" || quick[0].ID != res.NewID {
		t.Fatalf("quick %+v", quick)
	}
	var kids []struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	c.do("GET", fmt.Sprintf("/api/children?id=%d", st.Root.ID), nil, &kids)
	for _, k := range kids {
		if k.Name == "下载" {
			c.do("POST", "/api/quick/add", map[string]int{"id": k.ID}, nil)
		}
	}
	c.do("GET", "/api/quick", nil, &quick)
	if len(quick) != 2 || quick[0].Name != "下载" {
		t.Fatalf("quick after add %+v", quick)
	}
	c.do("POST", "/api/quick/remove", map[string]string{"path": filepath.Join(root, "下载")}, nil)
	c.do("GET", "/api/quick", nil, &quick)
	if len(quick) != 1 {
		t.Fatalf("quick after remove %+v", quick)
	}
	c.do("POST", "/api/delete", map[string]any{"items": []Ref{{D: res.NewID}}}, &res)
	if res.Done != 1 {
		t.Fatalf("delete %+v", res)
	}
	c.do("GET", "/api/quick", nil, &quick)
	if len(quick) != 0 {
		t.Errorf("deleted folder still listed %+v", quick)
	}
}
