# PortFlow 部署与运维

本文档面向小规模内部部署：一台控制面主机和多台 Linux Agent 节点。控制面使用 Docker Compose，Agent 使用 systemd。

## 1. 部署前检查

控制面主机需要：

- Docker Engine 和 Docker Compose；使用中文管理脚本时，Debian/Ubuntu 可在确认后从 Docker 官方仓库安装；
- 指向该主机的真实 DNS 域名；
- 可用的 80/TCP、443/TCP 和 443/UDP 主机端口；
- 至少 1 GiB 可用内存和可持久磁盘空间。

Agent 节点需要：

- Linux AMD64 或 ARM64；
- systemd；
- 能通过 HTTPS 主动访问控制面；
- 转发规则所需监听端口未被其他进程占用。

Compose 和 Agent 本身不会修改防火墙。中文 `install.sh` 可以在目标机经明确确认后，为 Agent 入口监听端口添加可恢复的 UFW/firewalld 规则；它不会自动启用防火墙，也不会修改无法安全识别的 nftables/iptables 自定义规则集。任何修改都会记录，恢复时只删除 PortFlow 添加的规则。

Docker 发布的控制面端口可能绕过 UFW/firewalld 普通规则，控制面还必须核对 Compose 绑定、云安全组和上游网络。Agent 直接监听的公网转发端口则需要在对应入口节点放行；内部 WireGuard 中转端口不应作为公网端口开放。

启动前先检查现有监听：

```bash
ss -ltnup
docker ps
```

## 2. 准备控制面配置

复制示例文件：

```bash
cp .env.production.example .env.production
chmod 600 .env.production
```

编辑 `.env.production`：

- `POSTGRES_PASSWORD`：至少 32 位随机值，建议仅使用十六进制字符，避免 URL 转义问题；
- `PORTFLOW_VERSION`：当前发布版本；
- `PORTFLOW_SITE_ADDRESS`：例如 `panel.example.com`；
- `CADDY_EMAIL`：用于 HTTPS 证书通知；
- `PORTFLOW_HTTP_BIND` 和 `PORTFLOW_HTTPS_BIND`：准备发布的主机端口。

密码不应提交到版本库或发送到聊天记录。可在已有 OpenSSL 的机器上生成十六进制值：

```bash
openssl rand -hex 32
```

先仅渲染和检查 Compose，该命令不会启动容器：

```bash
docker compose --env-file .env.production config --quiet
```

可重复的 PostgreSQL 真实服务集成测试见 [TESTING.md](./TESTING.md)。测试 profile 只使用 Compose 内部网络，不绑定主机端口。

## 3. 启动控制面

```bash
docker compose --env-file .env.production build
docker compose --env-file .env.production up -d
docker compose --env-file .env.production ps
```

组件边界：

- PostgreSQL 只加入容器内部网络，不绑定主机端口；
- Go 控制面只向 Caddy 暴露 8080，不直接发布到主机；
- Caddy 提供 Web UI、API 反向代理和 HTTPS；
- 数据库、Caddy 证书均使用命名卷持久化；
- 容器日志默认每个文件 10 MiB，保留 5 份。

验证：

```bash
docker compose --env-file .env.production ps
docker compose --env-file .env.production exec control \
  /portflow-control -healthcheck http://127.0.0.1:8080/api/v1/health
curl --fail --show-error https://panel.example.com/api/v1/health
```

首次打开 Web UI 后，使用“首次部署”创建管理员。

## 4. 安装 Agent

在开发机或构建机上构建指定版本：

```bash
make build VERSION=1.0.1
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -buildvcs=false -trimpath \
  -ldflags "-s -w -X main.agentVersion=1.0.1" \
  -o bin/portflow-agent-linux-arm64 ./cmd/agent
```

将与节点架构匹配的 Agent、`deploy/agent/install.sh` 和 systemd unit 复制到节点，先校验文件来源和摘要，再执行：

```bash
sudo ./deploy/agent/install.sh ./bin/portflow-agent
```

安装脚本只会：

- 创建无登录 shell 的 `portflow-agent` 系统用户；
- 安装 `/usr/local/bin/portflow-agent`；
- 创建权限为 `0700` 的 `/var/lib/portflow-agent`；
- 安装但不启动 systemd unit。

它不会修改防火墙。

## 5. 安全注册 Agent

在 Web UI 生成一次性令牌。为避免令牌出现在进程参数和 shell 历史中，将它写入只有 Agent 用户可读的临时文件：

```bash
sudo install -m 0600 -o portflow-agent -g portflow-agent /dev/null \
  /var/lib/portflow-agent/enrollment-token
sudo sh -c 'trap "stty echo" EXIT; stty -echo; read -r TOKEN; printf "%s\n" "$TOKEN" > /var/lib/portflow-agent/enrollment-token; unset TOKEN'
printf '\n'
```

第二条命令会等待在终端隐藏输入令牌。输入后执行一次性注册：

```bash
sudo -u portflow-agent /usr/local/bin/portflow-agent \
  -config /var/lib/portflow-agent/config.json \
  -control-url https://panel.example.com \
  -enrollment-token-file /var/lib/portflow-agent/enrollment-token \
  -enroll-only \
  -name 'Shanghai Edge 01' \
  -region 'Shanghai'
sudo rm -f /var/lib/portflow-agent/enrollment-token
sudo systemctl enable --now portflow-agent
sudo systemctl status portflow-agent --no-pager
```

Agent 配置保存为 `0600`，systemd 以非 root 用户运行 Agent，仅保留绑定 1024 以下端口所需的 `CAP_NET_BIND_SERVICE`。

### 记录已有 WireGuard 地址

PortFlow 不会自行创建 WireGuard 接口。确认专用接口已经存在且两端能够通过私网地址互通后，可在维护窗口停止 Agent，将该节点的既有 WireGuard IPv4 地址写入本地配置，再重新启动：

```bash
sudo systemctl stop portflow-agent
sudo -u portflow-agent /usr/local/bin/portflow-agent \
  -config /var/lib/portflow-agent/config.json \
  -tunnel-address 10.203.0.1 \
  -configure-only
sudo systemctl start portflow-agent
```

Agent 下一次心跳会把地址报告给控制面。这里的命令只更新 PortFlow 自身的 `0600` 配置文件，不创建接口、不修改路由或防火墙。入口和出口都上报地址后，管理员才能启用中转规则。

## 6. 日常检查

```bash
docker compose --env-file .env.production ps
docker compose --env-file .env.production logs --since 30m control
sudo systemctl is-active portflow-agent
sudo journalctl -u portflow-agent --since '30 minutes ago' --no-pager
```

如果新配置失败，Agent 会继续使用上一份有效配置，并在面板上显示具体错误。

## 7. PostgreSQL 备份与恢复

备份不会停止数据面：

```bash
install -d -m 0700 backups
docker compose --env-file .env.production exec -T postgres \
  pg_dump -U portflow -d portflow --format=custom \
  > "backups/portflow-$(date -u +%Y%m%dT%H%M%SZ).dump"
```

将备份复制到另一台主机定期验证可恢复性。恢复前应另行保留当前数据库，并计划控制面维护窗口：

```bash
docker compose --env-file .env.production stop control
docker compose --env-file .env.production exec -T postgres \
  pg_dump -U portflow -d portflow --format=custom > backups/before-restore.dump
docker compose --env-file .env.production exec -T postgres \
  pg_restore -U portflow -d portflow --clean --if-exists < backups/selected.dump
docker compose --env-file .env.production start control
```

恢复期间 Agent 的现有转发仍继续运行，但不能从面板修改配置。

## 8. 升级与回滚

版本同时包含控制面协议和 Agent 心跳字段变更时，必须先升级控制面并确认健康，再逐台升级 Agent。旧 Agent 未上报的新指标会由新控制面按零值处理；新 Agent 不应连接仍在运行旧协议的控制面。

控制面升级前先备份 PostgreSQL，修改 `PORTFLOW_VERSION`，再构建新镜像：

```bash
docker compose --env-file .env.production build
docker compose --env-file .env.production up -d
docker compose --env-file .env.production ps
```

迁移在控制面启动时事务化执行。应保留升级前的数据库备份和镜像。如需回滚，恢复旧的 `PORTFLOW_VERSION` 与兼容数据库备份后重新启动。

Agent 升级会短暂中断该节点上的转发，应在维护窗口内逐台执行。升级前保留旧二进制和配置：

```bash
sudo cp -a /usr/local/bin/portflow-agent /usr/local/bin/portflow-agent.previous
sudo cp -a /var/lib/portflow-agent/config.json /var/lib/portflow-agent/config.json.previous
sudo systemctl stop portflow-agent
sudo ./deploy/agent/install.sh ./bin/portflow-agent
sudo systemctl start portflow-agent
sudo systemctl is-active portflow-agent
```

失败时回滚：

```bash
sudo systemctl stop portflow-agent
sudo install -m 0755 -o root -g root /usr/local/bin/portflow-agent.previous /usr/local/bin/portflow-agent
sudo install -m 0600 -o portflow-agent -g portflow-agent \
  /var/lib/portflow-agent/config.json.previous /var/lib/portflow-agent/config.json
sudo systemctl start portflow-agent
```

## 9. 停用与卸载 Agent

仅停用：

```bash
sudo systemctl disable --now portflow-agent
```

卸载程序但保留身份和配置：

```bash
sudo systemctl disable --now portflow-agent
sudo rm -f /etc/systemd/system/portflow-agent.service /usr/local/bin/portflow-agent
sudo systemctl daemon-reload
```

`/var/lib/portflow-agent` 包含节点凭证和最后一份有效配置，默认不删除。只有在节点已从控制面吊销且明确不再恢复时，才应手工清理数据目录和系统用户。
