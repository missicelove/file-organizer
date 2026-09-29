package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

type Place struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Kind  string `json:"kind"` // drive | folder
	Total uint64 `json:"total,omitempty"`
	Free  uint64 `json:"free,omitempty"`
}

type App struct {
	tree  *Tree
	hist  *History
	quick *QuickStore

	scanMu    sync.Mutex
	cur       *scanner
	scanPath  string
	scanStart time.Time
	scanTook  time.Duration
	scanErr   string
	scanning  bool
}

// newApp 创建程序状态；“最近新建的文件夹”保存在日志文件旁边。
func newApp(logPath string) *App {
	quickPath := ""
	if logPath != "" {
		quickPath = filepath.Join(filepath.Dir(logPath), "最近文件夹.json")
	}
	return &App{tree: &Tree{dirs: map[int]*Dir{}}, hist: newHistory(logPath), quick: loadQuick(quickPath)}
}

// normalizeInput 整理用户输入的路径：去掉引号（资源管理器“复制为路径”会带引号），补全盘符根目录。
func normalizeInput(p string) (string, error) {
	p = strings.TrimSpace(p)
	p = strings.Trim(p, `"'`)
	p = strings.TrimSpace(p)
	if p == "" {
		return "", errors.New("请输入要整理的文件夹路径")
	}
	if len(p) == 2 && p[1] == ':' && ((p[0] >= 'A' && p[0] <= 'Z') || (p[0] >= 'a' && p[0] <= 'z')) {
		p += string(os.PathSeparator)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return "", errors.New("找不到这个文件夹：" + abs)
		}
		return "", errors.New("无法打开这个文件夹：" + errText(err))
	}
	if !fi.IsDir() {
		return "", errors.New("这不是一个文件夹：" + abs)
	}
	return abs, nil
}

func (a *App) StartScan(input string, skipHidden bool) error {
	path, err := normalizeInput(input)
	if err != nil {
		return err
	}
	a.scanMu.Lock()
	if a.cur != nil {
		a.cur.cancelled.Store(true)
	}
	s := &scanner{t: a.tree, skipHidden: skipHidden, sem: make(chan struct{}, 16)}
	s.current.Store(path)
	a.cur, a.scanPath, a.scanStart, a.scanErr, a.scanning = s, path, time.Now(), "", true
	a.scanMu.Unlock()

	go func() {
		name := filepath.Base(path)
		if name == string(os.PathSeparator) || name == "." || strings.HasSuffix(name, ":"+string(os.PathSeparator)) {
			name = path
		}
		root := a.tree.newDirNode(name, nil)
		if fi, err := os.Stat(path); err == nil {
			root.MTime = fi.ModTime().Unix()
		}
		s.scanDir(root, path)
		s.wg.Wait()
		if s.cancelled.Load() {
			return
		}
		computeAgg(root)
		errs := countErrs(root)
		took := time.Since(a.scanStart)

		t := a.tree
		t.mu.Lock()
		t.root, t.rootPath, t.skipHidden = root, path, skipHidden
		t.dirs = map[int]*Dir{}
		t.register(root)
		t.scannedAt, t.scanTook, t.errCount = time.Now(), took, errs
		t.version.Add(1)
		t.mu.Unlock()

		a.scanMu.Lock()
		if a.cur == s {
			a.scanning, a.scanTook = false, took
		}
		a.scanMu.Unlock()
		debug.FreeOSMemory() // 尽快归还上一次扫描结果占用的内存
	}()
	return nil
}

func (a *App) CancelScan() {
	a.scanMu.Lock()
	defer a.scanMu.Unlock()
	if a.cur != nil && a.scanning {
		a.cur.cancelled.Store(true)
		a.scanning = false
		a.scanErr = "已取消扫描"
	}
}

func (a *App) ScanStatus() ScanProgress {
	a.scanMu.Lock()
	defer a.scanMu.Unlock()
	p := ScanProgress{Running: a.scanning, Error: a.scanErr}
	if s := a.cur; s != nil {
		p.Files, p.Dirs, p.Bytes = s.files.Load(), s.dirs.Load(), s.bytes.Load()
		if c, ok := s.current.Load().(string); ok {
			p.Current = c
		}
		if a.scanning {
			p.Elapsed = time.Since(a.scanStart).Milliseconds()
		} else {
			p.Elapsed = a.scanTook.Milliseconds()
			p.Done = a.scanErr == ""
		}
	}
	return p
}
