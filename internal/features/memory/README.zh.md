# Fleet Memory

> [English](README.md) | 中文

Memory 是 Fleet 拥有的独立服务。Agent Module 通过类型化 Kitex 客户端直连。
服务拥有知识、任务回执和提交恢复；Supervisor 在普通的受限 Worker 中执行模型
审阅。Fleet 只管理服务进程和发现。Supervisor 离线时仍可读取，已有连接也不依赖
Fleet Web 进程。standard 默认启用 Agent 参与，minimal 默认关闭。

## 配置与使用

Fleet 默认启动 managed Basic Memory。在所属 Home 的 `juex.yaml` 中显式选择
Advanced：

```yaml
fleet:
  services:
    memory:
      config:
        strategy: advanced
```

Agent 的 `modules.memory.enabled` 控制是否参与；`service` 选择 Fleet 服务身份；
`profile` 为 `agent` 或 `supervisor`，Supervisor 角色默认使用对应 profile。
这些是可信启动配置提供的能力，不是完整身份认证，模型工具参数不能修改它们。

CLI 命令通过 `--service <identity>` 选择 Fleet 服务，默认 `memory`。Agent 使用
其他服务时，将此参数设为该 Agent 的 `modules.memory.service` 值；CLI 管理操作
不会推断 Agent 上下文。

`juex fleet services status memory` 查看生命周期，`juex memory status` 查看业务
就绪状态。Agent 可搜索预览、读取条目、提交显式提案、查询回执、读取允许的保留
证据。接纳仅表示已提交；只有 committed 回执表示知识已改变。必要指引内置，关闭
Skills、Hooks、MCP 和 Extensions 后仍可使用。

可信用户修正、删除、禁止存储区间与显式重新学习使用
`juex memory admin --file request.json`。例如：

```json
{"key":"forget-release-v1","action":"delete","entry_ids":["release-convention"]}
```

精确字段以请求/响应类型和工具 schema 为准。管理操作隔离旧任务。删除移除 Memory
拥有的知识及投影，清除匹配的保留提案/证据正文，并禁止从这些来源重新提取。
no-store 同时移除包含该来源的条目，保留无关条目。两种操作均不擦除原始 Thread
历史、Supervisor 历史、已投递上下文或外部副本。

## 权威与恢复

`$JUEX_HOME/services/memory/memory/<id>.md` 是权威条目，使用 JSON frontmatter
（YAML 的子集）。稳定 ID、正文、scope、来源/时间及结构化事实属于知识版本。
`state/` 保存持久请求、租约、回执、源进度、禁止学习约束和提交 intent。
生成的 `MEMORY.md` 最多包含 200 个热条目。搜索/实体投影从 Markdown 在内存中
重建，无需数据库。

一个服务持有写入租约。提交 intent 先于条目更改与回执持久化；恢复完成后读者
才能看到结果。预期版本和任务凭据拒绝过期写入。索引失败与知识提交分别报告。
搜索仍覆盖冷条目；只有成功的显式正文读取提高热度，预览、维护、recall 和重建
均不提高热度。recall 使用情况单独记录。

停止/重启服务保留数据和已接纳任务。关闭单个 Agent 保留 Fleet 知识。已有 Agent
私有文件保持原样，不导入、不迁移。Supervisor 的 stop、disable、reset、remove
分别报告进程结果与服务确认的任务回收。服务离线时明确持久记录“回收未确认”。

## Basic 与 Advanced

Basic 支持显式搜索/提案及 Supervisor 审阅，不自动 recall 或提取。Advanced
使用相同数据并增加有界自动工作：

- 历史需有五个已结束且未处理的 Generation，空闲 60 秒且没有 pending Input。
  少量历史等待 24 小时后可处理；显式手动维护跳过数量/等待门槛，可以在对话运行时
  接收请求，但派发仍等待空闲窗口并检查参与状态。显式提案优先。
- 源 Agent 持久记录参与/退出边界以及拟提交/已接纳游标。重新启用从新边界开始。
  冻结批次最多 100 个事件、32 KiB 原始对话证据。不可用或超限的源提交报错且
  不推进进度。维护 Thread、注入 recall、压缩摘要和工具输出不作为独立事实。
- 每个 Fleet 同时运行一个 Worker。每次 attempt 最多 180 秒、八次 Provider
  请求、16K 上下文、每次请求 4096 输出 token。基础设施失败最多退避重试两次。
  耗尽重试或被拒绝的批次不会自动重建；手动维护可以显式重试。
  只有 applied/no_change 推进已覆盖历史游标，Thread 完成不能证明业务完成。
- Worker 只有受限 Memory 搜索/读取/历史/决策工具，恢复后也保持此边界，不获得
  Main 的管理或通用工具。
- recall 在每个已接纳输入的准备边界运行一次，包括 Turn 中途输入，先于 Provider
  执行；最多 500 毫秒、八个条目、4096 字节。被动上下文检查及工具迭代复用冻结
  快照。新准备清除旧 recall；服务失败可观察且不使普通对话失败，显式工具仍报错。

结构化事实使用显式实体 ID、类型化值/关系、直接来源、记录/生效时间和
valid/superseded/disputed 状态。当前查询排除被取代和争议事实；时间查询保留有
证据支持的历史。姓名不能确定身份。MBTI 必须是带时间的自述，生日派生标签标为
derived。策略切换后 Basic 仍保留结构化数据。
