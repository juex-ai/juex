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
就绪状态。Agent 可搜索预览、读取条目、提交显式提案和读取允许的保留证据。
服务接纳提案后，Source Agent 即完成提交，不等待或轮询审阅结果。接纳表示已提交，
不表示已记住。Supervisor 负责后台工作；用户可用 `juex memory result <id>` 按需
查询回执。必要指引内置，关闭
Skills、Hooks、MCP 和 Extensions 后仍可使用。
同一 Fleet 内所有已提交知识均在 Agent 之间共享，包括带 Workspace/Project 元数据
的条目。Basic 搜索/读取与 Advanced recall 的可见范围一致，调用方 Workspace 不会
隐式过滤知识。搜索可显式按来源 Agent 或 Workspace/Project 适用语境筛选：来源
Agent 匹配任一已记录来源，各元数据条件精确匹配并取 AND；留空表示搜索全部 Fleet
知识。搜索预览不携带完整来源，读取条目可获取来源。

条目的 `scope` 描述适用语境，不表示访问隔离。Supervisor 任务可跨语境合并、纠正
或移除知识，并保留多个 Agent 来源及有意义的项目限定。不同项目的独立规则应保留
为不同条目；共享事实不代表普遍适用。来源必须来自任务证据或已经提交的知识。
共享来源不授予原始历史读取权限：历史仍限定在调用方 Thread 或当前任务证据内。
修改继续受任务凭据、版本及用户禁止学习约束限制。

提案 key 独立标识请求，与条目 ID 不同。工具 schema 公开条目 ID 约束。
Worker 按搜索返回的 ID 读取条目，并从条目或任务证据复制来源引用。
决定请求校验失败时，任务仍可在原有预算内纠正；遇到结果不确定的传输错误，
重试相同决定以取回回执。成功的决定回执才会结算任务。

可信用户修正、删除、禁止存储区间与显式重新学习使用
`juex memory admin --file request.json`。例如：

```json
{"key":"forget-release-v1","action":"delete","entry_ids":["release-convention"]}
```

Fleet Web 的 Memory 导航提供默认 `memory` 服务的搜索、查看、编辑和确认删除，
无需 Agent 或 Supervisor 在线。编辑保留身份、语境和来源；遇到并发修改需
重新加载当前版本。用户编辑和删除会终止尚未完成的 Memory 审阅。

精确字段以请求/响应类型和工具 schema 为准。管理操作隔离旧任务。删除移除 Memory
拥有的知识及投影，清除匹配的保留提案/证据正文，并禁止从这些来源重新提取。
no-store 同时移除包含该来源的条目，保留无关条目。即使被移除条目还引用其他
来源，也只抑制用户选定的 no-store 区间。两种操作均不擦除原始 Thread
历史、Supervisor 历史、已投递上下文或外部副本。

## 权威与恢复

`$JUEX_HOME/services/memory/memory/<id>.md` 是权威条目，使用 JSON frontmatter
（YAML 的子集）。稳定 ID、正文、适用语境元数据、来源/时间及结构化事实属于知识版本。
`state/` 保存持久请求、租约、回执、源进度、禁止学习约束和提交 intent。
生成的 `MEMORY.md` 最多包含 200 个热条目。搜索/实体投影从 Markdown 在内存中
重建，无需数据库。已有带 scope 的条目沿用相同格式，无需重写正文、身份、来源或
时间即可共享，不需要数据转换。

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
- 运行中的 Supervisor 会尽量复用其管理的空闲 Memory Worker。每次任务使用新的
  Context Generation、授权和执行预算，同一 Thread 保留历史和累计用量。
  Supervisor 重启或原 Worker 不可用时，允许新建 Thread。
- Worker 只有受限 Memory 领域/事实发现与搜索/读取/历史/决策工具，恢复后也保持此边界，不获得
  Main 的管理或通用工具。
- recall 在每个已接纳输入的准备边界运行一次，包括 Turn 中途输入，先于 Provider
  执行；最多 500 毫秒、八个条目、4096 字节。被动上下文检查及工具迭代复用冻结
  快照。新准备清除旧 recall；服务失败可观察且不使普通对话失败，显式工具仍报错。

## 领域与证据驱动维护

`knowledge/` 拥有十一个内置领域声明：身份、人际、知识兴趣、健康、项目、爱好、
偏好、财务、义务、临时情境和开放 Other。服务、Agent 模板和 Fleet UI 使用同一份
声明。规范关系定义类型、必要限定项、基数、竞争范围、时间规则、证据和示例。
Worker 按需读取相关模板，先查询已有实体/事实再修改；普通 Agent 提供证据，不修改
本体。不引入自动衰减或每领域专用 Agent。

实体 ID 跨领域共享，同名不代表同一身份。每个事实 ID 只有一个所属条目。重复确认
补充原始来源，不刷新记录/生效时间。模型维护的新事实或变更必须引用当前 assignment
的用户原始证据；assistant 复述和召回内容不是新的确认。服务校验完整候选存储，
包括跨条目竞争、引用、原始来源和预期版本。有证据的语义更正/撤回由 Worker 审核，
并保留审计事实。用户强制覆盖和删除/no-store 走可信管理入口；模型证据不能获得该权限。

`valid`、`superseded`、`corrected`、`retracted`、`disputed` 表示存储的陈述状态。
生效区间为左闭右开；替代表示过去真实但已结束，更正表示先前陈述错误。未知日期保留
未指定端点和时间说明，按时点查询不猜测未知起点或结束时间。截止时间使用 `due_at`：未履行义务变为逾期，不自动变为已完成。
当前查询返回适用事实，历史查询包含审计陈述；按时点查询表示该时点的有效事实，
不表示当时数据库知道什么。更正/撤回不会因查询过去时点重新成为真实事实。

搜索、普通读取、召回和 Web 事实查询使用相同的服务端投影。结构化当前视图依据选中
事实生成名称、摘要和正文，避免混合条目的旧陈述通过散文泄漏。这种投影不能证明
存储正文的语义真实；Worker 必须在同一事务中协调修改正文和事实。用户/维护读取
保留完整审计记录。未修改、没有 Facts 的正文条目无需转换即可继续使用。

Fleet Memory 即使在领域为空时也提供有方向的领域结构，并以有界分页展示可筛选的
持久化实体/关系、生命周期、范围和来源。选择实体可跨领域查看，条目链接保留查询。
不会仅凭来源 ID 编造原始历史链接。刷新反映已提交事实，并独立显示热索引是否就绪。
浏览器不自行计算生命周期。

较大的只读工具结果通过 `memory_read` 结果句柄按不可变、输入局部的页面读取，
受限 Worker 不获得文件系统权限。分页重新检查用户管理 fence 和 History 权限，
在新输入或重新打开后失效，并保持在工具输出限制内。成功写入回执立即可见。
查询仍需收窄以适应 Worker 上下文；分页不增加八次请求或 16K 预算。
