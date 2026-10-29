package main

import (
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed web
var webFS embed.FS

type ctxKey struct{}

// reqCtx 是每个请求的上下文：外层处理器解析出 base path 和当次读取的 .vps.env
type reqCtx struct {
	base string // 形如 /abcd1234/
	env  Env
}

type Server struct {
	dir, script string
	auth        *Auth
	jobs        *Jobs
	pages       *template.Template
	mux         *http.ServeMux

	kmu    sync.Mutex
	kernel string
	kat    time.Time
}

var (
	panelPathRe = regexp.MustCompile(`^[A-Za-z0-9_-]{4,64}$`)
	tokenRe     = regexp.MustCompile(`^[0-9a-f]{8,64}$`)
)

func newServer(dir, script string) *Server {
	s := &Server{dir: dir, script: script, auth: newAuth(), jobs: &Jobs{}}
	s.pages = template.Must(template.ParseFS(webFS, "web/*.html"))
	static, _ := fs.Sub(webFS, "web/static")

	m := http.NewServeMux()
	m.HandleFunc("GET /{$}", s.appPage)
	m.HandleFunc("GET /login", s.loginPage)
	m.HandleFunc("POST /login", s.loginPost)
	m.HandleFunc("POST /logout", s.logout)
	m.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	m.HandleFunc("GET /api/overview", s.api(s.overview))
	m.HandleFunc("POST /api/action/{name}", s.api(s.doAction))
	m.HandleFunc("GET /api/job", s.api(s.jobStatus))
	m.HandleFunc("GET /api/rules", s.api(s.getRules))
	m.HandleFunc("POST /api/rules", s.api(s.saveRules))
	m.HandleFunc("GET /api/logs", s.api(s.logs))
	s.mux = m
	return s
}

// Handler 在路由前先剥掉 /<PANEL_PATH> 前缀（nginx 的 proxy_pass 不带 URI，完整路径会透传过来）。
// PANEL_PATH 每次请求都重新读取；前缀不对的请求一律 404，不泄露面板的存在。
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		// 不能用 no-referrer：该策略下浏览器发 POST 时会把 Origin 写成 "null"，同源校验会把正常登录也拒掉
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")

		env, err := loadEnv(s.dir)
		if err != nil || !panelPathRe.MatchString(env["PANEL_PATH"]) {
			http.Error(w, "panel not configured", http.StatusServiceUnavailable)
			return
		}
		base := "/" + env["PANEL_PATH"]
		if r.URL.Path == base {
			http.Redirect(w, r, base+"/", http.StatusMovedPermanently)
			return
		}
		if !strings.HasPrefix(r.URL.Path, base+"/") {
			http.NotFound(w, r)
			return
		}
		inner := strings.TrimPrefix(r.URL.Path, base)
		// 路径里出现 ..、// 或反斜杠一律 404；否则内层 ServeMux 会做路径清理并 301，且跳转目标会丢掉 base 前缀
		if strings.Contains(inner, "..") || strings.Contains(inner, "//") || strings.Contains(inner, `\`) {
			http.NotFound(w, r)
			return
		}
		r2 := r.Clone(context.WithValue(r.Context(), ctxKey{}, &reqCtx{base: base + "/", env: env}))
		r2.URL.Path = inner
		r2.URL.RawPath = ""
		s.mux.ServeHTTP(w, r2)
	})
}

func rc(r *http.Request) *reqCtx { return r.Context().Value(ctxKey{}).(*reqCtx) }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func jsonErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (s *Server) authed(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	return err == nil && s.auth.valid(c.Value)
}

// sameOrigin 防 CSRF 的补充：带 Origin 的请求，其主机必须和 Host 一致（再配合 SameSite=Strict）
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	return err == nil && u.Host == r.Host
}

// api 给接口加上登录校验；变更类接口只接受 POST（路由已限定），并校验 Origin
func (s *Server) api(h func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authed(r) {
			jsonErr(w, http.StatusUnauthorized, "未登录或登录已过期")
			return
		}
		if r.Method == http.MethodPost && !sameOrigin(r) {
			jsonErr(w, http.StatusForbidden, "来源校验失败")
			return
		}
		h(w, r)
	}
}

// ---------- 页面与登录 ----------

func (s *Server) render(w http.ResponseWriter, code int, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	_ = s.pages.ExecuteTemplate(w, name, data)
}

type pageData struct {
	Base, Version, Error string
}

func (s *Server) appPage(w http.ResponseWriter, r *http.Request) {
	if !s.authed(r) {
		http.Redirect(w, r, rc(r).base+"login", http.StatusSeeOther)
		return
	}
	s.render(w, 200, "app.html", pageData{Base: rc(r).base, Version: version})
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, 200, "login.html", pageData{Base: rc(r).base, Version: version})
}

func (s *Server) loginPost(w http.ResponseWriter, r *http.Request) {
	c := rc(r)
	d := pageData{Base: c.base, Version: version}
	if !sameOrigin(r) {
		d.Error = "来源校验失败"
		s.render(w, http.StatusForbidden, "login.html", d)
		return
	}
	ip := clientIP(r)
	if locked, left := s.auth.locked(ip); locked {
		d.Error = fmt.Sprintf("失败次数过多，请 %d 分钟后再试", int(left.Minutes())+1)
		s.render(w, http.StatusTooManyRequests, "login.html", d)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		d.Error = "请求无效"
		s.render(w, http.StatusBadRequest, "login.html", d)
		return
	}
	cr, err := loadCreds(s.dir)
	if err != nil {
		d.Error = "面板还没有设置密码，请在服务器上执行: vps panel-passwd"
		s.render(w, http.StatusInternalServerError, "login.html", d)
		return
	}
	if !cr.check(r.PostForm.Get("username"), r.PostForm.Get("password")) {
		s.auth.recordFail(ip)
		d.Error = "用户名或密码错误"
		s.render(w, http.StatusUnauthorized, "login.html", d)
		return
	}
	s.auth.recordOK(ip)
	s.auth.setCookie(w, r, c.base, s.auth.newSession(), int(sessionTTL.Seconds()))
	http.Redirect(w, r, c.base, http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if ck, err := r.Cookie(cookieName); err == nil {
		s.auth.drop(ck.Value)
	}
	s.auth.setCookie(w, r, rc(r).base, "", -1)
	http.Redirect(w, r, rc(r).base+"login", http.StatusSeeOther)
}

// ---------- 概览 ----------

type svcState struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Active bool   `json:"active"`
}

type kv struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Note  string `json:"note,omitempty"`
}

type overviewResp struct {
	Version    string     `json:"version"`
	Now        string     `json:"now"`
	Services   []svcState `json:"services"`
	Kernel     string     `json:"kernel"`
	ServerIP   string     `json:"server_ip"`
	SNI        string     `json:"sni"`
	ArgoDomain string     `json:"argo_domain"`
	Ports      []kv       `json:"ports"`
	Subs       []kv       `json:"subs"`
	Nodes      []string   `json:"nodes"`
	Panel      []kv       `json:"panel"`
}

var serviceUnits = []string{"sing-box", "sb-nginx", "sb-argo", "sb-panel"}

func serviceState(unit string) svcState {
	out, _ := exec.Command("systemctl", "is-active", unit).CombinedOutput()
	st := strings.TrimSpace(string(out))
	if st == "" || strings.Contains(st, "not found") {
		st = "unknown"
	}
	return svcState{Name: unit, State: st, Active: st == "active"}
}

func (s *Server) kernelVersion() string {
	s.kmu.Lock()
	defer s.kmu.Unlock()
	if time.Since(s.kat) < 30*time.Second {
		return s.kernel
	}
	bin := os.Getenv("SB_BIN")
	if bin == "" {
		bin = "/usr/local/bin/sing-box"
	}
	out, err := exec.Command(bin, "version").Output()
	s.kernel = ""
	if err == nil {
		s.kernel = strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	}
	s.kat = time.Now()
	return s.kernel
}

// 订阅里的 v2ray 文件是 base64 编码的节点链接（不含 ShadowTLS）
func (s *Server) nodeLinks(token string) []string {
	if !tokenRe.MatchString(token) {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(s.dir, "www", token, "v2ray"))
	if err != nil {
		return nil
	}
	dec, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(string(dec), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	c := rc(r)
	e := c.env
	o := overviewResp{
		Version:    version,
		Now:        time.Now().In(cst).Format("2006-01-02 15:04:05"),
		Kernel:     s.kernelVersion(),
		ServerIP:   e["SERVER_IP"],
		SNI:        e["SNI"],
		ArgoDomain: e["ARGO_DOMAIN"],
		Nodes:      s.nodeLinks(e["SUB_TOKEN"]),
	}
	for _, u := range serviceUnits {
		o.Services = append(o.Services, serviceState(u))
	}
	o.Ports = []kv{
		{"xtls-reality", e["PORT_REALITY"], "TCP"},
		{"ShadowTLS", e["PORT_STLS"], "TCP"},
		{"h2-reality", e["PORT_H2"], "TCP"},
		{"Hysteria2", e["PORT_HY2"], "UDP"},
		{"AnyTLS", e["PORT_ANYTLS"], "TCP"},
		{"vmess-ws", e["PORT_VMESS"], "仅本机，经 nginx + Argo"},
		{"订阅 / 面板入口", e["SUB_PORT"], "TCP"},
	}
	if tokenRe.MatchString(e["SUB_TOKEN"]) {
		b := fmt.Sprintf("http://%s:%s/%s/", e.addr(), e["SUB_PORT"], e["SUB_TOKEN"])
		o.Subs = []kv{
			{"Clash 订阅（只有节点）", b + "proxies", ""},
			{"完整 Clash 配置（规则 + 节点）", b + "clash", ""},
			{"Shadowrocket 订阅", b + "shadowrocket", ""},
			{"通用订阅（不含 ShadowTLS）", b + "v2ray", ""},
		}
	}
	o.Panel = []kv{{"HTTP 入口", fmt.Sprintf("http://%s:%s%s", e.addr(), e["SUB_PORT"], c.base), "明文传输"}}
	if e["ARGO_DOMAIN"] != "" {
		o.Panel = append(o.Panel, kv{"Argo HTTPS 入口", "https://" + e["ARGO_DOMAIN"] + c.base, "推荐；重启隧道后域名会变"})
	}
	writeJSON(w, 200, o)
}

// ---------- 动作与任务 ----------

func (s *Server) doAction(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	spec, ok := actions[name]
	if !ok {
		jsonErr(w, http.StatusNotFound, "未知的动作")
		return
	}
	args := []string{name}
	if spec.needsHost {
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if err := r.ParseForm(); err != nil {
			jsonErr(w, http.StatusBadRequest, "请求无效")
			return
		}
		host := strings.TrimSpace(r.PostForm.Get("host"))
		if !validHost(host) {
			jsonErr(w, http.StatusBadRequest, "不是合法的域名")
			return
		}
		args = append(args, host)
	}
	id, err := s.jobs.Start(name, jobTimeout, func(ctx context.Context, w io.Writer) error {
		fmt.Fprintf(w, "$ vps.sh %s\n", strings.Join(args, " "))
		return runScript(ctx, w, s.script, args...)
	})
	if err != nil {
		jsonErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, 202, map[string]any{"id": id, "restarts_argo": spec.restartsArgo})
}

func (s *Server) jobStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"job": s.jobs.Snapshot()})
}

// ---------- 自定义规则 ----------

func (s *Server) getRules(w http.ResponseWriter, r *http.Request) {
	text, err := readCustomRules(s.dir)
	if err != nil {
		jsonErr(w, 500, "读取自定义规则失败: "+err.Error())
		return
	}
	groups := readGroups(s.dir)
	if groups == nil {
		groups = []string{}
	}
	writeJSON(w, 200, map[string]any{"text": text, "groups": groups})
}

func (s *Server) saveRules(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRulesBytes+4096)
	var in struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		jsonErr(w, http.StatusBadRequest, "请求无效或内容过大")
		return
	}
	text := strings.TrimRight(strings.ReplaceAll(in.Text, "\r\n", "\n"), "\n ")
	if errs := validateRules(text, readGroups(s.dir)); errs != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "规则校验未通过，没有保存", "errors": errs})
		return
	}
	if text != "" {
		text += "\n"
	}
	path := customRulesPath(s.dir)
	old, _ := readCustomRules(s.dir)
	id, err := s.jobs.Start("save-rules", jobTimeout, func(ctx context.Context, w io.Writer) error {
		if err := writeFileAtomic(path, []byte(text), 0o644); err != nil {
			return fmt.Errorf("保存失败: %w", err)
		}
		fmt.Fprintln(w, "自定义规则已保存，正在重新生成订阅 ...")
		if err := runScript(ctx, w, s.script, "rebuild"); err != nil {
			fmt.Fprintln(w, "\n重新生成订阅失败，正在回滚到保存前的自定义规则 ...")
			_ = writeFileAtomic(path, []byte(old), 0o644)
			if err2 := runScript(ctx, w, s.script, "rebuild"); err2 != nil {
				fmt.Fprintln(w, "回滚后重新生成也失败:", err2)
			} else {
				fmt.Fprintln(w, "已回滚，订阅已恢复")
			}
			return err
		}
		return nil
	})
	if err != nil {
		jsonErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, 202, map[string]any{"id": id})
}

// ---------- 日志 ----------

var logUnits = map[string]bool{"sing-box": true, "sb-nginx": true, "sb-argo": true, "sb-panel": true}

func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	unit := r.URL.Query().Get("unit")
	if !logUnits[unit] {
		jsonErr(w, http.StatusBadRequest, "未知的服务")
		return
	}
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	if n < 10 {
		n = 100
	}
	if n > 1000 {
		n = 1000
	}
	cmd := exec.Command("journalctl", "-u", unit, "-n", strconv.Itoa(n), "--no-pager")
	cmd.Env = append(os.Environ(), "TZ=Asia/Shanghai")
	out, err := cmd.CombinedOutput()
	if len(out) > 512*1024 {
		out = out[len(out)-512*1024:]
	}
	text := stripANSI(string(out))
	if err != nil && text == "" {
		text = "读取日志失败: " + err.Error()
	}
	writeJSON(w, 200, map[string]string{"unit": unit, "text": text})
}
