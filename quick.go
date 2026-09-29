package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// QuickStore 记录“最近新建的文件夹”（也可以手动添加），保存在配置目录中，程序重启后仍然保留。
// 文件夹被重命名或移动时同步更新路径。
type QuickStore struct {
	mu    sync.Mutex
	file  string
	paths []string // 最新的在前
}

const maxQuick = 20

func loadQuick(file string) *QuickStore {
	q := &QuickStore{file: file}
	if file != "" {
		if b, err := os.ReadFile(file); err == nil {
			json.Unmarshal(b, &q.paths)
		}
	}
	return q
}

func samePath(a, b string) bool { return strings.EqualFold(filepath.Clean(a), filepath.Clean(b)) }

// isUnder 判断 p 是否等于 base 或位于 base 之下。
func isUnder(p, base string) bool {
	p, base = filepath.Clean(p), filepath.Clean(base)
	if samePath(p, base) {
		return true
	}
	prefix := strings.TrimRight(base, string(os.PathSeparator)) + string(os.PathSeparator)
	return len(p) > len(prefix) && strings.EqualFold(p[:len(prefix)], prefix)
}

func (q *QuickStore) list() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]string(nil), q.paths...)
}

func (q *QuickStore) add(p string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := []string{p}
	for _, x := range q.paths {
		if !samePath(x, p) {
			out = append(out, x)
		}
	}
	if len(out) > maxQuick {
		out = out[:maxQuick]
	}
	q.paths = out
	q.save()
}

// remove 从列表中移除一条记录（不影响磁盘上的文件夹）。
func (q *QuickStore) remove(p string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := q.paths[:0]
	for _, x := range q.paths {
		if !samePath(x, p) {
			out = append(out, x)
		}
	}
	q.paths = out
	q.save()
}

// removeUnder 移除 p 本身以及 p 之下的所有记录（文件夹被删除或撤销新建时使用）。
func (q *QuickStore) removeUnder(p string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := q.paths[:0]
	for _, x := range q.paths {
		if !isUnder(x, p) {
			out = append(out, x)
		}
	}
	q.paths = out
	q.save()
}

// rename 在文件夹从 from 移动/重命名为 to 之后更新记录。
func (q *QuickStore) rename(from, to string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	changed := false
	for i, x := range q.paths {
		if isUnder(x, from) {
			q.paths[i] = to + x[len(filepath.Clean(from)):]
			changed = true
		}
	}
	if changed {
		q.save()
	}
}

func (q *QuickStore) save() {
	if q.file == "" {
		return
	}
	os.MkdirAll(filepath.Dir(q.file), 0o755)
	b, _ := json.MarshalIndent(q.paths, "", "  ")
	os.WriteFile(q.file, b, 0o644)
}
