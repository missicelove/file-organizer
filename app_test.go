package main

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

// ------------------------------------------------------------ 测试辅助

func writeFile(t *testing.T, p string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(strings.Repeat("x", size)), 0o644); err != nil {
		t.Fatal(err)
	}
}

// 构建一个模拟的“杂乱硬盘”目录结构。
func makeMessyTree(t *testing.T) string {
	root := t.TempDir()
	files := map[string]int{
		"照片/2019/旅行/IMG_001.JPG":    1000,
		"照片/2019/旅行/IMG_002.jpg":    2000,
		"照片/2020/家庭/合影.png":         3000,
		"工作/项目A/需求文档.docx":          400,
		"工作/项目A/报价单.xlsx":           500,
		"工作/项目A/资料/参考.pdf":          600,
		"工作/项目B/深/很深/非常深/最深/说明.txt": 70,
		"工作/项目B/深/很深/非常深/最深/截图.png": 800,
		"下载/setup.exe":              5000,
		"下载/电影.mp4":                 9000,
		"下载/歌曲.mp3":                 900,
		"下载/无扩展名文件":                 10,
		"下载/archive.tar.gz":         300,
		"杂项/a.txt":                  1,
		"杂项/b.TXT":                  2,
		"根目录文件.pdf":                 50,
		".隐藏文件夹/secret.txt":         99,
		"工作/.hidden.txt":            5,
	}
	for p, sz := range files {
		writeFile(t, filepath.Join(root, filepath.FromSlash(p)), sz)
	}
	os.MkdirAll(filepath.Join(root, "空文件夹"), 0o755)
	markHidden(t, filepath.Join(root, ".隐藏文件夹"))
	markHidden(t, filepath.Join(root, "工作", ".hidden.txt"))
	return root
}

func scanApp(t *testing.T, root string, skipHidden bool) *App {
	t.Helper()
	a := newApp(filepath.Join(t.TempDir(), "log.txt"))
	if err := a.StartScan(root, skipHidden); err != nil {
		t.Fatal(err)
	}
	waitScan(t, a)
	return a
}

func waitScan(t *testing.T, a *App) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for a.ScanStatus().Running {
		if time.Now().After(deadline) {
			t.Fatal("scan timeout")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (a *App) dirByPath(t *testing.T, rel string) *Dir {
	t.Helper()
	a.tree.mu.RLock()
	defer a.tree.mu.RUnlock()
	d := a.tree.lookupDir(filepath.Join(a.tree.rootPath, filepath.FromSlash(rel)))
	if d == nil {
		t.Fatalf("dir %q not in tree", rel)
	}
	return d
}

func fileRef(t *testing.T, a *App, rel string) Ref {
	d := a.dirByPath(t, filepath.ToSlash(filepath.Dir(filepath.FromSlash(rel))))
	return Ref{D: d.ID, N: filepath.Base(rel)}
}

// snapshot 生成树的规范化描述（相对路径 → 大小），用于和重新扫描的结果比较。
func snapshot(tr *Tree) []string {
	tr.mu.RLock()
	defer tr.mu.RUnlock()
	var out []string
	var walk func(d *Dir)
	walk = func(d *Dir) {
		rel := relPath(tr.root, d)
		out = append(out, fmt.Sprintf("D %s size=%d files=%d dirs=%d", rel, d.Size, d.NFiles, d.NDirs))
		for _, f := range d.Files {
			out = append(out, fmt.Sprintf("F %s size=%d ext=%s", filepath.Join(rel, f.Name), f.Size, f.Ext))
		}
		for _, c := range d.Dirs {
			if tr.dirs[c.ID] != c {
				out = append(out, "UNREGISTERED "+c.Name)
			}
			if c.Parent != d {
				out = append(out, "BADPARENT "+c.Name)
			}
			walk(c)
		}
	}
	walk(tr.root)
	sort.Strings(out)
	return out
}

// assertTreeMatchesDisk 检查内存中的树与磁盘上的真实状态完全一致。
func assertTreeMatchesDisk(t *testing.T, a *App) {
	t.Helper()
	fresh := scanApp(t, a.tree.rootPath, a.tree.skipHidden)
	got, want := snapshot(a.tree), snapshot(fresh.tree)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("tree out of sync with disk\n--- in memory ---\n%s\n--- on disk ---\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	a.tree.mu.RLock()
	n := 0
	var count func(d *Dir)
	count = func(d *Dir) {
		n++
		for _, c := range d.Dirs {
			count(c)
		}
	}
	count(a.tree.root)
	if n != len(a.tree.dirs) {
		t.Fatalf("dirs map has %d entries, tree has %d dirs", len(a.tree.dirs), n)
	}
	a.tree.mu.RUnlock()
}

func mustExist(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("expected %s to exist: %v", p, err)
	}
}

func mustNotExist(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Lstat(p); err == nil {
		t.Fatalf("expected %s to not exist", p)
	}
}

// ------------------------------------------------------------ 测试用例

func TestValidateName(t *testing.T) {
	good := []string{"a.txt", "照片 2020", "报告(最终版).docx", ".gitignore", "CONSOLE.txt", "a..b"}
	bad := []string{"", "  ", ".", "..", "a/b", `a\b`, "a:b", "a*b", "a?b", `a"b`, "a<b", "a>b", "a|b", "CON", "con.txt", "NUL", "com1.log", "LPT9", "abc.", "abc ", " abc", "a\tb", strings.Repeat("长", 256)}
	for _, n := range good {
		if err := validateName(n); err != nil {
			t.Errorf("validateName(%q) = %v, want ok", n, err)
		}
	}
	for _, n := range bad {
		if err := validateName(n); err == nil {
			t.Errorf("validateName(%q) = ok, want error", n)
		}
	}
}

func TestNormalizeInput(t *testing.T) {
	dir := t.TempDir()
	for _, in := range []string{dir, `"` + dir + `"`, "  " + dir + "  ", `'` + dir + `'`} {
		got, err := normalizeInput(in)
		if err != nil || got != dir {
			t.Errorf("normalizeInput(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := normalizeInput(filepath.Join(dir, "nope")); err == nil {
		t.Error("expected error for missing dir")
	}
	f := filepath.Join(dir, "f.txt")
	writeFile(t, f, 1)
	if _, err := normalizeInput(f); err == nil {
		t.Error("expected error for file path")
	}
	if _, err := normalizeInput(""); err == nil {
		t.Error("expected error for empty")
	}
}

func TestExtAndCategory(t *testing.T) {
	cases := map[string]string{"a.JPG": "jpg", "archive.tar.gz": "gz", ".gitignore": "", "noext": "", "x.": "", "报告.DOCX": "docx"}
	for name, want := range cases {
		if got := extOf(name); got != want {
			t.Errorf("extOf(%q)=%q want %q", name, got, want)
		}
	}
	if categoryOf("jpg") != "image" || categoryOf("") != "noext" || categoryOf("zzz") != "other" || categoryOf("pdf") != "pdf" {
		t.Error("category mapping wrong")
	}
}

func TestNaturalCompare(t *testing.T) {
	names := []string{"文件10", "文件2", "文件1", "b", "A", "文件02"}
	sort.Slice(names, func(i, j int) bool { return naturalCompare(names[i], names[j]) < 0 })
	want := "A,b,文件1,文件2,文件02,文件10"
	if got := strings.Join(names, ","); got != want {
		t.Errorf("got %s want %s", got, want)
	}
}

func TestScan(t *testing.T) {
	root := makeMessyTree(t)
	a := scanApp(t, root, true)
	tr := a.tree
	if tr.root.NFiles != 16 {
		t.Errorf("NFiles = %d, want 16 (hidden skipped)", tr.root.NFiles)
	}
	wantSize := int64(1000 + 2000 + 3000 + 400 + 500 + 600 + 70 + 800 + 5000 + 9000 + 900 + 10 + 300 + 1 + 2 + 50)
	if tr.root.Size != wantSize {
		t.Errorf("Size = %d, want %d", tr.root.Size, wantSize)
	}
	deep := a.dirByPath(t, "工作/项目B/深/很深/非常深/最深")
	if deep.NFiles != 2 || deep.Size != 870 {
		t.Errorf("deep dir: %d files %d bytes", deep.NFiles, deep.Size)
	}
	if a.dirByPath(t, "工作/项目B").Size != 870 {
		t.Error("aggregate size wrong")
	}
	// 包含隐藏文件
	a2 := scanApp(t, root, false)
	if a2.tree.root.NFiles != 18 {
		t.Errorf("with hidden: NFiles = %d, want 18", a2.tree.root.NFiles)
	}
}

func TestScanSkipsSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	root := makeMessyTree(t)
	// 指向上级的目录链接会造成死循环，必须跳过
	os.Symlink(root, filepath.Join(root, "工作", "循环链接"))
	os.Symlink(filepath.Join(root, "下载", "电影.mp4"), filepath.Join(root, "电影快捷方式.mp4"))
	a := scanApp(t, root, true)
	if a.tree.root.NFiles != 16 {
		t.Errorf("NFiles = %d, want 16", a.tree.root.NFiles)
	}
}

func TestScanUnreadableDir(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip()
	}
	root := makeMessyTree(t)
	locked := filepath.Join(root, "锁定")
	writeFile(t, filepath.Join(locked, "x.txt"), 5)
	os.Chmod(locked, 0)
	defer os.Chmod(locked, 0o755)
	a := scanApp(t, root, true)
	if a.tree.errCount != 1 || a.dirByPath(t, "锁定").Err == "" {
		t.Errorf("errCount = %d", a.tree.errCount)
	}
}

func TestQuery(t *testing.T) {
	root := makeMessyTree(t)
	a := scanApp(t, root, true)
	tr := a.tree
	rid := tr.root.ID

	rows := tr.collect(&Query{Dir: rid})
	// 根目录：7 个文件夹（照片 工作 下载 杂项 空文件夹）+ 1 文件
	var dirs, files []string
	for _, r := range rows {
		if r.Kind == "d" {
			dirs = append(dirs, r.Name)
		} else {
			files = append(files, r.Name)
		}
	}
	if len(dirs) != 5 || len(files) != 1 {
		t.Errorf("root listing: dirs=%v files=%v", dirs, files)
	}
	if rows[0].Kind != "d" {
		t.Error("dirs should come first")
	}

	rows = tr.collect(&Query{Dir: rid, Recursive: true, Ext: "png"})
	if len(rows) != 2 {
		t.Fatalf("png count = %d", len(rows))
	}
	for _, r := range rows {
		if r.Loc == "" {
			t.Error("recursive rows need location")
		}
	}
	rows = tr.collect(&Query{Dir: rid, Recursive: true, Ext: "jpg", Sort: "size", Desc: true})
	if len(rows) != 2 || rows[0].Size != 2000 {
		t.Errorf("jpg (case-insensitive ext) rows = %+v", rows)
	}
	rows = tr.collect(&Query{Dir: rid, Recursive: true, Cat: "image"})
	if len(rows) != 4 {
		t.Errorf("image category count = %d", len(rows))
	}
	rows = tr.collect(&Query{Dir: rid, Recursive: true, Ext: "-"})
	if len(rows) != 1 || rows[0].Name != "无扩展名文件" {
		t.Errorf("no-ext rows = %+v", rows)
	}
	rows = tr.collect(&Query{Dir: rid, Recursive: true, Q: "项目"})
	if len(rows) != 2 || rows[0].Kind != "d" {
		t.Errorf("search dirs = %+v", rows)
	}
	rows = tr.collect(&Query{Dir: rid, Recursive: true, Q: "IMG"})
	if len(rows) != 2 {
		t.Errorf("search case-insensitive = %d", len(rows))
	}
	rows = tr.collect(&Query{Dir: rid, Recursive: true, Sort: "size", Desc: true})
	if rows[0].Name != "电影.mp4" {
		t.Errorf("biggest file = %s", rows[0].Name)
	}
}

func TestStats(t *testing.T) {
	root := makeMessyTree(t)
	a := scanApp(t, root, true)
	cats := a.tree.stats(a.tree.root)
	by := map[string]CatStat{}
	var total int64
	for _, c := range cats {
		by[c.Key] = c
		total += c.Size
	}
	if total != a.tree.root.Size {
		t.Errorf("category total %d != root size %d", total, a.tree.root.Size)
	}
	if by["image"].Count != 4 || by["image"].Size != 6800 {
		t.Errorf("image stat = %+v", by["image"])
	}
	if by["video"].Size != 9000 || cats[0].Key != "video" {
		t.Errorf("video should be largest: %+v", cats[0])
	}
	if len(by["doc"].Exts) != 2 { // docx, txt
		t.Errorf("doc exts = %+v", by["doc"].Exts)
	}
}

func TestMoveFilesIntoNewFolder(t *testing.T) {
	root := makeMessyTree(t)
	a := scanApp(t, root, true)
	res, err := a.Mkdir(MkdirReq{Parent: a.tree.root.ID, Name: "整理后的图片"})
	if err != nil {
		t.Fatal(err)
	}
	target := res.NewID
	// 用“查询全选”的方式把所有图片移动到新文件夹
	res, err = a.Move(MoveReq{Query: &Query{Dir: a.tree.root.ID, Recursive: true, Cat: "image"}, Target: target, Conflict: "rename"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Done != 4 || len(res.Errors) != 0 {
		t.Fatalf("move result %+v", res)
	}
	for _, n := range []string{"IMG_001.JPG", "IMG_002.jpg", "合影.png", "截图.png"} {
		mustExist(t, filepath.Join(root, "整理后的图片", n))
	}
	mustNotExist(t, filepath.Join(root, "照片", "2019", "旅行", "IMG_001.JPG"))
	if a.dirByPath(t, "整理后的图片").Size != 6800 || a.dirByPath(t, "照片").Size != 0 {
		t.Error("aggregates not updated")
	}
	assertTreeMatchesDisk(t, a)

	// 再次移动：已在目标中的文件应跳过
	res, _ = a.Move(MoveReq{Query: &Query{Dir: a.tree.root.ID, Recursive: true, Cat: "image"}, Target: target})
	if res.Done != 0 || res.Skipped != 4 {
		t.Errorf("second move %+v", res)
	}

	// 撤销移动，再撤销新建文件夹
	if _, _, err := a.Undo(); err != nil {
		t.Fatal(err)
	}
	mustExist(t, filepath.Join(root, "照片", "2019", "旅行", "IMG_001.JPG"))
	mustExist(t, filepath.Join(root, "工作", "项目B", "深", "很深", "非常深", "最深", "截图.png"))
	assertTreeMatchesDisk(t, a)
	if _, _, err := a.Undo(); err != nil {
		t.Fatal(err)
	}
	mustNotExist(t, filepath.Join(root, "整理后的图片"))
	assertTreeMatchesDisk(t, a)
	if _, _, err := a.Undo(); err == nil {
		t.Error("expected nothing to undo")
	}
}

func TestMoveConflicts(t *testing.T) {
	root := makeMessyTree(t)
	writeFile(t, filepath.Join(root, "目标", "a.txt"), 7)
	writeFile(t, filepath.Join(root, "目标", "a (1).txt"), 8)
	a := scanApp(t, root, true)
	tgt := a.dirByPath(t, "目标").ID

	res, err := a.Move(MoveReq{Items: []Ref{fileRef(t, a, "杂项/a.txt")}, Target: tgt, Conflict: "skip"})
	if err != nil || res.Skipped != 1 || res.Done != 0 {
		t.Fatalf("skip: %+v %v", res, err)
	}
	mustExist(t, filepath.Join(root, "杂项", "a.txt"))

	res, err = a.Move(MoveReq{Items: []Ref{fileRef(t, a, "杂项/a.txt")}, Target: tgt, Conflict: "rename"})
	if err != nil || res.Done != 1 {
		t.Fatalf("rename: %+v %v", res, err)
	}
	mustExist(t, filepath.Join(root, "目标", "a (2).txt"))
	if b, _ := os.ReadFile(filepath.Join(root, "目标", "a.txt")); len(b) != 7 {
		t.Error("existing file was overwritten!")
	}
	assertTreeMatchesDisk(t, a)
}

func TestMoveFolders(t *testing.T) {
	root := makeMessyTree(t)
	a := scanApp(t, root, true)
	projA := a.dirByPath(t, "工作/项目A")
	projAID := projA.ID
	down := a.dirByPath(t, "下载")

	// 文件夹 + 其内部的文件同时被选中：内部文件随文件夹移动，不单独处理
	res, err := a.Move(MoveReq{Items: []Ref{{D: projA.ID}, fileRef(t, a, "工作/项目A/报价单.xlsx")}, Target: down.ID})
	if err != nil || res.Done != 1 || len(res.Errors) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	mustExist(t, filepath.Join(root, "下载", "项目A", "资料", "参考.pdf"))
	if a.dirByPath(t, "下载/项目A").ID != projAID {
		t.Error("moved folder should keep its id")
	}
	assertTreeMatchesDisk(t, a)

	// 不能移动到自身的子文件夹中
	sub := a.dirByPath(t, "下载/项目A/资料")
	res, _ = a.Move(MoveReq{Items: []Ref{{D: projAID}}, Target: sub.ID})
	if res.Done != 0 || len(res.Errors) != 1 {
		t.Errorf("expected refusal: %+v", res)
	}
	// 不能移动根目录
	res, _ = a.Move(MoveReq{Items: []Ref{{D: a.tree.root.ID}}, Target: sub.ID})
	if res.Done != 0 || len(res.Errors) != 1 {
		t.Errorf("expected root refusal: %+v", res)
	}
	a.Undo()
	mustExist(t, filepath.Join(root, "工作", "项目A", "资料", "参考.pdf"))
	assertTreeMatchesDisk(t, a)
}

func TestRename(t *testing.T) {
	root := makeMessyTree(t)
	a := scanApp(t, root, true)

	if _, err := a.Rename(RenameReq{Item: fileRef(t, a, "杂项/a.txt"), NewName: "b.TXT"}); err == nil {
		t.Error("expected conflict error")
	}
	if _, err := a.Rename(RenameReq{Item: fileRef(t, a, "杂项/a.txt"), NewName: "bad:name"}); err == nil {
		t.Error("expected invalid name error")
	}
	if _, err := a.Rename(RenameReq{Item: fileRef(t, a, "杂项/a.txt"), NewName: "  笔记.md  "}); err != nil {
		t.Fatal(err)
	}
	mustExist(t, filepath.Join(root, "杂项", "笔记.md"))
	rows := a.tree.collect(&Query{Dir: a.tree.root.ID, Recursive: true, Ext: "md"})
	if len(rows) != 1 {
		t.Error("renamed file ext not updated")
	}
	// 只修改大小写
	if _, err := a.Rename(RenameReq{Item: fileRef(t, a, "杂项/笔记.md"), NewName: "笔记.MD"}); err != nil {
		t.Fatalf("case-only rename: %v", err)
	}
	names, _ := os.ReadDir(filepath.Join(root, "杂项"))
	found := false
	for _, e := range names {
		found = found || e.Name() == "笔记.MD"
	}
	if !found {
		t.Error("case-only rename not applied on disk")
	}
	// 重命名文件夹
	d := a.dirByPath(t, "工作/项目B")
	if _, err := a.Rename(RenameReq{Item: Ref{D: d.ID}, NewName: "项目B-归档"}); err != nil {
		t.Fatal(err)
	}
	mustExist(t, filepath.Join(root, "工作", "项目B-归档", "深", "很深", "非常深", "最深", "说明.txt"))
	if _, err := a.Rename(RenameReq{Item: Ref{D: a.tree.root.ID}, NewName: "x"}); err == nil {
		t.Error("renaming root should fail")
	}
	assertTreeMatchesDisk(t, a)
	for i := 0; i < 3; i++ {
		if _, _, err := a.Undo(); err != nil {
			t.Fatal(err)
		}
	}
	mustExist(t, filepath.Join(root, "杂项", "a.txt"))
	mustExist(t, filepath.Join(root, "工作", "项目B"))
	assertTreeMatchesDisk(t, a)
}

func TestMkdirValidation(t *testing.T) {
	root := makeMessyTree(t)
	a := scanApp(t, root, true)
	if _, err := a.Mkdir(MkdirReq{Parent: a.tree.root.ID, Name: "下载"}); err == nil {
		t.Error("duplicate folder should fail")
	}
	if _, err := a.Mkdir(MkdirReq{Parent: a.tree.root.ID, Name: "a?b"}); err == nil {
		t.Error("invalid name should fail")
	}
	if _, err := a.Mkdir(MkdirReq{Parent: 999999, Name: "x"}); err == nil {
		t.Error("bad parent should fail")
	}
}

func TestBatchRename(t *testing.T) {
	root := makeMessyTree(t)
	a := scanApp(t, root, true)
	q := &Query{Dir: a.dirByPath(t, "照片/2019/旅行").ID, Sort: "name"}
	res, err := a.BatchRename(BatchRenameReq{Query: q, Template: "旅行_{n}", Start: 1, Digits: 3, Preview: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.OK != 2 || res.Plans[0].New != "旅行_001.JPG" || res.Plans[1].New != "旅行_002.jpg" {
		t.Fatalf("preview %+v", res.Plans)
	}
	mustExist(t, filepath.Join(root, "照片", "2019", "旅行", "IMG_001.JPG")) // 预览不应改动文件
	res, err = a.BatchRename(BatchRenameReq{Query: q, Template: "旅行_{n}", Start: 1, Digits: 3})
	if err != nil || res.Done != 2 {
		t.Fatalf("%+v %v", res, err)
	}
	mustExist(t, filepath.Join(root, "照片", "2019", "旅行", "旅行_001.JPG"))
	assertTreeMatchesDisk(t, a)

	// 查找替换
	res, _ = a.BatchRename(BatchRenameReq{Query: q, Find: "旅行", Replace: "Trip"})
	if res.Done != 2 {
		t.Fatalf("find/replace %+v", res)
	}
	mustExist(t, filepath.Join(root, "照片", "2019", "旅行", "Trip_002.jpg"))

	// 本批次内互换名称（a→b, b→a）不应冲突
	writeFile(t, filepath.Join(root, "互换", "x.txt"), 1)
	writeFile(t, filepath.Join(root, "互换", "y.txt"), 2)
	a = scanApp(t, root, true)
	sw := a.dirByPath(t, "互换")
	res, err = a.BatchRename(BatchRenameReq{Items: []Ref{{D: sw.ID, N: "x.txt"}, {D: sw.ID, N: "y.txt"}}, Template: "{name}", Find: "x", Replace: "y"})
	// x→y 与 y 冲突（y 不在改名之列：y 的新名称仍是 y）
	if err != nil || res.Done != 0 || res.Skipped != 1 {
		t.Fatalf("expected conflict: %+v %v", res, err)
	}
	// 真正的互换：模板 {n}.txt，顺序 y, x → y 改为 1、x 改为 2；然后再换回
	res, _ = a.BatchRename(BatchRenameReq{Items: []Ref{{D: sw.ID, N: "x.txt"}, {D: sw.ID, N: "y.txt"}}, Template: "{n}", Start: 1})
	if res.Done != 2 {
		t.Fatalf("%+v", res)
	}
	res, _ = a.BatchRename(BatchRenameReq{Items: []Ref{{D: sw.ID, N: "1.txt"}, {D: sw.ID, N: "2.txt"}}, Template: "{n}", Start: 2})
	// 1→2, 2→3：2 被本批次改走，所以不冲突
	if res.Done != 2 || len(res.Errors) != 0 {
		t.Fatalf("chain rename %+v", res)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "互换", "2.txt")); len(b) != 1 {
		t.Error("chain rename corrupted content")
	}
	if b, _ := os.ReadFile(filepath.Join(root, "互换", "3.txt")); len(b) != 2 {
		t.Error("chain rename corrupted content")
	}
	// 生成同名的批次内冲突
	res, _ = a.BatchRename(BatchRenameReq{Items: []Ref{{D: sw.ID, N: "2.txt"}, {D: sw.ID, N: "3.txt"}}, Template: "同名", Preview: true})
	if res.OK != 0 {
		t.Errorf("duplicate names in batch should conflict: %+v", res.Plans)
	}
	assertTreeMatchesDisk(t, a)
	// 撤销链式重命名
	a.Undo()
	mustExist(t, filepath.Join(root, "互换", "1.txt"))
	mustExist(t, filepath.Join(root, "互换", "2.txt"))
	if b, _ := os.ReadFile(filepath.Join(root, "互换", "1.txt")); len(b) != 1 {
		t.Error("undo chain rename wrong content")
	}
	assertTreeMatchesDisk(t, a)
	entries, _ := os.ReadDir(filepath.Join(root, "互换"))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".~rename") {
			t.Error("temp file left behind")
		}
	}
}

func TestUndoAfterExternalChange(t *testing.T) {
	root := makeMessyTree(t)
	a := scanApp(t, root, true)
	tgt := a.dirByPath(t, "杂项").ID
	a.Move(MoveReq{Items: []Ref{fileRef(t, a, "下载/歌曲.mp3")}, Target: tgt})
	// 用户在外部把原位置放了一个同名文件
	writeFile(t, filepath.Join(root, "下载", "歌曲.mp3"), 1)
	res, _, err := a.Undo()
	if err != nil || len(res.Errors) != 1 {
		t.Fatalf("expected undo to refuse overwriting: %+v %v", res, err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "下载", "歌曲.mp3")); len(b) != 1 {
		t.Error("undo overwrote a file")
	}
}

func TestCopyAll(t *testing.T) {
	src := makeMessyTree(t)
	dst := filepath.Join(t.TempDir(), "copy")
	if err := copyAll(src, dst); err != nil {
		t.Fatal(err)
	}
	a1, a2 := scanApp(t, src, false), scanApp(t, dst, false)
	if strings.Join(snapshot(a1.tree), "\n") != strings.Join(snapshot(a2.tree), "\n") {
		t.Error("copy differs")
	}
	if err := copyAll(src, dst); err == nil {
		t.Error("copy onto existing should fail")
	}
}

func TestLookupDir(t *testing.T) {
	root := makeMessyTree(t)
	a := scanApp(t, root, true)
	tr := a.tree
	if tr.lookupDir(root) != tr.root {
		t.Error("root lookup")
	}
	if tr.lookupDir(filepath.Dir(root)) != nil {
		t.Error("parent of root should be outside tree")
	}
	if tr.lookupDir(root+"x") != nil {
		t.Error("sibling with same prefix should be outside tree")
	}
	if d := tr.lookupDir(filepath.Join(root, "工作", "项目A")); d == nil || d.Name != "项目A" {
		t.Error("nested lookup")
	}
}

// 随机操作压力测试：大量随机的移动/重命名/新建/撤销之后，内存中的树必须与磁盘一致。
func TestRandomOperations(t *testing.T) {
	root := t.TempDir()
	rng := rand.New(rand.NewSource(42))
	exts := []string{"jpg", "png", "pdf", "docx", "txt", "mp4", "zip", ""}
	for i := 0; i < 300; i++ {
		depth := rng.Intn(6)
		p := root
		for d := 0; d < depth; d++ {
			p = filepath.Join(p, fmt.Sprintf("d%d", rng.Intn(4)))
		}
		name := fmt.Sprintf("f%d", i)
		if e := exts[rng.Intn(len(exts))]; e != "" {
			name += "." + e
		}
		writeFile(t, filepath.Join(p, name), rng.Intn(5000))
	}
	a := scanApp(t, root, true)
	before := snapshot(a.tree)
	allDirs := func() []*Dir {
		a.tree.mu.RLock()
		defer a.tree.mu.RUnlock()
		var out []*Dir
		for _, d := range a.tree.dirs {
			out = append(out, d)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		return out
	}
	randomFiles := func(n int) []Ref {
		rows := a.tree.collect(&Query{Dir: a.tree.root.ID, Recursive: true, Sort: "name"})
		var refs []Ref
		for i := 0; i < n && len(rows) > 0; i++ {
			r := rows[rng.Intn(len(rows))]
			refs = append(refs, Ref{D: r.D, N: r.Name})
		}
		// 去重
		seen := map[Ref]bool{}
		out := refs[:0]
		for _, r := range refs {
			if !seen[r] {
				seen[r] = true
				out = append(out, r)
			}
		}
		return out
	}
	for step := 0; step < 95; step++ {
		dirs := allDirs()
		switch rng.Intn(6) {
		case 0:
			a.Move(MoveReq{Items: randomFiles(1 + rng.Intn(10)), Target: dirs[rng.Intn(len(dirs))].ID, Conflict: "rename"})
		case 1:
			d := dirs[rng.Intn(len(dirs))]
			a.Move(MoveReq{Items: []Ref{{D: d.ID}}, Target: dirs[rng.Intn(len(dirs))].ID})
		case 2:
			a.Mkdir(MkdirReq{Parent: dirs[rng.Intn(len(dirs))].ID, Name: fmt.Sprintf("新%d", step)})
		case 3:
			if f := randomFiles(1); len(f) == 1 {
				a.Rename(RenameReq{Item: f[0], NewName: fmt.Sprintf("r%d.%s", step, exts[rng.Intn(len(exts)-1)])})
			}
		case 4:
			a.BatchRename(BatchRenameReq{Items: randomFiles(5), Template: "{name}_{n}", Start: step})
		case 5:
			a.Undo()
		}
	}
	assertTreeMatchesDisk(t, a)
	// 全部撤销后应恢复原状
	for {
		res, _, err := a.Undo()
		if err != nil {
			break
		}
		if len(res.Errors) > 0 {
			t.Errorf("undo errors: %v", res.Errors)
		}
	}
	assertTreeMatchesDisk(t, a)
	if after := snapshot(a.tree); strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Errorf("undo all did not restore original state\n%s\n---\n%s", strings.Join(before, "\n"), strings.Join(after, "\n"))
	}
}
