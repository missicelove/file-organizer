package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 通过 HTTP 接口完整走一遍：扫描 → 浏览 → 统计 → 新建 → 归类移动 → 重命名 → 批量重命名 → 撤销。

type apiClient struct {
	t     *testing.T
	base  string
	token string
}

func (c *apiClient) do(method, path string, body any, out any) int {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, rd)
	req.Header.Set("X-Token", c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			c.t.Fatalf("%s %s: bad json %q", method, path, data)
		}
	}
	return resp.StatusCode
}

func startTestServer(t *testing.T) (*apiClient, *server) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "log.txt")
	s := &server{app: newApp(logPath), token: "abc123", port: l.Addr().(*net.TCPAddr).Port, logPath: logPath, quit: make(chan struct{})}
	ts := &httptest.Server{Listener: l, Config: &http.Server{Handler: s.handler()}}
	ts.Start()
	t.Cleanup(ts.Close)
	return &apiClient{t: t, base: fmt.Sprintf("http://127.0.0.1:%d", s.port), token: s.token}, s
}

type stateResp struct {
	Scanned bool `json:"scanned"`
	Scan    struct {
		Running bool   `json:"running"`
		Error   string `json:"error"`
	} `json:"scan"`
	Root struct {
		ID     int    `json:"id"`
		Path   string `json:"path"`
		NFiles int    `json:"nFiles"`
		Size   int64  `json:"size"`
	} `json:"root"`
	Undoable   int `json:"undoable"`
	Categories []struct {
		Key string `json:"key"`
	} `json:"categories"`
}

func (c *apiClient) scan(path string) stateResp {
	c.t.Helper()
	var ok map[string]any
	if code := c.do("POST", "/api/scan", map[string]any{"path": path, "skipHidden": true}, &ok); code != 200 {
		c.t.Fatalf("scan status %d %v", code, ok)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		var st stateResp
		c.do("GET", "/api/state", nil, &st)
		if !st.Scan.Running {
			return st
		}
		if time.Now().After(deadline) {
			c.t.Fatal("scan timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAPISecurity(t *testing.T) {
	c, s := startTestServer(t)
	// 没有令牌
	resp, _ := http.Get(c.base + "/api/state")
	if resp.StatusCode != 403 {
		t.Errorf("no token: %d", resp.StatusCode)
	}
	// 错误令牌
	bad := *c
	bad.token = "wrong"
	if code := bad.do("GET", "/api/state", nil, nil); code != 403 {
		t.Errorf("bad token: %d", code)
	}
	// 伪造 Host（DNS 重绑定）
	req, _ := http.NewRequest("GET", c.base+"/api/state", nil)
	req.Host = fmt.Sprintf("evil.example:%d", s.port)
	req.Header.Set("X-Token", c.token)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 403 {
		t.Errorf("bad host: %d", resp.StatusCode)
	}
	// 页面本身不需要令牌
	resp, _ = http.Get(c.base + "/")
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(body), "文件整理助手") {
		t.Errorf("index: %d", resp.StatusCode)
	}
	for _, f := range []string{"/app.js", "/style.css"} {
		if resp, _ := http.Get(c.base + f); resp.StatusCode != 200 {
			t.Errorf("%s: %d", f, resp.StatusCode)
		}
	}
}

func TestAPIWorkflow(t *testing.T) {
	c, srv := startTestServer(t)
	root := makeMessyTree(t)

	var st stateResp
	c.do("GET", "/api/state", nil, &st)
	if st.Scanned || len(st.Categories) < 10 {
		t.Fatalf("initial state %+v", st)
	}
	var e map[string]string
	if code := c.do("POST", "/api/scan", map[string]any{"path": filepath.Join(root, "不存在")}, &e); code != 400 || !strings.Contains(e["error"], "找不到") {
		t.Errorf("scan missing: %d %v", code, e)
	}
	st = c.scan(`"` + root + `"`) // 带引号的路径（资源管理器“复制为路径”）
	if !st.Scanned || st.Root.NFiles != 16 {
		t.Fatalf("after scan %+v", st)
	}
	rid := st.Root.ID

	var kids []struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
		Size int64  `json:"size"`
	}
	c.do("GET", fmt.Sprintf("/api/children?id=%d", rid), nil, &kids)
	if len(kids) != 5 || kids[0].Name != "下载" { // 按大小排序，下载最大
		t.Fatalf("children %+v", kids)
	}

	var stats struct {
		Cats []CatStat `json:"cats"`
		Size int64     `json:"size"`
	}
	c.do("GET", fmt.Sprintf("/api/stats?id=%d", rid), nil, &stats)
	if stats.Size != st.Root.Size || stats.Cats[0].Key != "video" {
		t.Errorf("stats %+v", stats)
	}

	var list ListResult
	c.do("POST", "/api/list", Query{Dir: rid, Recursive: true, Cat: "doc", Limit: 2}, &list)
	if list.Total != 4 || len(list.Rows) != 2 { // 需求文档.docx、说明.txt、a.txt、b.TXT（隐藏文件已跳过）
		t.Fatalf("list docs total=%d rows=%d", list.Total, len(list.Rows))
	}
	c.do("POST", "/api/list", Query{Dir: rid, Recursive: true, Cat: "doc", Offset: 3, Limit: 2}, &list)
	if len(list.Rows) != 1 {
		t.Errorf("paging: %d", len(list.Rows))
	}

	// 新建文件夹并把所有文档归类进去
	var res OpResult
	c.do("POST", "/api/mkdir", MkdirReq{Parent: rid, Name: "文档归档"}, &res)
	if res.NewID == 0 {
		t.Fatal("mkdir failed")
	}
	target := res.NewID
	c.do("POST", "/api/move", map[string]any{"query": Query{Dir: rid, Recursive: true, Cat: "doc"}, "target": target, "conflict": "rename"}, &res)
	if res.Done != 4 || !strings.HasSuffix(res.Target, "文档归档") {
		t.Fatalf("move %+v", res)
	}
	mustExist(t, filepath.Join(root, "文档归档", "说明.txt"))

	var loc map[string]int
	c.do("GET", "/api/locate?path="+urlQuery(filepath.Join(root, "文档归档")), nil, &loc)
	if loc["id"] != target {
		t.Errorf("locate %v", loc)
	}

	// 重命名
	c.do("POST", "/api/rename", RenameReq{Item: Ref{D: target, N: "说明.txt"}, NewName: "项目说明.txt"}, &res)
	mustExist(t, filepath.Join(root, "文档归档", "项目说明.txt"))
	if code := c.do("POST", "/api/rename", RenameReq{Item: Ref{D: target, N: "项目说明.txt"}, NewName: "bad|name.txt"}, &e); code != 400 {
		t.Errorf("invalid rename accepted")
	}

	// 批量重命名预览
	var br BatchRenameResult
	c.do("POST", "/api/batch-rename", map[string]any{"query": Query{Dir: target, Sort: "name"}, "template": "文档_{n}", "start": 1, "digits": 2, "preview": true}, &br)
	if br.OK != 4 || br.Plans[0].New != "文档_01.TXT" && br.Plans[0].New != "文档_01.txt" {
		t.Errorf("batch preview %+v", br.Plans)
	}

	// 历史与撤销
	var hist []Batch
	c.do("GET", "/api/history", nil, &hist)
	if len(hist) != 3 {
		t.Fatalf("history %d", len(hist))
	}
	for i := 0; i < 3; i++ {
		var u map[string]any
		if code := c.do("POST", "/api/undo", nil, &u); code != 200 {
			t.Fatalf("undo %d: %v", code, u)
		}
	}
	mustNotExist(t, filepath.Join(root, "文档归档"))
	mustExist(t, filepath.Join(root, "工作", "项目B", "深", "很深", "非常深", "最深", "说明.txt"))
	if code := c.do("POST", "/api/undo", nil, &e); code != 400 {
		t.Error("expected nothing to undo")
	}
	logData, _ := os.ReadFile(srv.logPath)
	if !strings.Contains(string(logData), "撤销：移动 4 项") || !strings.Contains(string(logData), "项目说明.txt") {
		t.Errorf("operation log incomplete:\n%s", logData)
	}

	// 浏览文件夹（扫描前选择路径用）
	var br2 struct {
		Path   string                  `json:"path"`
		Parent string                  `json:"parent"`
		Dirs   []struct{ Name string } `json:"dirs"`
	}
	c.do("GET", "/api/browse?path="+urlQuery(root), nil, &br2)
	if br2.Path != root || len(br2.Dirs) != 5 || br2.Parent == "" {
		t.Errorf("browse %+v", br2)
	}
}

func urlQuery(s string) string { return url.QueryEscape(s) }
