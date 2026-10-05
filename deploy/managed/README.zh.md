# Managed 部署

[English](README.md) | 简体中文

部署工具管理五个平台服务、PostgreSQL 和 HTTPS 网关。macOS/Linux 的原生目录与
Shell 使用 **Host**，Linux 的 gVisor 隔离和 XFS 配额使用 **Hosted**。初始化时固定
后端；启动不会自动回退到另一种执行模式。

## macOS/Linux Host

准备 Python 3.11+、相同主版本的 PostgreSQL 服务端和客户端工具、nginx、OpenSSL、
GNU tar（macOS 使用 `gtar`）。按依赖的正常方式安装；PostgreSQL 除可执行文件外，还
必须具备运行库和共享资源。安装依赖与创建 JueX 状态是两个步骤。此模式不需要 Docker、
runsc 或 XFS。以所属非 root 用户运行，并具备可用的 macOS launchd 会话或 Linux
`systemd --user` 管理器。

解压并校验对应的 `juex_platform_VERSION_OS_ARCH.tar.gz`，在发布目录运行
（本地构建可使用 `--bin-dir /absolute/repo/dist`）：

```sh
python3 deploy/managed/operator.py --root /absolute/private/juex-platform install \
  --backend host --workspace /absolute/private/juex-workspaces \
  --bin-dir /absolute/release/bin --public-url https://machine.example:8443 \
  --postgres-bin /absolute/postgresql/bin --nginx /absolute/bin/nginx \
  --tar /absolute/bin/gtar --local-tls --admin-email admin@example.com
```

`--local-tls` 为指定域名/IP 创建部署专属 CA，不修改 OS 或浏览器的信任设置。
向客户端明确分发 `tls/ca.pem`；CLI 使用 `--ca-file` 或 `JUEX_CA_FILE`。
已有受信 TLS 时提供 `--tls-certificate` 和 `--tls-key`；私有签发 CA 额外使用
`--tls-ca`。其他设备访问时，域名/IP 必须包含在证书中。首位管理员的一次性设置链接
保存在私有 `secrets/bootstrap.json`。

初始化拒绝已有的部署和 Workspace 目录，创建专属的 socket-only PGDATA、nginx
前缀目录、日志和唯一 OS 服务名，不接管系统 PostgreSQL 或 nginx 服务。规范化后的
路径须足够短，以满足 PostgreSQL Unix socket 限制。初始化未完成时保留
`maintenance/install-incomplete` 并拒绝启动；保留失败证据，修正依赖或配置后使用
新目录重试。

网关和服务监听绑定 `0.0.0.0`，RPC 要求服务 mTLS。网关是 LAN/NetBird 客户端的公开
origin。Host Agents 使用同一 OS 用户；独立 Home/Workspace 目录不构成安全隔离。
原生任务在所属用户的服务会话中启动，不承诺 macOS 登录前运行。安装时保存的环境和
PATH 必须包含 Agent 所需的工具。

## Linux Hosted

只有 Execution 持有 Docker socket。托管负载使用 `runsc`，可信平台服务使用 `runc`。

安装 Docker Engine、Compose、`runsc`、Python 3.11+、GNU tar、`iptables`、`findmnt` 和 `xfsprogs`。准备开启 `prjquota,nosuid,nodev` 的**独立 XFS 文件系统**，由 root 持有且权限为 0700，并与部署目录分开。运维工具不会格式化磁盘。提供匹配公开 HTTPS 域名的 TLS 证书和私钥、已有的 Hosted 基础镜像、两个未占用的私有 IPv4 网段，以及 Hosted 容器能够访问的主机地址。必须明确配置 Hosted DNS。存储目录不能由用户控制。

执行 `docker build -f deploy/managed/Dockerfile -t juex-platform:VERSION .` 构建平台镜像。基础镜像参数允许使用部署者控制的镜像源。执行 `docker build -f deploy/managed/Dockerfile.hosted -t juex-hosted:VERSION .` 构建支持 Python/Node 的 Hosted 镜像。用户依赖安装到持久 Home/Workspace，系统包放入版本化镜像配方。初始化前拉取 PostgreSQL 和网关镜像；初始化会记录不可变的本地镜像 ID。保留这些镜像用于回滚。将本目录复制到 `/opt/juex`。

```sh
sudo python3 /opt/juex/operator.py --root /var/lib/juex-management init \
  --workspace /srv/juex-workspaces --image juex-platform:VERSION \
  --hosted-image juex-hosted:VERSION --public-url https://juex.example.com \
  --host-ip 172.30.0.1 --dns 1.1.1.1 \
  --tls-certificate /secure/fullchain.pem --tls-key /secure/privkey.pem
sudo python3 /opt/juex/operator.py --root /var/lib/juex-management resume
```

平台 bridge 默认使用 `172.30.0.0/24`，Hosted 使用 `172.31.0.0/16`。如果与已有路由冲突，必须覆盖。只有 HTTPS 端口公开绑定 `0.0.0.0`。Execution 的主机网络端点受运维工具配置的防火墙限制；数据库与其他 RPC 不发布端口。直接运行 `docker compose up` 会绕过恢复检查，启动服务应使用 `operator.py up/resume`。

使用 `docker compose --env-file /var/lib/juex-management/compose.env -f /var/lib/juex-management/compose.yaml exec management juex-management bootstrap --email admin@example.com` 生成首位管理员设置链接。这是一次性秘密 URL，必须私下交付。未配置 SMTP 时仍能复制邀请链接。模型凭据通过 Management 运维 CLI 配置。不能向 Agent 暴露部署私有文件和服务证书。

网关使用平台网段的 `.11` 地址。Management 和 Execution 只信任该代理提供的 `X-Real-IP`；网关的两个路由均以真实客户端地址覆盖此头，使认证和设备限流按客户端独立计算。自定义反向代理需在两个服务上设置 `JUEX_TRUSTED_PROXIES`（或 `--trusted-proxies`），明确列出代理的 IP/CIDR，并在边缘覆盖 `X-Real-IP`。默认不信任任何代理；不能信任用户可控制的地址头或整个客户端网段。恢复时会按平台子网重新绑定此设置。

配置 SMTP 时，将密码放入临时导出的 `JUEX_SMTP_CREDENTIAL` 环境变量，不写入 Shell 历史，然后运行：

```sh
docker compose --env-file /var/lib/juex-management/compose.env \
  -f /var/lib/juex-management/compose.yaml exec -T -e JUEX_SMTP_CREDENTIAL \
  management juex-management smtp seal --address smtp.example.com:587 \
  --from juex@example.com --username mailer
unset JUEX_SMTP_CREDENTIAL
```

将输出的 `JUEX_SMTP_CONFIG=...` 一行保存到 `secrets/management.env`，替换已有值，再使用 `operator.py up` 按新配置重建 Management。整个 SMTP 配置使用部署主密钥加密，不能持久保存密码输入。重新运行即可替换凭据；删除配置并重建 Management 即停用邮件。已有邮件队列仍会持久保存。主密钥须放入下述独立恢复归档，恢复时需要匹配的密钥。

普通进程日志放在 `logs/SERVICE/juex-*.log`，保留七天、每文件上限 10 MiB、每服务最多七份。空闲服务每小时清理过期日志。Docker 不重复存储进程日志。业务回执、用量和审计使用独立的数据库保留规则。

## 统一运维

`operator.py --root ROOT status` 和 `logs --service SERVICE` 只读检查状态和日志。
`down` 排空工作并停止自有服务，保留数据；`resume` 启动服务，通过健康检查后重新接纳
工作；`up` 启动服务但不清除维护状态。Host 命令以所属用户运行，Hosted 要求 Linux
root。Host 安装后的入口为 `ROOT/operator/operator.py`，不依赖源码 checkout。

`manage` 使用部署配置调用现有 Management 运维 CLI，例如
`manage model put --help` 或 `manage smtp seal --help`。仅当运维环境显式提供时，
才传入 `JUEX_MODEL_API_KEY` 和 `JUEX_SMTP_CREDENTIAL`。修改服务环境配置后，
使用 `down` 再 `resume`，让运行进程读取新设置。

## 一致性备份

```sh
sudo python3 /opt/juex/operator.py --root /var/lib/juex-management backup \
  --destination /backup/juex-data --recovery-destination /secure-backup/juex-keys
```

两个目标目录必须是私有目录（0700），与部署目录、Workspace 及彼此分开。按要防护的故障选择独立存储。默认保留七组**完整配对备份**；数据包没有匹配的密钥包就无法恢复。

备份先暂停接纳新工作，等待已接纳的模型和应用工作完成，检查外部操作与 Hosted 用户进程，再优雅停止服务写入者及自有 Host executor 或空闲 Hosted guest。排空期间仍可取消任务并接收回执。繁忙、结果未知或未正常停止的工作会让备份失败并保留维护状态，不会被静默杀死。检查 `maintenance/report.json` 和服务日志，明确处理或取消原任务后，重试备份或执行 `resume`。
已确认永久销毁的 Hosted 环境，其操作收据保留原始结果并列入审核清单，但不再阻塞备份。

备份覆盖 PostgreSQL、Blob、托管 Workspace/Home、执行日志账本、配置和固定的原生发布或容器镜像。主密钥、服务身份和 TLS 材料只写入单独的恢复位置。文件校验和及配对清单标识成功。排队任务保留原始 ID。外部设备文件和进程内存不属于平台备份内容。Host 备份无法限制其他 OS 进程；备份期间不能从 JueX 之外修改其 Home/Workspace。原生停机超时会保留停止标记和维护状态，不强杀工作；显式 resume 会清除该标记。

Linux Hosted 需要每日备份时，将 `juex-backup.service` 和 `juex-backup.timer` 安装到 `/etc/systemd/system`。创建仅 root 可读的 `/etc/juex/backup.env`，填写 `JUEX_DEPLOY_ROOT`、`JUEX_BACKUP_DESTINATION` 和 `JUEX_RECOVERY_DESTINATION`，然后执行 `systemctl daemon-reload` 与 `systemctl enable --now juex-backup.timer`。定时器按本地时间每天 03:00 执行，最多随机延迟五分钟，并补执行错过的运行。通过 `systemctl status juex-backup.service` 监控失败；备份失败可能有意保留维护状态。

## 整套恢复

Host 恢复前先用 `down` 停止源部署，将原目录另行保留，然后在相同 OS、架构和已经空出的原规范路径恢复部署及 Workspace。PostgreSQL 主版本必须保持一致；主版本升级需另行迁移。Host 恢复允许覆盖依赖工具路径。

Hosted 使用新的部署目录和空的独立 XFS 挂载点。目标 Docker daemon 不能已有另一套 `juex` Compose 部署；工具会拒绝替换。恢复前应明确退役旧部署，或使用另一主机/daemon。

```sh
sudo python3 /opt/juex/operator.py --root /var/lib/juex-recovered restore \
  --backup /backup/juex-data/juex-BACKUP-ID \
  --recovery /secure-backup/juex-keys/juex-BACKUP-ID \
  --workspace /srv/juex-recovered-workspaces --host-ip 172.30.0.1
```

Hosted 恢复会校验全部校验和、加载固定镜像、恢复数据库、在解包 Workspace/Home **之前**初始化 XFS project quota，然后检查完整目录树和限制。保留部署者设置的 SMTP、LAN 允许列表和资源参数，只修改机器相关绑定。普通运行日志重新开始。未完成的恢复保持关闭，不能恢复运行。

恢复成功后仅 PostgreSQL 运行。检查 `maintenance/recovery-review.json` 中的成员权限、设备权限和排队工作。开放公开网关前撤销过时权限：

```sh
sudo python3 /opt/juex/operator.py --root /var/lib/juex-recovered recovery-revoke \
  --tenant TENANT-ID --user USER-ID
sudo python3 /opt/juex/operator.py --root /var/lib/juex-recovered recovery-revoke \
  --device DEVICE-ID
sudo python3 /opt/juex/operator.py --root /var/lib/juex-recovered resume \
  --acknowledge-recovery REVIEW-SHA256
```

撤销成员权限会暂停访问并保留数据，同时保留最后一位活跃管理员。每次撤权都会更新所需的审核 hash。如果撤权失败或中断，执行 `report` 重新检查离线数据库并获得新的 hash，然后才能恢复运行。`up` 不能绕过确认。重新开放时先启动服务并检查 mTLS 健康状态，再释放接纳门控；不会重建丢失的远程进程内存或重放未知命令。

## 验证与升级

验证以仓库的 [local-test skill](../../.agents/skills/juex-localtest/SKILL.zh.md) 为准。
真实 Host 验收还需运行所属 OS 的服务管理器和 HTTPS 网关；Hosted 验收需要 Linux
Docker、gVisor 和有配额的 XFS。单测或编译不能证明任一部署模式已经可用。

使用新发布的部署工具执行升级：Host 使用
`upgrade --bin-dir /absolute/new-release/bin`，Hosted 使用
`upgrade --image IMAGE --hosted-image IMAGE`。两者都要求 `--destination` 和
`--recovery-destination` 完成升级前的完整备份。升级先排空工作并停止自有执行器，
保留旧版本，固定新二进制及部署工具或镜像，然后恢复运行。失败保持接纳关闭。
旧二进制不代表 schema 可以安全降级；回滚须使用对应的配对备份。平台/Web 与设备
协议版本应协调发布。

首个 managed 版本从新状态开始。所需旧资料应通过明确迁移导入，不能让 managed
服务指向旧的本地 Home 目录。
