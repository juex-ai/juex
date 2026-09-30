# 托管基础镜像

> [English](README.md) | 中文

使用 Docker 构建这份运维管理的配方，再将生成的 `sha256:` 镜像 ID 配置到 Execution。
镜像包含 Git、Python 虚拟环境、Node.js 24、C/C++ 工具链和 ripgrep。
guest 二进制与 JueX 服务使用同一版本，由 Execution 只读挂载。
受信任的部署方配方使用常规 Docker 构建器构建，生成镜像中的 Agent 工作负载始终使用
gVisor。隔离的构建器可以使用桥接网络并传入 `--build-arg BUILD_DNS=<可达的IP>`。

Python 环境放在 `/workspace` 或 `/home/agent` 下，例如
`python3 -m venv /home/agent/.venvs/project`。Node 项目依赖安装到项目 Workspace，
全局 npm 工具使用 `/home/agent/.local`。这些路径在容器替换后保留。
系统包通过显式镜像配方安装，运行环境的根文件系统只读。
部署备份需要记录配方与生成的镜像 ID。

Execution 需要 Linux 5.14 或更新版本、cgroup v2、Docker 的 iptables 后端、
gVisor `runsc` 和 `findmnt`。为 `backend.workspace_root` 准备独立的 XFS 文件系统，
以 `prjquota` 挂载，由 root 持有且权限为 0700。将文件系统 UUID 配置为
`backend.storage_identity`。控制状态必须位于另一个文件系统；Execution 不负责格式化或挂载磁盘。

每个 Agent 的 Workspace 和 Home 共用字节数、inode 数硬限额，默认 2 GiB、131072 个 inode。
PostgreSQL 保存存储池身份、项目编号和限额。挂载丢失、身份改变或配额未启用时拒绝启动。
存储池容量与 PostgreSQL、控制状态分别规划；每个 Agent 的限额不代表已预留总磁盘空间。
文件内容需要与分配记录、配额元数据一起备份，恢复这些信息后才能允许执行。
详见 [Execution 契约](../../internal/execution/README.zh.md)。
