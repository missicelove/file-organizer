//go:build !windows

package main

import "image"

// 非 Windows 平台只支持程序自己能解码的图片格式。
func platformThumbExts() []string { return nil }

func platformThumb(path string, size int) (image.Image, error) { return nil, errNoThumb }
