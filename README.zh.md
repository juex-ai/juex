# JueX

> [English](README.md) | 中文

JueX 是服务化的 Managed Agent 平台。一次部署通过一个 Management Dashboard
服务多个租户和用户。每个用户在每个租户中拥有一个 Fleet；Agents 在共享的持久
Runtime 中运行，使用部署管理的 Host 或 Hosted 环境，也可使用明确授权的远程设备。
部署默认使用单租户。

## 部署

按照 [Linux 部署指南](deploy/managed/README.zh.md) 初始化 Docker Compose、
PostgreSQL、HTTPS、gVisor 和持久存储。部署方生成首位管理员的一次性初始化链接，
配置模型凭据。管理员邀请成员，用户通过邮箱和密码登录。

关闭浏览器不会停止已接纳的工作。Memory 和 Calendar 是独立的 Fleet 应用，
按各自生命周期持续工作。

## 客户端

发布归档包含 Linux/macOS、amd64/arm64 的 `juex` 和 `juex-executor`。
下载归档并校验发布校验和，或在检出的发布版本中使用 Python 3.11+ 安装器：

```sh
python3 scripts/install.py --version VERSION
export JUEX_SERVER=https://juex.example.com
juex login --email user@example.com --password-stdin
juex tenant list
juex fleet show
juex agent list
```

登录命令从标准输入读取密码，不要将密码放进命令参数。CLI 会话凭据以私密文件保存，
作用域绑定平台公开 origin。详见 [客户端 CLI](internal/entrypoints/clientcli/README.zh.md)
及命令帮助。

通过明确的私密状态目录接入远程 Linux/macOS 设备：

```sh
juex-executor --state /absolute/private/device-state pair --server https://juex.example.com
juex-executor --state /absolute/private/device-state run
```

在 Dashboard 批准配对，并在设备本地确认授权。设备执行使用当前 OS 用户权限。
后台模式、授权和恢复说明见 [Execution](internal/execution/README.zh.md)。

## 开发

使用 `mise.toml` 固定的版本，执行 `mise exec -- make build`。
`make build-clients` 仅构建两个用户客户端；`make build-go` 使用已有内嵌 Web 资源
构建全部服务和客户端。`make install-local` 安装客户端，不启动或重启服务。

验收遵循 [本地测试 Skill](.agents/skills/juex-localtest/SKILL.zh.md)。
数据库测试要求能够创建数据库的隔离 PostgreSQL 测试角色；真实模型测试还需要明确的
私密模型配置。前端开发见 [frontend/README.zh.md](frontend/README.zh.md)。

## 项目导航

- [DOMAIN.zh.md](DOMAIN.zh.md)：身份、所有权、生命周期和不变量。
- [ARCHITECTURE.zh.md](ARCHITECTURE.zh.md)：服务边界和持久化。
- [PHILOSOPHY.zh.md](PHILOSOPHY.zh.md)：原则及取舍。
- [DESIGN.zh.md](DESIGN.zh.md)：Dashboard 交互和视觉契约。
- [Managed 平台 ADR](docs/adr/0003-managed-agent-platform.zh.md)：架构决策理由。
