# PortFlow 首版发布清单

本清单用于内部团队首版发布。目标机可通过中文管理器在明确确认后添加并记录 UFW/firewalld 规则；不支持的防火墙应另行制定最小变更和回滚方案。

## 1. 发布前

- 将 `.env.production.example` 复制为权限 `0600` 的 `.env.production`，替换数据库密码、64 位二次验证加密密钥、版本、真实域名和通知邮箱；
- 确认 DNS A/AAAA 记录已经指向控制面主机；
- 运行 `./scripts/preflight.sh .env.production`，所有 `FAIL` 必须清零；
- CI 或仅渲染配置时可使用 `./scripts/preflight.sh .env.production --offline`；正式发布前仍必须运行不带 `--offline` 的完整预检；
- 使用 `ss -ltnup` 和 `docker ps` 人工复核 80/TCP、443/TCP、443/UDP 及 Agent 业务监听端口；
- 核对云安全组和上游网络；在入口 Agent 使用防火墙菜单放行实际业务端口，并验证恢复命令，不对公网开放内部 WireGuard 中转端口；
- 对现有 PostgreSQL 执行备份，并在另一位置保留当前控制面/Web 镜像版本；
- 记录当前 `.env.production`、Compose 渲染结果和 Agent 二进制 SHA-256，不记录明文密码或节点凭证。

## 2. 构建与启动

```bash
docker compose --env-file .env.production build
docker compose --env-file .env.production up -d
docker compose --env-file .env.production ps
```

控制面健康后再逐台升级 Agent。不要同时重启全部节点；每次只处理一台，并等待该节点恢复在线和配置同步。

## 3. 上线验收

- `/api/v1/health` 返回 `status=ok`；
- 管理员可以登录，“系统设置”中的发布就绪检查全部通过；
- PostgreSQL、控制面和 Web 容器健康且没有持续重启；
- 注册一台测试 Agent，确认 45 秒内显示在线；
- 创建停用的 TCP、UDP、TCP+UDP 测试线路，确认配置版本下发成功后再启用；
- 分别验证双向 TCP、UDP 回程、带宽上限、CIDR 拒绝规则和连接快照；
- 停止控制面 1～2 分钟，确认 Agent 已有转发不中断，恢复控制面后 Agent 自动重连；
- 主动制造一个监听冲突，确认 Agent 保留上一份有效配置并在面板显示失败原因；
- 检查操作审计、Agent 日志、节点指标和实时连接页均能读取真实数据。

## 4. 回滚触发条件

出现以下任一情况时停止继续升级并回滚：

- 控制面健康检查持续失败；
- 数据库迁移失败或关键数据不可读；
- Agent 新版本无法恢复在线；
- 新配置导致原有转发中断且自动回退无效；
- HTTPS、登录或会话行为异常。

控制面回滚：恢复旧的 `PORTFLOW_VERSION` 和兼容数据库备份，再重新启动 Compose。Agent 回滚：停止单台 Agent、恢复旧二进制与配置备份、启动并验证，再决定是否处理下一台。详细命令见 [DEPLOYMENT.md](./DEPLOYMENT.md)。

## 5. 发布完成

- 保存发布版本、时间、执行人、镜像摘要和验收结果；
- 确认备份可恢复，临时注册令牌和测试规则已经删除；
- 清理测试容器、网络、数据卷和不再需要的临时镜像；
- 不清理生产 PostgreSQL、Caddy 数据卷或 Agent 身份配置。

WireGuard 自动建链、主动延迟/丢包探测、历史连接明细、任意多跳、面板内的域名/TLS 自动管理和 Agent 自动升级不属于首版范围。部署入口仍由 Caddy 按现有配置自动申请和续期证书。
