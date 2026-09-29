#!/bin/sh
# 构建 Windows 版本：生成 dist/文件整理助手.exe
set -e
cd "$(dirname "$0")"
go vet ./...
go test ./...
GOOS=windows GOARCH=amd64 go vet ./...
mkdir -p dist
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "dist/文件整理助手.exe" .
ls -la dist
