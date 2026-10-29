#!/usr/bin/env bash
#
# VPS 一键部署脚本 —— 把 VPS 变成你自己的订阅（一次生成 4 个节点）
#   xtls-reality / ShadowTLS / h2-reality / vmess-ws(Cloudflare Argo)
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

VERSION='2.1.0'
ORIG_ARGS=("$@")

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
NGINX_CONF="$SB_DIR/nginx.conf"
CLOUDFLARED="$SB_DIR/cloudflared"
ARGO_METRICS='127.0.0.1:20241'
NGINX_SERVICE='/etc/systemd/system/sb-nginx.service'
ARGO_SERVICE='/etc/systemd/system/sb-argo.service'
CUSTOM_RULES="$SB_DIR/custom_rules.txt"

CORE_REPO='SagerNet/sing-box'
GH_MIRRORS=('' 'https://ghfast.top/' 'https://gh-proxy.com/' 'https://ghproxy.net/')

DEFAULT_SNI='addons.mozilla.org'
DEFAULT_CDN_HOST='skk.moe'

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

check_root() {
  [ "$(id -u)" = '0' ] && return 0
  command -v sudo >/dev/null 2>&1 || die '需要 root 权限：请切换到 root，或先安装 sudo'
  # 一键命令是 bash <(wget ...)，直接 sudo 会丢失该文件描述符，所以先把脚本落盘再提权重跑
  local tmp; tmp=$(mktemp /tmp/vps.XXXXXX) || die '无法创建临时文件'
  if [ -f "$0" ]; then
    cat "$0" > "$tmp"
  else
    # $0 是管道（bash <(...)），已被读走一部分无法回头读，重新下载一份
    fetch "https://raw.githubusercontent.com/$GH_USER/$GH_REPO/$GH_BRANCH/vps.sh" "$tmp" \
      || { rm -f "$tmp"; die '提权前重新下载脚本失败，请改用：wget -O vps.sh <脚本地址> && sudo bash vps.sh'; }
  fi
  grep -q '^VERSION=' "$tmp" || { rm -f "$tmp"; die '脚本内容异常，已中止'; }
  info '需要 root 权限，使用 sudo 重新执行'
  exec sudo bash -c 'f=$1; shift; bash "$f" "$@"; rc=$?; rm -f "$f"; exit $rc' _ "$tmp" "${ORIG_ARGS[@]}"
}
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
  command -v nginx   >/dev/null 2>&1 || miss+=(nginx)
  if [ ${#miss[@]} -gt 0 ]; then
    info "缺少依赖: ${miss[*]}，尝试自动安装"
    pkg_install "${miss[@]}" || warn '自动安装失败，若后续报错请手动安装'
  fi
  command -v nginx >/dev/null 2>&1 || die '需要 nginx 来托管订阅，请手动安装后重试'
  # 发行版自带的 nginx 默认服务会占 80 端口，我们用自己的 sb-nginx，这里关掉它
  systemctl disable --now nginx >/dev/null 2>&1
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
SNI='$SNI'
UUID='$UUID'
REALITY_PRIV='$REALITY_PRIV'
REALITY_PUB='$REALITY_PUB'
SHORT_ID='$SHORT_ID'
SS_PASS='$SS_PASS'
STLS_PASS='$STLS_PASS'
PORT_REALITY='$PORT_REALITY'
PORT_STLS='$PORT_STLS'
PORT_VMESS='$PORT_VMESS'
PORT_H2='$PORT_H2'
SUB_PORT='$SUB_PORT'
SUB_TOKEN='$SUB_TOKEN'
CDN_HOST='$CDN_HOST'
CDN_PORT='$CDN_PORT'
ARGO_DOMAIN='$ARGO_DOMAIN'
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

# 在已用端口之外随机挑一个
pick_port() {
  local p
  while :; do
    p=$(rand_port)
    case " $USED_PORTS " in *" $p "*) continue ;; esac
    USED_PORTS="$USED_PORTS $p"; echo "$p"; return
  done
}

assign_ports() {
  USED_PORTS="${PANEL_PORT:-}"
  PORT_REALITY=$(pick_port); PORT_STLS=$(pick_port)
  PORT_VMESS=$(pick_port);   PORT_H2=$(pick_port)
  SUB_PORT=$(pick_port)
}

install_core() {
  info '获取 sing-box 最新版本号'
  local tag ver
  local json
  json=$(fetch "https://api.github.com/repos/$CORE_REPO/releases/latest" -)
  tag=$(printf '%s\n' "$json" | grep -m1 '"tag_name"' | cut -d'"' -f4)
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


install_cloudflared() {
  [ -x "$CLOUDFLARED" ] && return 0
  mkdir -p "$SB_DIR"
  local arch
  case "$ARCH" in
    amd64) arch=amd64 ;; arm64) arch=arm64 ;; armv7) arch=arm ;; 386) arch=386 ;;
    *) die "cloudflared 不支持的架构: $ARCH" ;;
  esac
  info '下载 cloudflared（Argo 隧道）'
  fetch "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-$arch" "$CLOUDFLARED" \
    || die 'cloudflared 下载失败'
  chmod 755 "$CLOUDFLARED"
  ok 'cloudflared 已安装'
}

# ============ 密钥与配置 ============

gen_keys() {
  [ -n "$UUID" ]      || UUID=$("$SB_BIN" generate uuid)
  [ -n "$SHORT_ID" ]  || SHORT_ID=$(rand_hex 8)
  [ -n "$SS_PASS" ]   || SS_PASS=$(openssl rand -base64 16)
  [ -n "$STLS_PASS" ] || STLS_PASS=$(rand_hex 12)
  if [ -z "$REALITY_PRIV" ] || [ -z "$REALITY_PUB" ]; then
    local kp; kp=$("$SB_BIN" generate reality-keypair)
    REALITY_PRIV=$(echo "$kp" | awk '/PrivateKey/{print $2}')
    REALITY_PUB=$(echo  "$kp" | awk '/PublicKey/{print $2}')
  fi
  [ -n "$REALITY_PRIV" ] && [ -n "$REALITY_PUB" ] || die 'Reality 密钥生成失败'
}

# 四个入站：xtls-reality / ShadowTLS / vmess-ws(仅本机，经 nginx+Argo) / h2-reality
write_singbox_config() {
  mkdir -p "$SB_DIR"
  cat > "$SB_CONF" <<EOF
{
  "log": { "level": "warn", "timestamp": true },
  "inbounds": [
    {
      "type": "vless",
      "tag": "xtls-reality",
      "listen": "::",
      "listen_port": $PORT_REALITY,
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
    },
    {
      "type": "shadowtls",
      "tag": "shadowtls",
      "listen": "::",
      "listen_port": $PORT_STLS,
      "detour": "shadowtls-in",
      "version": 3,
      "users": [{ "password": "$STLS_PASS" }],
      "handshake": { "server": "$SNI", "server_port": 443 },
      "strict_mode": true
    },
    {
      "type": "shadowsocks",
      "tag": "shadowtls-in",
      "listen": "127.0.0.1",
      "network": "tcp",
      "method": "2022-blake3-aes-128-gcm",
      "password": "$SS_PASS",
      "multiplex": { "enabled": true, "padding": true }
    },
    {
      "type": "vmess",
      "tag": "vmess-ws",
      "listen": "127.0.0.1",
      "listen_port": $PORT_VMESS,
      "users": [{ "uuid": "$UUID", "alterId": 0 }],
      "transport": {
        "type": "ws",
        "path": "/$UUID-vmess",
        "max_early_data": 2560,
        "early_data_header_name": "Sec-WebSocket-Protocol"
      },
      "multiplex": { "enabled": true, "padding": true }
    },
    {
      "type": "vless",
      "tag": "h2-reality",
      "listen": "::",
      "listen_port": $PORT_H2,
      "users": [{ "uuid": "$UUID" }],
      "tls": {
        "enabled": true,
        "server_name": "$SNI",
        "reality": {
          "enabled": true,
          "handshake": { "server": "$SNI", "server_port": 443 },
          "private_key": "$REALITY_PRIV",
          "short_id": ["$SHORT_ID"]
        }
      },
      "transport": { "type": "http" },
      "multiplex": { "enabled": true, "padding": true }
    }
  ],
  "outbounds": [
    { "type": "direct", "tag": "direct" }
  ]
}
EOF
  chmod 600 "$SB_CONF"
}

# nginx：一边托管订阅，一边把 /<uuid>-vmess 的 websocket 反代给 sing-box，Argo 隧道指向它
write_nginx_conf() {
  local v6=''
  [ -f /proc/net/if_inet6 ] && v6="listen [::]:$SUB_PORT;"
  cat > "$NGINX_CONF" <<EOF
user root;
worker_processes 1;
error_log /dev/null;
pid /run/sb-nginx.pid;
events { worker_connections 1024; }
http {
  default_type text/plain;
  access_log off;
  sendfile on;
  server {
    listen $SUB_PORT;
    $v6
    server_name _;
    root $WWW;
    location /$SUB_TOKEN/ { autoindex off; }
    location /$UUID-vmess {
      if (\$http_upgrade != "websocket") { return 404; }
      proxy_pass http://127.0.0.1:$PORT_VMESS;
      proxy_http_version 1.1;
      proxy_set_header Upgrade \$http_upgrade;
      proxy_set_header Connection "upgrade";
      proxy_set_header X-Real-IP \$remote_addr;
      proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
      proxy_set_header Host \$host;
      proxy_redirect off;
    }
    location / { return 404; }
  }
}
EOF
}

# ============ 订阅产出 ============

reality_link() {
  echo "vless://${UUID}@$(addr):${PORT_REALITY}?encryption=none&flow=xtls-rprx-vision&security=reality&sni=${SNI}&fp=chrome&pbk=${REALITY_PUB}&sid=${SHORT_ID}&type=tcp#${NODE_NAME}-reality"
}

h2_link() {
  echo "vless://${UUID}@$(addr):${PORT_H2}?encryption=none&security=reality&sni=${SNI}&fp=chrome&pbk=${REALITY_PUB}&sid=${SHORT_ID}&type=http#${NODE_NAME}-h2-reality"
}

vmess_link() {
  local j
  j=$(printf '{"v":"2","ps":"%s-vmess-ws","add":"%s","port":"%s","id":"%s","aid":"0","scy":"auto","net":"ws","type":"none","host":"%s","path":"/%s-vmess","tls":"","sni":"","alpn":"","fp":""}' \
      "$NODE_NAME" "$CDN_HOST" "$CDN_PORT" "$UUID" "$ARGO_DOMAIN" "$UUID")
  echo "vmess://$(printf '%s' "$j" | base64 | tr -d '\n')"
}

SMUX="smux: { enabled: true, protocol: 'h2mux', padding: true, max-connections: '8', min-streams: '16', statistic: true, only-tcp: false }"

write_proxies_yaml() {
  local f="$WWW/$SUB_TOKEN/proxies" a; a=$(addr)
  {
    echo 'proxies:'
    echo "  - {name: \"$NODE_NAME-reality\", type: vless, server: $a, port: $PORT_REALITY, uuid: $UUID, network: tcp, udp: true, tls: true, flow: xtls-rprx-vision, servername: $SNI, client-fingerprint: chrome, reality-opts: {public-key: $REALITY_PUB, short-id: \"$SHORT_ID\"}}"
    echo "  - {name: \"$NODE_NAME-shadowtls\", type: ss, server: $a, port: $PORT_STLS, cipher: 2022-blake3-aes-128-gcm, password: \"$SS_PASS\", plugin: shadow-tls, client-fingerprint: chrome, plugin-opts: {host: $SNI, password: \"$STLS_PASS\", version: 3}, $SMUX}"
    echo "  - {name: \"$NODE_NAME-h2-reality\", type: vless, server: $a, port: $PORT_H2, uuid: $UUID, network: http, tls: true, servername: $SNI, client-fingerprint: chrome, reality-opts: {public-key: $REALITY_PUB, short-id: \"$SHORT_ID\"}, $SMUX}"
    if [ -n "$ARGO_DOMAIN" ]; then
      echo "  - {name: \"$NODE_NAME-vmess-ws\", type: vmess, server: $CDN_HOST, port: $CDN_PORT, uuid: $UUID, udp: true, tls: false, alterId: 0, cipher: auto, network: ws, ws-opts: { path: \"/$UUID-vmess\", headers: {Host: $ARGO_DOMAIN} }, $SMUX}"
    fi
  } > "$f"
}

write_v2ray_sub() {
  # ShadowTLS 没有通用链接格式，/v2ray 里不含它，请用 /proxies 或 /clash
  local raw; raw="$(reality_link)
$(h2_link)"
  [ -n "$ARGO_DOMAIN" ] && raw="$raw
$(vmess_link)"
  printf '%s' "$raw" | base64 | tr -d '\n' > "$WWW/$SUB_TOKEN/v2ray"
}

write_clash_full() {
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
  local out="$WWW/$SUB_TOKEN/clash" nc=0
  if [ -s "$CUSTOM_RULES" ]; then
    awk -v f="$CUSTOM_RULES" '
      { print }
      /^rules:/ && !done {
        while ((getline l < f) > 0) {
          sub(/\r$/, "", l); sub(/^[[:space:]]+/, "", l)
          if (l ~ /^(#|$)/) continue
          print "- " l
        }
        close(f); done = 1
      }' "$out" > "$out.tmp" && mv -f "$out.tmp" "$out"
    nc=$(grep -cvE '^[[:space:]]*(#|$)' "$CUSTOM_RULES")
  fi
  ok "完整配置已生成（$(sed -n '/^rules:/,$p' "$out" | grep -c '^- ') 条规则，其中自定义 $nc 条）"
}

build_sub() {
  mkdir -p "$WWW/$SUB_TOKEN"
  : > "$WWW/index.html"
  write_proxies_yaml
  write_v2ray_sub
  write_clash_full
  chmod -R 644 "$WWW"/index.html "$WWW/$SUB_TOKEN"/* 2>/dev/null
  chmod 755 "$WWW" "$WWW/$SUB_TOKEN"
  ok '订阅文件已生成'
  return 0
}

# ============ Argo 隧道 ============

# 从 cloudflared 的 metrics 接口取临时域名，最多等 ~60 秒
fetch_argo_domain() {
  local i d
  for i in $(seq 1 30); do
    d=$(fetch "http://$ARGO_METRICS/quicktunnel" - 2>/dev/null | awk -F '"' '{print $4}')
    case "$d" in *.trycloudflare.com) ARGO_DOMAIN="$d"; return 0 ;; esac
    sleep 2
  done
  return 1
}

# 隧道重启后域名会变：重新取域名并刷新订阅（由 sb-argo 服务启动后自动调用）
do_resync() {
  load_env
  [ -n "$SUB_TOKEN" ] || return 0
  local old="$ARGO_DOMAIN"
  if fetch_argo_domain; then
    if [ "$ARGO_DOMAIN" != "$old" ]; then
      save_env; build_sub >/dev/null
      ok "Argo 域名已更新: $ARGO_DOMAIN"
    else
      ok "Argo 域名未变: $ARGO_DOMAIN"
    fi
  else
    warn '未能获取 Argo 域名，vmess-ws 暂不可用'
    return 1
  fi
}

# ============ 服务 ============

# 隧道的转发端口（SUB_PORT）写死在 unit 里，改了端口必须重写这个文件
write_argo_service() {
  cat > "$ARGO_SERVICE" <<EOF
[Unit]
Description=Cloudflare quick tunnel (Argo) for vmess-ws
After=network-online.target sb-nginx.service
Wants=network-online.target

[Service]
Type=simple
User=root
TimeoutStartSec=120
Restart=always
RestartSec=5
ExecStart=$CLOUDFLARED tunnel --edge-ip-version auto --protocol http2 --no-autoupdate --metrics $ARGO_METRICS --url http://localhost:$SUB_PORT
ExecStartPost=-$SELF resync

[Install]
WantedBy=multi-user.target
EOF
}

install_services() {
  local nginx_bin; nginx_bin=$(command -v nginx)
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

  cat > "$NGINX_SERVICE" <<EOF
[Unit]
Description=sing-box subscription + ws reverse proxy (nginx)
After=network-online.target

[Service]
Type=simple
User=root
ExecStartPre=$nginx_bin -t -c $NGINX_CONF
ExecStart=$nginx_bin -c $NGINX_CONF -g 'daemon off;'
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

  write_argo_service

  systemctl daemon-reload
  systemctl enable sing-box sb-nginx sb-argo >/dev/null 2>&1
  ok '服务已注册 (sing-box / sb-nginx / sb-argo)'
}

open_firewall() {
  local ports=("$PORT_REALITY" "$PORT_STLS" "$PORT_H2" "$SUB_PORT")
  if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q 'Status: active'; then
    for p in "${ports[@]}"; do ufw allow "$p" >/dev/null 2>&1; done
    ok 'ufw 端口已放行'
  elif command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state >/dev/null 2>&1; then
    for p in "${ports[@]}"; do firewall-cmd --permanent --add-port="$p/tcp" >/dev/null 2>&1; done
    firewall-cmd --reload >/dev/null 2>&1
    ok 'firewalld 端口已放行'
  fi
}

restart_all() {
  systemctl restart sing-box sb-nginx
  sleep 1
  systemctl is-active --quiet sing-box || { err 'sing-box 启动失败，日志如下：'; journalctl -u sing-box -n 20 --no-pager; return 1; }
  systemctl is-active --quiet sb-nginx || { err 'nginx 启动失败，日志如下：'; journalctl -u sb-nginx -n 20 --no-pager; return 1; }
  systemctl restart sb-argo
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
  green '  通用订阅（v2rayN / Shadowrocket / Nekobox，不含 ShadowTLS）'
  echo  "    $base/v2ray"
  echo
  cyan '════════════════ 节点链接 ════════════════'
  echo
  echo "  $(reality_link)"
  echo
  echo "  $(h2_link)"
  [ -n "$ARGO_DOMAIN" ] && { echo; echo "  $(vmess_link)"; }
  echo
  if [ -z "$NO_QR" ] && command -v qrencode >/dev/null 2>&1; then
    cyan '──────────── xtls-reality 二维码 ────────────'
    qrencode -t ANSIUTF8 "$(reality_link)"
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
  local s
  for s in sing-box sb-nginx sb-argo; do
    systemctl is-active --quiet "$s" && green "  $s: 运行中" || red "  $s: 未运行"
  done
  echo "  服务器IP: ${SERVER_IP:-?}"
  echo "  reality : ${PORT_REALITY:-?}    shadowtls: ${PORT_STLS:-?}    h2-reality: ${PORT_H2:-?}"
  echo "  vmess-ws: ${ARGO_DOMAIN:-（尚无 Argo 域名）}"
  echo "  订阅端口: ${SUB_PORT:-?}    伪装域名: ${SNI:-?}"
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

  [ -n "$NODE_NAME" ] || NODE_NAME="VPS-${SERVER_IP}"
  [ -n "$SNI" ]       || SNI="$DEFAULT_SNI"
  [ -n "$CDN_HOST" ]  || CDN_HOST="$DEFAULT_CDN_HOST"
  [ -n "$CDN_PORT" ]  || CDN_PORT='80'
  [ -n "$PORT_REALITY" ] || assign_ports
  [ -n "$SUB_TOKEN" ] || SUB_TOKEN=$(rand_hex 16)

  install_core
  install_cloudflared
  gen_keys
  save_env
  write_singbox_config
  validate || die '配置有误，已中止'
  write_nginx_conf
  build_sub
  install_services
  install_shortcut
  open_firewall
  restart_all || die '服务启动失败'

  info '等待 Argo 隧道分配临时域名（最多约 60 秒）'
  if fetch_argo_domain; then
    ok "Argo 域名: $ARGO_DOMAIN"
    save_env; build_sub
  else
    warn 'Argo 域名获取失败，vmess-ws 节点暂缺；稍后菜单里选「刷新 Argo 域名」'
  fi

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
  write_nginx_conf
  build_sub
  open_firewall
  restart_all
}

valid_host() {
  local h="$1"
  [ "${#h}" -le 253 ] && printf '%s' "$h" | grep -qE '^([A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.)+[A-Za-z]{2,63}$'
}

do_set_sni() {
  local host="$1"
  check_root
  valid_host "$host" || die "SNI 不是合法域名: ${host:-（空）}"
  load_env   # load_env 会覆盖变量，所以必须在它之后再赋值
  [ -n "$SUB_TOKEN" ] || die '尚未安装'
  SNI="$host"; save_env
  regen_and_restart && ok "SNI 已更新为 $SNI"
}

do_reroll_ports() {
  check_root; load_env
  [ -n "$SUB_TOKEN" ] || die '尚未安装'
  assign_ports; save_env
  write_argo_service; systemctl daemon-reload   # 隧道要指向新的订阅端口
  regen_and_restart && ok "端口已重新随机，新的订阅端口: $SUB_PORT"
}

do_change_port() {
  check_root; load_env
  echo; cyan "当前 SNI: $SNI"
  echo -n "新 SNI（回车保持） > "; read -r s
  echo -n '是否重新随机所有端口？[y/N] '; read -r a
  [ -n "$s" ] && { do_set_sni "$s" || return; }
  case "$a" in y|Y) do_reroll_ports || return ;; esac
  show_sub
}

do_reset_keys() {
  check_root; load_env
  [ -n "$SUB_TOKEN" ] || die '尚未安装'
  UUID=''; REALITY_PRIV=''; REALITY_PUB=''; SHORT_ID=''; SS_PASS=''; STLS_PASS=''
  gen_keys; save_env
  regen_and_restart && { fetch_argo_domain && save_env && build_sub >/dev/null; ok '密钥已重置'; }
}

do_regen_uuid() {
  yellow '将重新生成 UUID、Reality 密钥和各类密码，所有旧客户端都要重新导入订阅'
  echo -n '确认？[y/N] '; read -r a
  case "$a" in y|Y) ;; *) info '已取消'; return ;; esac
  do_reset_keys && show_sub
}

do_rebuild() {
  check_root; load_env
  [ -n "$SUB_TOKEN" ] || die '尚未安装'
  [ -f "$SB_DIR/config.tpl.yaml" ] || die '规则模板不存在，请先执行: vps rules'
  build_sub
}

do_restart() {
  check_root
  restart_all && ok '已重启 sing-box / sb-nginx / sb-argo'
}

do_regen_token() {
  check_root; load_env
  local old="$SUB_TOKEN"
  SUB_TOKEN=$(rand_hex 16); save_env
  [ -n "$old" ] && rm -rf "${WWW:?}/$old"
  write_nginx_conf; build_sub && systemctl restart sb-nginx sb-argo && { ok '订阅地址已更换，旧地址立即失效'; show_sub; }
}

do_refresh_argo() {
  check_root; load_env
  systemctl restart sb-argo
  do_resync && show_sub
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
  echo; yellow '将卸载 sing-box、nginx 订阅服务和 Argo 隧道'
  echo -n "是否同时删除配置目录 $SB_DIR ？[y/N] "; read -r a
  systemctl stop sing-box sb-nginx sb-argo 2>/dev/null
  systemctl disable sing-box sb-nginx sb-argo 2>/dev/null
  rm -f "$SB_SERVICE" "$NGINX_SERVICE" "$ARGO_SERVICE" "$SB_BIN" "$SHORTCUT"
  systemctl daemon-reload
  case "$a" in
    y|Y) rm -rf "$SB_DIR"; ok "已删除 $SB_DIR" ;;
    *)   ok "配置保留在 $SB_DIR" ;;
  esac
  ok '卸载完成'
}

show_log() {
  journalctl -u sing-box -n 40 --no-pager; echo
  journalctl -u sb-nginx -n 10 --no-pager; echo
  journalctl -u sb-argo  -n 15 --no-pager
}

# ============ 菜单 ============

menu() {
  while true; do
    echo
    cyan "═══════ VPS 一键订阅脚本 v$VERSION ═══════"
    echo '   1) 安装 / 重装'
    echo '   2) 查看订阅地址和节点'
    echo '   3) 更换 SNI / 随机端口'
    echo '   4) 重置 UUID 和 Reality 密钥'
    echo '   5) 更换订阅地址（旧地址作废）'
    echo '   6) 刷新 Argo 域名（vmess-ws）'
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
      6)  do_refresh_argo ;;
      7)  do_update_core ;;
      8)  do_update_rules ;;
      9)  echo -n '1)启动 2)停止 3)重启 > '; read -r s
          case "$s" in
            1) systemctl start sing-box sb-nginx sb-argo   && ok '已启动' ;;
            2) systemctl stop sing-box sb-nginx sb-argo    && ok '已停止' ;;
            3) systemctl restart sing-box sb-nginx sb-argo && ok '已重启' ;;
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

ACTION=''; ACTION_ARG=''
while [ $# -gt 0 ]; do
  case "$1" in
    -d|--domain)  SERVER_IP="$2"; IS_IPV6='false'; shift 2 ;;
    -s|--sni)     SNI="$2"; shift 2 ;;
    -n|--name)    NODE_NAME="$2"; shift 2 ;;
    -c|--cdn)     CDN_HOST="$2"; shift 2 ;;
    -m|--mirror)  GH_MIRRORS=("$2" "${GH_MIRRORS[@]}"); shift 2 ;;
    set-sni)      ACTION=set-sni; ACTION_ARG="${2:-}"; shift; [ $# -gt 0 ] && shift ;;
    install|sub|status|log|update|rules|resync|uninstall|reroll-ports|reset-keys|reset-token|refresh-argo|rebuild|restart|bbr) ACTION="$1"; shift ;;
    -h|--help)
      cat <<EOF
VPS 一键订阅脚本 v$VERSION —— 一次生成 4 个节点：
  xtls-reality / ShadowTLS / h2-reality / vmess-ws(Argo)

  bash <(wget -qO- https://raw.githubusercontent.com/$GH_USER/$GH_REPO/$GH_BRANCH/vps.sh) [动作] [选项]

动作:
  install     安装（不带动作时进入交互菜单）
  sub         打印订阅地址和节点链接
  status      查看状态
  log         查看日志
  update      更新 sing-box 内核
  rules       更新分流规则
  resync      刷新 Argo 域名并重新生成订阅
  set-sni H   更换 SNI 为 H 并重新生成
  reroll-ports  重新随机所有端口（订阅地址会变）
  reset-keys  重置 UUID / 密钥 / 密码（旧客户端需重新导入）
  reset-token 更换订阅地址 token
  refresh-argo  重启 Argo 隧道并刷新域名
  rebuild     只重新生成订阅文件（改自定义规则后用）
  restart     重启 sing-box / sb-nginx / sb-argo
  bbr         开启 BBR
  uninstall   卸载

选项:
  -d, --domain ADDR   手动指定服务器 IP 或域名
  -s, --sni HOST      Reality / ShadowTLS 伪装域名（默认 $DEFAULT_SNI）
  -n, --name NAME     节点名字
  -c, --cdn HOST      vmess-ws 走的 CDN 接入域名（默认 $DEFAULT_CDN_HOST）
  -m, --mirror URL    优先使用的 GitHub 加速前缀
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
  resync)    do_resync ;;
  uninstall) do_uninstall ;;
  set-sni)       do_set_sni "$ACTION_ARG" ;;
  reroll-ports)  do_reroll_ports ;;
  reset-keys)    do_reset_keys ;;
  reset-token)   do_regen_token ;;
  refresh-argo)  do_refresh_argo ;;
  rebuild)       do_rebuild ;;
  restart)       do_restart ;;
  bbr)           do_bbr ;;
  '')        menu ;;
esac
