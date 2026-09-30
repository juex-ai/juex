# Managed Runtime

> [English](README.md) | 中文

Runtime 拥有 `runtime` PostgreSQL schema。Agent 是持久身份，Activation 是可替换的租约持有者。
Main 与 Workers 分别保存输入、历史、上下文代际和取消状态。调度器交错处理不同用户，限制活跃 Thread；空闲 Activation 不占用执行槽位。

输入回执和 request ID 去重在执行前提交。事件使用 Thread 内连续序号；重建上下文不读取 Workspace 文件。
Turn 固定指令和模型配置。每次新模型调用通过注入的业务接口重新检查当前权限与凭据可用性，Runtime 不查询 Management 表。

所有 Activation 写入都锁定并校验数据库租约；接管会增加 fencing 代际，过期实例不能发布回复。
恢复沿用原 Turn，将未确认的模型请求标为 unknown。Provider adapter 的一次网络请求对应一条持久 attempt；完整、部分和缺失用量分别记录。
模型请求的恢复规则不能作为重放外部工具操作的依据。

成员、代执行者和 Agent 执行代际防止撤销后恢复使旧队列工作复活。人工取消持久化且只影响该 Thread。
迟到的 Provider 用量可以结算已取消请求，但不能追加 Assistant 消息。服务关闭保留未完成工作以供恢复。

HTTP/Web 会话链路已实现。工具执行、设备路由、上下文压缩和独立应用集成仍属于平台重构的其他工作；会话测试通过不代表这些能力已经验收。
