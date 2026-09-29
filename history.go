package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Action struct {
	Op    string `json:"op"` // move | mkdir
	From  string `json:"from,omitempty"`
	To    string `json:"to"`
	IsDir bool   `json:"isDir"`
}

type Batch struct {
	ID      int       `json:"id"`
	Time    time.Time `json:"time"`
	Desc    string    `json:"desc"`
	Actions []Action  `json:"-"`
	Count   int       `json:"count"`
}

// History 保存可撤销的操作，并把所有操作追加写入日志文件，方便事后追查。
type History struct {
	mu      sync.Mutex
	batches []*Batch
	nextID  int
	logPath string
}

const maxUndo = 100

func newHistory(logPath string) *History {
	return &History{logPath: logPath}
}

func (h *History) push(desc string, acts []Action) {
	if len(acts) == 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.nextID++
	b := &Batch{ID: h.nextID, Time: time.Now(), Desc: desc, Actions: acts, Count: len(acts)}
	h.batches = append(h.batches, b)
	if len(h.batches) > maxUndo {
		h.batches = h.batches[len(h.batches)-maxUndo:]
	}
	h.write(b.Time, desc, acts)
}

// logOnly 只写日志、不进入撤销列表（用于移到回收站等无法在程序内撤销的操作）。
func (h *History) logOnly(desc string, acts []Action) {
	if len(acts) == 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.write(time.Now(), desc, acts)
}

func (h *History) pop() *Batch {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.batches) == 0 {
		return nil
	}
	b := h.batches[len(h.batches)-1]
	h.batches = h.batches[:len(h.batches)-1]
	return b
}

func (h *History) list() []Batch {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Batch, 0, len(h.batches))
	for i := len(h.batches) - 1; i >= 0; i-- {
		out = append(out, *h.batches[i])
	}
	return out
}

func (h *History) logUndo(b *Batch, acts []Action) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.write(time.Now(), "撤销："+b.Desc, acts)
}

func (h *History) write(tm time.Time, desc string, acts []Action) {
	if h.logPath == "" {
		return
	}
	os.MkdirAll(filepath.Dir(h.logPath), 0o755)
	f, err := os.OpenFile(h.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.Size() == 0 {
		f.WriteString("\uFEFF") // UTF-8 BOM，旧版记事本也能正确显示中文
	}
	fmt.Fprintf(f, "[%s] %s\r\n", tm.Format("2006-01-02 15:04:05"), desc)
	for _, a := range acts {
		switch a.Op {
		case "move":
			fmt.Fprintf(f, "    %s\r\n      → %s\r\n", a.From, a.To)
		case "mkdir":
			fmt.Fprintf(f, "    新建 %s\r\n", a.To)
		case "trash":
			fmt.Fprintf(f, "    移到回收站 %s\r\n", a.To)
		case "rmdir":
			fmt.Fprintf(f, "    删除空文件夹 %s\r\n", a.To)
		}
	}
}
