#!/usr/bin/env bash
#
# VPS 一键部署脚本 —— 把 VPS 变成你自己的订阅
#
# 用法: bash <(wget -qO- https://raw.githubusercontent.com/a1511058529/vpn/main/vps.sh)
#   或: bash <(curl -fsSL https://raw.githubusercontent.com/a1511058529/vpn/main/vps.sh)
#
# 装完会得到:
#   http://VPS_IP:PORT/<token>/proxies   Clash 订阅（节点列表，配合本地规则用）
#   http://VPS_IP:PORT/<token>/clash     完整 Clash 配置（规则 + 节点，开箱即用）
#   http://VPS_IP:PORT/<token>/v2ray     base64 节点链接（v2rayN / Shadowrocket 等）
#
# 安装后输入 vps 重新打开菜单
#

VERSION='1.0.0'
GH_USER='a1511058529'
GH_REPO='vpn'
GH_BRANCH='main'

SB_BIN='/usr/local/bin/sing-box'
SB_DIR='/etc/sing-box'
SB_CONF="$SB_DIR/config.json"
WWW="$SB_DIR/www"
ENVF="$SB_DIR/.vps.env"
SELF="$SB_DIR/vps.sh"
SHORTCUT='/usr/local/bin/vps'
SB_SERVICE='/etc/systemd/system/sing-box.service'
SUB_SERVICE='/etc/systemd/system/sb-sub.service'

CORE_REPO='SagerNet/sing-box'
GH_MIRRORS=('' 'https://ghfast.top/' 'https://gh-proxy.com/' 'https://ghproxy.net/')

DEFAULT_SNI='www.apple.com'

red()   { printf '\033[31m%s\033[0m\n' "$*"; }
green() { printf '\033[32m%s\033[0m\n' "$*"; }
yellow(){ printf '\033[33m%s\033[0m\n' "$*"; }
cyan()  { printf '\033[36m%s\033[0m\n' "$*"; }
info()  { printf '\033[36m[*]\033[0m %s\n' "$*"; }
ok()    { printf '\033[32m[+]\033[0m %s\n' "$*"; }
warn()  { printf '\033[33m[!]\033[0m %s\n' "$*"; }
err()   { printf '\033[31m[x]\033[0m %s\n' "$*" >&2; }
die()   { err "$*"; exit 1; }

# ============ 环境 ============

check_root()    { [ "$(id -u)" = '0' ] || die '需要 root 权限，请用 sudo 运行'; }
check_systemd() { command -v systemctl >/dev/null 2>&1 || die '未检测到 systemd，本脚本仅支持 systemd 系统'; }

pkg_install() {
  [ $# -eq 0 ] && return 0
  if   command -v apt-get >/dev/null 2>&1; then apt-get update -qq >/dev/null 2>&1; apt-get install -y -qq "$@" >/dev/null 2>&1
  elif command -v dnf     >/dev/null 2>&1; then dnf install -y -q "$@" >/dev/null 2>&1
  elif command -v yum     >/dev/null 2>&1; then yum install -y -q "$@" >/dev/null 2>&1
  elif command -v apk     >/dev/null 2>&1; then apk add --no-cache "$@" >/dev/null 2>&1
  elif command -v pacman  >/dev/null 2>&1; then pacman -Sy --noconfirm "$@" >/dev/null 2>&1
  else return 1
  fi
}

check_deps() {
  local miss=()
  command -v curl >/dev/null 2>&1 || command -v wget >/dev/null 2>&1 || miss+=(curl)
  command -v tar     >/dev/null 2>&1 || miss+=(tar)
  command -v openssl >/dev/null 2>&1 || miss+=(openssl)
  command -v python3 >/dev/null 2>&1 || miss+=(python3)
  if [ ${#miss[@]} -gt 0 ]; then
    info "缺少依赖: ${miss[*]}，尝试自动安装"
    pkg_install "${miss[@]}" || warn '自动安装失败，若后续报错请手动安装'
  fi
  command -v python3 >/dev/null 2>&1 || die '需要 python3 来托管订阅，请手动安装后重试'
  command -v qrencode >/dev/null 2>&1 || pkg_install qrencode
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64)  ARCH='amd64' ;;
    aarch64|arm64) ARCH='arm64' ;;
    armv7l|armv7)  ARCH='armv7' ;;
    i386|i686)     ARCH='386'   ;;
    s390x)         ARCH='s390x' ;;
    *) die "不支持的架构: $(uname -m)" ;;
  esac
}

# ============ 下载（多镜像回退） ============

fetch() {
  local url="$1" out="$2" try prefix
  local -a prefixes=('')
  case "$url" in
    https://github.com/*|https://raw.githubusercontent.com/*|https://api.github.com/*)
      prefixes=("${GH_MIRRORS[@]}") ;;
  esac
  for prefix in "${prefixes[@]}"; do
    try="${prefix}${url}"
    if command -v curl >/dev/null 2>&1; then
      if [ "$out" = '-' ]; then curl -fsSL --connect-timeout 10 --max-time 300 "$try" && return 0
      else curl -fsSL --connect-timeout 10 --max-time 300 -o "$out" "$try" && return 0; fi
    else
      if [ "$out" = '-' ]; then wget -qO- --timeout=15 "$try" && return 0
      else wget -qO "$out" --timeout=15 "$try" && return 0; fi
    fi
    [ -n "$prefix" ] && warn "镜像失败 ($prefix)，换下一个"
  done
  return 1
}

# ============ 状态 ============

load_env() { [ -f "$ENVF" ] && . "$ENVF"; return 0; }

save_env() {
  mkdir -p "$SB_DIR"
  cat > "$ENVF" <<EOF
SERVER_IP='$SERVER_IP'
IS_IPV6='$IS_IPV6'
NODE_NAME='$NODE_NAME'
VLESS_PORT='$VLESS_PORT'
UUID='$UUID'
SNI='$SNI'
REALITY_PRIV='$REALITY_PRIV'
REALITY_PUB='$REALITY_PUB'
SHORT_ID='$SHORT_ID'
HY2_ENABLE='$HY2_ENABLE'
HY2_PORT='$HY2_PORT'
HY2_PASS='$HY2_PASS'
SUB_PORT='$SUB_PORT'
SUB_TOKEN='$SUB_TOKEN'
EOF
  chmod 600 "$ENVF"
}

rand_hex()  { openssl rand -hex "${1:-16}"; }
rand_port() { shuf -i 20000-60000 -n1 2>/dev/null || echo $(( RANDOM % 40000 + 20000 )); }

# get_ip <4|6> <url> —— curl / wget 都支持
get_ip() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsS"$1" --max-time 8 "$2" 2>/dev/null | tr -d '[:space:]'
  else
    wget -qO- --timeout=8 --inet"$1" "$2" 2>/dev/null | tr -d '[:space:]'
  fi
}

detect_ip() {
  local ip u
  for u in 'https://api.ipify.org' 'https://ipv4.icanhazip.com' 'https://ifconfig.me/ip'; do
    ip=$(get_ip 4 "$u")
    [ -n "$ip" ] && { SERVER_IP="$ip"; IS_IPV6='false'; return 0; }
  done
  for u in 'https://api64.ipify.org' 'https://ipv6.icanhazip.com'; do
    ip=$(get_ip 6 "$u")
    [ -n "$ip" ] && { SERVER_IP="$ip"; IS_IPV6='true'; return 0; }
  done
  return 1
}

# 节点地址：IPv6 需要方括号
addr() { [ "$IS_IPV6" = 'true' ] && echo "[$SERVER_IP]" || echo "$SERVER_IP"; }

# ============ 内核 ============

install_core() {
  info '获取 sing-box 最新版本号'
  local tag ver
  tag=$(fetch "https://api.github.com/repos/$CORE_REPO/releases/latest" - \
        | grep -m1 '"tag_name"' | cut -d'"' -f4)
  [ -n "$tag" ] || die '无法获取版本号，检查网络或用 -m 指定镜像'
  ver="${tag#v}"
  info "最新版本: $tag  架构: $ARCH"

  local tmp; tmp=$(mktemp -d)
  local pkg="sing-box-${ver}-linux-${ARCH}"
  info '下载内核'
  fetch "https://github.com/$CORE_REPO/releases/download/$tag/${pkg}.tar.gz" "$tmp/sb.tar.gz" \
    || { rm -rf "$tmp"; die '内核下载失败'; }
  tar -xzf "$tmp/sb.tar.gz" -C "$tmp" || { rm -rf "$tmp"; die '解压失败'; }
  install -m 755 "$tmp/$pkg/sing-box" "$SB_BIN" || { rm -rf "$tmp"; die '安装内核失败'; }
  rm -rf "$tmp"
  ok "内核已安装: $($SB_BIN version 2>/dev/null | head -1)"
}

# ============ 密钥与配置 ============

gen_keys() {
  [ -n "$UUID" ]       || UUID=$("$SB_BIN" generate uuid)
  [ -n "$SHORT_ID" ]   || SHORT_ID=$(rand_hex 8)
  if [ -z "$REALITY_PRIV" ] || [ -z "$REALITY_PUB" ]; then
    local kp; kp=$("$SB_BIN" generate reality-keypair)
    REALITY_PRIV=$(echo "$kp" | awk '/PrivateKey/{print $2}')
    REALITY_PUB=$(echo  "$kp" | awk '/PublicKey/{print $2}')
  fi
  [ -n "$REALITY_PRIV" ] && [ -n "$REALITY_PUB" ] || die 'Reality 密钥生成失败'
}

gen_hy2_cert() {
  [ -f "$SB_DIR/hy2.key" ] && [ -f "$SB_DIR/hy2.crt" ] && return 0
  info '生成 Hysteria2 自签证书'
  openssl ecparam -genkey -name prime256v1 -out "$SB_DIR/hy2.key" 2>/dev/null
  openssl req -new -x509 -days 3650 -key "$SB_DIR/hy2.key" -out "$SB_DIR/hy2.crt" \
    -subj "/CN=$SNI" 2>/dev/null
  chmod 600 "$SB_DIR/hy2.key"
}

write_singbox_config() {
  mkdir -p "$SB_DIR"
  local hy2_inbound=''
  if [ "$HY2_ENABLE" = 'true' ]; then
    gen_hy2_cert
    hy2_inbound=$(cat <<EOF
,
    {
      "type": "hysteria2",
      "tag": "hy2-in",
      "listen": "::",
      "listen_port": $HY2_PORT,
      "users": [{ "password": "$HY2_PASS" }],
      "tls": {
        "enabled": true,
        "alpn": ["h3"],
        "certificate_path": "$SB_DIR/hy2.crt",
        "key_path": "$SB_DIR/hy2.key"
      }
    }
EOF
)
  fi

  cat > "$SB_CONF" <<EOF
{
  "log": { "level": "warn", "timestamp": true },
  "inbounds": [
    {
      "type": "vless",
      "tag": "vless-in",
      "listen": "::",
      "listen_port": $VLESS_PORT,
      "users": [{ "uuid": "$UUID", "flow": "xtls-rprx-vision" }],
      "tls": {
        "enabled": true,
        "server_name": "$SNI",
        "reality": {
          "enabled": true,
          "handshake": { "server": "$SNI", "server_port": 443 },
          "private_key": "$REALITY_PRIV",
          "short_id": ["$SHORT_ID"]
        }
      }
    }$hy2_inbound
  ],
  "outbounds": [
    { "type": "direct", "tag": "direct" }
  ]
}
EOF
  chmod 600 "$SB_CONF"
}

# ============ 订阅产出 ============

vless_link() {
  echo "vless://${UUID}@$(addr):${VLESS_PORT}?encryption=none&flow=xtls-rprx-vision&security=reality&sni=${SNI}&fp=chrome&pbk=${REALITY_PUB}&sid=${SHORT_ID}&type=tcp#${NODE_NAME}"
}

hy2_link() {
  echo "hysteria2://${HY2_PASS}@$(addr):${HY2_PORT}/?insecure=1&sni=${SNI}#${NODE_NAME}-hy2"
}

write_proxies_yaml() {
  # Clash proxy-provider 格式：只有节点列表
  cat > "$WWW/$SUB_TOKEN/proxies" <<EOF
proxies:
  - name: "$NODE_NAME"
    type: vless
    server: $SERVER_IP
    port: $VLESS_PORT
    uuid: $UUID
    network: tcp
    udp: true
    tls: true
    flow: xtls-rprx-vision
    servername: $SNI
    client-fingerprint: chrome
    reality-opts:
      public-key: $REALITY_PUB
      short-id: "$SHORT_ID"
EOF
  if [ "$HY2_ENABLE" = 'true' ]; then
    cat >> "$WWW/$SUB_TOKEN/proxies" <<EOF
  - name: "$NODE_NAME-hy2"
    type: hysteria2
    server: $SERVER_IP
    port: $HY2_PORT
    password: "$HY2_PASS"
    sni: $SNI
    skip-cert-verify: true
    udp: true
EOF
  fi
}

write_v2ray_sub() {
  local raw; raw=$(vless_link)
  [ "$HY2_ENABLE" = 'true' ] && raw="$raw
$(hy2_link)"
  printf '%s' "$raw" | base64 | tr -d '\n' > "$WWW/$SUB_TOKEN/v2ray"
}

write_clash_full() {
  # 完整 Clash 配置 = 规则模板 + 指回本机 /proxies 的 provider
  local tpl="$SB_DIR/config.tpl.yaml"
  if [ ! -f "$tpl" ]; then
    info '下载分流规则模板'
    fetch "https://raw.githubusercontent.com/$GH_USER/$GH_REPO/$GH_BRANCH/config.tpl.yaml" "$tpl" \
      || fetch "https://cdn.jsdelivr.net/gh/$GH_USER/$GH_REPO@$GH_BRANCH/config.tpl.yaml" "$tpl" \
      || { warn '规则模板下载失败，/clash 端点将不可用（/proxies 不受影响）'; rm -f "$tpl"; return 1; }
    grep -q '__SUB_URL__' "$tpl" || { warn '模板内容异常，跳过'; rm -f "$tpl"; return 1; }
  fi
  local sub_url="http://$(addr):${SUB_PORT}/${SUB_TOKEN}/proxies"
  sed -e "s|__SUB_URL__|${sub_url}|g" \
      -e "s|__SECRET__|$(rand_hex 16)|g" \
      -e "s|__ALLOW_LAN__|false|g" \
      -e "s|__TUN_ENABLE__|false|g" \
      -e "s|__CTRL_LISTEN__|127.0.0.1:9090|g" \
      "$tpl" > "$WWW/$SUB_TOKEN/clash"
  ok "完整配置已生成（$(sed -n '/^rules:/,$p' "$WWW/$SUB_TOKEN/clash" | grep -c '^- ') 条规则）"
}

build_sub() {
  mkdir -p "$WWW/$SUB_TOKEN"
  # 根目录放空 index，防止列目录把 token 暴露出去
  : > "$WWW/index.html"
  write_proxies_yaml
  write_v2ray_sub
  write_clash_full
  chmod -R 644 "$WWW"/index.html "$WWW/$SUB_TOKEN"/* 2>/dev/null
  chmod 755 "$WWW" "$WWW/$SUB_TOKEN"
  ok '订阅文件已生成'
  return 0
}

# ============ 服务 ============

install_services() {
  cat > "$SB_SERVICE" <<EOF
[Unit]
Description=sing-box service
After=network-online.target nss-lookup.target
Wants=network-online.target

[Service]
Type=simple
User=root
LimitNOFILE=1000000
CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_BIND_SERVICE CAP_NET_RAW
AmbientCapabilities=CAP_NET_ADMIN CAP_NET_BIND_SERVICE CAP_NET_RAW
Restart=always
RestartSec=5
ExecStart=$SB_BIN run -c $SB_CONF

[Install]
WantedBy=multi-user.target
EOF

  local bind='0.0.0.0'
  [ "$IS_IPV6" = 'true' ] && bind='::'
  cat > "$SUB_SERVICE" <<EOF
[Unit]
Description=sing-box subscription http server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
Restart=always
RestartSec=5
WorkingDirectory=$WWW
ExecStart=$(command -v python3) -m http.server $SUB_PORT --bind $bind --directory $WWW

[Install]
WantedBy=multi-user.target
EOF

  systemctl daemon-reload
  systemctl enable sing-box sb-sub >/dev/null 2>&1
  ok '服务已注册 (sing-box / sb-sub)'
}

open_firewall() {
  local ports=("$VLESS_PORT" "$SUB_PORT")
  [ "$HY2_ENABLE" = 'true' ] && ports+=("$HY2_PORT")
  if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q 'Status: active'; then
    for p in "${ports[@]}"; do ufw allow "$p" >/dev/null 2>&1; done
    ok 'ufw 端口已放行'
  elif command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state >/dev/null 2>&1; then
    for p in "${ports[@]}"; do
      firewall-cmd --permanent --add-port="$p/tcp" >/dev/null 2>&1
      firewall-cmd --permanent --add-port="$p/udp" >/dev/null 2>&1
    done
    firewall-cmd --reload >/dev/null 2>&1
    ok 'firewalld 端口已放行'
  fi
}

restart_all() {
  systemctl restart sing-box sb-sub
  sleep 1
  systemctl is-active --quiet sing-box || { err 'sing-box 启动失败，日志如下：'; journalctl -u sing-box -n 20 --no-pager; return 1; }
  systemctl is-active --quiet sb-sub   || { err '订阅服务启动失败，日志如下：'; journalctl -u sb-sub -n 20 --no-pager; return 1; }
  return 0
}

validate() {
  "$SB_BIN" check -c "$SB_CONF" >/dev/null 2>&1 && { ok '服务端配置校验通过'; return 0; }
  err '服务端配置校验未通过：'
  "$SB_BIN" check -c "$SB_CONF" 2>&1 | tail -20
  return 1
}

install_shortcut() {
  mkdir -p "$SB_DIR"
  if [ -f "$0" ] && [ "$0" != "$SELF" ]; then cp -f "$0" "$SELF" 2>/dev/null; fi
  [ -f "$SELF" ] || fetch "https://raw.githubusercontent.com/$GH_USER/$GH_REPO/$GH_BRANCH/vps.sh" "$SELF" 2>/dev/null
  [ -f "$SELF" ] && { chmod 755 "$SELF"; ln -sf "$SELF" "$SHORTCUT"; ok '快捷命令已安装：输入 vps 可重新打开菜单'; }
}

# ============ 展示 ============

show_sub() {
  load_env
  [ -n "$SUB_TOKEN" ] || die '尚未安装，请先执行安装'
  local base="http://$(addr):${SUB_PORT}/${SUB_TOKEN}"
  echo
  cyan '════════════════ 订阅地址 ════════════════'
  echo
  green '  Clash 订阅（只有节点，配合你本地的规则配置用）'
  echo  "    $base/proxies"
  echo
  green '  完整 Clash 配置（规则 + 节点，新机器直接导入）'
  echo  "    $base/clash"
  echo
  green '  通用订阅（v2rayN / Shadowrocket / Nekobox）'
  echo  "    $base/v2ray"
  echo
  cyan '════════════════ 节点链接 ════════════════'
  echo
  echo "  $(vless_link)"
  [ "$HY2_ENABLE" = 'true' ] && { echo; echo "  $(hy2_link)"; }
  echo
  if command -v qrencode >/dev/null 2>&1; then
    cyan '──────────────── 节点二维码 ────────────────'
    qrencode -t ANSIUTF8 "$(vless_link)"
  fi
  cyan '═════════════════════════════════════════'
  warn '订阅地址是明文 HTTP，谁拿到谁就能用你的节点，别外传'
  echo
}

show_status() {
  load_env
  echo
  cyan '════════════ 状态 ════════════'
  if [ -x "$SB_BIN" ]; then
    echo "  内核    : $($SB_BIN version 2>/dev/null | head -1)"
  else
    echo '  内核    : 未安装'; cyan '═════════════════════════════'; echo; return
  fi
  systemctl is-active --quiet sing-box && green '  节点服务: 运行中' || red '  节点服务: 未运行'
  systemctl is-active --quiet sb-sub   && green '  订阅服务: 运行中' || red '  订阅服务: 未运行'
  echo "  服务器IP: ${SERVER_IP:-?}"
  echo "  VLESS   : 端口 ${VLESS_PORT:-?}  SNI ${SNI:-?}"
  [ "$HY2_ENABLE" = 'true' ] && echo "  Hy2     : 端口 ${HY2_PORT}" || echo '  Hy2     : 未启用'
  echo "  订阅端口: ${SUB_PORT:-?}"
  cyan '═════════════════════════════'
  echo
}

# ============ 动作 ============

do_install() {
  check_root; check_systemd; check_deps; detect_arch
  load_env

  info '探测服务器公网 IP'
  if [ -z "$SERVER_IP" ]; then
    detect_ip || die '无法探测公网 IP，请用 -d <IP或域名> 手动指定'
  fi
  ok "服务器地址: $SERVER_IP"

  [ -n "$NODE_NAME" ]  || NODE_NAME="VPS-${SERVER_IP}"
  [ -n "$SNI" ]        || SNI="$DEFAULT_SNI"
  [ -n "$VLESS_PORT" ] || VLESS_PORT=$(rand_port)
  [ -n "$SUB_PORT" ]   || { SUB_PORT=$(rand_port); while [ "$SUB_PORT" = "$VLESS_PORT" ]; do SUB_PORT=$(rand_port); done; }
  [ -n "$SUB_TOKEN" ]  || SUB_TOKEN=$(rand_hex 16)
  [ -n "$HY2_ENABLE" ] || HY2_ENABLE='false'
  [ -n "$HY2_PORT" ]   || { HY2_PORT=$(rand_port); while [ "$HY2_PORT" = "$VLESS_PORT" ] || [ "$HY2_PORT" = "$SUB_PORT" ]; do HY2_PORT=$(rand_port); done; }
  [ -n "$HY2_PASS" ]   || HY2_PASS=$(rand_hex 12)

  install_core
  gen_keys
  save_env
  write_singbox_config
  validate || die '配置有误，已中止'
  build_sub
  install_services
  install_shortcut
  open_firewall
  restart_all || die '服务启动失败'

  echo
  green '部署完成'
  show_sub
  echo '  常用命令：'
  echo '    vps                        打开菜单'
  echo '    systemctl status sing-box  查看节点服务'
  echo '    journalctl -u sing-box -f  实时日志'
  echo
}

regen_and_restart() {
  write_singbox_config
  validate || return 1
  build_sub
  open_firewall
  restart_all
}

do_change_port() {
  check_root; load_env
  echo; cyan "当前 VLESS 端口: $VLESS_PORT   SNI: $SNI"
  echo -n "新端口（回车随机） > "; read -r p
  [ -n "$p" ] || p=$(rand_port)
  case "$p" in ''|*[!0-9]*) die '端口必须是数字' ;; esac
  [ "$p" -ge 1 ] && [ "$p" -le 65535 ] || die '端口超出范围'
  echo -n "新 SNI（回车保持 $SNI） > "; read -r s
  VLESS_PORT="$p"; [ -n "$s" ] && SNI="$s"
  save_env
  regen_and_restart && { ok '已更新'; show_sub; }
}

do_regen_uuid() {
  check_root; load_env
  yellow '将重新生成 UUID 和 Reality 密钥，所有旧客户端都要重新导入订阅'
  echo -n '确认？[y/N] '; read -r a
  case "$a" in y|Y) ;; *) info '已取消'; return ;; esac
  UUID=''; REALITY_PRIV=''; REALITY_PUB=''; SHORT_ID=''
  gen_keys; save_env
  regen_and_restart && { ok '密钥已重置'; show_sub; }
}

do_regen_token() {
  check_root; load_env
  local old="$SUB_TOKEN"
  SUB_TOKEN=$(rand_hex 16); save_env
  [ -n "$old" ] && rm -rf "${WWW:?}/$old"
  build_sub && restart_all && { ok '订阅地址已更换，旧地址立即失效'; show_sub; }
}

do_toggle_hy2() {
  check_root; load_env
  if [ "$HY2_ENABLE" = 'true' ]; then
    HY2_ENABLE='false'; info '关闭 Hysteria2'
  else
    HY2_ENABLE='true'
    [ -n "$HY2_PORT" ] || HY2_PORT=$(rand_port)
    [ -n "$HY2_PASS" ] || HY2_PASS=$(rand_hex 12)
    info "启用 Hysteria2（UDP 端口 $HY2_PORT）"
  fi
  save_env
  regen_and_restart && { ok '已切换'; show_sub; }
}

do_update_core() {
  check_root; detect_arch; load_env
  install_core
  validate && restart_all && ok '内核已更新并重启'
}

do_update_rules() {
  check_root; load_env
  [ -n "$SUB_TOKEN" ] || die '尚未安装'
  rm -f "$SB_DIR/config.tpl.yaml"
  write_clash_full && ok '分流规则已更新（/clash 端点）'
}

do_bbr() {
  check_root
  if sysctl net.ipv4.tcp_congestion_control 2>/dev/null | grep -q bbr; then
    ok 'BBR 已经是开启状态'; return
  fi
  grep -q '^net.core.default_qdisc' /etc/sysctl.conf 2>/dev/null \
    || echo 'net.core.default_qdisc=fq' >> /etc/sysctl.conf
  grep -q '^net.ipv4.tcp_congestion_control' /etc/sysctl.conf 2>/dev/null \
    || echo 'net.ipv4.tcp_congestion_control=bbr' >> /etc/sysctl.conf
  sysctl -p >/dev/null 2>&1
  sysctl net.ipv4.tcp_congestion_control 2>/dev/null | grep -q bbr \
    && ok 'BBR 已开启' || warn '开启失败，内核可能不支持（需 4.9+）'
}

do_uninstall() {
  check_root
  echo; yellow '将卸载 sing-box 和订阅服务'
  echo -n "是否同时删除配置目录 $SB_DIR ？[y/N] "; read -r a
  systemctl stop sing-box sb-sub 2>/dev/null
  systemctl disable sing-box sb-sub 2>/dev/null
  rm -f "$SB_SERVICE" "$SUB_SERVICE" "$SB_BIN" "$SHORTCUT"
  systemctl daemon-reload
  case "$a" in
    y|Y) rm -rf "$SB_DIR"; ok "已删除 $SB_DIR" ;;
    *)   ok "配置保留在 $SB_DIR" ;;
  esac
  ok '卸载完成'
}

show_log() { journalctl -u sing-box -n 50 --no-pager; echo; journalctl -u sb-sub -n 20 --no-pager; }

# ============ 菜单 ============

menu() {
  while true; do
    echo
    cyan "═══════ VPS 一键订阅脚本 v$VERSION ═══════"
    echo '   1) 安装 / 重装'
    echo '   2) 查看订阅地址和节点'
    echo '   3) 更换端口 / SNI'
    echo '   4) 重置 UUID 和 Reality 密钥'
    echo '   5) 更换订阅地址（旧地址作废）'
    echo '   6) 开关 Hysteria2 节点'
    echo '   7) 更新 sing-box 内核'
    echo '   8) 更新分流规则'
    echo '   9) 启动 / 停止 / 重启'
    echo '  10) 查看状态'
    echo '  11) 查看日志'
    echo '  12) 开启 BBR'
    echo '  13) 卸载'
    echo '   0) 退出'
    cyan '══════════════════════════════════════'
    echo -n '请选择 > '
    read -r c
    case "$c" in
      1)  do_install ;;
      2)  show_sub ;;
      3)  do_change_port ;;
      4)  do_regen_uuid ;;
      5)  do_regen_token ;;
      6)  do_toggle_hy2 ;;
      7)  do_update_core ;;
      8)  do_update_rules ;;
      9)  echo -n '1)启动 2)停止 3)重启 > '; read -r s
          case "$s" in
            1) systemctl start sing-box sb-sub   && ok '已启动' ;;
            2) systemctl stop sing-box sb-sub    && ok '已停止' ;;
            3) systemctl restart sing-box sb-sub && ok '已重启' ;;
          esac ;;
      10) show_status ;;
      11) show_log ;;
      12) do_bbr ;;
      13) do_uninstall ;;
      0)  exit 0 ;;
      *)  warn '无效选择' ;;
    esac
  done
}

# ============ 参数 ============

ACTION=''
while [ $# -gt 0 ]; do
  case "$1" in
    -d|--domain)  SERVER_IP="$2"; IS_IPV6='false'; shift 2 ;;
    -p|--port)    VLESS_PORT="$2"; shift 2 ;;
    -s|--sni)     SNI="$2"; shift 2 ;;
    -n|--name)    NODE_NAME="$2"; shift 2 ;;
    -P|--sub-port) SUB_PORT="$2"; shift 2 ;;
    -m|--mirror)  GH_MIRRORS=("$2" "${GH_MIRRORS[@]}"); shift 2 ;;
    -y|--hy2)     HY2_ENABLE='true'; shift ;;
    install|sub|status|log|update|rules|uninstall) ACTION="$1"; shift ;;
    -h|--help)
      cat <<EOF
VPS 一键订阅脚本 v$VERSION

  bash <(wget -qO- https://raw.githubusercontent.com/$GH_USER/$GH_REPO/$GH_BRANCH/vps.sh) [动作] [选项]

动作:
  install     安装（不带动作时进入交互菜单）
  sub         打印订阅地址和节点链接
  status      查看状态
  log         查看日志
  update      更新 sing-box 内核
  rules       更新分流规则
  uninstall   卸载

选项:
  -d, --domain ADDR   手动指定服务器 IP 或域名
  -p, --port N        VLESS 端口（默认随机）
  -P, --sub-port N    订阅服务端口（默认随机）
  -s, --sni HOST      Reality 伪装域名（默认 $DEFAULT_SNI）
  -n, --name NAME     节点名字
  -y, --hy2           同时启用 Hysteria2 节点
  -m, --mirror URL    优先使用的 GitHub 加速前缀

示例（全自动装好）:
  bash <(wget -qO- .../vps.sh) install -y
EOF
      exit 0 ;;
    *) die "未知参数: $1（-h 查看帮助）" ;;
  esac
done

case "$ACTION" in
  install)   do_install ;;
  sub)       show_sub ;;
  status)    show_status ;;
  log)       show_log ;;
  update)    do_update_core ;;
  rules)     do_update_rules ;;
  uninstall) do_uninstall ;;
  '')        menu ;;
esac
