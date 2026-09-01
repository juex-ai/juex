# Fleet

> [English](README.md) | 中文

此包负责 registry 范围内的 Resident Agent 健康与生命周期策略。

- `Status` 把 Workspace 绑定与运行时健康保留为两个独立维度，从 runtime metadata 投影提供服务的二进制版本，并且只在进程和 endpoint 身份确认健康后，尽力加入 RSS 和区间 CPU 使用率。当 PID 存活但其操作系统进程 fingerprint 与 runtime record 不同时，只有 endpoint 也不是记录中的精确 Runtime Instance，Fleet 才把记录判为 stale；缺失或无法读取的进程身份仍视为不确定。
- `Add` 按标准 marker 规则注册一个已有绝对 Workspace，应用可选 name/autostart metadata，并可立即启动。
- `SetEnabled` 使 disable 可逆：disable 会先停止再持久化 flag，enable 不会隐式启动。
- `Remove` 要求 transport confirmation，停止并锁住 endpoint，然后把有意的 registry 与匹配 marker 删除委托给 agentstate。
- `Start` 启动 detached 的 `juex -C <workspace> listen` 子进程，并等待精确的 PID 和 endpoint 身份。Supervisor 只传递继承的启动环境和 `JUEX_HOME`；子进程自行解析 Workspace YAML 与 `.env`，避免 Agent 间环境泄漏。
- `Stop` 请求绑定到实例的自关闭；绝不向记录中的 PID 发信号或强制终止。
- `Restart` 在 graceful shutdown 前检测活跃、pending-drain 或已失败的 Thread 工作，协商绑定身份的 `runtime_restart` intent，并且只有 replacement 确认相同 Thread 和 Turn 后才提交一个 `system_notice` continuation Turn：活跃工作需要类型化 restart cause；此前失败的工作必须仍处于 errored 且 error kind 相同。已完成、用户取消或被取代的 Turn，以及工作正常的 replacement，都不会收到 continuation。已有 history 保留，先前 Tool Call 不会重放。缺少 acknowledgement、status 读取失败和 continuation 提交失败会被报告，但不改变进程重启成功；`Stop` 绝不发送 restart intent 或恢复工作。
- `RestartRunningAgents` 从一个 status snapshot 中顺序重启已启用、已绑定且健康的 entry，报告 skip 与 failure，并在单个重启出错后继续。
- `Serve` 执行一次 reconciliation，接管已验证 runtime，启动启用 autostart 的 Agent，并保持常驻但不拥有 child lifetime。Reconciliation 只有在获取 endpoint maintenance guard、精确重读 runtime、再次发现 process-start mismatch，且 endpoint probe 不是 exact 后才删除 reused-PID runtime record；它绝不向当前占用该 PID 的进程发信号。
- `Logs` 只 tail `Start` 创建的 Fleet 自有输出；外部启动后被接管的进程保留原来的 terminal、service 或重定向目标。
- `Endpoint` 只在为即时 proxy request 重新检查已绑定健康进程与精确 endpoint 身份后，才暴露 runtime metadata。
- `Config` 读取已绑定 Workspace config，不创建身份。`UpdateConfig` 验证并原子写入 replacement config，随后在同一个 lifecycle lock 下按与 `Restart` 相同的 Turn continuation policy 重启。Fleet HTTP response 会把每个 `environment.variables` 值替换为 `[REDACTED_ENV]`；PUT 在验证前把未改变的 placeholder 与现有文件合并，因此浏览器编辑既不会暴露也不会擦除 secret。若确实要写入该字面量，提交 `!juex/literal "[REDACTED_ENV]"`；Fleet 会在持久化字符串前去掉 control tag。
- `GCCandidates` 只列出确定的 Workspace orphan；`DeleteOrphans` 在 agentstate 执行原子的 registry-boundary 删除前锁定并重新验证每个 candidate。GC 与有意的 `Remove` 保持分离。

此包组合 `internal/agentstate` 处理 registry identity，组合 `internal/endpoint` 处理 runtime identity 和 maintenance guard，组合 `internal/config` 验证 replacement Workspace config，并组合 `internal/processmetrics` 获取跨平台进程计数器。HTTP routing、JSON shape 和 reverse proxy 行为留在 `internal/fleetweb`；Cobra output、prompt 和稳定 CLI exit category 留在 `internal/cli`。原生 launchd、systemd user 和 termux-services 注册留在 `internal/fleetservice`；此包既不渲染 service definition，也不调用平台服务管理器。
