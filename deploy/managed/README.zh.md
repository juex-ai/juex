# Managed 部署

[English](README.md) | 简体中文

这是官方单机 Linux 部署：一个 Management dashboard、共享的 Runtime/Memory/Calendar/Execution 服务、PostgreSQL 和 HTTPS 网关。只有 Execution 持有 Docker socket。托管负载使用 `runsc`，可信平台服务使用 `runc`。

## 前提

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

## 一致性备份

```sh
sudo python3 /opt/juex/operator.py --root /var/lib/juex-management backup \
  --destination /backup/juex-data --recovery-destination /secure-backup/juex-keys
```

两个目标目录必须是私有目录（0700），与部署目录、Workspace 及彼此分开。按要防护的故障选择独立存储。默认保留七组**完整配对备份**；数据包没有匹配的密钥包就无法恢复。

备份先暂停接纳新工作，等待已接纳的模型和应用工作完成，检查外部操作与 Hosted 用户进程，再优雅停止服务写入者和空闲 Hosted guest。排空期间仍可取消任务并接收回执。繁忙、结果未知或未正常停止的工作会让备份失败并保留维护状态，不会被静默杀死。检查 `maintenance/report.json` 和服务日志，明确处理或取消原任务后，重试备份或执行 `resume`。

备份覆盖 PostgreSQL、Blob、Hosted Workspace/Home、执行日志账本、配置和固定镜像。主密钥、服务身份和 TLS 材料只写入单独的恢复位置。文件校验和及配对清单标识成功。排队任务保留原始 ID。外部设备文件和进程内存不属于平台备份内容。

需要每日备份时，将 `juex-backup.service` 和 `juex-backup.timer` 安装到 `/etc/systemd/system`。创建仅 root 可读的 `/etc/juex/backup.env`，填写 `JUEX_DEPLOY_ROOT`、`JUEX_BACKUP_DESTINATION` 和 `JUEX_RECOVERY_DESTINATION`，然后执行 `systemctl daemon-reload` 与 `systemctl enable --now juex-backup.timer`。定时器按本地时间每天 03:00 执行，最多随机延迟五分钟，并补执行错过的运行。通过 `systemctl status juex-backup.service` 监控失败；备份失败可能有意保留维护状态。

## 整套恢复

使用新的部署目录和空的独立 XFS 挂载点。目标 Docker daemon 不能已有另一套 `juex` Compose 部署；工具会拒绝替换。恢复前应明确退役旧部署，或使用另一主机/daemon。

```sh
sudo python3 /opt/juex/operator.py --root /var/lib/juex-recovered restore \
  --backup /backup/juex-data/juex-BACKUP-ID \
  --recovery /secure-backup/juex-keys/juex-BACKUP-ID \
  --workspace /srv/juex-recovered-workspaces --host-ip 172.30.0.1
```

恢复会校验全部校验和、加载固定镜像、恢复数据库、在解包 Workspace/Home **之前**初始化 XFS project quota，然后检查完整目录树和限制。保留部署者设置的 SMTP、LAN 允许列表和资源参数，只修改机器相关绑定。普通运行日志重新开始。未完成的恢复保持关闭，不能恢复运行。

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

`uv run python -m unittest discover -s deploy/managed` 检查运维恢复边界。仓库 PostgreSQL E2E 覆盖排空和离线撤权；实际部署验收还必须在 Linux Docker、gVisor 和 XFS 上完成备份恢复，并检查文件和配额。

服务端与 Web 协调发布。替换固定镜像 ID 前必须完成整套备份并保留旧部署配方。设备协议不兼容时必须升级设备 CLI。首个 managed 版本从新状态开始，所需旧资料人工迁移。不能让 managed 服务指向旧的本地 Home 目录。
