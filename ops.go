package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Ref 指向树中的一个条目：文件为 {D: 所在文件夹ID, N: 文件名}，文件夹为 {D: 文件夹ID}。
type Ref struct {
	D int    `json:"d"`
	N string `json:"n,omitempty"`
}

type item struct {
	isDir  bool
	dir    *Dir // 文件夹本身，或文件所在的文件夹
	name   string
	parent *Dir
}

func (it item) path(t *Tree) string {
	if it.isDir {
		return t.pathOf(it.dir)
	}
	return filepath.Join(t.pathOf(it.dir), it.name)
}

type OpResult struct {
	Done    int      `json:"done"`
	Skipped int      `json:"skipped"`
	Errors  []string `json:"errors"`
	NewID   int      `json:"newId,omitempty"`
	Target  string   `json:"target,omitempty"`
}

func (r *OpResult) fail(format string, a ...any) {
	if len(r.Errors) < 200 {
		r.Errors = append(r.Errors, fmt.Sprintf(format, a...))
	}
}

// resolve 把前端传来的引用（或查询条件）转换为树中的条目。调用者持有锁。
func (t *Tree) resolve(refs []Ref, q *Query) ([]item, error) {
	var items []item
	if q != nil {
		for _, r := range t.collect(q) {
			if r.Kind == "d" {
				items = append(items, item{isDir: true, dir: r.dir, name: r.dir.Name, parent: r.dir.Parent})
			} else {
				items = append(items, item{dir: r.dir, name: r.Name, parent: r.dir})
			}
		}
		return items, nil
	}
	for _, r := range refs {
		d := t.dirs[r.D]
		if d == nil {
			return nil, errors.New("所选项目已不存在，请刷新后重试")
		}
		if r.N == "" {
			items = append(items, item{isDir: true, dir: d, name: d.Name, parent: d.Parent})
		} else {
			i := d.fileIndex(r.N)
			if i < 0 {
				return nil, fmt.Errorf("文件“%s”已不存在，请刷新后重试", r.N)
			}
			items = append(items, item{dir: d, name: d.Files[i].Name, parent: d})
		}
	}
	return items, nil
}

// moveFS 移动文件或文件夹；跨磁盘时自动改为“复制后删除”。dst 必须不存在。
func moveFS(src, dst string) error {
	err := os.Rename(src, dst)
	if err == nil || !isCrossDevice(err) {
		return err
	}
	if err := copyAll(src, dst); err != nil {
		os.RemoveAll(dst)
		return err
	}
	return os.RemoveAll(src)
}

func copyAll(src, dst string) error {
	fi, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		if err := os.Mkdir(dst, 0o755); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := copyAll(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
				return err
			}
		}
		return os.Chtimes(dst, fi.ModTime(), fi.ModTime())
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chtimes(dst, fi.ModTime(), fi.ModTime())
}

// treeMove 在文件系统完成 src→dst 之后同步更新内存中的树。调用者持有写锁。
func (t *Tree) treeMove(src, dst string, isDir bool) {
	srcParent := t.lookupDir(filepath.Dir(src))
	dstParent := t.lookupDir(filepath.Dir(dst))
	newName := filepath.Base(dst)
	if isDir {
		var node *Dir
		if srcParent != nil {
			node = srcParent.childDir(filepath.Base(src))
		}
		if node != nil {
			srcParent.removeDir(node)
			srcParent.addAgg(-node.Size, -node.NFiles, -(node.NDirs + 1))
			node.Parent = nil
		}
		if dstParent == nil {
			if node != nil {
				t.unregister(node)
			}
			return
		}
		if node == nil {
			node = t.scanSubtree(dst, dstParent)
			t.register(node)
		}
		node.Name = newName
		node.Parent = dstParent
		dstParent.Dirs = append(dstParent.Dirs, node)
		dstParent.addAgg(node.Size, node.NFiles, node.NDirs+1)
		return
	}
	var f File
	found := false
	if srcParent != nil {
		if i := srcParent.fileIndex(filepath.Base(src)); i >= 0 {
			f, found = srcParent.Files[i], true
			srcParent.Files = append(srcParent.Files[:i], srcParent.Files[i+1:]...)
			srcParent.addAgg(-f.Size, -1, 0)
		}
	}
	if dstParent == nil {
		return
	}
	if !found {
		fi, err := os.Lstat(dst)
		if err != nil {
			return
		}
		f = File{Size: fi.Size(), MTime: fi.ModTime().Unix()}
	}
	f.Name, f.Ext = newName, extOf(newName)
	dstParent.Files = append(dstParent.Files, f)
	dstParent.addAgg(f.Size, 1, 0)
}

func (t *Tree) checkProtected(p string) error {
	if r := protectedReason(p); r != "" {
		return fmt.Errorf("“%s” 位于%s，为防止系统出错，不允许在此处整理", p, r)
	}
	return nil
}

// ---------------------------------------------------------------- 移动

type MoveReq struct {
	Items    []Ref  `json:"items"`
	Query    *Query `json:"query"`
	Target   int    `json:"target"`
	Conflict string `json:"conflict"` // rename | skip
}

func (a *App) Move(req MoveReq) (*OpResult, error) {
	t := a.tree
	t.mu.Lock()
	defer t.mu.Unlock()
	defer t.version.Add(1)
	target := t.dirs[req.Target]
	if target == nil {
		return nil, errors.New("目标文件夹不存在，请刷新后重试")
	}
	targetPath := t.pathOf(target)
	if err := t.checkProtected(targetPath); err != nil {
		return nil, err
	}
	items, err := t.resolve(req.Items, req.Query)
	if err != nil {
		return nil, err
	}
	// 如果某个文件夹本身也被选中移动，则其内部的条目随它一起移动，无需单独处理。
	moving := map[*Dir]bool{}
	for _, it := range items {
		if it.isDir {
			moving[it.dir] = true
		}
	}
	coveredBy := func(d *Dir) bool {
		for p := d; p != nil; p = p.Parent {
			if moving[p] {
				return true
			}
		}
		return false
	}
	res := &OpResult{Target: targetPath}
	var acts []Action
	for _, it := range items {
		if it.isDir && coveredBy(it.dir.Parent) || !it.isDir && coveredBy(it.dir) {
			continue
		}
		if it.parent == target {
			res.Skipped++
			continue
		}
		src := it.path(t)
		if it.isDir {
			if it.dir == t.root {
				res.fail("不能移动扫描的根文件夹")
				continue
			}
			if it.dir.isAncestorOf(target) {
				res.fail("不能把文件夹“%s”移动到它自己或它的子文件夹中", it.name)
				continue
			}
		}
		if err := t.checkProtected(src); err != nil {
			res.fail("%v", err)
			continue
		}
		dst := filepath.Join(targetPath, it.name)
		if exists(dst) {
			if req.Conflict == "skip" {
				res.Skipped++
				continue
			}
			dst = uniquePath(targetPath, it.name, it.isDir)
		}
		if err := moveFS(src, dst); err != nil {
			res.fail("移动“%s”失败：%s", it.name, osErrText(err))
			continue
		}
		t.treeMove(src, dst, it.isDir)
		acts = append(acts, Action{Op: "move", From: src, To: dst, IsDir: it.isDir})
		res.Done++
	}
	a.hist.push(fmt.Sprintf("移动 %d 项到 %s", len(acts), targetPath), acts)
	return res, nil
}

// ---------------------------------------------------------------- 重命名

type RenameReq struct {
	Item    Ref    `json:"item"`
	NewName string `json:"newName"`
}

func (a *App) Rename(req RenameReq) (*OpResult, error) {
	t := a.tree
	t.mu.Lock()
	defer t.mu.Unlock()
	defer t.version.Add(1)
	newName := strings.TrimSpace(req.NewName)
	if err := validateName(newName); err != nil {
		return nil, err
	}
	items, err := t.resolve([]Ref{req.Item}, nil)
	if err != nil {
		return nil, err
	}
	it := items[0]
	if it.isDir && it.dir == t.root {
		return nil, errors.New("不能重命名扫描的根文件夹")
	}
	if it.name == newName {
		return &OpResult{}, nil
	}
	src := it.path(t)
	if err := t.checkProtected(src); err != nil {
		return nil, err
	}
	dst := filepath.Join(filepath.Dir(src), newName)
	if exists(dst) && !sameFile(src, dst) {
		return nil, fmt.Errorf("此位置已经存在名为“%s”的文件或文件夹", newName)
	}
	if err := os.Rename(src, dst); err != nil {
		return nil, fmt.Errorf("重命名失败：%s", osErrText(err))
	}
	t.treeMove(src, dst, it.isDir)
	a.hist.push(fmt.Sprintf("重命名 %s → %s", it.name, newName), []Action{{Op: "move", From: src, To: dst, IsDir: it.isDir}})
	return &OpResult{Done: 1}, nil
}

// ---------------------------------------------------------------- 新建文件夹

type MkdirReq struct {
	Parent int    `json:"parent"`
	Name   string `json:"name"`
}

func (a *App) Mkdir(req MkdirReq) (*OpResult, error) {
	t := a.tree
	t.mu.Lock()
	defer t.mu.Unlock()
	defer t.version.Add(1)
	name := strings.TrimSpace(req.Name)
	if err := validateName(name); err != nil {
		return nil, err
	}
	parent := t.dirs[req.Parent]
	if parent == nil {
		return nil, errors.New("上级文件夹不存在，请刷新后重试")
	}
	pp := t.pathOf(parent)
	if err := t.checkProtected(pp); err != nil {
		return nil, err
	}
	p := filepath.Join(pp, name)
	if exists(p) {
		return nil, fmt.Errorf("此位置已经存在名为“%s”的文件或文件夹", name)
	}
	if err := os.Mkdir(p, 0o755); err != nil {
		return nil, fmt.Errorf("新建文件夹失败：%s", osErrText(err))
	}
	d := t.newDirNode(name, parent)
	d.MTime = time.Now().Unix()
	parent.Dirs = append(parent.Dirs, d)
	parent.addAgg(0, 0, 1)
	t.register(d)
	a.hist.push("新建文件夹 "+p, []Action{{Op: "mkdir", To: p, IsDir: true}})
	return &OpResult{Done: 1, NewID: d.ID}, nil
}

// ---------------------------------------------------------------- 批量重命名

type BatchRenameReq struct {
	Items    []Ref  `json:"items"`
	Query    *Query `json:"query"`
	Template string `json:"template"` // 可用 {name} 原名、{n} 序号
	Start    int    `json:"start"`
	Digits   int    `json:"digits"`
	Find     string `json:"find"`
	Replace  string `json:"replace"`
	Preview  bool   `json:"preview"`
}

type RenamePlan struct {
	Old    string `json:"old"`
	New    string `json:"new"`
	Loc    string `json:"loc"`
	Status string `json:"status"` // ok | same | 错误说明
}

type BatchRenameResult struct {
	Plans []RenamePlan `json:"plans"`
	OK    int          `json:"ok"`
	OpResult
}

func applyTemplate(req *BatchRenameReq, name string, isDir bool, n int) string {
	base, ext := splitExt(name)
	if isDir {
		base, ext = name, ""
	}
	if req.Find != "" {
		base = strings.ReplaceAll(base, req.Find, req.Replace)
	}
	tpl := req.Template
	if strings.TrimSpace(tpl) == "" {
		tpl = "{name}"
	}
	num := strconv.Itoa(n)
	for len(num) < req.Digits {
		num = "0" + num
	}
	out := strings.ReplaceAll(tpl, "{name}", base)
	out = strings.ReplaceAll(out, "{n}", num)
	return strings.TrimSpace(out) + ext
}

func (a *App) BatchRename(req BatchRenameReq) (*BatchRenameResult, error) {
	t := a.tree
	t.mu.Lock()
	defer t.mu.Unlock()
	defer t.version.Add(1)
	items, err := t.resolve(req.Items, req.Query)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, errors.New("没有选择任何项目")
	}
	type plan struct {
		it       item
		src, dst string
		status   string
	}
	plans := make([]*plan, len(items))
	// 同一文件夹内将被占用的名称（小写） -> 数量
	taken := map[string]int{}
	renaming := map[string]bool{} // 本批次中将被改走的原路径（小写）
	for i, it := range items {
		src := it.path(t)
		newName := applyTemplate(&req, it.name, it.isDir, req.Start+i)
		p := &plan{it: it, src: src, dst: filepath.Join(filepath.Dir(src), newName), status: "ok"}
		plans[i] = p
		if it.isDir && it.dir == t.root {
			p.status = "不能重命名根文件夹"
		} else if newName == it.name {
			p.status = "same"
		} else if err := validateName(newName); err != nil {
			p.status = err.Error()
		} else if err := t.checkProtected(src); err != nil {
			p.status = "受保护的位置"
		}
		if p.status == "ok" {
			renaming[strings.ToLower(src)] = true
		}
	}
	for _, p := range plans {
		key := strings.ToLower(p.dst)
		if p.status != "ok" {
			key = strings.ToLower(p.src) // 不会被改名的项目保留原名
		}
		taken[key]++
	}
	for _, p := range plans {
		if p.status != "ok" {
			continue
		}
		key := strings.ToLower(p.dst)
		if taken[key] > 1 {
			p.status = "与本批次中其他项目重名"
		} else if exists(p.dst) && !renaming[key] && !sameFile(p.src, p.dst) {
			p.status = "目标名称已存在"
		}
	}
	res := &BatchRenameResult{}
	for i, p := range plans {
		if p.status == "ok" {
			res.OK++
		}
		if i < 1000 {
			res.Plans = append(res.Plans, RenamePlan{Old: p.it.name, New: filepath.Base(p.dst), Loc: relPath(t.root, p.it.parent), Status: p.status})
		}
	}
	if req.Preview {
		return res, nil
	}
	// 两阶段重命名：先改成临时名，再改成最终名，避免本批次内名称互换时互相冲突。
	stamp := strconv.FormatInt(time.Now().UnixNano(), 36)
	var staged []*plan
	var tmps []string
	for i, p := range plans {
		if p.status != "ok" {
			if p.status != "same" {
				res.Skipped++
			}
			continue
		}
		tmp := filepath.Join(filepath.Dir(p.src), fmt.Sprintf(".~rename-%s-%d", stamp, i))
		if err := os.Rename(p.src, tmp); err != nil {
			res.fail("重命名“%s”失败：%s", p.it.name, osErrText(err))
			continue
		}
		staged = append(staged, p)
		tmps = append(tmps, tmp)
	}
	var acts []Action
	for i, p := range staged {
		if exists(p.dst) {
			res.fail("重命名“%s”失败：目标名称已被占用", p.it.name)
			os.Rename(tmps[i], p.src)
			continue
		}
		if err := os.Rename(tmps[i], p.dst); err != nil {
			res.fail("重命名“%s”失败：%s", p.it.name, osErrText(err))
			os.Rename(tmps[i], p.src)
			continue
		}
		t.treeMove(p.src, p.dst, p.it.isDir)
		acts = append(acts, Action{Op: "move", From: p.src, To: p.dst, IsDir: p.it.isDir})
		res.Done++
	}
	a.hist.push(fmt.Sprintf("批量重命名 %d 项", len(acts)), acts)
	return res, nil
}

// ---------------------------------------------------------------- 撤销

func (a *App) Undo() (*OpResult, string, error) {
	t := a.tree
	t.mu.Lock()
	defer t.mu.Unlock()
	defer t.version.Add(1)
	b := a.hist.pop()
	if b == nil {
		return nil, "", errors.New("没有可以撤销的操作")
	}
	res := &OpResult{}
	var undone []Action
	// 第一阶段：把要恢复的条目先改成临时名称，避免本批次内名称互换/链式改名时互相占位。
	type staged struct {
		act Action
		tmp string
	}
	var moves []staged
	stamp := strconv.FormatInt(time.Now().UnixNano(), 36)
	for i := len(b.Actions) - 1; i >= 0; i-- {
		act := b.Actions[i]
		if act.Op != "move" {
			continue
		}
		if !exists(act.To) {
			res.fail("“%s” 已不存在，无法撤销", act.To)
			continue
		}
		tmp := filepath.Join(filepath.Dir(act.To), fmt.Sprintf(".~undo-%s-%d", stamp, i))
		if err := os.Rename(act.To, tmp); err != nil {
			res.fail("撤销“%s”失败：%s", filepath.Base(act.To), osErrText(err))
			continue
		}
		t.treeMove(act.To, tmp, act.IsDir)
		moves = append(moves, staged{act, tmp})
	}
	// 第二阶段：移回原位置。
	for _, m := range moves {
		act := m.act
		err := func() error {
			if exists(act.From) {
				return fmt.Errorf("“%s” 已被占用，无法撤销", act.From)
			}
			if !exists(filepath.Dir(act.From)) {
				if err := os.MkdirAll(filepath.Dir(act.From), 0o755); err != nil {
					return fmt.Errorf("无法恢复到“%s”：%s", act.From, osErrText(err))
				}
			}
			if err := moveFS(m.tmp, act.From); err != nil {
				return fmt.Errorf("撤销“%s”失败：%s", filepath.Base(act.To), osErrText(err))
			}
			return nil
		}()
		if err != nil {
			res.fail("%v", err)
			if os.Rename(m.tmp, act.To) == nil {
				t.treeMove(m.tmp, act.To, act.IsDir)
			}
			continue
		}
		t.treeMove(m.tmp, act.From, act.IsDir)
		undone = append(undone, Action{Op: "move", From: act.To, To: act.From, IsDir: act.IsDir})
		res.Done++
	}
	for i := len(b.Actions) - 1; i >= 0; i-- {
		act := b.Actions[i]
		if act.Op != "mkdir" {
			continue
		}
		if err := os.Remove(act.To); err != nil {
			if exists(act.To) {
				res.fail("文件夹“%s”不是空的，未删除", filepath.Base(act.To))
			}
			continue
		}
		if parent := t.lookupDir(filepath.Dir(act.To)); parent != nil {
			if d := parent.childDir(filepath.Base(act.To)); d != nil {
				parent.removeDir(d)
				parent.addAgg(-d.Size, -d.NFiles, -(d.NDirs + 1))
				t.unregister(d)
			}
		}
		undone = append(undone, Action{Op: "rmdir", To: act.To, IsDir: true})
		res.Done++
	}
	a.hist.logUndo(b, undone)
	return res, b.Desc, nil
}

func osErrText(err error) string {
	var pe *os.PathError
	var le *os.LinkError
	inner := err
	if errors.As(err, &pe) {
		inner = pe.Err
	} else if errors.As(err, &le) {
		inner = le.Err
	}
	switch {
	case os.IsPermission(err):
		return "没有权限（文件可能为只读或需要管理员权限）"
	case os.IsNotExist(err):
		return "文件不存在"
	case isFileInUse(err):
		return "文件正被其他程序使用，请先关闭它"
	}
	return inner.Error()
}
