package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const (
	maxRulesBytes = 256 * 1024
	maxRulesLines = 5000
	maxLineLen    = 512
)

func customRulesPath(dir string) string { return filepath.Join(dir, "custom_rules.txt") }
func templatePath(dir string) string    { return filepath.Join(dir, "config.tpl.yaml") }

var (
	// - {name: 组名, type: ...} 这种 flow 写法（当前模板使用）
	groupFlowRe = regexp.MustCompile(`^\s*-\s*\{\s*name:\s*(.+?)\s*,\s*type:`)
	// - name: 组名 这种 block 写法
	groupBlockRe = regexp.MustCompile(`^\s*-\s*name:\s*(.+?)\s*$`)
)

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '\'' && s[len(s)-1] == '\'' || s[0] == '"' && s[len(s)-1] == '"') {
		return s[1 : len(s)-1]
	}
	return s
}

// groupNames 从模板的 proxy-groups 段提取策略组名。只做文本扫描，不引入 YAML 库。
func groupNames(tpl string) []string {
	var names []string
	in := false
	for _, line := range strings.Split(tpl, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "proxy-groups:") {
			in = true
			continue
		}
		if in && strings.HasPrefix(line, "rules:") {
			break
		}
		if !in {
			continue
		}
		if m := groupFlowRe.FindStringSubmatch(line); m != nil {
			names = append(names, unquote(m[1]))
		} else if m := groupBlockRe.FindStringSubmatch(line); m != nil {
			names = append(names, unquote(m[1]))
		}
	}
	return names
}

// 面板允许写入的规则类型（mihomo 支持的常用子集）
var ruleTypes = map[string]bool{
	"DOMAIN": true, "DOMAIN-SUFFIX": true, "DOMAIN-KEYWORD": true, "DOMAIN-REGEX": true, "GEOSITE": true,
	"IP-CIDR": true, "IP-CIDR6": true, "GEOIP": true, "IP-ASN": true, "SRC-IP-CIDR": true,
	"DST-PORT": true, "SRC-PORT": true, "PROCESS-NAME": true,
}

// 只有 IP 类规则才能带 no-resolve
var noResolveTypes = map[string]bool{"IP-CIDR": true, "IP-CIDR6": true, "GEOIP": true, "IP-ASN": true}

var builtinPolicies = map[string]bool{"DIRECT": true, "REJECT": true, "REJECT-DROP": true}

var portRangeRe = regexp.MustCompile(`^[0-9]{1,5}(-[0-9]{1,5})?$`)

type LineErr struct {
	Line int    `json:"line"`
	Msg  string `json:"msg"`
}

func validValue(typ, v string) string {
	if v == "" {
		return "值不能为空"
	}
	if typ != "PROCESS-NAME" && strings.ContainsAny(v, " \t") {
		return "值里不能有空格"
	}
	switch typ {
	case "IP-CIDR", "SRC-IP-CIDR":
		if ip, _, err := net.ParseCIDR(v); err != nil || ip.To4() == nil {
			return "不是合法的 IPv4 CIDR（例如 10.0.0.0/8）"
		}
	case "IP-CIDR6":
		if ip, _, err := net.ParseCIDR(v); err != nil || ip.To4() != nil {
			return "不是合法的 IPv6 CIDR"
		}
	case "DST-PORT", "SRC-PORT":
		if !portRangeRe.MatchString(v) {
			return "端口必须是数字或范围（例如 443 或 8000-9000）"
		}
		for _, p := range strings.Split(v, "-") {
			if n, _ := strconv.Atoi(p); n < 1 || n > 65535 {
				return "端口超出 1-65535"
			}
		}
	}
	return ""
}

// validateRules 逐行校验自定义规则，返回带行号的错误；没有错误时返回 nil。
// 策略名写错会导致整份 /clash 配置在客户端加载失败，所以这里必须在保存前拦住。
func validateRules(text string, groups []string) []LineErr {
	var errs []LineErr
	if len(text) > maxRulesBytes {
		return []LineErr{{0, fmt.Sprintf("内容过大（超过 %d KB）", maxRulesBytes/1024)}}
	}
	policy := map[string]bool{}
	for g := range builtinPolicies {
		policy[g] = true
	}
	for _, g := range groups {
		policy[g] = true
	}
	lines := strings.Split(text, "\n")
	if len(lines) > maxRulesLines {
		return []LineErr{{0, fmt.Sprintf("规则过多（超过 %d 行）", maxRulesLines)}}
	}
	for i, raw := range lines {
		n := i + 1
		line := strings.TrimSpace(strings.TrimRight(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if len(line) > maxLineLen {
			errs = append(errs, LineErr{n, "这一行太长"})
			continue
		}
		parts := strings.Split(line, ",")
		for k := range parts {
			parts[k] = strings.TrimSpace(parts[k])
		}
		typ := strings.ToUpper(parts[0])

		if typ == "MATCH" {
			if len(parts) != 2 {
				errs = append(errs, LineErr{n, "MATCH 的格式是 MATCH,策略"})
			} else if !policy[parts[1]] {
				errs = append(errs, LineErr{n, "未知的策略: " + parts[1]})
			}
			continue
		}
		if !ruleTypes[typ] {
			errs = append(errs, LineErr{n, "不支持的规则类型: " + parts[0]})
			continue
		}
		if len(parts) != 3 && len(parts) != 4 {
			errs = append(errs, LineErr{n, "格式应为 类型,值,策略[,no-resolve]"})
			continue
		}
		if msg := validValue(typ, parts[1]); msg != "" {
			errs = append(errs, LineErr{n, msg})
			continue
		}
		if !policy[parts[2]] {
			errs = append(errs, LineErr{n, "未知的策略: " + parts[2] + "（必须是 DIRECT / REJECT / REJECT-DROP 或模板里的策略组名）"})
			continue
		}
		if len(parts) == 4 {
			if !strings.EqualFold(parts[3], "no-resolve") {
				errs = append(errs, LineErr{n, "第 4 段只能是 no-resolve"})
			} else if !noResolveTypes[typ] {
				errs = append(errs, LineErr{n, typ + " 不能带 no-resolve"})
			}
		}
	}
	return errs
}

func readCustomRules(dir string) (string, error) {
	b, err := os.ReadFile(customRulesPath(dir))
	if os.IsNotExist(err) {
		return "", nil
	}
	return string(b), err
}

func readGroups(dir string) []string {
	b, err := os.ReadFile(templatePath(dir))
	if err != nil {
		return nil
	}
	return groupNames(string(b))
}
