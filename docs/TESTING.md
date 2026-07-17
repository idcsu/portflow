# PortFlow 测试

## 本地检查

```bash
make test
go vet ./...
go test -race ./...
npm --prefix web run build
```

构建 AMD64 二进制和 ARM64 Agent：

```bash
make build VERSION=1.0.0
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -buildvcs=false -trimpath \
  -ldflags "-s -w -X main.agentVersion=1.0.0" \
  -o /tmp/portflow-agent-linux-arm64 ./cmd/agent
```

## 真实 PostgreSQL 集成测试

Compose 的 `test` profile 会创建隔离的 PostgreSQL 数据卷和一次性 Go 测试容器。PostgreSQL 不绑定任何主机端口，测试容器通过 Compose 内部网络访问它。默认正式部署不会启动该 profile。

```bash
docker compose -p portflow-integration \
  --env-file .env.production.example \
  --profile test run --rm integration-test
```

无论测试是否成功，最后都清理隔离容器、网络和数据卷：

```bash
docker compose -p portflow-integration \
  --env-file .env.production.example \
  --profile test down -v --remove-orphans
```

集成测试覆盖：

- 所有 PostgreSQL 迁移；
- 初始管理员与一次性注册令牌；
- 成员创建、角色/状态更新、最后管理员保护和旧会话撤销；
- 系统设置接口的管理员权限与运行策略一致性；
- Agent 注册和凭证校验；
- UDP 规则创建与节点配置版本递增；
- 带宽上限的数据库持久化与 Agent 配置下发；
- 单节点资源历史聚合、流量计数器差值与 Agent 重启归零；
- 根磁盘使用率、非回环接口网络速率和 `0008` 指标迁移；
- `0009` 实时连接快照迁移、完整快照替换与空快照清理；
- Agent 完整配置读取；
- 心跳、节点运行状态和线路级统计；
- 分钟级心跳快照、流量差值聚合与 Agent 计数器归零；
- 审计记录写入、JSON 详情读取和时间倒序查询。

## Caddy 配置验证

构建 Web 镜像后可在不监听端口的情况下校验 Caddyfile：

```bash
docker run --rm \
  -e PORTFLOW_SITE_ADDRESS=panel.example.com \
  -e CADDY_EMAIL=admin@example.com \
  portflow-web:1.0.0 \
  caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile
```
