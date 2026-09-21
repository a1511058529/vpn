# vpn

把一台 VPS 一键变成你自己的订阅。

- **服务端**：sing-box + VLESS-Vision-Reality（不需要域名和证书），可选 Hysteria2
- **订阅**：VPS 自己托管，吐出 Clash / 通用两种格式
- **分流规则**：`config.tpl.yaml`，9815 条规则，国内直连、国外走代理、广告拦截

## 用法

在 VPS 上（root）：

```bash
bash <(wget -qO- https://raw.githubusercontent.com/a1511058529/vpn/main/vps.sh)
```

选 `1) 安装`，一分钟后打印出订阅地址。以后输入 `vps` 再打开菜单。

全自动安装（连 Hysteria2 一起）：

```bash
bash <(wget -qO- https://raw.githubusercontent.com/a1511058529/vpn/main/vps.sh) install -y
```

## 装完得到三个地址

| 地址 | 内容 | 给谁用 |
|---|---|---|
| `http://IP:PORT/<token>/proxies` | 只有节点列表 | 填进你已有的 Clash 配置当订阅 |
| `http://IP:PORT/<token>/clash` | 规则 + 节点，完整配置 | 新机器直接导入，开箱即用 |
| `http://IP:PORT/<token>/v2ray` | base64 节点链接 | v2rayN / Shadowrocket / Nekobox |

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
 1) 安装 / 重装                 8) 更新分流规则
 2) 查看订阅地址和节点          9) 启动 / 停止 / 重启
 3) 更换端口 / SNI             10) 查看状态
 4) 重置 UUID 和 Reality 密钥  11) 查看日志
 5) 更换订阅地址（旧的作废）   12) 开启 BBR
 6) 开关 Hysteria2 节点        13) 卸载
 7) 更新 sing-box 内核
```

## 命令行参数

```
-d ADDR      手动指定服务器 IP 或域名
-p N         VLESS 端口（默认随机）
-P N         订阅服务端口（默认随机）
-s HOST      Reality 伪装域名（默认 www.apple.com）
-n NAME      节点名字
-y           同时启用 Hysteria2
-m URL       GitHub 加速前缀（墙内机器用）
```

## 注意

- **订阅是明文 HTTP**，靠 32 位随机 token 保护，谁拿到链接谁就能用你的节点，别外传。泄露了就跑菜单 `5)` 换一个，旧地址立刻作废。
- 仓库里只有规则模板和脚本，**不包含任何 UUID、密钥、订阅地址**。这些在 VPS 上生成，只存在 `/etc/sing-box/.vps.env`（权限 600）。
- 装在国内 VPS 上没有意义，这是给境外 VPS 用的。
- `config.tpl.yaml` 的规则来自开源规则集整理，广告拦截里可能误伤个别站点。真遇到打不开的，在 Clash 面板把 `🛑 拦截广告` 策略组从 `REJECT` 切到 `DIRECT`。

## 文件

```
vps.sh            VPS 一键脚本
config.tpl.yaml   分流规则模板（带占位符，脚本安装时填充）
config.yaml       本地用的成品配置（含私人订阅地址，不要提交到公开仓库）
```
