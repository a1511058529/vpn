// vps-panel：vps.sh 的 Web 运维面板。
//
// 面板本身不改任何 sing-box 配置：所有变更都通过调用 vps.sh 的非交互动作完成，
// 对 .vps.env 只读。只监听 127.0.0.1，由 sb-nginx 反代到 /<PANEL_PATH>/ 下。
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

var version = "dev"

// 页面和日志里的时间一律使用北京时间
var cst = time.FixedZone("CST", 8*3600)

func sbDir() string {
	if d := os.Getenv("SB_DIR"); d != "" {
		return d
	}
	return "/etc/sing-box"
}

func scriptPath(dir string) string {
	if p := os.Getenv("VPS_SCRIPT"); p != "" {
		return p
	}
	return filepath.Join(dir, "vps.sh")
}

func main() {
	time.Local = cst
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "serve":
		serve()
	case "passwd":
		passwdCmd()
	case "version", "-v", "--version":
		fmt.Println(version)
	default:
		fmt.Fprintln(os.Stderr, "用法: vps-panel [serve|passwd|version]")
		os.Exit(2)
	}
}

var portRe = regexp.MustCompile(`^[0-9]{1,5}$`)

func serve() {
	dir := sbDir()
	env, err := loadEnv(dir)
	if err != nil {
		log.Fatalf("读取 %s/.vps.env 失败: %v", dir, err)
	}
	if !portRe.MatchString(env["PANEL_PORT"]) {
		log.Fatalf("PANEL_PORT 无效（%q），请先执行: vps panel-install", env["PANEL_PORT"])
	}
	s := newServer(dir, scriptPath(dir))
	addr := "127.0.0.1:" + env["PANEL_PORT"]
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	log.Printf("vps-panel %s 监听 %s", version, addr)
	log.Fatal(srv.ListenAndServe())
}
