package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
)

const appName = "文件整理助手"

func main() {
	port := flag.Int("port", 0, "监听端口（默认自动选择）")
	noBrowser := flag.Bool("no-browser", false, "不自动打开浏览器")
	token := flag.String("token", "", "访问令牌（默认随机生成）")
	webDir := flag.String("webdir", "", "开发用：从该目录读取界面文件")
	logFile := flag.String("log", "", "操作日志文件路径（默认保存在用户的应用数据目录中）")
	trashDir := flag.String("trashdir", "", "开发用：删除时移到该目录，而不是系统回收站")
	flag.Parse()
	if *trashDir != "" {
		trashFunc = func(paths []string) error {
			for _, p := range paths {
				if err := os.Rename(p, uniquePath(*trashDir, filepath.Base(p), true)); err != nil {
					return err
				}
			}
			return nil
		}
	}

	platformInit()

	logPath := *logFile
	if dir, err := os.UserConfigDir(); err == nil && logPath == "" {
		logPath = filepath.Join(dir, appName, "操作日志.txt")
	}

	if *token == "" {
		b := make([]byte, 16)
		rand.Read(b)
		*token = hex.EncodeToString(b)
	}

	l, err := listen(*port)
	if err != nil {
		fatal("无法启动本地服务：%v", err)
	}
	s := &server{app: newApp(logPath), token: *token, port: l.Addr().(*net.TCPAddr).Port, logPath: logPath, webDir: *webDir, quit: make(chan struct{}), thumbs: newThumbCache(96 << 20)}
	url := fmt.Sprintf("http://127.0.0.1:%d/#t=%s", s.port, s.token)

	fmt.Println("==============================================")
	fmt.Println("  " + appName + " 已启动")
	fmt.Println("==============================================")
	fmt.Println()
	fmt.Println("  浏览器会自动打开操作界面。如果没有打开，")
	fmt.Println("  请复制下面的地址粘贴到浏览器地址栏：")
	fmt.Println()
	fmt.Println("  " + url)
	fmt.Println()
	fmt.Println("  使用期间请不要关闭本窗口；关闭本窗口即退出程序。")
	fmt.Println()

	go func() {
		srv := &http.Server{Handler: s.handler(), ErrorLog: log.New(io.Discard, "", 0)}
		if err := srv.Serve(l); err != nil {
			fatal("本地服务出错：%v", err)
		}
	}()
	if !*noBrowser {
		if err := openBrowser(url); err != nil {
			fmt.Println("  （自动打开浏览器失败，请手动复制上面的地址）")
		}
	}
	<-s.quit
	fmt.Println("  程序已退出。")
}

func fatal(format string, a ...any) {
	fmt.Printf("\n  出错了："+format+"\n\n  按回车键退出……", a...)
	bufio.NewReader(os.Stdin).ReadString('\n')
	os.Exit(1)
}
