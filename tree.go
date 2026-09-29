package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Dir 是扫描得到的一个文件夹节点。Size/NFiles/NDirs 为整个子树的汇总值。
type Dir struct {
	ID     int
	Name   string
	Parent *Dir
	Dirs   []*Dir
	Files  []File
	Size   int64
	NFiles int
	NDirs  int
	MTime  int64
	Err    string
}

type File struct {
	Name  string
	Ext   string // 小写，不含点
	Size  int64
	MTime int64
}

// Tree 保存当前扫描结果。所有读写都通过 mu 保护。
type Tree struct {
	mu         sync.RWMutex
	root       *Dir
	rootPath   string
	dirs       map[int]*Dir
	nextID     atomic.Int64
	skipHidden bool
	scannedAt  time.Time
	scanTook   time.Duration
	errCount   int

	version atomic.Int64 // 每次修改树都会递增，用于让缓存失效
	cacheMu sync.Mutex
	cache   struct {
		version int64
		key     string
		rows    []Row
	}
}

func extOf(name string) string {
	i := strings.LastIndexByte(name, '.')
	if i <= 0 || i == len(name)-1 {
		return ""
	}
	return strings.ToLower(name[i+1:])
}

// addAgg 把变化量累加到 d 及其所有祖先上。
func (d *Dir) addAgg(size int64, nfiles, ndirs int) {
	for p := d; p != nil; p = p.Parent {
		p.Size += size
		p.NFiles += nfiles
		p.NDirs += ndirs
	}
}

func (d *Dir) childDir(name string) *Dir {
	for _, c := range d.Dirs {
		if c.Name == name {
			return c
		}
	}
	for _, c := range d.Dirs {
		if strings.EqualFold(c.Name, name) {
			return c
		}
	}
	return nil
}

func (d *Dir) fileIndex(name string) int {
	for i := range d.Files {
		if d.Files[i].Name == name {
			return i
		}
	}
	for i := range d.Files {
		if strings.EqualFold(d.Files[i].Name, name) {
			return i
		}
	}
	return -1
}

func (d *Dir) removeDir(c *Dir) {
	for i, x := range d.Dirs {
		if x == c {
			d.Dirs = append(d.Dirs[:i], d.Dirs[i+1:]...)
			return
		}
	}
}

func (d *Dir) isAncestorOf(x *Dir) bool {
	for p := x; p != nil; p = p.Parent {
		if p == d {
			return true
		}
	}
	return false
}

// pathOf 返回节点的完整路径（调用者需持有锁）。
func (t *Tree) pathOf(d *Dir) string {
	if d == t.root || d.Parent == nil {
		return t.rootPath
	}
	var parts []string
	for p := d; p != nil && p != t.root; p = p.Parent {
		parts = append(parts, p.Name)
	}
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return filepath.Join(append([]string{t.rootPath}, parts...)...)
}

// relPath 返回 d 相对于 base 的路径（d 必须在 base 之下）。
func relPath(base, d *Dir) string {
	var parts []string
	for p := d; p != nil && p != base; p = p.Parent {
		parts = append(parts, p.Name)
	}
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return strings.Join(parts, string(os.PathSeparator))
}

// lookupDir 按完整路径查找树中的文件夹，不在树中返回 nil。
func (t *Tree) lookupDir(path string) *Dir {
	if t.root == nil {
		return nil
	}
	rel, err := filepath.Rel(t.rootPath, filepath.Clean(path))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return nil
	}
	d := t.root
	if rel == "." {
		return d
	}
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		if d = d.childDir(part); d == nil {
			return nil
		}
	}
	return d
}

func (t *Tree) register(d *Dir) {
	t.dirs[d.ID] = d
	for _, c := range d.Dirs {
		t.register(c)
	}
}

func (t *Tree) unregister(d *Dir) {
	delete(t.dirs, d.ID)
	for _, c := range d.Dirs {
		t.unregister(c)
	}
}

func (t *Tree) newDirNode(name string, parent *Dir) *Dir {
	return &Dir{ID: int(t.nextID.Add(1)), Name: name, Parent: parent}
}

// ---------------------------------------------------------------- 扫描

type ScanProgress struct {
	Running bool   `json:"running"`
	Done    bool   `json:"done"`
	Files   int64  `json:"files"`
	Dirs    int64  `json:"dirs"`
	Bytes   int64  `json:"bytes"`
	Current string `json:"current"`
	Error   string `json:"error"`
	Elapsed int64  `json:"elapsedMs"`
}

type scanner struct {
	t          *Tree
	skipHidden bool
	sem        chan struct{}
	wg         sync.WaitGroup
	files      atomic.Int64
	dirs       atomic.Int64
	bytes      atomic.Int64
	errs       atomic.Int64
	current    atomic.Value
	cancelled  atomic.Bool
}

// classify 判断目录项应当如何处理：跳过、当作文件夹、当作文件。
const (
	entrySkip = iota
	entryDir
	entryFile
)

func classify(info fs.FileInfo, skipHidden bool) int {
	m := info.Mode()
	if m&fs.ModeSymlink != 0 {
		return entrySkip
	}
	if skipHidden && isHidden(info) {
		return entrySkip
	}
	if info.IsDir() {
		return entryDir
	}
	if isLinkDir(info) { // Windows 上的目录联接（junction）等
		return entrySkip
	}
	if m&(fs.ModeNamedPipe|fs.ModeSocket|fs.ModeDevice|fs.ModeCharDevice) != 0 {
		return entrySkip
	}
	return entryFile
}

func (s *scanner) scanDir(d *Dir, path string) {
	if s.cancelled.Load() {
		return
	}
	s.dirs.Add(1)
	if n := s.dirs.Load(); n%64 == 0 {
		s.current.Store(path)
	}
	f, err := os.Open(path)
	if err != nil {
		d.Err = errText(err)
		s.errs.Add(1)
		return
	}
	entries, err := f.ReadDir(-1)
	f.Close()
	if err != nil && len(entries) == 0 {
		d.Err = errText(err)
		s.errs.Add(1)
		return
	}
	var subdirs []*Dir
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		switch classify(info, s.skipHidden) {
		case entryDir:
			c := s.t.newDirNode(e.Name(), d)
			c.MTime = info.ModTime().Unix()
			subdirs = append(subdirs, c)
		case entryFile:
			sz := info.Size()
			d.Files = append(d.Files, File{Name: e.Name(), Ext: extOf(e.Name()), Size: sz, MTime: info.ModTime().Unix()})
			s.files.Add(1)
			s.bytes.Add(sz)
		}
	}
	d.Dirs = subdirs
	for _, c := range subdirs {
		cp := filepath.Join(path, c.Name)
		select {
		case s.sem <- struct{}{}:
			s.wg.Add(1)
			go func(c *Dir, cp string) {
				defer s.wg.Done()
				s.scanDir(c, cp)
				<-s.sem
			}(c, cp)
		default:
			s.scanDir(c, cp)
		}
	}
}

// computeAgg 自底向上计算汇总值。
func computeAgg(d *Dir) {
	d.Size, d.NFiles, d.NDirs = 0, len(d.Files), len(d.Dirs)
	for i := range d.Files {
		d.Size += d.Files[i].Size
	}
	for _, c := range d.Dirs {
		computeAgg(c)
		d.Size += c.Size
		d.NFiles += c.NFiles
		d.NDirs += c.NDirs
	}
}

func countErrs(d *Dir) int {
	n := 0
	if d.Err != "" {
		n = 1
	}
	for _, c := range d.Dirs {
		n += countErrs(c)
	}
	return n
}

// scanSubtree 同步扫描一个路径（用于操作后把新出现的文件夹加入树），调用者持有锁。
func (t *Tree) scanSubtree(path string, parent *Dir) *Dir {
	s := &scanner{t: t, skipHidden: t.skipHidden, sem: make(chan struct{}, 8)}
	d := t.newDirNode(filepath.Base(path), parent)
	if fi, err := os.Stat(path); err == nil {
		d.MTime = fi.ModTime().Unix()
	}
	s.scanDir(d, path)
	s.wg.Wait()
	computeAgg(d)
	return d
}

func errText(err error) string {
	switch {
	case os.IsPermission(err):
		return "没有访问权限"
	case os.IsNotExist(err):
		return "路径不存在"
	}
	return err.Error()
}
