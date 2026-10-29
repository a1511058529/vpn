# vpn

把一台 VPS 一键变成你自己的订阅，一次生成 6 个节点。

| 节点 | 协议 | 说明 |
|---|---|---|
| `-reality` | VLESS + XTLS-Vision + Reality | 最抗封，保底线路 |
| `-shadowtls` | ShadowTLS v3 + SS2022 | 伪装成正常 TLS |
| `-h2-reality` | VLESS + Reality + HTTP 传输 | 带多路复用 |
| `-vmess-ws` | VMess + WebSocket，经 Cloudflare Argo 隧道 | 走 Cloudflare 线路，不暴露端口 |
| `-hysteria2` | Hysteria2（UDP / QUIC） | 丢包多的线路、晚高峰通常最快 |
| `-anytls` | AnyTLS | 较新的 TCP 协议，抗流量特征识别 |

Hysteria2 和 AnyTLS 用自签证书，客户端靠证书 SHA256 指纹锁定，不怕中间人。

服务端还做了几件事：按服务器的 IP 栈设置 DNS 偏好（坏掉的 IPv6 不会拖慢访问）、sing-box 自带 NTP 校时（Reality 对时间敏感）、流量嗅探，以及可选的 **AI 走 WARP**：ChatGPT / Claude / Gemini 等 AI 流量从 Cloudflare WARP 出去，避开机房 IP 被拦。安装时自动向 Cloudflare 注册免费 WARP 账户，注册失败就自动关掉，不影响节点；`vps warp on|off` 随时开关。

客户端判断国内外要用的地理数据（GeoIP / GeoSite）由服务器下载好，经订阅地址的 `geo/` 路径提供，电脑不用去连国内经常打不开的 GitHub。`vps rules` 会同时更新规则和地理数据。

> **装完一定要去云厂商控制台放行端口。** 脚本只能开系统里的 ufw / firewalld，腾讯云、阿里云、甲骨文等的「安全组」默认只放 22/80/443，不放行的话节点全部连不上（只有走 Argo 的 vmess-ws 能用）。需要放行的端口在安装结束和 `vps sub` 时会打印出来：几个 TCP 端口 + Hysteria2 的 UDP 端口。

分流规则在 `config.tpl.yaml`（约 1900 条）：国内直连、国外走代理，海外 AI 服务单独一个 `🤖 AI` 分组（可单独选节点），Cloudflare 上的国内站直连、国外站走 `🌩️ Cloudflare` 分组。没被规则列出的国外网站由最后的「🐟 漏网之鱼」兜底走代理。

装好后还会同时装一个 **Web 运维面板**（`panel/`，Go 编写）：网页上查看状态、更换 SNI / 端口 / 密钥、编辑自定义规则、看日志。

## 用法

在 VPS 上（root）：

```bash
bash <(wget -qO- https://raw.githubusercontent.com/a1511058529/vpn/main/vps.sh)
```

选 `1) 安装`，一两分钟后打印出订阅地址。以后输入 `vps` 再打开菜单。

不进菜单、直接装好：

```bash
bash <(wget -qO- https://raw.githubusercontent.com/a1511058529/vpn/main/vps.sh) install
```

## 装完得到三个地址

| 地址 | 内容 | 给谁用 |
|---|---|---|
| `http://IP:PORT/<token>/proxies` | 6 个节点 | 填进你已有的 Clash 配置当订阅 |
| `http://IP:PORT/<token>/clash` | 规则 + 节点，完整配置 | 新机器直接导入，开箱即用 |
| `http://IP:PORT/<token>/v2ray` | base64 节点链接 | v2rayN / Shadowrocket / Nekobox（不含 ShadowTLS） |

## 换掉旧机场

本地 `config.yaml` 里找到这一行，把地址换成新 VPS 的 `/proxies`：

```yaml
proxy-providers:
  Sub:
    url: "http://你的VPS_IP:端口/token/proxies"
```

规则一个字都不用动。

## 菜单

```
 1) 安装 / 重装                  8) 更新分流规则和地理数据
 2) 查看订阅地址和节点           9) 启动 / 停止 / 重启
 3) 更换 SNI / 随机端口         10) 查看状态
 4) 重置 UUID 和 Reality 密钥   11) 查看日志
 5) 更换订阅地址（旧的作废）    12) 开启 BBR
 6) 刷新 Argo 域名（vmess-ws）  13) 卸载
 7) 更新 sing-box 内核          14) 面板信息 / 安装更新面板
                                15) 重置面板密码
                                16) AI 走 WARP 出口：开 / 关
```

## Web 面板

一键安装时会自动装好。已经装过节点的老机器，执行 `vps panel-install` 补装即可，不用重装节点。

```bash
vps panel          # 显示面板地址
vps panel-install  # 安装或更新面板（重新下载最新版）
vps panel-passwd   # 重置密码（所有已登录的会话立即失效）
```

面板有两个入口，地址里带一段随机路径：

- **HTTP 入口** `http://IP:订阅端口/<随机路径>/`：明文传输。
- **Argo HTTPS 入口** `https://xxx.trycloudflare.com/<随机路径>/`：推荐。

用户名 `admin`，初始密码在安装时显示一次。面板只监听本机，由 nginx 反代，不额外开放端口；所有变更都是调用 `vps.sh` 的动作，面板本身不直接改配置。

页面：**概览**（服务状态、端口、订阅与节点链接）、**运维**（换 SNI、随机端口、重置密钥、换订阅地址、刷新 Argo、重启、更新内核、BBR）、**规则**（自定义规则 + 恢复默认模板）、**日志**。

### 自定义规则

在「规则」页一行写一条，例如 `DOMAIN-SUFFIX,example.com,DIRECT`。它们插在规则的**最前面**，优先级最高，保存前会逐行校验（策略必须是 `DIRECT` / `REJECT` / `REJECT-DROP` 或模板里的策略组名，写错会报行号），失败自动回滚。

> 自定义规则只出现在完整配置 `/clash` 里。如果你的客户端用的是 `/proxies` 加本地规则，改规则不会到客户端；请把 `/clash` 作为客户端的订阅地址，之后在面板里改规则，客户端刷新订阅就生效。

### 面板的注意事项

- **走 HTTP 入口时密码和会话是明文传输的**，建议优先用 Argo HTTPS 入口。
- **入口地址会变**：「随机端口」会改变 HTTP 入口；而「换 SNI / 随机端口 / 重置密钥 / 换订阅地址 / 刷新 Argo / 重启 / 更新内核」都会重启 Argo 隧道，临时域名随之改变，如果你正通过 Argo 地址访问，页面会失去连接。页面在这类操作前会提示；操作后用 HTTP 入口，或在服务器上执行 `vps panel` 查看新地址。
- **「恢复默认模板」会覆盖对模板的手动修改**，自定义规则不受影响。
- 面板和 SSH 菜单同时操作可能互相覆盖，请避免同时使用。
- 登录同一 IP 连续失败 5 次会锁定 15 分钟。

## 命令行参数

```
-d ADDR      手动指定服务器 IP 或域名
-s HOST      Reality / ShadowTLS 伪装域名（默认 addons.mozilla.org）
-n NAME      节点名字
-c HOST      vmess-ws 的 CDN 接入域名（默认 skk.moe）
-m URL       GitHub 加速前缀（墙内机器用）
```

不进菜单、直接执行某个操作（面板也是通过这些动作工作的）：

```
vps set-sni <域名>   vps reroll-ports   vps reset-keys   vps reset-token
vps refresh-argo     vps rebuild        vps restart      vps bbr
vps rules            vps update         vps panel        vps panel-install   vps panel-passwd
vps warp on|off
```

## 关于 vmess-ws 和 Argo

vmess-ws 只监听 VPS 本机，由 nginx 反代，再经 `cloudflared` 的**临时隧道**暴露到 Cloudflare。客户端连的是 CDN 接入域名（默认 `skk.moe:80`），用 Host 头指向隧道域名。

- 临时隧道的域名（`xxx.trycloudflare.com`）**每次隧道重启都会变**。脚本已让 `sb-argo` 服务启动后自动取新域名并刷新订阅，客户端更新一下订阅即可。
- 手动刷新：菜单 `6)`，或 `vps resync`。
- 这条线路依赖 Cloudflare 和 CDN 接入域名可用；它挂了不影响另外 5 个节点。

## 从旧版本升级

老机器重新跑一遍一键安装命令（或 `vps` → `1)`）即可补上新节点。已有的 UUID、密钥、端口和订阅地址都保留，只新增 Hysteria2 / AnyTLS 两个端口（记得去安全组放行）。客户端刷新订阅即可。

## 注意

- **订阅是明文 HTTP**，靠 32 位随机 token 保护，谁拿到链接谁就能用你的节点，别外传。泄露了就跑菜单 `5)` 换一个，旧地址立刻作废。
- 仓库里只有规则模板和脚本，**不包含任何 UUID、密钥、订阅地址**。这些在 VPS 上生成，只存在 `/etc/sing-box/.vps.env`（权限 600）。
- 装在国内 VPS 上没有意义，这是给境外 VPS 用的。
- 默认使用 systemd，脚本会自带一个独立的 nginx 实例（`sb-nginx`），并关闭发行版自带的 nginx 默认服务以免占用 80 端口。

## 文件

```
vps.sh            VPS 一键脚本
config.tpl.yaml   分流规则模板（带占位符，脚本安装时填充）
panel/            Web 面板源码（Go）；推 panel-v* tag 时由 GitHub Actions 编译 5 个架构并发布到 Release
config.yaml       本地用的成品配置（含私人订阅地址，不要提交到公开仓库）
```
