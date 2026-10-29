package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	cookieName    = "vps_panel_session"
	sessionTTL    = 7 * 24 * time.Hour
	maxFails      = 5
	lockDuration  = 15 * time.Minute
	passwordChars = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
)

type creds struct {
	Username     string `json:"username"`
	PasswordHash string `json:"password_hash"`
}

func credsPath(dir string) string { return filepath.Join(dir, "panel.json") }

func loadCreds(dir string) (*creds, error) {
	b, err := os.ReadFile(credsPath(dir))
	if err != nil {
		return nil, err
	}
	var c creds
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	if c.Username == "" || c.PasswordHash == "" {
		return nil, fmt.Errorf("panel.json 内容不完整")
	}
	return &c, nil
}

// 16 位随机密码，字符集去掉了容易混淆的 0/O/1/l/I；用拒绝采样避免取模偏差
func randomPassword(n int) (string, error) {
	limit := 256 - 256%len(passwordChars)
	out := make([]byte, 0, n)
	buf := make([]byte, 64)
	for len(out) < n {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for _, b := range buf {
			if int(b) >= limit {
				continue
			}
			out = append(out, passwordChars[int(b)%len(passwordChars)])
			if len(out) == n {
				break
			}
		}
	}
	return string(out), nil
}

// passwdCmd 实现 `vps-panel passwd`：生成新密码，写入 panel.json（0600），明文只打印这一次
func passwdCmd() {
	dir := sbDir()
	pw, err := randomPassword(16)
	if err != nil {
		fmt.Fprintln(os.Stderr, "生成密码失败:", err)
		os.Exit(1)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pw), 10)
	if err != nil {
		fmt.Fprintln(os.Stderr, "生成哈希失败:", err)
		os.Exit(1)
	}
	b, _ := json.MarshalIndent(creds{Username: "admin", PasswordHash: string(hash)}, "", "  ")
	if err := writeFileAtomic(credsPath(dir), append(b, '\n'), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "写入 panel.json 失败:", err)
		os.Exit(1)
	}
	fmt.Println("面板用户名: admin")
	fmt.Println("面板密码:   " + pw)
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, mode); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, path)
}

type failRec struct {
	n           int
	lockedUntil time.Time
}

type Auth struct {
	mu       sync.Mutex
	sessions map[string]time.Time
	fails    map[string]*failRec
	now      func() time.Time
}

func newAuth() *Auth {
	return &Auth{sessions: map[string]time.Time{}, fails: map[string]*failRec{}, now: time.Now}
}

// clientIP 取得真实客户端地址。
// 面板只被本机 nginx 访问，所以来源永远是回环地址；此时才信任 nginx 写入的 X-Client-IP
// （nginx 只在请求来自 cloudflared 隧道时才采信 CF-Connecting-IP，直连用 $remote_addr，
// 因此攻击者无法通过伪造请求头来绕开登录限速）。非回环来源一律用 RemoteAddr，忽略该头。
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if h := net.ParseIP(r.Header.Get("X-Client-IP")); h != nil {
			return h.String()
		}
	}
	return host
}

// locked 返回该 IP 是否处于锁定期，以及剩余时间
func (a *Auth) locked(ip string) (bool, time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	f := a.fails[ip]
	if f == nil {
		return false, 0
	}
	if d := f.lockedUntil.Sub(a.now()); d > 0 {
		return true, d
	}
	return false, 0
}

func (a *Auth) recordFail(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	// 顺带清理已过期的记录，防止攻击者用大量不同 IP 撑大内存
	if len(a.fails) > 4096 {
		for k, v := range a.fails {
			if now.After(v.lockedUntil) && v.n < maxFails {
				delete(a.fails, k)
			}
		}
	}
	f := a.fails[ip]
	if f == nil {
		f = &failRec{}
		a.fails[ip] = f
	}
	if !f.lockedUntil.IsZero() && now.After(f.lockedUntil) {
		f.n, f.lockedUntil = 0, time.Time{} // 锁定期已过，重新计数
	}
	f.n++
	if f.n >= maxFails {
		f.lockedUntil = now.Add(lockDuration)
	}
}

func (a *Auth) recordOK(ip string) {
	a.mu.Lock()
	delete(a.fails, ip)
	a.mu.Unlock()
}

// check 校验用户名密码。用户名错误时同样会跑一次 bcrypt，避免通过耗时探测用户名
func (c *creds) check(user, pass string) bool {
	userOK := subtle.ConstantTimeCompare([]byte(user), []byte(c.Username)) == 1
	passOK := bcrypt.CompareHashAndPassword([]byte(c.PasswordHash), []byte(pass)) == nil
	return userOK && passOK
}

func (a *Auth) newSession() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	tok := hex.EncodeToString(b)
	a.mu.Lock()
	now := a.now()
	for k, exp := range a.sessions {
		if now.After(exp) {
			delete(a.sessions, k)
		}
	}
	a.sessions[tok] = now.Add(sessionTTL)
	a.mu.Unlock()
	return tok
}

func (a *Auth) valid(tok string) bool {
	if tok == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	exp, ok := a.sessions[tok]
	if !ok {
		return false
	}
	if a.now().After(exp) {
		delete(a.sessions, tok)
		return false
	}
	return true
}

func (a *Auth) drop(tok string) {
	a.mu.Lock()
	delete(a.sessions, tok)
	a.mu.Unlock()
}

func (a *Auth) setCookie(w http.ResponseWriter, r *http.Request, base, tok string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    tok,
		Path:     base,
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		// 经 Argo（Cloudflare 终结 TLS）访问时浏览器侧是 HTTPS，此时加上 Secure
		Secure: r.Header.Get("X-Forwarded-Proto") == "https",
	})
}
