//go:build !windows

package main

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

// 非 Windows 平台的实现主要用于开发和测试。

func platformInit() {}

func isHidden(info fs.FileInfo) bool { return strings.HasPrefix(info.Name(), ".") }

func isLinkDir(info fs.FileInfo) bool { return false }

func isCrossDevice(err error) bool { return errors.Is(err, syscall.EXDEV) }

func isFileInUse(err error) bool { return errors.Is(err, syscall.ETXTBSY) }

func protectedReason(p string) string {
	for _, s := range []string{"/System", "/usr", "/bin", "/sbin", "/Library", "/Applications", "/etc", "/opt/homebrew"} {
		if p == s || strings.HasPrefix(p, s+"/") {
			return "系统文件夹"
		}
	}
	return ""
}

func listPlaces() []Place {
	var out []Place
	if home, err := os.UserHomeDir(); err == nil {
		for _, f := range []struct{ dir, name string }{{"Desktop", "桌面"}, {"Documents", "文档"}, {"Downloads", "下载"}, {"Pictures", "图片"}} {
			p := filepath.Join(home, f.dir)
			if fi, err := os.Stat(p); err == nil && fi.IsDir() {
				out = append(out, Place{Name: f.name, Path: p, Kind: "folder"})
			}
		}
		out = append(out, Place{Name: "个人文件夹", Path: home, Kind: "folder"})
	}
	out = append(out, Place{Name: "根目录 (/)", Path: "/", Kind: "drive"})
	if entries, err := os.ReadDir("/Volumes"); err == nil {
		for _, e := range entries {
			out = append(out, Place{Name: e.Name(), Path: filepath.Join("/Volumes", e.Name()), Kind: "drive"})
		}
	}
	return out
}

func opener() string {
	if runtime.GOOS == "darwin" {
		return "open"
	}
	return "xdg-open"
}

func openPath(p string) error { return exec.Command(opener(), p).Start() }

func openBrowser(url string) error { return exec.Command(opener(), url).Start() }

func revealPath(p string) error {
	if runtime.GOOS == "darwin" {
		return exec.Command("open", "-R", p).Start()
	}
	return exec.Command("xdg-open", filepath.Dir(p)).Start()
}
