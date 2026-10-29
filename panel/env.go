package main

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Env 是 .vps.env 的只读视图。该文件由 vps.sh 的 save_env 写成 KEY='value'。
type Env map[string]string

func parseEnv(r io.Reader) Env {
	e := Env{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '\'' && v[len(v)-1] == '\'' || v[0] == '"' && v[len(v)-1] == '"') {
			v = v[1 : len(v)-1]
		}
		e[k] = v
	}
	return e
}

// 每次请求都重新读取：面板不缓存任何由脚本维护的状态
func loadEnv(dir string) (Env, error) {
	f, err := os.Open(filepath.Join(dir, ".vps.env"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseEnv(f), nil
}

// addr 返回可直接放进 URL 的主机部分，IPv6 需要方括号
func (e Env) addr() string {
	ip := e["SERVER_IP"]
	if e["IS_IPV6"] == "true" && !strings.HasPrefix(ip, "[") {
		return "[" + ip + "]"
	}
	return ip
}
