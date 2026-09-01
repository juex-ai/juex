# Fleet 服务注册

> [English](README.md) | 中文

此包把常驻 Fleet Supervisor 注册到当前用户的原生服务管理器。它不管理单个 Agent。

- `New` 构建平台特定的计划，其命令为 `juex fleet serve`；定义中绝不固化 `fleet.addr`。
- `ExistingServeOptions` 读取当前 launchd、systemd 或 Termux 定义，使 CLI 能在替换前拒绝格式错误或不属于 Juex 的定义；解析出的选项不是配置输入。
- `Install` 在启用和启动服务前发布定义。
- `Installed` 先检查有效定义，再在平台可在定义文件缺失时继续保留已加载服务的情况下严格检查原生管理器状态。
- `Uninstall` 查询原生管理器状态，确认 Supervisor 已停止，然后删除定义。
- launchd 定义使用 `AbandonProcessGroup`；systemd user unit 使用 `KillMode=process`；Termux 服务要求显式确认的 `down`。
- 定义会持久化一条供常驻 Agent 及其子进程使用的显式可执行文件搜索路径。Juex 可执行文件目录和 `~/.local/bin` 位于最前，安装器 `PATH` 中安全的绝对路径保持原顺序，相对路径被丢弃，最后追加平台默认路径。

服务身份包含规范化的 `JUEX_HOME` slug 和 hash，因此可以独立注册多个 home。定义发布把每次 crash-safe 文件替换委托给 `internal/homestore`，同时在 Termux 多文件定义之间保持事务性。Termux 会先写入 `down` sentinel，再暴露 `log/run` 和 `run`，随后启用并重启服务，使重新安装采用更新后的命令。CLI flag 与输出、稳定地址验证和 home 配置持久化仍属于 `internal/cli` 与 `internal/config`；Agent reconciliation 和 detached child 生命周期仍属于 `internal/fleet`。
