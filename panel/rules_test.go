package main

import (
	"os"
	"strings"
	"testing"
)

var testGroups = []string{"🔰 选择节点", "🇨🇳 国内网站", "🛑 拦截广告", "🎮 Steam 登录/下载"}

func TestGroupNamesFromRealTemplate(t *testing.T) {
	b, err := os.ReadFile("../config.tpl.yaml")
	if err != nil {
		t.Skip("找不到仓库里的 config.tpl.yaml")
	}
	got := groupNames(string(b))
	if len(got) != 12 {
		t.Fatalf("应提取到 12 个策略组，实际 %d: %v", len(got), got)
	}
	want := []string{"♻️ 自动选择", "🔰 选择节点", "🌏 爱奇艺&哔哩哔哩", "📺 动画疯", "🎮 Steam 登录/下载", "🎮 Steam 商店/社区",
		"🌩️ Cloudflare", "☁️ OneDrive", "🎓学术网站", "🇨🇳 国内网站", "🛑 拦截广告", "🐟 漏网之鱼"}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("第 %d 个组名 = %q，期望 %q", i, got[i], w)
		}
	}
}

func TestGroupNamesStyles(t *testing.T) {
	tpl := "dns: {}\nproxy-groups:\n- {name: 'A 组', type: select, proxies: [x]}\n- {name: \"B,组\", type: select}\n  - name: C组\n    type: select\n- {name: D, type: url-test, proxies: []}\nrules:\n- {name: 不该被提取, type: x}\n"
	got := groupNames(tpl)
	want := []string{"A 组", "B,组", "C组", "D"} // 带引号的组名（含逗号）能被完整提取；规则行里引用它不可能（逗号是分隔符）
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("得到 %q，期望 %q", got, want)
	}
}

func TestValidateRulesOK(t *testing.T) {
	text := strings.Join([]string{
		"# 注释和空行都应跳过", "",
		"DOMAIN-SUFFIX,example.com,DIRECT",
		"  DOMAIN,foo.bar , 🛑 拦截广告  ",
		"DOMAIN-KEYWORD,ads,REJECT",
		"DOMAIN-REGEX,^ad[0-9]+\\.com$,REJECT-DROP",
		"IP-CIDR,10.0.0.0/8,DIRECT,no-resolve",
		"IP-CIDR6,2001:db8::/32,🔰 选择节点,no-resolve",
		"GEOIP,CN,🇨🇳 国内网站,no-resolve",
		"DST-PORT,443,🔰 选择节点",
		"DST-PORT,8000-9000,DIRECT",
		"PROCESS-NAME,Google Chrome,DIRECT",
		"MATCH,🔰 选择节点",
		"domain-suffix,lower.case,DIRECT",
		"DOMAIN,steam.test,🎮 Steam 登录/下载\r",
	}, "\n")
	if errs := validateRules(text, testGroups); errs != nil {
		t.Fatalf("合法规则被拒绝: %+v", errs)
	}
	if errs := validateRules("", testGroups); errs != nil {
		t.Errorf("空内容应合法（用来清空自定义规则）: %+v", errs)
	}
}

func TestValidateRulesBad(t *testing.T) {
	cases := []struct {
		line, want string
	}{
		{"DOMAIN-SUFFIX,example.com", "格式"},
		{"DOMAIN-SUFFIX,example.com,DIRECT,extra,more", "格式"},
		{"DOMAIN-SUFFIX,example.com,不存在的组", "未知的策略"},
		{"DOMAIN-SUFFIX,example.com,direct", "未知的策略"}, // 策略名区分大小写
		{"FOO-BAR,x,DIRECT", "不支持的规则类型"},
		{"RULE-SET,x,DIRECT", "不支持的规则类型"},
		{"DOMAIN,,DIRECT", "值不能为空"},
		{"DOMAIN,has space.com,DIRECT", "空格"},
		{"IP-CIDR,300.0.0.1/8,DIRECT", "IPv4 CIDR"},
		{"IP-CIDR,10.0.0.0,DIRECT", "IPv4 CIDR"},
		{"IP-CIDR,2001:db8::/32,DIRECT", "IPv4 CIDR"},
		{"IP-CIDR6,10.0.0.0/8,DIRECT", "IPv6 CIDR"},
		{"DST-PORT,abc,DIRECT", "端口"},
		{"DST-PORT,70000,DIRECT", "超出"},
		{"DST-PORT,0,DIRECT", "超出"},
		{"DOMAIN,example.com,DIRECT,no-resolve", "不能带 no-resolve"},
		{"IP-CIDR,10.0.0.0/8,DIRECT,whatever", "no-resolve"},
		{"MATCH", "MATCH"},
		{"MATCH,DIRECT,extra", "MATCH"},
		{"MATCH,不存在", "未知的策略"},
		{strings.Repeat("a", maxLineLen+1), "太长"},
	}
	for _, c := range cases {
		errs := validateRules(c.line, testGroups)
		if len(errs) != 1 {
			t.Errorf("%q: 应报 1 个错误，实际 %+v", c.line, errs)
			continue
		}
		if !strings.Contains(errs[0].Msg, c.want) {
			t.Errorf("%q: 错误信息 %q 不含 %q", c.line, errs[0].Msg, c.want)
		}
	}
}

func TestValidateRulesLineNumbers(t *testing.T) {
	text := "DOMAIN,ok.com,DIRECT\n# 注释\n\nDOMAIN,bad.com,没有这个组\nDOMAIN,ok2.com,DIRECT\nFOO,x,DIRECT\n"
	errs := validateRules(text, testGroups)
	if len(errs) != 2 || errs[0].Line != 4 || errs[1].Line != 6 {
		t.Fatalf("行号不对（注释和空行也要计入）: %+v", errs)
	}
}

func TestValidateRulesNoTemplateGroups(t *testing.T) {
	// 读不到模板时只允许内置策略，写了组名的规则要被拒绝，而不是放行
	if errs := validateRules("DOMAIN,a.com,🔰 选择节点", nil); len(errs) != 1 {
		t.Errorf("没有策略组信息时，引用组名应被拒绝: %+v", errs)
	}
	if errs := validateRules("DOMAIN,a.com,DIRECT", nil); errs != nil {
		t.Errorf("内置策略应始终可用: %+v", errs)
	}
}

func TestValidateRulesLimits(t *testing.T) {
	big := strings.Repeat("DOMAIN,a.com,DIRECT\n", maxRulesLines+1)
	if errs := validateRules(big, nil); len(errs) != 1 || !strings.Contains(errs[0].Msg, "过多") {
		t.Errorf("行数超限应被拒绝: %+v", errs)
	}
	huge := strings.Repeat("#", maxRulesBytes+1)
	if errs := validateRules(huge, nil); len(errs) != 1 || !strings.Contains(errs[0].Msg, "过大") {
		t.Errorf("体积超限应被拒绝: %+v", errs)
	}
}
