# PortFlow 中文一键部署

根目录的 `install.sh` 是中文交互式部署管理器，适合把项目发布到 GitHub 后使用。它负责控制面的安装维护，也可以在 Agent 节点管理转发监听端口。所有系统级变更都会先说明原因与恢复方法，并要求明确确认。

## 1. 前置条件

控制面服务器需要：

- Linux AMD64 或 ARM64；
- root 或 `sudo` 权限；
- Docker Engine 与 `docker compose` 插件，或者允许脚本在 Debian/Ubuntu 上从 Docker 官方 APT 仓库安装；
- `curl`、`tar` 和至少 1 GiB 可用磁盘空间；
- 一个已经解析到服务器公网 IP 的域名；
- 准备使用的 HTTP/HTTPS 端口没有被其他服务占用。

先执行只读检查：

```bash
bash install.sh check
```

脚本检测到端口占用时只会提示，不会停止已有服务。自动安装 Docker 和添加防火墙规则都默认关闭，只有交互确认后才执行。

### 可选安装 Docker

没有检测到 Docker Engine 或 Compose 时：

- Debian、Ubuntu：中文说明将添加的软件源、软件包、启动的服务和恢复方式，确认后按 [Docker 官方 APT 仓库流程](https://docs.docker.com/engine/install/) 安装；
- 其他系统：停止自动安装，并提示根据 [Docker 官方系统文档](https://docs.docker.com/engine/install/) 手工处理；
- 检测到 `docker.io`、旧版 Compose、`containerd` 等冲突包时，会再次单独确认，不会直接移除；
- 覆盖既有 Docker 软件源文件前，会备份到 `/var/backups/portflow-docker-repository-*`；
- 启动 Docker 会创建容器网桥和 Docker 自己的防火墙规则，确认界面会明确提示发布端口可能绕过 UFW/firewalld；
- 卸载 PortFlow 不会顺带卸载 Docker，避免影响机器上的其他容器。

若确实要恢复脚本安装的 Docker，应先确认没有其他容器依赖，再按 Docker 官方卸载文档移除 `docker-ce`、`docker-ce-cli`、`containerd.io`、Buildx、Compose 插件、`docker.sources` 和签名密钥。Docker 数据目录不会因为卸载软件包自动删除。

## 2. 从 GitHub 一键安装

发布 GitHub 仓库后，把下面的 `OWNER/REPO` 替换成实际仓库。建议始终固定到正式标签，不要直接使用可能变化的 `main` 分支：

```bash
curl -fsSL https://raw.githubusercontent.com/OWNER/REPO/v1.1.2/install.sh \
  | sudo bash -s -- install --repo OWNER/REPO --version 1.1.2
```

脚本会从 `/dev/tty` 读取交互输入，所以通过管道运行时仍可填写域名、证书邮箱和端口。全新安装会在服务器本地分别生成 PostgreSQL 密码和二次验证加密密钥，不会显示在屏幕上。从 v1.0.x 首次升级时，旧管理器尚不认识新变量，兼容层会临时使用原安装器生成的稳定 64 位十六进制机密；后续由新版管理器将同一值明确写入 `PORTFLOW_MFA_ENCRYPTION_KEY`，不会破坏已经绑定的验证器。

更谨慎的做法是先下载、检查，再执行：

```bash
curl -fL https://raw.githubusercontent.com/OWNER/REPO/v1.1.2/install.sh -o portflow-install.sh
less portflow-install.sh
sudo bash portflow-install.sh install --repo OWNER/REPO --version 1.1.2
```

也可以从已经下载的源码离线安装：

```bash
sudo bash install.sh install --source "$PWD" --version 1.1.2
```

安装内容默认位于：

```text
/opt/portflow/releases/    各个源码版本
/opt/portflow/current      当前版本软链接
/opt/portflow/shared/      权限 0600 的生产配置
/opt/portflow/backups/     PostgreSQL 备份
/usr/local/bin/portflow    中文管理命令
```

安装完成后访问脚本显示的 HTTPS 地址，并在 Web 页面初始化第一名管理员。

## 3. 日常管理

直接运行交互菜单：

```bash
sudo portflow
```

也可以直接执行操作：

```bash
sudo portflow status
sudo portflow logs
sudo portflow backup
sudo portflow settings
sudo portflow restart
sudo portflow update
sudo portflow rollback
sudo portflow firewall
sudo portflow uninstall
```

“修改设置”支持域名、证书邮箱、HTTP 端口和 HTTPS 端口。修改后会先运行发布预检，再由用户选择应用。数据库密码不会在设置菜单展示或随意轮换，避免破坏已有 PostgreSQL 数据卷的认证信息。

## 4. 防火墙与端口连通性

这里需要区分两类端口：

- 控制面 80/443：由 Docker Compose 发布。Docker 官方提醒，发布的容器端口可能绕过 UFW/firewalld 的普通规则，因此不能把一条 UFW 规则当作控制面的安全边界；应同时核对 Compose 绑定地址、云安全组和上游路由器。
- Agent 转发监听端口：由宿主机进程直接监听。如果 Agent 所在机器启用了入站防火墙，必须放行线路使用的 TCP、UDP 或 TCP+UDP 端口。

在安装了控制面管理命令的机器运行：

```bash
sudo portflow firewall
```

在只有 Agent 的节点，可以直接从固定 GitHub 标签启动防火墙菜单：

```bash
curl -fsSL https://raw.githubusercontent.com/OWNER/REPO/v1.1.2/install.sh \
  | sudo bash -s -- firewall
```

防火墙管理器：

- 自动识别处于启用状态的 UFW 或 firewalld；
- 支持单端口和端口范围，以及 TCP、UDP 或两者；
- 修改前显示端口、协议、原因、恢复方式和云安全组提醒；
- UFW 使用带 `PortFlow managed` 注释的规则；
- firewalld 使用独立的 `portflow-managed` 服务；
- 将实际添加的规则记录在 `/var/lib/portflow-firewall/rules`；
- “恢复全部 PortFlow 规则”只删除记录中的规则，不重置整个防火墙。

脚本不会自动启用一个原本关闭的 UFW/firewalld，因为这可能在没有 SSH 放行规则时锁断远程连接。遇到原生 nftables、iptables 自定义链时也不会猜测插入位置或持久化方式，而是给出中文人工处理提示。

只需要在真正接收公网流量的入口 Agent 放行线路监听端口。双节点中转的内部 WireGuard 端口不要当成公网端口开放；云安全组仍需在云平台单独配置。

## 5. 更新与回滚

更新前管理器会：

1. 备份当前 PostgreSQL；
2. 下载并检查目标标签的部署文件；
3. 将新版本安装到独立发布目录；
4. 切换 `current` 并构建新镜像；
5. 等待控制面健康检查；
6. 若启动失败，自动恢复旧版本、旧版本号和旧容器。

新版管理器还会把本次更新的完整预检、构建和启动输出保存到临时诊断文件；更新成功后自动删除，失败时在终端显示文件路径，避免回滚后的正常日志掩盖真正原因。

更新命令：

```bash
sudo portflow update --version 1.1.2
```

旧版本目录默认保留，可通过菜单中的“回滚版本”选择。数据库迁移可能不兼容旧程序时，应按发布说明同时恢复更新前的数据库备份。

## 6. 卸载策略

普通卸载默认：

- 停止并删除 PortFlow 容器和网络；
- 删除程序版本与管理命令；
- 保留 Docker 数据卷、生产配置和数据库备份，方便重新安装。

只有明确选择“删除数据卷”并输入 `DELETE` 时，才会删除 PostgreSQL 与 Caddy 数据卷。已有备份会先移动到安装目录之外，不会随程序目录一起删除。

如果存在 PortFlow 记录的 Agent 防火墙规则，卸载时会询问是否一并恢复；默认仍然保留。脚本不会重置防火墙，也不会删除不属于 PortFlow 的规则。

## 7. GitHub 发布注意事项

发布前确认：

- `install.sh`、`scripts/preflight.sh` 和 `scripts/install_test.sh` 在 Git 中具有可执行权限；
- `.env.production`、数据库备份、前端依赖和本地产物没有提交，仓库已提供 `.gitignore`；
- 正式版本使用不可随意移动的标签，例如 `v1.1.2`；
- 发布后先在一台全新测试机执行安装、更新、自动回滚和保留数据卸载；
- Agent 节点继续按 [DEPLOYMENT.md](./DEPLOYMENT.md) 的 systemd 流程逐台安装。控制面管理器不会远程登录节点或批量重启 Agent。
