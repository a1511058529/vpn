package main

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	testPath  = "abcd1234"
	testToken = "0123456789abcdef0123456789abcdef"
	testPass  = "Correct-Horse-9"
)

type fixture struct {
	t      *testing.T
	dir    string
	log    string // 假脚本记录收到的参数
	ts     *httptest.Server
	srv    *Server
	client *http.Client
	base   string
}

const fakeScript = `#!/bin/sh
echo "$@" >> "$FAKE_LOG"
[ -n "$FAKE_SLEEP" ] && sleep "$FAKE_SLEEP"
if [ "$1" = "rebuild" ] && [ -f "$FAKE_FAIL_ONCE" ]; then rm -f "$FAKE_FAIL_ONCE"; echo "rebuild 失败"; exit 1; fi
echo "[+] ok $*"
`

const testTemplate = "port: 7890\nproxy-groups:\n- {name: 🔰 选择节点, type: select, proxies: [DIRECT]}\n- {name: 🛑 拦截广告, type: select, proxies: [REJECT]}\nrules:\n- MATCH,DIRECT\n"

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	env := "SERVER_IP='43.0.0.1'\nIS_IPV6='false'\nSNI='addons.mozilla.org'\nPORT_REALITY='11111'\nPORT_STLS='22222'\nPORT_VMESS='33333'\nPORT_H2='44444'\n" +
		"SUB_PORT='29708'\nSUB_TOKEN='" + testToken + "'\nARGO_DOMAIN='foo-bar.trycloudflare.com'\nPANEL_PORT='40000'\nPANEL_PATH='" + testPath + "'\n"
	must(t, os.WriteFile(filepath.Join(dir, ".vps.env"), []byte(env), 0o600))
	must(t, os.MkdirAll(filepath.Join(dir, "www", testToken), 0o755))
	nodes := base64.StdEncoding.EncodeToString([]byte("vless://node-a\nvmess://node-b\n"))
	must(t, os.WriteFile(filepath.Join(dir, "www", testToken, "v2ray"), []byte(nodes), 0o644))
	must(t, os.WriteFile(filepath.Join(dir, "config.tpl.yaml"), []byte(testTemplate), 0o644))
	h, _ := bcrypt.GenerateFromPassword([]byte(testPass), bcrypt.MinCost)
	cj, _ := json.Marshal(creds{Username: "admin", PasswordHash: string(h)})
	must(t, os.WriteFile(filepath.Join(dir, "panel.json"), cj, 0o600))

	script := filepath.Join(dir, "vps.sh")
	must(t, os.WriteFile(script, []byte(fakeScript), 0o755))
	logf := filepath.Join(dir, "calls.log")
	t.Setenv("FAKE_LOG", logf)
	t.Setenv("FAKE_FAIL_ONCE", filepath.Join(dir, "fail-once"))
	t.Setenv("FAKE_SLEEP", "")

	srv := newServer(dir, script)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	jar, _ := cookiejar.New(nil)
	return &fixture{t: t, dir: dir, log: logf, ts: ts, srv: srv, base: "/" + testPath + "/",
		client: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) do(method, path, body string, hdr map[string]string) (*http.Response, string) {
	f.t.Helper()
	req, err := http.NewRequest(method, f.ts.URL+path, strings.NewReader(body))
	must(f.t, err)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := f.client.Do(req)
	must(f.t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func formHdr(extra ...string) map[string]string {
	h := map[string]string{"Content-Type": "application/x-www-form-urlencoded"}
	for i := 0; i+1 < len(extra); i += 2 {
		h[extra[i]] = extra[i+1]
	}
	return h
}

func (f *fixture) login(extra ...string) *http.Response {
	f.t.Helper()
	body := url.Values{"username": {"admin"}, "password": {testPass}}.Encode()
	resp, _ := f.do("POST", f.base+"login", body, formHdr(extra...))
	return resp
}

func (f *fixture) calls() []string {
	b, _ := os.ReadFile(f.log)
	s := strings.TrimSpace(string(b))
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func (f *fixture) waitJob() JobView {
	f.t.Helper()
	for i := 0; i < 300; i++ {
		_, body := f.do("GET", f.base+"api/job", "", nil)
		var r struct{ Job *JobView }
		_ = json.Unmarshal([]byte(body), &r)
		if r.Job != nil && r.Job.State != "running" {
			return *r.Job
		}
		time.Sleep(20 * time.Millisecond)
	}
	f.t.Fatal("任务没有结束")
	return JobView{}
}

// ---------- 路由与 base path ----------

func TestBasePathRouting(t *testing.T) {
	f := newFixture(t)
	if r, _ := f.do("GET", "/"+testPath, "", nil); r.StatusCode != 301 || r.Header.Get("Location") != f.base {
		t.Errorf("不带斜杠应 301 到 %s: %d %s", f.base, r.StatusCode, r.Header.Get("Location"))
	}
	for _, p := range []string{"/", "/login", "/other/", "/" + testPath + "x/", "/api/overview", "/" + testPath + "/../etc/passwd"} {
		if r, _ := f.do("GET", p, "", nil); r.StatusCode != 404 {
			t.Errorf("%s 应 404（不泄露面板存在），实际 %d", p, r.StatusCode)
		}
	}
	r, body := f.do("GET", f.base+"login", "", nil)
	if r.StatusCode != 200 || !strings.Contains(body, `href="`+f.base+`static/style.css"`) || !strings.Contains(body, `action="`+f.base+`login"`) {
		t.Errorf("登录页应带 base path: %d", r.StatusCode)
	}
	// 静态资源不需要登录（里面没有任何秘密）
	if r, body := f.do("GET", f.base+"static/app.js", "", nil); r.StatusCode != 200 || !strings.Contains(body, "use strict") {
		t.Errorf("静态资源应可访问: %d", r.StatusCode)
	}
	// 安全响应头
	h := r.Header
	if h.Get("Cache-Control") != "no-store" || h.Get("X-Frame-Options") != "DENY" || !strings.Contains(h.Get("Content-Security-Policy"), "default-src 'self'") {
		t.Errorf("缺少安全响应头: %v", h)
	}
}

func TestNotConfigured(t *testing.T) {
	f := newFixture(t)
	must(t, os.WriteFile(filepath.Join(f.dir, ".vps.env"), []byte("SERVER_IP='1.1.1.1'\n"), 0o600))
	if r, _ := f.do("GET", f.base, "", nil); r.StatusCode != 503 {
		t.Errorf("没有 PANEL_PATH 应 503，实际 %d", r.StatusCode)
	}
	must(t, os.Remove(filepath.Join(f.dir, ".vps.env")))
	if r, _ := f.do("GET", f.base, "", nil); r.StatusCode != 503 {
		t.Errorf("没有 .vps.env 应 503，实际 %d", r.StatusCode)
	}
}

// PANEL_PATH 每次请求重新读取
func TestPanelPathReadPerRequest(t *testing.T) {
	f := newFixture(t)
	b, _ := os.ReadFile(filepath.Join(f.dir, ".vps.env"))
	must(t, os.WriteFile(filepath.Join(f.dir, ".vps.env"), []byte(strings.ReplaceAll(string(b), testPath, "newpath99")), 0o600))
	if r, _ := f.do("GET", f.base+"login", "", nil); r.StatusCode != 404 {
		t.Errorf("旧路径应失效: %d", r.StatusCode)
	}
	if r, _ := f.do("GET", "/newpath99/login", "", nil); r.StatusCode != 200 {
		t.Errorf("新路径应生效: %d", r.StatusCode)
	}
}

// ---------- 认证 ----------

func TestAuthRequired(t *testing.T) {
	f := newFixture(t)
	if r, _ := f.do("GET", f.base, "", nil); r.StatusCode != 303 || r.Header.Get("Location") != f.base+"login" {
		t.Errorf("未登录访问首页应跳转登录: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	for _, c := range []struct{ m, p string }{
		{"GET", "api/overview"}, {"GET", "api/job"}, {"GET", "api/rules"}, {"GET", "api/logs?unit=sing-box"},
		{"POST", "api/action/restart"}, {"POST", "api/rules"},
	} {
		r, body := f.do(c.m, f.base+c.p, "{}", nil)
		if r.StatusCode != 401 || !strings.Contains(body, "未登录") {
			t.Errorf("%s %s 未登录应 401: %d %s", c.m, c.p, r.StatusCode, body)
		}
	}
	if len(f.calls()) != 0 {
		t.Error("未登录时不应执行任何脚本")
	}
}

func TestLoginFlowAndCookie(t *testing.T) {
	f := newFixture(t)
	bad := url.Values{"username": {"admin"}, "password": {"wrong"}}.Encode()
	if r, body := f.do("POST", f.base+"login", bad, formHdr()); r.StatusCode != 401 || !strings.Contains(body, "用户名或密码错误") {
		t.Errorf("错误密码应 401: %d", r.StatusCode)
	}
	bad2 := url.Values{"username": {"root"}, "password": {testPass}}.Encode()
	if r, _ := f.do("POST", f.base+"login", bad2, formHdr()); r.StatusCode != 401 {
		t.Errorf("错误用户名应 401: %d", r.StatusCode)
	}

	r := f.login()
	if r.StatusCode != 303 || r.Header.Get("Location") != f.base {
		t.Fatalf("登录成功应 303 回首页: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	ck := r.Cookies()
	if len(ck) != 1 || ck[0].Name != cookieName || !ck[0].HttpOnly || ck[0].SameSite != http.SameSiteStrictMode || ck[0].Path != f.base || ck[0].Secure {
		t.Errorf("cookie 属性不对（HTTP 入口不应带 Secure）: %+v", ck)
	}
	if ck[0].MaxAge != int(sessionTTL.Seconds()) {
		t.Errorf("会话有效期应为 7 天: %d", ck[0].MaxAge)
	}
	if r, body := f.do("GET", f.base, "", nil); r.StatusCode != 200 || !strings.Contains(body, `name="base" content="`+f.base+`"`) {
		t.Errorf("登录后应能打开首页: %d", r.StatusCode)
	}
	// 退出
	if r, _ := f.do("POST", f.base+"logout", "", nil); r.StatusCode != 303 {
		t.Errorf("退出应 303: %d", r.StatusCode)
	}
	if r, _ := f.do("GET", f.base+"api/overview", "", nil); r.StatusCode != 401 {
		t.Errorf("退出后接口应 401: %d", r.StatusCode)
	}
}

func TestSecureCookieBehindHTTPS(t *testing.T) {
	f := newFixture(t)
	r := f.login("X-Forwarded-Proto", "https")
	ck := r.Cookies()
	if len(ck) != 1 || !ck[0].Secure {
		t.Errorf("经 Argo（X-Forwarded-Proto: https）登录时 cookie 应带 Secure: %+v", ck)
	}
}

func TestLoginLockoutPerIP(t *testing.T) {
	f := newFixture(t)
	bad := url.Values{"username": {"admin"}, "password": {"wrong"}}.Encode()
	for i := 0; i < maxFails; i++ {
		f.do("POST", f.base+"login", bad, formHdr("X-Client-IP", "203.0.113.9"))
	}
	if r := f.login("X-Client-IP", "203.0.113.9"); r.StatusCode != 429 {
		t.Errorf("连续失败 %d 次后，即使密码正确也应 429: %d", maxFails, r.StatusCode)
	}
	if r, body := f.do("POST", f.base+"login", bad, formHdr("X-Client-IP", "203.0.113.9")); r.StatusCode != 429 || !strings.Contains(body, "分钟后再试") {
		t.Errorf("锁定提示不对: %d", r.StatusCode)
	}
	if r := f.login("X-Client-IP", "198.51.100.5"); r.StatusCode != 303 {
		t.Errorf("别的 IP 不应被连坐: %d", r.StatusCode)
	}
}

// 直连（来源不是回环）的攻击者每次换一个 X-Client-IP 也绕不过限速
func TestLockoutNotBypassedByForgedHeader(t *testing.T) {
	f := newFixture(t)
	h := f.srv.Handler()
	attempt := func(pass, forged string) int {
		body := url.Values{"username": {"admin"}, "password": {pass}}.Encode()
		req := httptest.NewRequest("POST", f.base+"login", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("X-Client-IP", forged)
		req.RemoteAddr = "198.51.100.7:4444" // 非回环：头必须被忽略
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	for i := 0; i < maxFails; i++ {
		attempt("wrong", "10.0.0."+string(rune('1'+i)))
	}
	if code := attempt(testPass, "10.9.9.9"); code != 429 {
		t.Errorf("伪造 X-Client-IP 不应绕过锁定: %d", code)
	}
}

func TestCSRFOriginCheck(t *testing.T) {
	f := newFixture(t)
	f.login()
	r, _ := f.do("POST", f.base+"api/action/restart", "", map[string]string{"Origin": "http://evil.example"})
	if r.StatusCode != 403 {
		t.Errorf("跨站 Origin 应 403: %d", r.StatusCode)
	}
	if r, _ := f.do("POST", f.base+"logout", "", map[string]string{"Origin": "http://evil.example"}); r.StatusCode != 403 {
		t.Errorf("跨站退出也应 403: %d", r.StatusCode)
	}
	if r, _ := f.do("POST", f.base+"api/action/restart", "", map[string]string{"Origin": "null"}); r.StatusCode != 403 {
		t.Errorf("Origin: null 应 403: %d", r.StatusCode)
	}
	if len(f.calls()) != 0 {
		t.Error("被拒绝的请求不应执行脚本")
	}
	ok, _ := f.do("POST", f.base+"api/action/bbr", "", map[string]string{"Origin": f.ts.URL})
	if ok.StatusCode != 202 {
		t.Errorf("同源 Origin 应放行: %d", ok.StatusCode)
	}
	f.waitJob()
}

// ---------- 概览 ----------

func TestOverview(t *testing.T) {
	f := newFixture(t)
	f.login()
	r, body := f.do("GET", f.base+"api/overview", "", nil)
	if r.StatusCode != 200 {
		t.Fatalf("overview: %d %s", r.StatusCode, body)
	}
	var o overviewResp
	must(t, json.Unmarshal([]byte(body), &o))
	if o.ServerIP != "43.0.0.1" || o.SNI != "addons.mozilla.org" || o.ArgoDomain != "foo-bar.trycloudflare.com" {
		t.Errorf("基本信息不对: %+v", o)
	}
	if len(o.Services) != 4 {
		t.Errorf("应有 4 个服务: %+v", o.Services)
	}
	wantSub := "http://43.0.0.1:29708/" + testToken + "/"
	if len(o.Subs) != 3 || o.Subs[0].Value != wantSub+"proxies" || o.Subs[1].Value != wantSub+"clash" || o.Subs[2].Value != wantSub+"v2ray" {
		t.Errorf("订阅地址不对: %+v", o.Subs)
	}
	if len(o.Nodes) != 2 || o.Nodes[0] != "vless://node-a" || o.Nodes[1] != "vmess://node-b" {
		t.Errorf("节点链接应从 v2ray 文件解码: %+v", o.Nodes)
	}
	if len(o.Panel) != 2 || o.Panel[0].Value != "http://43.0.0.1:29708"+f.base || o.Panel[1].Value != "https://foo-bar.trycloudflare.com"+f.base {
		t.Errorf("面板入口应包含 HTTP 与 Argo HTTPS: %+v", o.Panel)
	}
	// 没有 Argo 域名时不显示 HTTPS 入口
	b, _ := os.ReadFile(filepath.Join(f.dir, ".vps.env"))
	must(t, os.WriteFile(filepath.Join(f.dir, ".vps.env"), []byte(strings.ReplaceAll(string(b), "foo-bar.trycloudflare.com", "")), 0o600))
	_, body = f.do("GET", f.base+"api/overview", "", nil)
	var o2 overviewResp
	must(t, json.Unmarshal([]byte(body), &o2))
	if len(o2.Panel) != 1 {
		t.Errorf("无 Argo 域名时只应有 HTTP 入口: %+v", o2.Panel)
	}
}

func TestOverviewRejectsBadToken(t *testing.T) {
	f := newFixture(t)
	f.login()
	b, _ := os.ReadFile(filepath.Join(f.dir, ".vps.env"))
	must(t, os.WriteFile(filepath.Join(f.dir, ".vps.env"), []byte(strings.ReplaceAll(string(b), testToken, "../../etc")), 0o600))
	_, body := f.do("GET", f.base+"api/overview", "", nil)
	var o overviewResp
	must(t, json.Unmarshal([]byte(body), &o))
	if len(o.Nodes) != 0 || len(o.Subs) != 0 {
		t.Errorf("token 格式不对时不应读取文件或生成链接（防路径穿越）: %+v", o)
	}
}

// ---------- 动作 ----------

func TestActions(t *testing.T) {
	f := newFixture(t)
	f.login()

	if r, _ := f.do("POST", f.base+"api/action/uninstall", "", nil); r.StatusCode != 404 {
		t.Errorf("白名单之外的动作应 404: %d", r.StatusCode)
	}
	if r, _ := f.do("POST", f.base+"api/action/..%2Fx", "", nil); r.StatusCode != 404 {
		t.Errorf("奇怪的动作名应 404: %d", r.StatusCode)
	}
	for _, host := range []string{"", "bad host", "x;y.com", "1.2.3.4"} {
		r, _ := f.do("POST", f.base+"api/action/set-sni", url.Values{"host": {host}}.Encode(), formHdr())
		if r.StatusCode != 400 {
			t.Errorf("非法 host %q 应 400: %d", host, r.StatusCode)
		}
	}
	if len(f.calls()) != 0 {
		t.Fatalf("被拒绝的请求不应执行脚本: %v", f.calls())
	}

	r, body := f.do("POST", f.base+"api/action/set-sni", url.Values{"host": {"example.org"}}.Encode(), formHdr())
	if r.StatusCode != 202 || !strings.Contains(body, `"restarts_argo":true`) {
		t.Fatalf("set-sni: %d %s", r.StatusCode, body)
	}
	j := f.waitJob()
	if j.State != "ok" || !strings.Contains(j.Output, "[+] ok set-sni example.org") {
		t.Errorf("任务结果不对: %+v", j)
	}
	if got := f.calls(); len(got) != 1 || got[0] != "set-sni example.org" {
		t.Errorf("脚本应收到恰好 'set-sni example.org': %v", got)
	}

	_, body = f.do("POST", f.base+"api/action/rebuild", "", nil)
	if !strings.Contains(body, `"restarts_argo":false`) {
		t.Errorf("rebuild 不重启 Argo: %s", body)
	}
	f.waitJob()
}

func TestActionBusy(t *testing.T) {
	f := newFixture(t)
	f.login()
	t.Setenv("FAKE_SLEEP", "0.5")
	if r, _ := f.do("POST", f.base+"api/action/restart", "", nil); r.StatusCode != 202 {
		t.Fatalf("第一个任务应被接受: %d", r.StatusCode)
	}
	r, body := f.do("POST", f.base+"api/action/bbr", "", nil)
	if r.StatusCode != 409 || !strings.Contains(body, "已有任务") {
		t.Errorf("已有任务在运行时应 409: %d %s", r.StatusCode, body)
	}
	if r, _ := f.do("POST", f.base+"api/rules", `{"text":""}`, nil); r.StatusCode != 409 {
		t.Errorf("保存规则也不能和别的任务并发: %d", r.StatusCode)
	}
	f.waitJob()
	if got := f.calls(); len(got) != 1 || got[0] != "restart" {
		t.Errorf("被拒绝的任务不应执行: %v", got)
	}
}

// ---------- 自定义规则 ----------

func TestRulesAPI(t *testing.T) {
	f := newFixture(t)
	f.login()

	_, body := f.do("GET", f.base+"api/rules", "", nil)
	var g struct {
		Text   string
		Groups []string
	}
	must(t, json.Unmarshal([]byte(body), &g))
	if g.Text != "" || len(g.Groups) != 2 || g.Groups[0] != "🔰 选择节点" {
		t.Errorf("初始读取不对: %s", body)
	}

	// 非法：整体拒绝，不落盘，不执行脚本，错误带行号
	r, body := f.do("POST", f.base+"api/rules", `{"text":"DOMAIN,a.com,DIRECT\nDOMAIN,b.com,没这个组\nFOO,x,DIRECT"}`, nil)
	var e struct {
		Error  string
		Errors []LineErr
	}
	must(t, json.Unmarshal([]byte(body), &e))
	if r.StatusCode != 400 || len(e.Errors) != 2 || e.Errors[0].Line != 2 || e.Errors[1].Line != 3 {
		t.Errorf("应 400 并带行号: %d %s", r.StatusCode, body)
	}
	if _, err := os.Stat(customRulesPath(f.dir)); err == nil {
		t.Error("校验失败不应写文件")
	}
	if len(f.calls()) != 0 {
		t.Error("校验失败不应执行脚本")
	}

	// 合法：CRLF 归一、写文件、执行 rebuild
	r, _ = f.do("POST", f.base+"api/rules", `{"text":"# 我的规则\r\nDOMAIN-SUFFIX,example.com,DIRECT\r\nDOMAIN,ad.test,🛑 拦截广告\r\n\r\n"}`, nil)
	if r.StatusCode != 202 {
		t.Fatalf("合法规则应 202: %d", r.StatusCode)
	}
	if j := f.waitJob(); j.State != "ok" || j.Name != "save-rules" {
		t.Errorf("任务应成功: %+v", j)
	}
	b, _ := os.ReadFile(customRulesPath(f.dir))
	if string(b) != "# 我的规则\nDOMAIN-SUFFIX,example.com,DIRECT\nDOMAIN,ad.test,🛑 拦截广告\n" {
		t.Errorf("文件内容不对: %q", b)
	}
	if got := f.calls(); len(got) != 1 || got[0] != "rebuild" {
		t.Errorf("应执行一次 rebuild: %v", got)
	}

	// 清空
	f.do("POST", f.base+"api/rules", `{"text":""}`, nil)
	f.waitJob()
	if b, _ := os.ReadFile(customRulesPath(f.dir)); len(b) != 0 {
		t.Errorf("清空后文件应为空: %q", b)
	}
}

// rebuild 失败时：回滚到保存前的内容，并再跑一次 rebuild 恢复订阅
func TestRulesRollbackOnRebuildFailure(t *testing.T) {
	f := newFixture(t)
	f.login()
	old := "DOMAIN,old.test,DIRECT\n"
	must(t, os.WriteFile(customRulesPath(f.dir), []byte(old), 0o644))
	must(t, os.WriteFile(filepath.Join(f.dir, "fail-once"), nil, 0o644))

	f.do("POST", f.base+"api/rules", `{"text":"DOMAIN,new.test,DIRECT"}`, nil)
	j := f.waitJob()
	if j.State != "failed" || !strings.Contains(j.Output, "回滚") || !strings.Contains(j.Output, "已回滚，订阅已恢复") {
		t.Errorf("应失败并回滚成功: %+v", j)
	}
	if b, _ := os.ReadFile(customRulesPath(f.dir)); string(b) != old {
		t.Errorf("应恢复保存前的内容: %q", b)
	}
	if got := f.calls(); len(got) != 2 || got[0] != "rebuild" || got[1] != "rebuild" {
		t.Errorf("应先 rebuild 失败、回滚后再 rebuild 一次: %v", got)
	}
}

func TestRulesBodyTooLarge(t *testing.T) {
	f := newFixture(t)
	f.login()
	huge := `{"text":"` + strings.Repeat("#", maxRulesBytes+10000) + `"}`
	if r, _ := f.do("POST", f.base+"api/rules", huge, nil); r.StatusCode != 400 {
		t.Errorf("超大请求应 400: %d", r.StatusCode)
	}
}

// ---------- 日志 ----------

func TestLogsUnitWhitelist(t *testing.T) {
	f := newFixture(t)
	f.login()
	for _, u := range []string{"", "nginx", "ssh", "sing-box;id", "../x"} {
		if r, _ := f.do("GET", f.base+"api/logs?unit="+url.QueryEscape(u), "", nil); r.StatusCode != 400 {
			t.Errorf("unit=%q 应 400: %d", u, r.StatusCode)
		}
	}
	r, body := f.do("GET", f.base+"api/logs?unit=sing-box&n=99999", "", nil)
	if r.StatusCode != 200 || !strings.Contains(body, `"text"`) {
		t.Errorf("合法服务应 200: %d %s", r.StatusCode, body)
	}
}
