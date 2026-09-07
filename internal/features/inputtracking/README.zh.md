# 输入跟踪

> [English](README.md) | 中文

`input-tracking` Module 提供持久化输入清单和 `check_inputs` 工具。标准模式默认开启，最简模式默认关闭；可以用 `modules.input-tracking.enabled` 覆盖。

Framework 在接收时登记直接用户输入，包括 Main 和直接发往 Worker 的输入。自动 continuation、Observation 和通知不登记。每次请求按接收顺序提供已投递但尚未勾选的输入。模型处理完后勾选；部分完成、失败、等待中的请求以及仍有效的约束保持未勾选。问题必须先回答再勾选，其他工具必须在前一个响应中完成。批量勾选原子提交，在同一 Thread 和工作范围内幂等。

Framework 拥有 `inputs.json`、输入原文、勾选事实和恢复机制。Module 没有独立状态文件，也不注册可退休删除的资源。消费输入的 Turn 可以结束而输入仍未勾选；这种记录用于提醒，不会重新排队执行。勾选立即移除提醒，文件记录等执行恢复不再依赖它时才删除。正常 Turn 结束不会自动勾选。`pending_count` 和 `send --wait` 保持投递语义。

关闭开关会移除工具和提醒，并停止登记新输入。已有未勾选输入保留；重新开启后在下一次正常执行时提供提醒，不补录关闭期间的消息，也不自动唤醒旧任务。Compaction 保持工作范围，并从持久状态重建提醒。用户 `/new` 结束之前的清单范围；还有未勾选输入时，模型 `context_new` 会被拒绝。

当前清单最多接受 256 条未勾选输入，满时在确认接收前拒绝新跟踪输入。投递 TTL 不会使已跟踪请求过期。大内容复用普通输入投影和 artifact 读取路径。清单预览共享 compaction 保留预算，避免压缩后又通过提醒恢复长输入全文。每条输入始终可被发现；上下文容量不足会报错，不会静默省略清单条目。

勾选是模型的判断，不是工作正确性的证明。清单不增加自动 continuation 或结束门禁。确定性覆盖位于 Framework 测试和 `tests/e2e/input_tracking_test.go`；真实模型 A/B 测试 `TestLiveInputTrackingAB` 需显式使用 `integration,input_tracking_eval` build tags，漏办、重复动作、提前勾选和调用/token 成本记录在 `.tmp/reports/input-tracking/`。

部署采用 clean break，原待输入文件名和 Generation seed 不再沿用。升级已有 Agent 前，需要完成或手动交接未处理输入及旧上下文；不提供旧格式读取或自动迁移。单独关闭此 Module 不会回退存储格式。
