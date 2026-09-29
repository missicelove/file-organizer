package main

import (
	"fmt"
	"sort"
	"strings"
)

// Query 描述文件列表的筛选条件。
type Query struct {
	Dir       int    `json:"dir"`
	Recursive bool   `json:"recursive"`
	Ext       string `json:"ext"` // 单个扩展名；"-" 表示无扩展名
	Cat       string `json:"cat"`
	Q         string `json:"q"`
	Sort      string `json:"sort"` // name|size|mtime|type|loc
	Desc      bool   `json:"desc"`
	Offset    int    `json:"offset"`
	Limit     int    `json:"limit"`
}

// Row 是列表中的一行（文件或文件夹）。
type Row struct {
	Kind   string `json:"k"` // "d" 文件夹 / "f" 文件
	D      int    `json:"d"` // 文件：所在文件夹 ID；文件夹：自身 ID
	Name   string `json:"n"`
	Ext    string `json:"e,omitempty"`
	Size   int64  `json:"s"`
	MTime  int64  `json:"t"`
	NFiles int    `json:"nf,omitempty"`
	P      int    `json:"p,omitempty"`   // 文件夹行：上级文件夹 ID
	Loc    string `json:"loc,omitempty"` // 相对于查询文件夹的位置
	Err    string `json:"err,omitempty"`

	dir *Dir
}

type ListResult struct {
	Rows      []Row `json:"rows"`
	Total     int   `json:"total"`
	TotalSize int64 `json:"totalSize"`
	FileCount int   `json:"fileCount"`
	DirCount  int   `json:"dirCount"`
}

func (q *Query) filtered() bool { return q.Ext != "" || q.Cat != "" }

func (q *Query) matchFile(f *File) bool {
	if q.Ext != "" {
		if q.Ext == "-" {
			if f.Ext != "" {
				return false
			}
		} else if f.Ext != q.Ext {
			return false
		}
	}
	if q.Cat != "" && categoryOf(f.Ext) != q.Cat {
		return false
	}
	return q.Q == "" || containsFold(f.Name, q.Q)
}

func containsFold(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}

// collect 返回满足条件的全部行（未分页、已排序）。调用者持有读锁。
// 同一查询翻页时直接复用上次的结果，树发生任何变化后缓存失效。
func (t *Tree) collect(q *Query) []Row {
	key := q.cacheKey()
	t.cacheMu.Lock()
	if t.cache.version == t.version.Load() && t.cache.key == key {
		rows := t.cache.rows
		t.cacheMu.Unlock()
		return rows
	}
	t.cacheMu.Unlock()
	rows := t.collectNoCache(q)
	t.cacheMu.Lock()
	t.cache.version, t.cache.key, t.cache.rows = t.version.Load(), key, rows
	t.cacheMu.Unlock()
	return rows
}

func (q *Query) cacheKey() string {
	return fmt.Sprintf("%d|%v|%s|%s|%s|%s|%v", q.Dir, q.Recursive, q.Ext, q.Cat, q.Q, q.Sort, q.Desc)
}

func (t *Tree) collectNoCache(q *Query) []Row {
	base := t.dirs[q.Dir]
	if base == nil {
		return nil
	}
	var rows []Row
	locCache := map[*Dir]string{}
	loc := func(d *Dir) string {
		if !q.Recursive {
			return ""
		}
		s, ok := locCache[d]
		if !ok {
			s = relPath(base, d)
			locCache[d] = s
		}
		return s
	}
	addDir := func(c *Dir) {
		rows = append(rows, Row{Kind: "d", D: c.ID, P: c.Parent.ID, Name: c.Name, Size: c.Size, MTime: c.MTime, NFiles: c.NFiles, Loc: loc(c.Parent), Err: c.Err, dir: c})
	}
	addFiles := func(d *Dir) {
		for i := range d.Files {
			f := &d.Files[i]
			if q.matchFile(f) {
				rows = append(rows, Row{Kind: "f", D: d.ID, Name: f.Name, Ext: f.Ext, Size: f.Size, MTime: f.MTime, Loc: loc(d), dir: d})
			}
		}
	}
	if !q.Recursive {
		if !q.filtered() {
			for _, c := range base.Dirs {
				if q.Q == "" || containsFold(c.Name, q.Q) {
					addDir(c)
				}
			}
		}
		addFiles(base)
	} else {
		var walk func(d *Dir)
		walk = func(d *Dir) {
			addFiles(d)
			for _, c := range d.Dirs {
				if q.Q != "" && !q.filtered() && containsFold(c.Name, q.Q) {
					addDir(c)
				}
				walk(c)
			}
		}
		walk(base)
	}
	sortRows(rows, q.Sort, q.Desc)
	return rows
}

func sortRows(rows []Row, key string, desc bool) {
	less := func(a, b *Row) int {
		// 文件夹始终排在文件前面
		if a.Kind != b.Kind {
			if a.Kind == "d" {
				return -2
			}
			return 2
		}
		switch key {
		case "size":
			return cmp64(a.Size, b.Size)
		case "mtime":
			return cmp64(a.MTime, b.MTime)
		case "type":
			if c := strings.Compare(a.Ext, b.Ext); c != 0 {
				return c
			}
		case "loc":
			if c := naturalCompare(a.Loc, b.Loc); c != 0 {
				return c
			}
		}
		return naturalCompare(a.Name, b.Name)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		c := less(&rows[i], &rows[j])
		if c == -2 {
			return true
		}
		if c == 2 {
			return false
		}
		if desc {
			return c > 0
		}
		return c < 0
	})
}

func cmp64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// naturalCompare 忽略大小写，并按数字大小比较数字片段（"文件2" < "文件10"）。不分配内存，便于大量排序。
func naturalCompare(a, b string) int {
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		ca, cb := a[i], b[j]
		if isDigit(ca) && isDigit(cb) {
			si, sj := i, j
			for i < len(a) && isDigit(a[i]) {
				i++
			}
			for j < len(b) && isDigit(b[j]) {
				j++
			}
			ta, tb := strings.TrimLeft(a[si:i], "0"), strings.TrimLeft(b[sj:j], "0")
			if len(ta) != len(tb) {
				if len(ta) < len(tb) {
					return -1
				}
				return 1
			}
			if c := strings.Compare(ta, tb); c != 0 {
				return c
			}
			continue
		}
		ca, cb = lowerASCII(ca), lowerASCII(cb)
		if ca != cb {
			if ca < cb {
				return -1
			}
			return 1
		}
		i++
		j++
	}
	return (len(a) - i) - (len(b) - j)
}

func lowerASCII(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 32
	}
	return c
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// ---------------------------------------------------------------- 类型统计

type ExtStat struct {
	Ext   string `json:"ext"`
	Count int    `json:"count"`
	Size  int64  `json:"size"`
}

type CatStat struct {
	Key   string    `json:"key"`
	Name  string    `json:"name"`
	Color string    `json:"color"`
	Count int       `json:"count"`
	Size  int64     `json:"size"`
	Exts  []ExtStat `json:"exts"`
}

func (t *Tree) stats(d *Dir) []CatStat {
	exts := map[string]*ExtStat{}
	var walk func(d *Dir)
	walk = func(d *Dir) {
		for i := range d.Files {
			f := &d.Files[i]
			e := exts[f.Ext]
			if e == nil {
				e = &ExtStat{Ext: f.Ext}
				exts[f.Ext] = e
			}
			e.Count++
			e.Size += f.Size
		}
		for _, c := range d.Dirs {
			walk(c)
		}
	}
	walk(d)
	cats := map[string]*CatStat{}
	for _, e := range exts {
		k := categoryOf(e.Ext)
		c := cats[k]
		if c == nil {
			info := categoryInfo(k)
			c = &CatStat{Key: k, Name: info.Name, Color: info.Color}
			cats[k] = c
		}
		c.Count += e.Count
		c.Size += e.Size
		c.Exts = append(c.Exts, *e)
	}
	out := make([]CatStat, 0, len(cats))
	for _, c := range cats {
		sort.Slice(c.Exts, func(i, j int) bool {
			if c.Exts[i].Size != c.Exts[j].Size {
				return c.Exts[i].Size > c.Exts[j].Size
			}
			return c.Exts[i].Ext < c.Exts[j].Ext
		})
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Size != out[j].Size {
			return out[i].Size > out[j].Size
		}
		return out[i].Key < out[j].Key
	})
	return out
}
