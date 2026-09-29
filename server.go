package main

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed web
var webFS embed.FS

type server struct {
	app     *App
	token   string
	port    int
	logPath string
	webDir  string
	quit    chan struct{}
	thumbs  *thumbCache
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func readJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 64<<20)).Decode(v); err != nil {
		return errors.New("请求格式错误")
	}
	return nil
}

func (s *server) handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(webFS, "web")
	if s.webDir != "" {
		static = os.DirFS(s.webDir) // 开发时直接读取磁盘上的前端文件
	}
	fileServer := http.FileServer(http.FS(static))
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		fileServer.ServeHTTP(w, r)
	}))

	api := func(pattern string, h func(w http.ResponseWriter, r *http.Request) (any, error)) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Token") != s.token {
				writeErr(w, http.StatusForbidden, errors.New("访问被拒绝，请从程序打开的页面使用"))
				return
			}
			v, err := h(w, r)
			if err != nil {
				writeErr(w, http.StatusBadRequest, err)
				return
			}
			writeJSON(w, v)
		})
	}
	t := s.app.tree

	api("GET /api/state", func(w http.ResponseWriter, r *http.Request) (any, error) {
		t.mu.RLock()
		defer t.mu.RUnlock()
		st := map[string]any{
			"scan":     s.app.ScanStatus(),
			"logPath":  s.logPath,
			"os":       runtime.GOOS,
			"sep":      string(os.PathSeparator),
			"scanned":  t.root != nil,
			"undoable": len(s.app.hist.list()),
		}
		type cat struct {
			Key   string   `json:"key"`
			Name  string   `json:"name"`
			Color string   `json:"color"`
			Exts  []string `json:"exts"`
		}
		var cats []cat
		for _, c := range categories {
			cats = append(cats, cat{c.Key, c.Name, c.Color, c.Exts})
		}
		st["categories"] = cats
		st["thumbExts"] = thumbExtList()
		if t.root != nil {
			st["root"] = map[string]any{
				"id": t.root.ID, "path": t.rootPath, "size": t.root.Size, "nFiles": t.root.NFiles,
				"nDirs": t.root.NDirs, "errors": t.errCount, "tookMs": t.scanTook.Milliseconds(),
				"skipHidden": t.skipHidden,
			}
		}
		return st, nil
	})

	api("POST /api/scan", func(w http.ResponseWriter, r *http.Request) (any, error) {
		var req struct {
			Path       string `json:"path"`
			SkipHidden bool   `json:"skipHidden"`
		}
		if err := readJSON(r, &req); err != nil {
			return nil, err
		}
		if err := s.app.StartScan(req.Path, req.SkipHidden); err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, nil
	})

	api("POST /api/scan/cancel", func(w http.ResponseWriter, r *http.Request) (any, error) {
		s.app.CancelScan()
		return map[string]bool{"ok": true}, nil
	})

	api("GET /api/places", func(w http.ResponseWriter, r *http.Request) (any, error) {
		return listPlaces(), nil
	})

	api("GET /api/browse", func(w http.ResponseWriter, r *http.Request) (any, error) {
		p := r.URL.Query().Get("path")
		type entry struct {
			Name string `json:"name"`
			Path string `json:"path"`
		}
		res := struct {
			Path   string  `json:"path"`
			Parent string  `json:"parent"`
			Dirs   []entry `json:"dirs"`
			Error  string  `json:"error,omitempty"`
		}{Dirs: []entry{}}
		path, err := normalizeInput(p)
		if err != nil {
			return nil, err
		}
		res.Path = path
		if parent := filepath.Dir(path); parent != path {
			res.Parent = parent
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			res.Error = errText(err)
		}
		for _, e := range entries {
			info, err := e.Info()
			if err != nil || classify(info, true) != entryDir {
				continue
			}
			res.Dirs = append(res.Dirs, entry{Name: e.Name(), Path: filepath.Join(path, e.Name())})
		}
		sort.Slice(res.Dirs, func(i, j int) bool { return naturalCompare(res.Dirs[i].Name, res.Dirs[j].Name) < 0 })
		return res, nil
	})

	type childInfo struct {
		ID          int    `json:"id"`
		Name        string `json:"name"`
		Size        int64  `json:"size"`
		NFiles      int    `json:"nFiles"`
		NDirs       int    `json:"nDirs"`
		HasChildren bool   `json:"hasChildren"`
		Err         string `json:"err,omitempty"`
	}
	info := func(d *Dir) childInfo {
		return childInfo{ID: d.ID, Name: d.Name, Size: d.Size, NFiles: d.NFiles, NDirs: d.NDirs, HasChildren: len(d.Dirs) > 0, Err: d.Err}
	}

	api("GET /api/children", func(w http.ResponseWriter, r *http.Request) (any, error) {
		t.mu.RLock()
		defer t.mu.RUnlock()
		id, _ := strconv.Atoi(r.URL.Query().Get("id"))
		d := t.dirs[id]
		if d == nil {
			return nil, errors.New("文件夹不存在")
		}
		out := make([]childInfo, 0, len(d.Dirs))
		for _, c := range d.Dirs {
			out = append(out, info(c))
		}
		if r.URL.Query().Get("sort") == "name" {
			sort.Slice(out, func(i, j int) bool { return naturalCompare(out[i].Name, out[j].Name) < 0 })
		} else {
			sort.Slice(out, func(i, j int) bool {
				if out[i].Size != out[j].Size {
					return out[i].Size > out[j].Size
				}
				return naturalCompare(out[i].Name, out[j].Name) < 0
			})
		}
		return out, nil
	})

	api("GET /api/dir", func(w http.ResponseWriter, r *http.Request) (any, error) {
		t.mu.RLock()
		defer t.mu.RUnlock()
		id, _ := strconv.Atoi(r.URL.Query().Get("id"))
		d := t.dirs[id]
		if d == nil {
			return nil, errors.New("文件夹不存在")
		}
		type crumb struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		}
		var crumbs []crumb
		for p := d; p != nil; p = p.Parent {
			crumbs = append([]crumb{{p.ID, p.Name}}, crumbs...)
		}
		return map[string]any{"info": info(d), "path": t.pathOf(d), "crumbs": crumbs, "files": len(d.Files)}, nil
	})

	api("GET /api/locate", func(w http.ResponseWriter, r *http.Request) (any, error) {
		t.mu.RLock()
		defer t.mu.RUnlock()
		d := t.lookupDir(r.URL.Query().Get("path"))
		if d == nil {
			return map[string]int{"id": 0}, nil
		}
		return map[string]int{"id": d.ID}, nil
	})

	api("POST /api/list", func(w http.ResponseWriter, r *http.Request) (any, error) {
		var q Query
		if err := readJSON(r, &q); err != nil {
			return nil, err
		}
		t.mu.RLock()
		defer t.mu.RUnlock()
		rows := t.collect(&q)
		res := ListResult{Total: len(rows), Rows: []Row{}}
		for i := range rows {
			if rows[i].Kind == "f" {
				res.TotalSize += rows[i].Size
				res.FileCount++
			} else {
				res.DirCount++
			}
		}
		if q.Limit <= 0 {
			q.Limit = 300
		}
		if q.Offset < len(rows) {
			end := min(q.Offset+q.Limit, len(rows))
			res.Rows = rows[q.Offset:end]
		}
		return res, nil
	})

	api("GET /api/stats", func(w http.ResponseWriter, r *http.Request) (any, error) {
		t.mu.RLock()
		defer t.mu.RUnlock()
		id, _ := strconv.Atoi(r.URL.Query().Get("id"))
		d := t.dirs[id]
		if d == nil {
			return nil, errors.New("文件夹不存在")
		}
		return map[string]any{"cats": t.stats(d), "size": d.Size, "count": d.NFiles}, nil
	})

	api("POST /api/move", func(w http.ResponseWriter, r *http.Request) (any, error) {
		var req MoveReq
		if err := readJSON(r, &req); err != nil {
			return nil, err
		}
		return s.app.Move(req)
	})

	api("POST /api/rename", func(w http.ResponseWriter, r *http.Request) (any, error) {
		var req RenameReq
		if err := readJSON(r, &req); err != nil {
			return nil, err
		}
		return s.app.Rename(req)
	})

	api("POST /api/mkdir", func(w http.ResponseWriter, r *http.Request) (any, error) {
		var req MkdirReq
		if err := readJSON(r, &req); err != nil {
			return nil, err
		}
		return s.app.Mkdir(req)
	})

	api("POST /api/batch-rename", func(w http.ResponseWriter, r *http.Request) (any, error) {
		var req BatchRenameReq
		if err := readJSON(r, &req); err != nil {
			return nil, err
		}
		return s.app.BatchRename(req)
	})

	api("POST /api/undo", func(w http.ResponseWriter, r *http.Request) (any, error) {
		res, desc, err := s.app.Undo()
		if err != nil {
			return nil, err
		}
		return map[string]any{"result": res, "desc": desc}, nil
	})

	api("GET /api/history", func(w http.ResponseWriter, r *http.Request) (any, error) {
		return s.app.hist.list(), nil
	})

	itemPath := func(r *http.Request) (string, error) {
		var req struct {
			Item Ref `json:"item"`
		}
		if err := readJSON(r, &req); err != nil {
			return "", err
		}
		t.mu.RLock()
		defer t.mu.RUnlock()
		items, err := t.resolve([]Ref{req.Item}, nil)
		if err != nil {
			return "", err
		}
		return items[0].path(t), nil
	}

	api("POST /api/open", func(w http.ResponseWriter, r *http.Request) (any, error) {
		p, err := itemPath(r)
		if err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, openPath(p)
	})

	api("POST /api/reveal", func(w http.ResponseWriter, r *http.Request) (any, error) {
		p, err := itemPath(r)
		if err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, revealPath(p)
	})

	api("POST /api/path", func(w http.ResponseWriter, r *http.Request) (any, error) {
		p, err := itemPath(r)
		if err != nil {
			return nil, err
		}
		return map[string]string{"path": p}, nil
	})

	api("POST /api/open-log", func(w http.ResponseWriter, r *http.Request) (any, error) {
		if _, err := os.Stat(s.logPath); err != nil {
			return nil, errors.New("还没有任何操作记录")
		}
		return map[string]bool{"ok": true}, revealPath(s.logPath)
	})

	api("POST /api/delete", func(w http.ResponseWriter, r *http.Request) (any, error) {
		var req DeleteReq
		if err := readJSON(r, &req); err != nil {
			return nil, err
		}
		return s.app.Delete(req)
	})

	// 最近新建的文件夹（只返回当前扫描范围内仍然存在的）
	api("GET /api/quick", func(w http.ResponseWriter, r *http.Request) (any, error) {
		type quickInfo struct {
			ID     int    `json:"id"`
			Name   string `json:"name"`
			Path   string `json:"path"`
			Parent string `json:"parent"`
			Size   int64  `json:"size"`
			NFiles int    `json:"nFiles"`
		}
		out := []quickInfo{}
		t.mu.RLock()
		defer t.mu.RUnlock()
		for _, p := range s.app.quick.list() {
			d := t.lookupDir(p)
			if d == nil || d == t.root {
				continue
			}
			out = append(out, quickInfo{ID: d.ID, Name: d.Name, Path: t.pathOf(d), Parent: relPath(t.root, d.Parent), Size: d.Size, NFiles: d.NFiles})
			if len(out) >= 8 {
				break
			}
		}
		return out, nil
	})

	api("POST /api/quick/add", func(w http.ResponseWriter, r *http.Request) (any, error) {
		var req struct {
			ID int `json:"id"`
		}
		if err := readJSON(r, &req); err != nil {
			return nil, err
		}
		t.mu.RLock()
		d := t.dirs[req.ID]
		var p string
		if d != nil {
			p = t.pathOf(d)
		}
		t.mu.RUnlock()
		if d == nil {
			return nil, errors.New("文件夹不存在")
		}
		s.app.quick.add(p)
		return map[string]bool{"ok": true}, nil
	})

	api("POST /api/quick/remove", func(w http.ResponseWriter, r *http.Request) (any, error) {
		var req struct {
			Path string `json:"path"`
		}
		if err := readJSON(r, &req); err != nil {
			return nil, err
		}
		s.app.quick.remove(req.Path)
		return map[string]bool{"ok": true}, nil
	})

	// 缩略图：<img> 标签无法附带请求头，因此令牌通过网址参数 t 传递。
	mux.HandleFunc("GET /api/thumb", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("t") != s.token && r.Header.Get("X-Token") != s.token {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		d, _ := strconv.Atoi(q.Get("d"))
		name := q.Get("n")
		size, _ := strconv.Atoi(q.Get("s"))
		size = max(32, min(size, 320))
		t.mu.RLock()
		dir := t.dirs[d]
		var path string
		if dir != nil && dir.fileIndex(name) >= 0 {
			path = filepath.Join(t.pathOf(dir), name)
		}
		t.mu.RUnlock()
		if path == "" {
			http.NotFound(w, r)
			return
		}
		fi, err := os.Stat(path)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		key := fmt.Sprintf("%s|%d|%d|%d", path, size, fi.ModTime().UnixNano(), fi.Size())
		data, ok := s.thumbs.get(key)
		if !ok {
			data, _ = makeThumb(path, extOf(name), size)
			s.thumbs.put(key, data)
		}
		if data == nil {
			w.Header().Set("Cache-Control", "private, max-age=600")
			http.Error(w, "no thumbnail", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "private, max-age=86400")
		w.Write(data)
	})

	api("POST /api/quit", func(w http.ResponseWriter, r *http.Request) (any, error) {
		go func() {
			time.Sleep(300 * time.Millisecond)
			close(s.quit)
		}()
		return map[string]bool{"ok": true}, nil
	})

	return s.guard(mux)
}

// guard 只接受来自本机页面的请求，防止其他网站借助浏览器访问本程序（DNS 重绑定等）。
func (s *server) guard(next http.Handler) http.Handler {
	allowed := map[string]bool{
		"127.0.0.1:" + strconv.Itoa(s.port): true,
		"localhost:" + strconv.Itoa(s.port): true,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowed[strings.ToLower(r.Host)] {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func listen(port int) (net.Listener, error) {
	if port > 0 {
		return net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	}
	for _, p := range []int{17853, 17854, 17855} {
		if l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p)); err == nil {
			return l, nil
		}
	}
	return net.Listen("tcp", "127.0.0.1:0")
}
