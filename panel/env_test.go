package main

import (
	"strings"
	"testing"
)

func TestParseEnv(t *testing.T) {
	in := "# 注释\n\nSERVER_IP='1.2.3.4'\nIS_IPV6='false'\nSNI=addons.mozilla.org\nQ=\"双引号\"\nEMPTY=''\nbad line\nKEY = ' spaced '\n"
	e := parseEnv(strings.NewReader(in))
	want := map[string]string{"SERVER_IP": "1.2.3.4", "IS_IPV6": "false", "SNI": "addons.mozilla.org", "Q": "双引号", "EMPTY": "", "KEY": " spaced "}
	for k, v := range want {
		if got, ok := e[k]; !ok || got != v {
			t.Errorf("%s = %q (存在=%v)，期望 %q", k, got, ok, v)
		}
	}
	if _, ok := e["bad line"]; ok {
		t.Error("无等号的行不应被解析")
	}
}

func TestEnvAddr(t *testing.T) {
	cases := []struct {
		e    Env
		want string
	}{
		{Env{"SERVER_IP": "1.2.3.4", "IS_IPV6": "false"}, "1.2.3.4"},
		{Env{"SERVER_IP": "2001:db8::1", "IS_IPV6": "true"}, "[2001:db8::1]"},
		{Env{"SERVER_IP": "[2001:db8::1]", "IS_IPV6": "true"}, "[2001:db8::1]"},
		{Env{"SERVER_IP": "example.com"}, "example.com"},
	}
	for _, c := range cases {
		if got := c.e.addr(); got != c.want {
			t.Errorf("addr(%v) = %q，期望 %q", c.e, got, c.want)
		}
	}
}
