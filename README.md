# PortFlow

PortFlow 是一个稳定优先的内部多节点 TCP/UDP 端口转发管理面板。内部团队首版功能已经完成，覆盖完整 Web 管理、权限与审计、多节点 Agent、TCP/UDP 直连与基于既有 WireGuard 的双节点中转、限速、监控、部署诊断和升级回滚。

项目仓库：[github.com/idcsu/portflow](https://github.com/idcsu/portflow)

完整的产品范围、稳定性要求和机器操作约束见 [PROJECT_SPEC.md](./PROJECT_SPEC.md)。
正式试运行的 Compose、HTTPS、systemd Agent、备份和回滚流程见 [docs/DEPLOYMENT.md](./docs/DEPLOYMENT.md)。
普通测试、数据竞争检查和真实 PostgreSQL 集成测试见 [docs/TESTING.md](./docs/TESTING.md)。
正式发布前请执行 [scripts/preflight.sh](./scripts/preflight.sh)，并逐项完成 [首版发布清单](./docs/RELEASE_CHECKLIST.md)。
可使用中文交互式 [一键部署与管理脚本](./docs/ONE_CLICK_INSTALL.md) 完成控制面安装、可选 Docker 安装、更新、设置、备份、回滚、Agent 端口防火墙管理和卸载。

## 快速安装

固定到正式标签 `v1.0.3` 安装：

```bash
curl -fsSL https://raw.githubusercontent.com/idcsu/portflow/v1.0.3/install.sh \
  | sudo bash -s -- install --repo idcsu/portflow --version 1.0.3
```

安装前只读检查：

```bash
curl -fsSL https://raw.githubusercontent.com/idcsu/portflow/v1.0.3/install.sh \
  | sudo bash -s -- check
```

安装完成后运行 `sudo portflow` 打开中文管理菜单。脚本支持 Debian/Ubuntu 上经确认安装 Docker Engine 和 Compose，也支持 Agent 入口端口的 UFW/firewalld 放行与恢复。脚本不会自动启用防火墙，不会重置现有规则；云安全组需要在云平台单独配置。

## 当前结构

```text
cmd/control      控制面服务入口
cmd/agent        节点 Agent 入口
internal/agent   Agent 本地配置与后续数据面实现
internal/control 控制面 HTTP API
internal/domain  节点与转发线路共享模型
web              React + TypeScript Web UI
```

## 本地开发

后端和 Agent：

```bash
make test
make build
PORTFLOW_SECURE_COOKIES=false ./bin/portflow-control -listen 127.0.0.1:8080
```

前端：

```bash
npm --prefix web install
npm --prefix web run dev
```

开发服务器仅绑定本地回环地址，不要求修改防火墙。

未设置 `DATABASE_URL` 时，控制端会明确警告并使用仅供开发的内存存储，重启后数据消失。正式部署必须配置 PostgreSQL；控制端首次连接时会自动执行事务化迁移。环境变量示例见 [.env.production.example](./.env.production.example)。

首次打开 Web UI 后，选择“首次部署？初始化管理员”。初始化接口只能成功执行一次，密码至少需要 12 个字符。

## 当前 API

- `GET /api/v1/health`：控制面健康检查；
- `POST /api/v1/setup/admin`：首次创建管理员，仅可使用一次；
- `POST /api/v1/auth/login`、`POST /api/v1/auth/logout`：会话登录与退出；
- `GET /api/v1/auth/me`：读取当前用户；
- `POST /api/v1/enrollment-tokens`：管理员创建一次性节点注册令牌；
- `POST /api/v1/agent/enroll`：Agent 使用一次性令牌注册；
- `POST /api/v1/agent/heartbeat`：Agent 凭证认证后上报状态、WireGuard 隧道地址、CPU/内存/磁盘/网络资源、转发统计和有界日志批次；
- `GET /api/v1/agent/config`：Agent 凭证认证后拉取完整版本化配置；
- `GET /api/v1/nodes`：读取已注册节点；
- `GET /api/v1/forward-rules`：读取转发线路；
- `POST /api/v1/forward-rules`：创建 TCP、UDP 或 TCP+UDP 直连线路并递增节点配置版本；
- `PUT /api/v1/forward-rules/{id}`：更新线路并触发相关节点同步；
- `DELETE /api/v1/forward-rules/{id}`：删除线路并触发节点停止监听；
- `GET /api/v1/dashboard/summary`：读取基于存储层的仪表盘摘要；
- `GET /api/v1/metrics/traffic?hours=24`：读取 1～168 小时的聚合流量趋势；
- `GET /api/v1/audit-events`：管理员分页读取登录、注册和配置变更审计；
- `GET /api/v1/system/settings`：管理员读取控制面正在执行的运行、安全、Agent 心跳、数据保留策略和发布就绪诊断；
- `GET /api/v1/agent-logs`：管理员按节点、级别和时间游标分页读取 Agent 运行日志。

## Agent 首次注册

在面板“添加新节点”中填好节点名称和地区并创建一次性令牌后，直接复制面板生成的命令到目标 Linux 节点执行。命令会自动识别 AMD64/ARM64、从 GitHub Release 下载指定版本、校验 SHA-256、安装 systemd 服务、注册节点并启动服务。

```bash
curl -fsSL https://raw.githubusercontent.com/idcsu/portflow/v1.0.3/install.sh \
  | sudo bash -s -- agent --repo idcsu/portflow --version 1.0.3 \
    --control-url https://control.example.com \
    --enrollment-token '<一次性令牌>' \
    --name 'Shanghai Edge 01' --region 'Shanghai'
```

Agent 仅允许远程控制地址使用 HTTPS；`http://127.0.0.1` 和 `http://localhost` 只用于本机开发。安装器将一次性令牌临时写入仅 Agent 用户可读的文件，以 `-enroll-only` 完成身份写入后立即删除该文件，再启动 systemd 服务。安装器不会修改主机防火墙或云安全组。

## Agent 运行与配置同步

- Agent 每 15 秒上报一次心跳，包含 CPU、内存、负载、根磁盘使用率、非回环接口收发速率、当前连接/会话数、线路级累计转发字节和最近一次配置应用结果；
- 控制面超过 45 秒没有收到心跳时，在查询结果中将节点判定为离线；
- 控制面不可用时，Agent 保持当前转发运行，并使用 15 秒到 2 分钟的有界指数退避重试；
- 发现配置版本变化后，Agent 拉取完整配置并先校验；
- 新监听器无法启动时保留原有配置和监听器；
- 新配置成功应用后才以 `0600` 权限、临时文件、`fsync` 和原子重命名方式保存；
- 保存失败时回退到上一份运行配置；
- 配置版本倒退、规则属于其他节点或包含当前版本不支持的能力时会被拒绝。

## 当前数据面范围

当前 Agent 已实现 TCP、UDP 和 TCP+UDP 单节点直连：

- 双向流复制和 TCP 半关闭；
- 目标拨号超时与监听失败隔离；
- 单规则最大连接数；
- 来源 CIDR 允许/拒绝规则，拒绝规则优先；
- 有界 16 KiB 缓冲池；
- 连接数、入口字节和出口字节统计；
- 规则保持相同监听地址时原地更新，避免无谓断开；
- Agent 退出或规则删除时关闭相关连接，避免退出被悬挂连接阻塞；
- UDP 按客户端地址维护回程会话，空闲 60 秒后自动清理；
- 同一规则可同时监听 TCP 和 UDP，两种协议的统计合并上报；
- 每条规则可设置双向合计带宽上限，TCP、UDP、所有连接和两个方向共享同一个有界令牌桶，`0` 表示不限速。

双节点中转的数据面已经接入现有 WireGuard 隧道：入口 Agent 监听公网端口并通过出口节点的 WireGuard 私网地址连接内部中转端口，出口 Agent 只在该私网地址上监听，再连接最终目标。TCP、UDP 和 TCP+UDP 均复用现有有界转发器。控制面会同时递增两端配置版本并分别跟踪应用状态；任一节点未上报私有隧道地址时，API 和 Web 都禁止启用规则。Agent 运行时不会自动创建 WireGuard 接口或修改防火墙；部署管理器可以在目标机得到明确确认后添加并记录最小端口规则。

双节点中转的带宽上限只在入口 Agent 执行一次，避免两端重复整形增加延迟。限速参数可以热更新，不重建监听器，也不会主动断开已有 TCP 连接或 UDP 会话。

## Web 线路管理闭环

- 创建界面开放 TCP、UDP 和 TCP+UDP 单节点直连；
- 支持入口节点、监听地址和端口、目标地址和端口、启停、双向合计带宽上限、最大连接数以及 CIDR 允许/拒绝配置；
- 数据库在创建、修改或删除规则时，于同一事务内递增相关节点的期望配置版本；
- Agent 心跳报告已应用/已尝试的配置版本及失败原因；
- 面板根据节点在线状态、规则版本和 Agent 应用结果显示“等待同步”“已生效”“应用失败”“节点离线”或“已停止”；
- 面板展示每条线路的实时连接/会话数和 Agent 本次运行累计流量；
- 相同节点、协议、监听地址和端口发生冲突时返回明确的 `409 listener_conflict`，不会覆盖现有线路；
- Web 每 15 秒刷新节点与线路状态；
- 双节点中转表单支持选择不同的入口与出口节点及内部端口；两端上报 WireGuard 地址后可以启用，线路路径和应用失败位置会明确展示。

## 成员与权限

- 管理员可以创建内部账号、分配管理员或普通成员角色、禁用/启用账号并重置密码；
- 普通成员可以查看节点、监控和实时连接，并管理单节点直连线路；节点注册、双节点中转、运行日志、操作审计和成员管理仅管理员可用；
- 角色、状态或密码变更会在同一存储操作中撤销目标账号全部旧会话，使权限立即生效；
- 当前管理员不能在自己的会话中禁用或降级自己，存储层还会原子阻止最后一名启用管理员被禁用或降级；
- 成员变更写入审计记录，密码明文和密码哈希均不会进入 API 响应或审计详情。

## 系统设置与策略核对

- Web “系统设置”页集中展示控制面版本、启动时间、连续运行时间和服务器时间；
- 展示当前会话有效期、密码长度、登录失败限速、Secure/HttpOnly/SameSite Cookie 状态；
- 展示 Agent 心跳周期、离线阈值、单次心跳连接/日志上限和每节点快照保留上限；
- 展示节点指标、Agent 日志、实时连接和操作审计的实际保留策略；
- 页面数据来自仅管理员可读的真实 API；本阶段不提供会影响现网的数据面或网络动态开关。

## 监控与审计

- 每个节点每分钟保留一份心跳快照，自动清理 30 天前的数据；
- 流量趋势使用相邻计数器差值，能识别 Agent 重启后的计数归零；
- 节点详情页支持最近 6 小时、24 小时、7 天和 30 天范围，展示 CPU、内存、负载、根磁盘、主机网络活动、连接数、单节点转发流量、配置同步状态与关联线路；
- 仪表盘展示最近 24 小时上下行流量和最近采样区间均速；
- Web “监控分析”页展示流量趋势和节点 CPU、内存、磁盘、主机网络与连接快照；
- Web “实时连接”页每 15 秒读取 TCP 连接和 UDP 会话的来源、目标、持续时间、最后活动时间与双向字节数，可按节点和协议筛选；
- Agent 每次最多上报 2000 条连接元数据，不采集转发内容；完整快照会清除已结束记录，截断快照只增量更新并限制每节点最多保留 4000 条，避免误删和无界增长；节点离线或超过 45 秒未上报时页面标记快照过期；
- Web “操作审计”页仅管理员可读，支持分页查看。
- Agent 在内存中保留最多 1000 条待上报运行日志，每次心跳最多发送 100 条；心跳失败时保留原批次，成功后确认删除；
- 控制面按节点与事件 ID 幂等去重，自动清理 14 天前的运行日志；
- Web “运行日志”页仅管理员可读，可按节点和信息、警告、错误级别筛选；
- 集中日志只使用有界内存队列和心跳批次，不在转发路径执行磁盘或网络 I/O，控制面故障不会阻塞已有转发。
