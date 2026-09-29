//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// 以下测试只在 Windows 上运行，覆盖 Windows 特有的行为。

// Windows 的临时目录位于 AppData 下，属于受保护位置，测试改用系统盘根目录下的临时文件夹。
func TestMain(m *testing.M) {
	tmp := os.Getenv("SystemDrive") + `\fo-test-tmp`
	if err := os.MkdirAll(tmp, 0o755); err == nil {
		os.Setenv("TMP", tmp)
		os.Setenv("TEMP", tmp)
	}
	code := m.Run()
	os.RemoveAll(tmp)
	os.Exit(code)
}

// markHidden 让通用测试中以点开头的“隐藏文件”在 Windows 上也带有隐藏属性。
func markHidden(t *testing.T, p string) {
	p16, _ := windows.UTF16PtrFromString(p)
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
	if err := windows.SetFileAttributes(p16, windows.FILE_ATTRIBUTE_HIDDEN); err != nil {
		t.Fatal(err)
	}
}

func TestWinJunctionIsSkipped(t *testing.T) {
	root := makeMessyTree(t)
	// 指向自身上级的目录联接：如果被跟随会无限循环
	link := filepath.Join(root, "工作", "联接到根目录")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, root).CombinedOutput(); err != nil {
		t.Skipf("mklink failed: %v %s", err, out)
	}
	a := scanApp(t, root, false)
	// 16 个普通文件 + 2 个隐藏位置的文件（skipHidden=false）
	if a.tree.root.NFiles != 18 {
		t.Errorf("NFiles = %d, want 18 (junction must be skipped)", a.tree.root.NFiles)
	}
	for _, c := range a.dirByPath(t, "工作").Dirs {
		if c.Name == "联接到根目录" {
			t.Error("junction should not appear as a folder")
		}
	}
}

func TestWinHiddenAndSystemAttributes(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "普通.txt"), 1)
	writeFile(t, filepath.Join(root, "隐藏.txt"), 2)
	writeFile(t, filepath.Join(root, "系统", "x.txt"), 3)
	setAttr := func(p string, attr uint32) {
		p16, _ := windows.UTF16PtrFromString(p)
		if err := windows.SetFileAttributes(p16, attr); err != nil {
			t.Fatal(err)
		}
	}
	setAttr(filepath.Join(root, "隐藏.txt"), windows.FILE_ATTRIBUTE_HIDDEN)
	setAttr(filepath.Join(root, "系统"), windows.FILE_ATTRIBUTE_SYSTEM)
	if n := scanApp(t, root, true).tree.root.NFiles; n != 1 {
		t.Errorf("skipHidden: NFiles = %d, want 1", n)
	}
	if n := scanApp(t, root, false).tree.root.NFiles; n != 3 {
		t.Errorf("with hidden: NFiles = %d, want 3", n)
	}
}

func TestWinProtectedPaths(t *testing.T) {
	sys := os.Getenv("SystemRoot")
	cases := map[string]bool{
		sys:                            true,
		filepath.Join(sys, "System32"): true,
		strings.ToLower(sys) + `\temp`: true,
		os.Getenv("ProgramFiles"):      true,
		filepath.Join(os.Getenv("ProgramFiles"), "某软件"): true,
		`C:\Users\someone\AppData\Local\x`:              true,
		`D:\$RECYCLE.BIN\S-1-5`:                         true,
		`D:\照片\2020`:                                    false,
		`C:\Users\someone\Documents`:                    false,
		sys + "x":                                       false, // 仅前缀相同的其他文件夹
	}
	for p, want := range cases {
		if got := protectedReason(p) != ""; got != want {
			t.Errorf("protectedReason(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestWinPlacesAndDriveRoot(t *testing.T) {
	places := listPlaces()
	foundDrive := false
	for _, p := range places {
		if p.Kind == "drive" && len(p.Path) == 3 && p.Path[1:] == `:\` && p.Total > 0 {
			foundDrive = true
		}
	}
	if !foundDrive {
		t.Errorf("no drive in places: %+v", places)
	}
	sysDrive := os.Getenv("SystemDrive") // 例如 "C:"
	got, err := normalizeInput(sysDrive)
	if err != nil || got != sysDrive+`\` {
		t.Errorf("normalizeInput(%q) = %q, %v", sysDrive, got, err)
	}
	got, err = normalizeInput(strings.ToLower(sysDrive) + "/")
	if err != nil || !strings.EqualFold(got, sysDrive+`\`) {
		t.Errorf("forward slash drive: %q, %v", got, err)
	}
}

func TestWinDriveRootTree(t *testing.T) {
	// 以盘符根目录作为扫描根时，路径拼接和查找必须正确（"C:\" 带结尾反斜杠）
	tr := &Tree{dirs: map[int]*Dir{}, rootPath: `C:\`}
	tr.root = &Dir{ID: 1, Name: `C:\`}
	child := &Dir{ID: 2, Name: "Users", Parent: tr.root}
	tr.root.Dirs = []*Dir{child}
	tr.register(tr.root)
	if p := tr.pathOf(child); p != `C:\Users` {
		t.Errorf("pathOf = %q", p)
	}
	if tr.lookupDir(`C:\Users`) != child || tr.lookupDir(`c:\users`) != child || tr.lookupDir(`C:\`) != tr.root {
		t.Error("lookupDir failed on drive root")
	}
	if tr.lookupDir(`D:\Users`) != nil {
		t.Error("other drive should not match")
	}
}

func TestWinMoveReadOnlyAndLockedFiles(t *testing.T) {
	root := makeMessyTree(t)
	ro := filepath.Join(root, "杂项", "a.txt")
	p16, _ := windows.UTF16PtrFromString(ro)
	windows.SetFileAttributes(p16, windows.FILE_ATTRIBUTE_READONLY)
	a := scanApp(t, root, true)
	tgt := a.dirByPath(t, "下载").ID
	res, err := a.Move(MoveReq{Items: []Ref{fileRef(t, a, "杂项/a.txt")}, Target: tgt})
	if err != nil || res.Done != 1 {
		t.Fatalf("read-only move: %+v %v", res, err)
	}
	// 被其他程序打开（未共享删除权限）的文件无法移动，应给出明确提示且文件保持原位
	locked := filepath.Join(root, "杂项", "b.TXT")
	f, err := os.Open(locked)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	res, _ = a.Move(MoveReq{Items: []Ref{fileRef(t, a, "杂项/b.TXT")}, Target: tgt})
	if res.Done != 0 || len(res.Errors) != 1 {
		t.Fatalf("locked move: %+v", res)
	}
	t.Logf("locked file message: %s", res.Errors[0])
	mustExist(t, locked)
	assertTreeMatchesDisk(t, a)
}
