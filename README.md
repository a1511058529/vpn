# vpn

把一台 VPS 一键变成你自己的订阅，一次生成 4 个节点。

| 节点 | 协议 | 说明 |
|---|---|---|
| `-reality` | VLESS + XTLS-Vision + Reality | 最抗封，保底线路 |
| `-shadowtls` | ShadowTLS v3 + SS2022 | 伪装成正常 TLS |
| `-h2-reality` | VLESS + Reality + HTTP 传输 | 带多路复用 |
| `-vmess-ws` | VMess + WebSocket，经 Cloudflare Argo 隧道 | 走 Cloudflare 线路，不暴露端口 |

分流规则在 `config.tpl.yaml`（9815 条）：国内直连、国外走代理、广告拦截。

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
| `http://IP:PORT/<token>/proxies` | 4 个节点 | 填进你已有的 Clash 配置当订阅 |
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
 1) 安装 / 重装                  8) 更新分流规则
 2) 查看订阅地址和节点           9) 启动 / 停止 / 重启
 3) 更换 SNI / 随机端口         10) 查看状态
 4) 重置 UUID 和 Reality 密钥   11) 查看日志
 5) 更换订阅地址（旧的作废）    12) 开启 BBR
 6) 刷新 Argo 域名（vmess-ws）  13) 卸载
 7) 更新 sing-box 内核
```

## 命令行参数

```
-d ADDR      手动指定服务器 IP 或域名
-s HOST      Reality / ShadowTLS 伪装域名（默认 addons.mozilla.org）
-n NAME      节点名字
-c HOST      vmess-ws 的 CDN 接入域名（默认 skk.moe）
-m URL       GitHub 加速前缀（墙内机器用）
```

## 关于 vmess-ws 和 Argo

vmess-ws 只监听 VPS 本机，由 nginx 反代，再经 `cloudflared` 的**临时隧道**暴露到 Cloudflare。客户端连的是 CDN 接入域名（默认 `skk.moe:80`），用 Host 头指向隧道域名。

- 临时隧道的域名（`xxx.trycloudflare.com`）**每次隧道重启都会变**。脚本已让 `sb-argo` 服务启动后自动取新域名并刷新订阅，客户端更新一下订阅即可。
- 手动刷新：菜单 `6)`，或 `vps resync`。
- 这条线路依赖 Cloudflare 和 CDN 接入域名可用；它挂了不影响另外 3 个节点。

## 注意

- **订阅是明文 HTTP**，靠 32 位随机 token 保护，谁拿到链接谁就能用你的节点，别外传。泄露了就跑菜单 `5)` 换一个，旧地址立刻作废。
- 仓库里只有规则模板和脚本，**不包含任何 UUID、密钥、订阅地址**。这些在 VPS 上生成，只存在 `/etc/sing-box/.vps.env`（权限 600）。
- 装在国内 VPS 上没有意义，这是给境外 VPS 用的。
- 广告拦截规则可能误伤个别站点。遇到打不开的，在 Clash 面板把 `🛑 拦截广告` 策略组从 `REJECT` 切到 `DIRECT`。
- 默认使用 systemd，脚本会自带一个独立的 nginx 实例（`sb-nginx`），并关闭发行版自带的 nginx 默认服务以免占用 80 端口。

## 文件

```
vps.sh            VPS 一键脚本
config.tpl.yaml   分流规则模板（带占位符，脚本安装时填充）
config.yaml       本地用的成品配置（含私人订阅地址，不要提交到公开仓库）
```
