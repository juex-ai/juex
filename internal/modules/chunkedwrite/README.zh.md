# 分块写

> [English](README.md) | 中文

每个启用的 Thread Module 拥有一个内存写入管理器。启动时根据当前 Generation 中
属于自己的执行 fact 和匹配的工具参数恢复活动缓冲。即使后续结果策略修改展示文本
或执行失败，commit 和 abort fact 仍表示写入已经终结。工具层负责路径、校验和与
原子发布验证，不解释 Thread 历史。

Close 和禁用释放缓冲。`/new` 在 Generation 切换提交后清空缓冲。不创建私有资源
文件。已提交的用户文件和 Journal 保留；重新启用可以恢复当前 Generation 中仍有
记录的活动写入。

Provider 折叠只影响请求。Module 选择自己的已完成工具对与摘要锚点，Framework
约束所有权、协议有效性和预算。禁用的 Module 不恢复缓冲，也不折叠历史分块。
通用 Provider 投影仍保证有效的工具配对和上下文限制。
当前输出预算容纳不下摘要时跳过折叠，由通用投影处理原始工具对。
