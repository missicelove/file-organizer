//go:build windows

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
	"unsafe"

	"golang.org/x/sys/windows"
)

func platformInit() {
	// 访问没有插入介质的读卡器/光驱时，不要弹出“驱动器中没有磁盘”之类的系统对话框。
	windows.SetErrorMode(windows.SEM_FAILCRITICALERRORS | windows.SEM_NOOPENFILEERRORBOX)
	// 关闭控制台的“快速编辑模式”：否则用户在窗口里点一下就会进入选择状态，
	// 导致写控制台的线程被挂起。
	var mode uint32
	if windows.GetConsoleMode(windows.Stdin, &mode) == nil {
		windows.SetConsoleMode(windows.Stdin, (mode&^windows.ENABLE_QUICK_EDIT_MODE)|windows.ENABLE_EXTENDED_FLAGS)
	}
	if p := windows.NewLazySystemDLL("kernel32.dll").NewProc("SetConsoleTitleW"); p.Find() == nil {
		if s, err := windows.UTF16PtrFromString("文件整理助手"); err == nil {
			p.Call(uintptr(unsafe.Pointer(s)))
		}
	}
}

func fileAttrs(info fs.FileInfo) (uint32, bool) {
	if d, ok := info.Sys().(*syscall.Win32FileAttributeData); ok && d != nil {
		return d.FileAttributes, true
	}
	return 0, false
}

func isHidden(info fs.FileInfo) bool {
	a, ok := fileAttrs(info)
	return ok && a&(syscall.FILE_ATTRIBUTE_HIDDEN|syscall.FILE_ATTRIBUTE_SYSTEM) != 0
}

// isLinkDir 识别目录联接（junction）、目录符号链接等：Go 不把它们当作目录，但它们带有目录属性。
func isLinkDir(info fs.FileInfo) bool {
	a, ok := fileAttrs(info)
	return ok && a&syscall.FILE_ATTRIBUTE_DIRECTORY != 0
}

func isCrossDevice(err error) bool {
	return errors.Is(err, windows.ERROR_NOT_SAME_DEVICE)
}

func isFileInUse(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}

func hasPathPrefix(p, prefix string) bool {
	if prefix == "" {
		return false
	}
	p, prefix = strings.ToLower(filepath.Clean(p)), strings.ToLower(filepath.Clean(prefix))
	return p == prefix || strings.HasPrefix(p, strings.TrimRight(prefix, `\`)+`\`)
}

// protectedReason 返回路径属于哪个系统位置；普通位置返回空字符串。
func protectedReason(p string) string {
	checks := []struct{ env, name string }{
		{"SystemRoot", "Windows 系统文件夹"},
		{"ProgramFiles", "程序安装文件夹"},
		{"ProgramFiles(x86)", "程序安装文件夹"},
		{"ProgramW6432", "程序安装文件夹"},
		{"ProgramData", "程序数据文件夹"},
	}
	for _, c := range checks {
		if hasPathPrefix(p, os.Getenv(c.env)) {
			return c.name
		}
	}
	lp := strings.ToLower(p) + `\`
	if strings.Contains(lp, `\appdata\`) {
		return "程序数据文件夹（AppData）"
	}
	if strings.Contains(lp, `\$recycle.bin\`) || strings.Contains(lp, `\system volume information\`) {
		return "系统文件夹"
	}
	return ""
}

func listPlaces() []Place {
	var out []Place
	add := func(id *windows.KNOWNFOLDERID, name string) {
		if p, err := windows.KnownFolderPath(id, windows.KF_FLAG_DEFAULT); err == nil && p != "" {
			out = append(out, Place{Name: name, Path: p, Kind: "folder"})
		}
	}
	add(windows.FOLDERID_Desktop, "桌面")
	add(windows.FOLDERID_Documents, "文档")
	add(windows.FOLDERID_Downloads, "下载")
	add(windows.FOLDERID_Pictures, "图片")
	if home, err := os.UserHomeDir(); err == nil {
		out = append(out, Place{Name: "个人文件夹", Path: home, Kind: "folder"})
	}
	mask, _ := windows.GetLogicalDrives()
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		root := string(rune('A'+i)) + `:\`
		rp, _ := windows.UTF16PtrFromString(root)
		typ := windows.GetDriveType(rp)
		kind := "本地磁盘"
		switch typ {
		case windows.DRIVE_REMOVABLE:
			kind = "U盘/移动磁盘"
		case windows.DRIVE_REMOTE:
			kind = "网络驱动器"
		case windows.DRIVE_CDROM:
			kind = "光驱"
		case windows.DRIVE_NO_ROOT_DIR, windows.DRIVE_UNKNOWN:
			continue
		}
		var free, total, totalFree uint64
		if err := windows.GetDiskFreeSpaceEx(rp, &free, &total, &totalFree); err != nil {
			continue // 没有插入介质的驱动器
		}
		label := make([]uint16, 261)
		fsName := make([]uint16, 261)
		name := kind
		if windows.GetVolumeInformation(rp, &label[0], uint32(len(label)), nil, nil, nil, &fsName[0], uint32(len(fsName))) == nil {
			if l := windows.UTF16ToString(label); l != "" {
				name = l
			}
		}
		out = append(out, Place{Name: name + " (" + root[:2] + ")", Path: root, Kind: "drive", Total: total, Free: totalFree})
	}
	return out
}

// shellOpen 用系统默认程序打开文件、文件夹或网址。
// 按照微软文档的要求，调用 ShellExecute 前在同一线程上初始化 COM。
func shellOpen(p string) error {
	verb, _ := windows.UTF16PtrFromString("open")
	file, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return err
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// 返回 S_OK 或 S_FALSE（已初始化）时都需要配对调用 CoUninitialize。
	if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE); err == nil || err == syscall.Errno(1) {
		defer windows.CoUninitialize()
	}
	return windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}

func openPath(p string) error { return shellOpen(p) }

func openBrowser(url string) error { return shellOpen(url) }

// revealPath 打开资源管理器并选中该文件。
func revealPath(p string) error {
	cmd := exec.Command("explorer.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer.exe /select,"` + p + `"`}
	return cmd.Start()
}
