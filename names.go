package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"
)

// 按 Windows 规则校验文件名（在所有平台上都使用同一套规则，保证行为一致）。
var reservedNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

func validateName(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("名称不能为空")
	}
	if name == "." || name == ".." {
		return errors.New("名称无效")
	}
	for _, r := range name {
		if r < 32 {
			return errors.New("名称不能包含控制字符")
		}
		if strings.ContainsRune(`<>:"/\|?*`, r) {
			return fmt.Errorf("名称不能包含下列字符：\\ / : * ? \" < > |")
		}
	}
	if strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return errors.New("名称不能以空格或句点结尾")
	}
	if strings.HasPrefix(name, " ") {
		return errors.New("名称不能以空格开头")
	}
	base := strings.ToUpper(name)
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	if reservedNames[strings.TrimRight(base, " ")] {
		return fmt.Errorf("“%s” 是 Windows 系统保留名称，不能使用", name)
	}
	if len(utf16.Encode([]rune(name))) > 255 {
		return errors.New("名称太长（最多 255 个字符）")
	}
	return nil
}

// splitExt 把 "a.b.txt" 拆成 "a.b" 和 ".txt"；以点开头且没有其他点的名称视为没有扩展名。
func splitExt(name string) (string, string) {
	i := strings.LastIndexByte(name, '.')
	if i <= 0 {
		return name, ""
	}
	return name[:i], name[i:]
}

// uniquePath 在 dir 中为 name 找一个不存在的名称："x.txt" → "x (1).txt" → "x (2).txt"。
func uniquePath(dir, name string, isDir bool) string {
	p := filepath.Join(dir, name)
	if !exists(p) {
		return p
	}
	base, ext := splitExt(name)
	if isDir {
		base, ext = name, ""
	}
	for i := 1; ; i++ {
		p = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", base, i, ext))
		if !exists(p) {
			return p
		}
	}
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil || !os.IsNotExist(err)
}

// sameFile 判断两个路径是否指向同一个文件（用于大小写不敏感文件系统上仅修改大小写的重命名）。
func sameFile(a, b string) bool {
	fa, err1 := os.Lstat(a)
	fb, err2 := os.Lstat(b)
	return err1 == nil && err2 == nil && os.SameFile(fa, fb)
}
