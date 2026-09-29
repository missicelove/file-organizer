//go:build !windows

package main

import "testing"

// 在 macOS/Linux 上以点开头的文件本身就是隐藏文件。
func markHidden(t *testing.T, p string) {}
