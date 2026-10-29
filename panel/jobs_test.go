package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeScript(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "vps.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func waitDone(t *testing.T, j *Jobs) *JobView {
	t.Helper()
	for i := 0; i < 200; i++ {
		if v := j.Snapshot(); v != nil && v.State != "running" {
			return v
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("任务没有在预期时间内结束")
	return nil
}

func TestJobOKAndOutput(t *testing.T) {
	script := writeScript(t, `printf '\033[32m[+]\033[0m 参数: %s\n' "$*"; echo "NO_QR=$NO_QR TZ=$TZ"`)
	j := &Jobs{}
	if j.Snapshot() != nil {
		t.Error("没跑过任务时快照应为 nil")
	}
	id, err := j.Start("t", 5*time.Second, func(ctx context.Context, w io.Writer) error {
		return runScript(ctx, w, script, "set-sni", "a b;c")
	})
	if err != nil || id != 1 {
		t.Fatalf("Start: %d %v", id, err)
	}
	v := waitDone(t, j)
	if v.State != "ok" || v.ID != 1 {
		t.Fatalf("状态不对: %+v", v)
	}
	if strings.Contains(v.Output, "\x1b") {
		t.Error("输出里的 ANSI 颜色码应被去掉")
	}
	if !strings.Contains(v.Output, "[+] 参数: set-sni a b;c") {
		t.Errorf("参数应原样传递而不经过 shell: %q", v.Output)
	}
	if !strings.Contains(v.Output, "NO_QR=1 TZ=Asia/Shanghai") {
		t.Errorf("应设置 NO_QR 和北京时区: %q", v.Output)
	}
	if v.Ended == "" || v.Started == "" {
		t.Error("应记录起止时间")
	}
}

func TestJobFailureExitCode(t *testing.T) {
	script := writeScript(t, `echo 出错了; exit 3`)
	j := &Jobs{}
	j.Start("t", 5*time.Second, func(ctx context.Context, w io.Writer) error { return runScript(ctx, w, script) })
	v := waitDone(t, j)
	if v.State != "failed" || v.ExitCode != 3 || !strings.Contains(v.Output, "出错了") || !strings.Contains(v.Output, "[失败]") {
		t.Errorf("失败状态不对: %+v", v)
	}
}

func TestJobBusyThenFree(t *testing.T) {
	script := writeScript(t, `sleep 0.4; echo done`)
	j := &Jobs{}
	run := func(ctx context.Context, w io.Writer) error { return runScript(ctx, w, script) }
	if _, err := j.Start("a", 5*time.Second, run); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Start("b", 5*time.Second, run); err != errBusy {
		t.Errorf("已有任务在运行时应返回 errBusy，实际 %v", err)
	}
	waitDone(t, j)
	id, err := j.Start("c", 5*time.Second, run)
	if err != nil || id != 2 {
		t.Errorf("任务结束后应能再启动，ID 递增: %d %v", id, err)
	}
	waitDone(t, j)
}

func TestJobTimeoutKillsProcessGroup(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "child-survived")
	// 脚本再起一个子进程；超时后整组都要被杀，子进程不能留下
	script := writeScript(t, `(sleep 2; echo x > `+marker+`) & sleep 30`)
	j := &Jobs{}
	start := time.Now()
	j.Start("t", 300*time.Millisecond, func(ctx context.Context, w io.Writer) error { return runScript(ctx, w, script) })
	v := waitDone(t, j)
	if time.Since(start) > 5*time.Second {
		t.Errorf("超时后应很快结束，耗时 %v", time.Since(start))
	}
	if v.State != "failed" || !strings.Contains(v.Output, "强制终止") {
		t.Errorf("应标记为超时失败: %+v", v)
	}
	time.Sleep(2500 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Error("子进程在超时后仍然存活（进程组没有被整体杀掉）")
	}
}

func TestJobOutputCap(t *testing.T) {
	j := &Jobs{}
	j.Start("t", 5*time.Second, func(ctx context.Context, w io.Writer) error {
		chunk := []byte(strings.Repeat("x", 1024))
		for i := 0; i < 600; i++ {
			w.Write(chunk)
		}
		w.Write([]byte("TAIL"))
		return nil
	})
	v := waitDone(t, j)
	if len(v.Output) > maxJobOutput+16 || !strings.HasSuffix(v.Output, "TAIL") {
		t.Errorf("输出应只保留尾部 %d 字节: 长度 %d", maxJobOutput, len(v.Output))
	}
}

func TestHostValidation(t *testing.T) {
	good := []string{"addons.mozilla.org", "www.apple.com", "a-b.example.co.uk"}
	bad := []string{"", "bad host", "a..b.com", "-x.com", "x_y.com", "x;y.com", "$(id).com", "nodot", "1.2.3.4", strings.Repeat("a", 64) + ".com", "ex.com\nX: 1"}
	for _, h := range good {
		if !validHost(h) {
			t.Errorf("%q 应合法", h)
		}
	}
	for _, h := range bad {
		if validHost(h) {
			t.Errorf("%q 应被拒绝", h)
		}
	}
}

func TestActionWhitelist(t *testing.T) {
	for _, n := range []string{"set-sni", "reroll-ports", "reset-keys", "reset-token", "refresh-argo", "restart", "update", "rebuild", "bbr", "rules"} {
		if _, ok := actions[n]; !ok {
			t.Errorf("白名单缺少 %s", n)
		}
	}
	for _, n := range []string{"install", "uninstall", "panel-install", "panel-passwd", "sub", "status", "log", "resync", "../x", ""} {
		if _, ok := actions[n]; ok {
			t.Errorf("%q 不应出现在白名单里（方案明确不做卸载/重装/面板自管理）", n)
		}
	}
	if !actions["set-sni"].needsHost {
		t.Error("set-sni 需要域名参数")
	}
}
