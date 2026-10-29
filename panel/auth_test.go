package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func reqFrom(remote, xClientIP string) *http.Request {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = remote
	if xClientIP != "" {
		r.Header.Set("X-Client-IP", xClientIP)
	}
	return r
}

func TestClientIP(t *testing.T) {
	cases := []struct {
		name, remote, hdr, want string
	}{
		{"回环来源信任 X-Client-IP", "127.0.0.1:5555", "203.0.113.9", "203.0.113.9"},
		{"IPv6 回环同样信任", "[::1]:5555", "203.0.113.9", "203.0.113.9"},
		{"回环来源没有头就用回环地址", "127.0.0.1:5555", "", "127.0.0.1"},
		{"非回环来源忽略 X-Client-IP（防伪造）", "198.51.100.7:4444", "203.0.113.9", "198.51.100.7"},
		{"伪造的非 IP 内容被忽略", "127.0.0.1:5555", "1.1.1.1\nX-Evil: 1", "127.0.0.1"},
		{"伪造的主机名被忽略", "127.0.0.1:5555", "evil.example", "127.0.0.1"},
	}
	for _, c := range cases {
		if got := clientIP(reqFrom(c.remote, c.hdr)); got != c.want {
			t.Errorf("%s: 得到 %q，期望 %q", c.name, got, c.want)
		}
	}
}

func TestLoginRateLimit(t *testing.T) {
	a := newAuth()
	now := time.Now()
	a.now = func() time.Time { return now }
	ip := "203.0.113.9"

	for i := 1; i < maxFails; i++ {
		a.recordFail(ip)
		if locked, _ := a.locked(ip); locked {
			t.Fatalf("第 %d 次失败就被锁定了，应该是第 %d 次", i, maxFails)
		}
	}
	a.recordFail(ip)
	locked, left := a.locked(ip)
	if !locked || left < lockDuration-time.Second {
		t.Fatalf("第 %d 次失败后应锁定约 %v，实际 locked=%v left=%v", maxFails, lockDuration, locked, left)
	}
	if l, _ := a.locked("198.51.100.1"); l {
		t.Error("别的 IP 不应受影响")
	}
	now = now.Add(lockDuration + time.Second)
	if l, _ := a.locked(ip); l {
		t.Error("锁定期过后应解除")
	}
	a.recordFail(ip) // 解除后重新计数，不应一次就再锁
	if l, _ := a.locked(ip); l {
		t.Error("锁定期过后应重新计数")
	}
	for i := 0; i < maxFails; i++ {
		a.recordFail(ip)
	}
	a.recordOK(ip)
	if l, _ := a.locked(ip); l {
		t.Error("登录成功应清除失败记录")
	}
}

func TestSessions(t *testing.T) {
	a := newAuth()
	now := time.Now()
	a.now = func() time.Time { return now }
	tok := a.newSession()
	if len(tok) != 64 {
		t.Errorf("token 应为 32 字节的十六进制（64 字符），得到 %d", len(tok))
	}
	if !a.valid(tok) || a.valid("") || a.valid("nope") {
		t.Error("会话校验结果不对")
	}
	if tok2 := a.newSession(); tok2 == tok {
		t.Error("两个会话 token 不应相同")
	}
	now = now.Add(sessionTTL + time.Second)
	if a.valid(tok) {
		t.Error("会话应在 7 天后过期")
	}
	tok3 := a.newSession()
	a.drop(tok3)
	if a.valid(tok3) {
		t.Error("退出登录后会话应失效")
	}
}

func TestCredsAndPasswdFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SB_DIR", dir)
	pw, err := randomPassword(16)
	if err != nil || len(pw) != 16 {
		t.Fatalf("randomPassword: %q %v", pw, err)
	}
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		p, _ := randomPassword(16)
		if seen[p] {
			t.Fatal("随机密码重复")
		}
		seen[p] = true
		for _, c := range p {
			if !containsRune(passwordChars, c) {
				t.Fatalf("密码含有不在字符集里的字符 %q", c)
			}
		}
	}
	h, _ := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.MinCost)
	if err := writeFileAtomic(credsPath(dir), []byte(`{"username":"admin","password_hash":"`+string(h)+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := loadCreds(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !c.check("admin", pw) || c.check("admin", pw+"x") || c.check("root", pw) {
		t.Error("凭据校验结果不对")
	}
	st, _ := osStat(credsPath(dir))
	if st&0o077 != 0 {
		t.Errorf("panel.json 权限应为 600，实际 %o", st)
	}
}

func containsRune(s string, r rune) bool {
	for _, c := range s {
		if c == r {
			return true
		}
	}
	return false
}
