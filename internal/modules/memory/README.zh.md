# Memory

> [English](README.md) | 中文

Memory 拥有 Agent 状态目录下 `modules/memory/` 中的持久知识。Main 与 Worker
Thread 共享该目录，不同 Agent 相互隔离。`standard` 启用 Memory，`minimal`
关闭它；可用 `modules.memory.enabled` 显式覆盖预设。关闭 Extensions、MCP、
Skills 与 Hooks 后，Memory 仍可运行，并提供自身的简短使用指导。
App 会先解析嵌入式调用的相对路径，再注入绝对的 Agent 存储作用域。

条目 Markdown 文件是权威数据。YAML frontmatter 包含名称、单行描述、类型
（`user`、`feedback`、`project` 或 `reference`）、创建时间和更新时间。同名写入
保留创建时间。检索在元数据和正文中进行字面子串匹配，使用 Unicode 简单大小写
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
