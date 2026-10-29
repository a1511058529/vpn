package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sync"
	"syscall"
	"time"
)

const (
	jobTimeout   = 300 * time.Second
	maxJobOutput = 256 * 1024
)

var errBusy = errors.New("已有任务在运行，请等它结束")

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

type job struct {
	id      int
	name    string
	state   string // running / ok / failed
	buf     []byte
	started time.Time
	ended   time.Time
	exit    int
}

// JobView 是任务的只读快照
type JobView struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	State    string `json:"state"`
	Output   string `json:"output"`
	Started  string `json:"started"`
	Ended    string `json:"ended"`
	ExitCode int    `json:"exit_code"`
}

// Jobs 同一时间只允许一个任务；只保留最近一个任务的结果
type Jobs struct {
	mu  sync.Mutex
	seq int
	cur *job
}

type jobWriter struct {
	j  *Jobs
	jb *job
}

func (w *jobWriter) Write(p []byte) (int, error) {
	w.j.mu.Lock()
	defer w.j.mu.Unlock()
	w.jb.buf = append(w.jb.buf, p...)
	if n := len(w.jb.buf); n > maxJobOutput {
		w.jb.buf = append([]byte(nil), w.jb.buf[n-maxJobOutput:]...)
	}
	return len(p), nil
}

// Start 异步执行 fn，立即返回任务 ID。已有任务在跑时返回 errBusy。
func (j *Jobs) Start(name string, timeout time.Duration, fn func(ctx context.Context, w io.Writer) error) (int, error) {
	j.mu.Lock()
	if j.cur != nil && j.cur.state == "running" {
		j.mu.Unlock()
		return 0, errBusy
	}
	j.seq++
	jb := &job{id: j.seq, name: name, state: "running", started: time.Now()}
	j.cur = jb
	j.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		err := fn(ctx, &jobWriter{j: j, jb: jb})
		timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)

		j.mu.Lock()
		defer j.mu.Unlock()
		jb.ended = time.Now()
		switch {
		case err == nil:
			jb.state = "ok"
		default:
			jb.state = "failed"
			jb.exit = -1
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				jb.exit = ee.ExitCode()
			}
			msg := "\n[失败] " + err.Error() + "\n"
			if timedOut {
				msg = "\n[失败] 超过 " + timeout.String() + " 仍未结束，已强制终止\n"
			}
			jb.buf = append(jb.buf, msg...)
		}
	}()
	return jb.id, nil
}

// Snapshot 返回最近一个任务；从未执行过任务时返回 nil
func (j *Jobs) Snapshot() *JobView {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.cur == nil {
		return nil
	}
	v := &JobView{
		ID:       j.cur.id,
		Name:     j.cur.name,
		State:    j.cur.state,
		Output:   stripANSI(string(j.cur.buf)),
		Started:  j.cur.started.In(cst).Format("2006-01-02 15:04:05"),
		ExitCode: j.cur.exit,
	}
	if !j.cur.ended.IsZero() {
		v.Ended = j.cur.ended.In(cst).Format("2006-01-02 15:04:05")
	}
	return v
}

// runScript 以参数数组直接执行脚本，不经过 shell，因此参数里的特殊字符不会被解释。
// 脚本放进独立进程组，超时或取消时整组杀掉。
func runScript(ctx context.Context, w io.Writer, script string, args ...string) error {
	cmd := exec.CommandContext(ctx, script, args...)
	cmd.Env = append(os.Environ(), "TZ=Asia/Shanghai", "NO_QR=1")
	cmd.Stdout, cmd.Stderr = w, w
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	return cmd.Run()
}

// actionSpec 描述一个允许从面板触发的 vps.sh 动作
type actionSpec struct {
	needsHost    bool // 需要一个域名参数（set-sni）
	restartsArgo bool // 会重启 Argo 隧道：临时域名会变，通过 Argo 访问的页面会失联
}

// 白名单：动作名必须在这里，面板不会执行任何其它参数
var actions = map[string]actionSpec{
	"set-sni":      {needsHost: true, restartsArgo: true},
	"reroll-ports": {restartsArgo: true},
	"reset-keys":   {restartsArgo: true},
	"reset-token":  {restartsArgo: true},
	"refresh-argo": {restartsArgo: true},
	"restart":      {restartsArgo: true},
	"update":       {restartsArgo: true},
	"rebuild":      {},
	"bbr":          {},
	"rules":        {},
}

var hostRe = regexp.MustCompile(`^([A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.)+[A-Za-z]{2,63}$`)

func validHost(h string) bool { return len(h) <= 253 && hostRe.MatchString(h) }
