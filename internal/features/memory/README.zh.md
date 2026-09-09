# Memory

> [English](README.md) | 中文

Memory 拥有 Agent 状态目录下 `modules/memory/` 中的持久知识。Main 与 Worker
Thread 共享该目录，不同 Agent 相互隔离。`standard` 启用 Memory，`minimal`
关闭它；可用 `modules.memory.enabled` 显式覆盖预设。关闭 Extensions、MCP、
Skills 与 Hooks 后，Memory 仍可运行，并提供自身的简短使用指导。
App 会先解析嵌入式调用的相对路径，再注入绝对的 Agent 存储作用域。

条目 Markdown 文件是权威数据。YAML frontmatter 包含名称、单行描述、类型
（`user`、`feedback`、`project` 或 `reference`）、创建时间和更新时间。名称拼写完全一致的写入
保留创建时间。所有平台的写入和删除都会拒绝与已有名称仅大小写不同的拼写，不会自动重命名或合并条目。
所有平台均禁止名称为 `MEMORY` 或 Windows 设备保留名，包括设备名
后接点号与后缀的情况。检索在元数据和正文中进行字面子串匹配，使用 Unicode 简单大小写
折叠，不将 `ß` 等字符展开为 `ss`。格式错误或不可读的单个条目会被跳过；目录
读取失败则作为操作错误返回。存储边界要求真实目录和普通条目文件，拒绝符号链接。

所有操作通过同一个稳定的文件系统事务锁协调。不同 Module 实例直接读取共享文件，
不维护缓存。锁等待响应取消，使用后保留锁文件。条目先原子发布，再重建派生的
`MEMORY.md` 索引。后续索引或持久化维护失败会明确报告条目已保存或已删除，不会
回滚权威知识。Thread 启动和成功压缩后，通过普通 Module 策略重建索引。维护失败
可观察但不阻断流程；取消和策略检查点失败仍向上传递。

构造、工具目录和指导文本不进行 Memory 文件操作。关闭后不提供工具、指导或维护。
知识在 `/new`、关闭进程、关闭和移除 Module 后保留；仅显式 `memory_delete`
删除条目。提示指导不会注入索引或全部条目正文。Memory 不自动提炼知识，不提供
旧工具别名或数据迁移。

## 从已退役的 Memory Extension 切换

`juex-extensions` 仓库不再分发 Memory bundle。安装器保留已有安装和 Agent 私有
数据。逐个 Agent 切换，并保留旧安装和知识作为备份。

1. 用 `juex agent list` 确认已注册的 Agent，再执行
   `juex agent stop --agent <agent-id>` 停止它。运行
   `juex agent config --agent <agent-id>` 定位其稀疏配置文件。
2. 编辑该配置，使最终生效的 `extensions.allow` 列表移除 `memory`，同时保留所有
   需要的其他 Extension。设置 `modules.memory.enabled: true`。若另行配置过旧
   Memory MCP、Skill 或 Hooks 的副本，也一并移除。配置和文件复制完成前保持
   Agent 停止，避免两个 Memory 提供者同时运行。
3. 只把需要保留且兼容的 UTF-8 Markdown 条目从
   `$JUEX_HOME/agents/<agent-id>/extensions/memory/` 复制到
   `$JUEX_HOME/agents/<agent-id>/modules/memory/`，必要时先创建目标目录。保留条目
   的 frontmatter 与正文；文件名主体必须等于 `name`，元数据和名称须满足当前工具
   schema。不要复制 `MEMORY.md`、锁、临时文件或符号链接。检查目标已有名称及其
   大小写变体，冲突时人工合并知识，不直接覆盖；原文件保持不动。
4. 执行 `juex agent start --agent <agent-id>`，再打开 Main 或发送一次请求激活
   Thread。Thread 启动会从已复制条目重建新索引。让 Agent 使用 `memory_search`
   查询一条已知事实，并在 Runtime 中确认三个内置 Memory 工具和预期的其他
   Extension。索引维护错误可观察，需要检查；Agent 正在运行不代表复制已成功。

`JUEX_HOME` 默认为 `~/.juex`。这是操作者执行的流程；安装或升级 Juex 不会自动复制、
转换或删除旧知识。
