# Juex 领域模型

> [English](DOMAIN.md) | 中文

本文是 Juex 产品语言、生命周期与不变量的规范来源。`ARCHITECTURE.md` 把这些概念映射到代码模块、接口、存储和测试；它不能重新定义其含义。

## 产品边界

Juex 是一个 bounded context：本地、可检查的 Agent Runtime，在 Workspace 中运行 Resident 或 Ephemeral Agent，把输入转为 Model 与 Tool 工作，并保持结果状态可继续和检查。

Go package、CLI command、HTTP route、Provider SDK、process manager 与 React application 是该上下文内的 Module 或 Adapter，不是独立 bounded context。外部 Model service、MCP Server、操作系统 service manager 与用户文件系统仍在 Juex 领域边界之外。

## 通用语言

### 身份与状态

| 术语 | 含义 |
| --- | --- |
| Agent Runtime | 接受输入、构建 Model context、调用 Provider、执行 Tool Call、持久化 Session state 并发出 Event 的本地系统。 |
| Workspace | Juex 加载工作局部 guidance/configuration 并保存 identity marker 的项目目录。它属于用户/项目，不属于 Agent state。 |
| Juex home | 由 `JUEX_HOME` 选择的有效 Juex-owned 可写根目录，默认为 `~/.juex`；限定 instance configuration、Extension、lock 与 Resident Agent registry。非默认 home 继承 `~/.juex/juex.yaml` 的只读配置基线。 |
| Resident Agent | 一对一绑定到 Workspace marker 的持久 Agent identity，存储在 Juex home registry 中并对 Fleet operation 可见。其 identity-owned state 在 Workspace 移动后保留。 |
| Ephemeral Agent | 使用私有临时 Agent state 的进程局部 Agent identity。它使用正常 Workspace 与用户 configuration/resource，但没有 Workspace marker，不注册到 Fleet，并在退出时删除，除非显式保留。 |
| Workspace marker | `.juex/juex.local.json`，从 Workspace 到 Resident Agent id 的窄绑定。Marker 是身份，不是配置或可复制 cache。 |
| Agent Address | 把已解析 Agent id 绑定到 identity-owned state directory 与 endpoint guard 的值。消费者使用 address，不从目录名推导 identity 或 Juex-home layout。 |
| Agent State Directory（`AgentStateDir`） | 一个 Agent identity 拥有的稳定 state root。Resident Agent 使用 `$JUEX_HOME/agents/<id>`；Ephemeral Agent 使用私有临时目录。它包含 registry record、Session、history、Artifact、log、Extension data 与生成的 Observable state，并在 Runtime Instance replacement 后保留。 |
| Runtime Instance | Agent 的一次 serving process incarnation，与 Agent id 独立识别，由 instance id、process id、endpoint、runtime start time、可选操作系统进程 fingerprint 与 binary version 描述。Restart 会更换 Runtime Instance，不改变 Agent。 |
| Workspace-local state | Workspace 下用户编写的 configuration 与 resource。Project-owned Observable definition 属于 Workspace-local；选中的 Extension definition 留在其 bundle；生成的 Observable state 不属于 Workspace-local。 |
| Agent state | Agent identity 拥有的 Runtime state，包括 Session history、Artifact、log、Extension data 与生成的 Observable state。它位于该 identity 的 AgentStateDir，与 Workspace 和 Runtime Instance 区分。 |
| Fleet | 一个有效 Juex home 下已注册 Resident Agent 的控制界面。它投影 binding/runtime health 并管理 lifecycle，不拥有用户编写的 Workspace content。 |

### Session 与 Turn

| 术语 | 含义 |
| --- | --- |
| Session | 可恢复的有序对话，具有 identity、kind、transcript、Event、usage、Model-owned working state 与 single-writer lock。 |
| Primary Session | 可被选为 Resident Agent active continuation target 的 Session。 |
| Side Session | 持久的探索 Session，可列出、可恢复，但绝不成为 Active Session。Primary Session 可在其 App 生命周期中把 Side Session 作为 delegated worker 管理。 |
| Active Session | Persisted history 中为默认 CLI、Web 与 external-event continuation 选中的 Primary Session。Resident App replacement 在发布新 App/Engine state 的同一序列化 transaction 中提交该 selection；即使 resident replacement 拒绝 candidate，显式 concurrent selection 仍 authoritative。 |
| Turn | 一个 user-originated 或 system-originated input，经过一或多个 Provider iteration 与 Tool Call batch 处理，直至 completion、cancellation 或 error。 |
| Pending input | Turn 或 compaction phase 活跃时 queue 的已接受 user steering 或 external input。它持久、有界、会过期，只在安全 Provider-iteration boundary admitted。 |
| Session state | 一个 Session 的 Model-owned Goal 与 Notes，区别于 Agent state 和 Runtime observed execution status。Managed Side Session 显式绑定同一 state 时，Primary Session 仍是 owner。 |
| Goal | 一个 Session 的 Model-owned completion contract，包括 description、acceptance criteria、status 与 continuation state。 |
| Notes | 一个 Session 的 Model-owned、有界工作 Markdown。Notes 在 compaction 后保留，并在每次 Provider request 中复述。 |
| Session scratchpad | Session-local 的长草稿与中间工作文件。它们显式管理，不会自动放入 Model context，并随 Session 删除。 |
| Compaction | Policy 驱动的 summary 操作，追加持久 compact marker，并改变后续 Active context，但不删除原 transcript。 |
| Context projection | 在 request time 用有界 preview 与 Artifact reference 表示大型用户输入或 Tool Result。Projection 保留 durable source content 与 transcript contract。 |

### Model、Tool 与 Guidance

| 术语 | 含义 |
| --- | --- |
| Provider | 在 Juex canonical Message、Tool definition、usage、stop reason 与外部 Model service 之间转换的 Adapter。 |
| Provider Profile | 一次 request 使用的已解析 Provider identity、Model、Protocol、endpoint/credential input、compatibility option 与 Capability Set。 |
| Request Epoch | 一个 effective Provider request envelope 的 durable、secret-safe identity。它记录安全 Provider setting，包括 hashed endpoint/header/query/cache-policy identity、按 section 去重的 system snapshot、有界 Tool 与 derived Runtime-context snapshot、有序 Message ID/content digest、compaction selection 与 one-shot policy context，不复制 transcript 或 wire request。 |
| Policy | 在 Framework checkpoint 评估的类型化 Module-owned rule。Framework 指定 owning Module identity 与 canonical Policy Point、排序 Policy 并发出中性 lifecycle fact；Lifecycle Hook 是一个 command-backed Policy source，不是所有 Policy 的定义。 |
| Protocol | Provider wire contract，例如 Anthropic Messages、OpenAI Responses、OpenAI Codex Responses 或 OpenAI-compatible Chat。 |
| Capability Set | 描述 Provider Profile 支持哪些可选行为的显式 gate，包括 Tool、vision、streaming、reasoning control/replay 与 output-token control。 |
| Tool | Model 可用的命名、带 schema 操作。Builtin、Skill、Observable、Model-state 与 MCP Tool 共用一个 Runtime catalog 和 result contract。 |
| Tool Group | 用于检查和展示相关 Tool 的稳定分类，不改变名称或 execution contract。 |
| Tool Call | Provider 请求的 Tool operation，在 Assistant message 内识别。其 result 按 Provider 顺序持久化，并在有效 Model context 中与 call 相邻。 |
| MCP Server | 贡献 Tool 并可能发出 external notification 的已配置 stdio process。一个 MCP Server 失败不会禁用健康 server 或 Builtin Tool。 |
| MCP Notification | 来自 MCP Server 的 external event，作为 Pending input 或 system-originated Turn admitted。它不是用户编写的 input。 |
| Extension | 具名目录，在 effective Extension allowlist 选中后可贡献 Skill、MCP Server、lifecycle Hook 与 read-only Observable definition。同名 Extension 构成 default-Home、effective-Home 与 Workspace override chain。 |
| Extension allowlist | 一个 Fleet 或 Workspace-bound Agent 允许的精确逻辑 Extension name。某层省略表示继承，显式层表示替换；没有 effective allowlist 就不选择 Extension。它不是 publisher/source authentication。 |
| Extension data directory | 一个 Agent 与一个逻辑 Extension 私有的持久 state，位于 `<AgentStateDir>/extensions/<name>`。它不同于选中的 Extension installation，并在 Runtime Instance 或 Workspace lifecycle 变化后保留，直到 Agent 被删除。 |
| Skill | 从配置 resource scope 发现的 Markdown instruction package，通过 prompt metadata 与 Tool access 提供给 Model。 |
| Prompt Section | 组装 system prompt 的具名部分，例如 guidance、available Skill、Runtime state 或 shell context。 |

### External Signal 与 Durable Content

| 术语 | 含义 |
| --- | --- |
| Observable | Project-owned 或由选中 Extension 定义的 external signal source，具有共享 lifecycle、全局唯一逻辑 id、resource source 与持久生成 state。Extension definition 为 read-only。 |
| Command Observable | 由 managed command 驱动的 Observable，其已解析、过滤、有界 output batch 变为 Observation。 |
| Schedule | 由 one-shot、daily、monthly-calendar 或 interval timetable 与预先编写 Observation payload 驱动的 Observable。Monthly recurrence 保留本地 wall-clock intent，跳过不存在的月日期与 DST gap，对 DST 重复 wall-clock time 只在较早 UTC instant 发出一次。 |
| Observation | Observable 发出的持久规范化 signal，包含 source identity、content、attachment、delivery state 与 admitted 时的 target Session。 |
| Event | 关于 Runtime activity 的稳定事实。Durable Event 在 live delivery 前提交到 Session journal；显式 transient Event 只存在于当前 subscriber。 |
| Artifact | `<AgentStateDir>/artifacts` 下的 durable Agent-owned byte，以安全 root-relative path 加 integrity metadata 寻址。Artifact reference 随 Agent 跨 Workspace 移动，不意味着 byte 对 Model 可见。 |
| User Media | 以 Artifact 保存、在 conversation 中表示为已验证 media reference 的 Session-scoped image input。Provider capability 决定 projection，而不是 durable reference 是否存在。 |

## 生命周期

### Resident Agent 身份

1. Stateful command 解析 Workspace 与 effective Juex home。
2. 没有 marker 的 Workspace 创建一个 Resident Agent id、发布其 AgentStateDir，并写 marker。
3. 后续 command 把已存 id 解析到同一个 Agent Address。Registry entry 缺失时明确失败，不会静默创建 replacement。
4. 移动后的 Workspace 可在验证后 rebind 到同一 Resident Agent。仍属于另一个 live Workspace 的复制 marker 会被拒绝。
5. Serving process 获取 Agent Address guard 并发布新 Runtime Instance。Restart 替换 instance，同时保留 Agent identity/state。显式 Fleet restart 可以在 replacement 确认相同 Session、Turn 与预期 terminal cause 后，为 interrupted 或 already-failed work 提交一次 continuation。它创建新 Turn，不 replay 已完成工作，也不覆盖 user cancellation。
6. Fleet stop 与 service removal 保留 Agent state。显式 Resident Agent removal 是 destructive boundary，但不删除用户编写的 Workspace file。

### Session 与 Turn

1. 工作附加到 Active Primary Session、创建新 Primary Session、创建 Side Session，或显式 resume 已记录 Session。
2. Turn admission 在输入进入 Provider request 前持久接受它。新 main input 先创建 non-replayable acceptance intent，再提交 Turn admission fact，随后成为 replayable admitted input。已有 durable Pending input 在同一 admission fact 提交前始终 replayable。Transcript repair 与有序 typed input policy 在 accepted message append 到 transcript 前运行；rejection 或 policy failure 结束 Turn，但不抹去 accepted input。
3. 每个 Provider iteration 接收 canonical context，可返回有序 Tool Call。每个 call 由 Turn、Provider iteration、Assistant message、call position 与 Tool Use ID 识别。
4. Runtime 把一个 Provider response 的完整有序 Tool Call set 视为一个 batch，包括长度为一的集合，并在任何 call start 前持久声明 batch。在 Tool Policy 或 handler 可跨越外部 side-effect boundary 前，持久标记每个 call started。Tool implementation 负责 raw output 与 structured diagnostic；有序 Tool Policy 与 context projection 产生 effective Tool Result。Runtime 在 append 有序 Tool Result batch 或再次请求 Provider 前，持久记录精确 Provider-visible success、failure、timeout 或 cancellation outcome。Raw diagnostic 绝不覆盖 transformed/projected outcome。Safety-policy failure 在 handler side effect 前 fail closed。
5. Restart recovery 区分已 declared 但未 started 的 call，以及已 started 但无 durable outcome 的 call。前者绝不会报告为 executed；后者变为 `TOOL_OUTCOME_UNKNOWN` 且不自动 retry。Durable outcome 按 Provider 顺序恰好恢复一次精确 Tool Result。
6. 只有 Assistant response durable 后才开始 finish attempt。它按稳定 Module 顺序评估每个 Finish Policy，只为第一个仍有效 continuation candidate 提交 state，把该 continuation durable admit 为 Pending input，然后才通知仅观察 callback。Stale candidate 不改变 control flow 并继续后续候选。
7. Pending input 只在 Provider iteration 之间 drain，并在 Finish Policy 运行后保持最终 completion authority。只有没有 accepted input 时，completion 才先关闭 active execution boundary，再提交 terminal Turn fact。Observation callback 可报告决策，但不能 approve、reject、reorder 或 replace。
8. Completion、cancellation、failure 或 process restart 后，transcript 与 Durable Event 仍是 resume 与 inspection 来源。
9. Active Primary Session 可为 delegated work 创建 process-managed Side Session。每个 Side Session 保持自己的 transcript、scratchpad、Pending input、lock 与 Turn lifecycle，同时共享 Primary Session 的 effective Workspace resource 与显式绑定的 Goal/Notes。
10. Subscribed Side Session terminal result 作为 durable `side_session` input 被 owning Primary Session 接受。Busy Primary Session 在正常安全 boundary queue 该 input，不丢弃。Subscription 在 child Turn 到达 terminal 时取样；之后 unsubscribe 只影响后续 Turn，不影响已经 accepted for delivery 的 result。Primary `/new` 结束 manager generation，并取消旧 Primary 尚未 delivery 的 result，不让它跨越该边界。Transient persistence failure 以有界 backoff retry；terminal delivery failure 在 Side Session status/Event stream 中保持可见。

### Pending Input

1. Accepted input 获得稳定 record/message id、expiry 与 durable `pending` record。
2. Framework-owned durable queue authoritative。内存 queue、Event status、Browser state 与 observer notification 都是 accepted record 的 projection，不能消费或丢弃它们。一个 Runtime lifecycle Interface 统一负责 direct input、MCP notification、Observation 与 Side Session result 的 start-versus-queue、Framework Turn identity、expiry、deduplication、final delivery classification 与 retry instruction。
3. Admission 在 message append 到 Active context 前标记 record。新 Turn admission fact 前的 failure 只留下 non-replayable intent；此前 accepted Pending record 保持 replayable。
4. Successful transcript processing 会记录，因此 cancellation、之后 Turn boundary 或 restart 不能执行同一 input 两次。Admitted 但未 processed 的 record 从 durable queue 恢复，不从 live transport 或 observer 恢复。Restart 用携带 accepted message id 的已提交 `turn.admitted` Event 与 transcript message id reconcile queue：committed admission 可完成 interrupted `accepting -> admitted` transition，uncommitted acceptance intent 保持 inert。Runtime 返回 opaque recovery handle 并负责 state classification；App 在 startup barrier 后执行 handle，使同步 input 与新 external input 不能超过最旧 durable record。
5. Expired input 变为 inert。Queue overflow 明确拒绝，不改变已 accepted record。
6. Turn failure 不静默丢弃 accepted input：retryable Provider failure 可继续使用它，terminal failure 在结束 Turn 前把它保存在 conversation history。
7. Pending 是 delivery state，不是 input kind。Queued message 保留 semantic source classification，包括 direct input、MCP notification、Observation 或 Runtime continuation。
8. Source Adapter 提供 semantic message kind、stable source identity、TTL 与 source-specific validity。它们不分配 Turn identity、不读取 Pending state、不决定 retryability、不重建 restart recovery。App 保留 Session lease、startup producer ordering 与执行 Runtime-issued start action。

### Active Session Replacement

1. App-owned transaction 创建并锁定 candidate Primary，不改变 persisted active history。Lock rejection 会保留 candidate identity 足够久，以关闭资源，并且只有无其他 actor 显式选中它时才删除；transaction 自身不发布 provisional durable continuation target。
2. Transaction 在 live publication 前构建、启动并验证 candidate Session Module、完整 Tool catalog、context 与 startup behavior。Prepare phase failure 会关闭 candidate set、释放其 lock/Session，并在不重写较新 active-history selection 的情况下有条件删除。
3. 在 App Session write lock 下，transaction 捕获精确 Engine checkpoint、发布完整 candidate Runtime bundle、重定向 durable Event/observability target，并运行 Session-start policy。若存在 Active Turn reservation 或内存 Pending input，则拒绝。
4. Policy success 仍处于 pre-commit。Cancellation 或 rejection 在关闭 candidate resource 前恢复 captured Engine checkpoint 与旧 Event/observability target。Rollback failure 与原 typed phase rejection 一起展示；Engine 仍引用的 resource 保持 open。
5. 把 candidate 持久化为 Active Session 是最后可失败 pre-commit gate。Commit 与 resident Session 比较。如果 write 在 replacement 后报告失败，transaction 会在释放 process-owned history lock 前恢复 Runtime 与精确 previous history，因此时间经过不能让 lock 可被夺取，显式 selection 也不能与 reconciliation 交错，包括 same-ID reactivation。Candidate cleanup 同样只删除未被选中的 Session；reconciliation 后的任何 selection 保持 authoritative。该 App replacement path 绝不把 history 当 provisional publication。
6. History gate 成功后，transaction 在释放 reader 前发布 App Session、lock、status 与 chunked-write state。因此 reader 要么看到完整旧 App/Engine state，要么看到完整新 state，绝不会混合。
7. 新 Session 提交后保持 authoritative，同时关闭旧 Module、single-writer lock 与 Session。Observability、status replay 或 superseded-resource cleanup failure 是稳定 diagnostic，绝不 rollback 已提交 replacement。

### Goal 生命周期

1. Model 显式创建 Session completion contract 前 Goal 不存在。普通 input 不创建 Goal。
2. `in_progress` 表示现在可以继续工作。Finish attempt 通常记录 Goal continuation 并开始下一 Provider iteration。若至少一个 subscribed managed Side Session 仍在运行，或 accepted subscribed result 尚未进入 Provider-visible processing，owning Primary 可改为结束当前 Turn；这包括已 queue 在当前 Provider iteration 后的 result。Durable subscribed result 提供下一 external input，不改变 Goal status 或 continuation count。Durable Assistant response 前的 Provider failure 不是 finish attempt：bounded Provider retry 与 Model fallback 负责该 failure，Goal 保持 `in_progress`，不创建 synthetic continuation。
3. `wait_for_user` 表示 Goal 未完成，但继续有效进展需要新 external input。它允许当前 Turn 结束而不记录 continuation。
4. 新 input 不修改 waiting Goal。Model 在下一 Provider request 看见 persisted contract，并显式选择 `in_progress`、terminal status 或继续 `wait_for_user`。
5. `success` 与 `failure` 是 terminal Goal status，允许 Turn 完成。Status change 保留 Goal contract 与累计 continuation count。

### Observable 与 Observation

1. Workspace 或选中 Extension 定义带 tag 的 Command Observable 或 Schedule。Project source 可写；Extension source read-only。
2. 启动或手动运行 source 时，在 AgentStateDir 记录 generated run state。
3. 每个 accepted signal 在异步 delivery 前规范化并持久记录为 Observation。
4. Delivery 投影 Framework-owned `queued` 或 `delivered` state。Delivery callback error 不能声明 Observation dropped；expiry、source deletion 或其他显式 cancellation boundary 负责 terminal discard。Source deletion 不能抹去 Observation 曾存在的历史事实。

### Compaction

1. Policy 或显式 request 选择较旧 Provider-visible context，同时按 token budget 保留近期 direct、MCP 与 Observable input，以及有效 in-progress execution 所需 Tool Call/Tool Result suffix。Candidate-specific budget 来自该 Model 配置 context window：自动 compaction 在 70% 触发，summary request 装入 80%，initial summary output 与 Tool Result limit 各用 0.5%，retained recent tail 用 5/64。
2. Selection 超出某 summary candidate 的 context window 时，candidate-specific request 可省略最旧完整 Tool Call/Tool Result exchange。它绝不省略 user-authored message 或改变 durable transcript。如果不可再缩减 message 仍无法装入 summary-request budget，在不调用 Provider 的情况下跳过 candidate。
3. Summary request 包含当前 Goal 与 Notes，作为 authoritative working state。
4. 成功 summary 作为带 selection/usage metadata 的 compact message append。
5. 未来 Provider request 使用最新 compact marker 加 retained message；persisted original transcript 仍可检查。
6. Cancellation 在 compact marker 提交前停止 summary work，因此未来 Active context 不变。
7. Model-change 与 one-shot system notice 留在 durable transcript，但不进入新 summary 或 retained input set。
8. 每次 summary Provider attempt 有自己的 `compaction` Request Epoch。Transport retry 保持关联该 epoch；semantic retry 或 Model fallback 在下一 Provider Call 前 checkpoint 新 epoch。

## 领域不变量

1. **单一身份绑定。** 一个 Workspace marker 指向一个 Resident Agent，一个 live registry record 反向指向其绑定 Workspace。
2. **身份是存储的，不是推导的。** Agent id 不能从 Workspace path 或 AgentStateDir basename 重算。Agent Address 负责映射。
3. **Agent identity 不等同于进程。** Agent id 不是 Runtime Instance id。一个 Agent Address 最多有一个 canonical serving instance，control operation 验证目标的精确 Agent 与 Runtime Instance。
4. **存储跟随所有权。** Workspace-authored configuration/resource 与 project Observable definition 留在 Workspace；Extension Observable definition 留在选中 Extension。Identity-owned Session、history、Artifact、log、Extension data 与 generated Observable state 留在 Agent。默认 `~/.juex/juex.yaml` 可提供共享配置，但非默认 Juex home 绝不把 Runtime state 或 instance configuration 写回默认 home。
5. **Command access 跟随 Agent ownership。** Sandboxed `exec_command` 与 Command Observable process 接收 Workspace 与当前 AgentStateDir 作为两个默认可写 root。`blocked_paths` 在任一 root 内保持 authoritative；该 grant 不隐含其他 AgentStateDir。
6. **Ephemeral work 隔离。** Ephemeral Agent 绝不创建、rebind、migrate 或注册 Resident Agent identity。
7. **只有 Primary Session 可激活。** Side Session 不能替换 Active Primary Session。
8. **Transcript 结构保持有效。** Tool Result 保留 Provider 顺序并匹配 Tool Call。Repair 恢复精确 durable outcome，把 declared-only call 报告为 not started，或把 started-without-outcome 报告为 `TOOL_OUTCOME_UNKNOWN`；绝不虚构成功执行或静默 retry uncertain side effect。Repair 显式且有记录。
9. **Accepted input 持久。** Failure/cancellation 可停止 Turn，但不能静默丢失 admission 已接受的 input。
10. **Provider detail 止于 Adapter。** Protocol-specific wire shape 不重新定义 Session、Turn、Tool 或 Event 含义。
11. **Capability 显式。** 可选 Provider 行为由已解析 Capability Set 启用，不在 call site 根据 Model name 猜测。
12. **Event gate fact 与 effect。** 必需 request Event 在其 Provider、Policy 或 Tool side effect 前提交。Tool declaration/start fact 使用稳定 Turn 与 Provider-iteration identity。Terminal Tool Event 在 transcript continuation 前提交精确 Provider-visible outcome；started-without-outcome 在 restart 后保持显式 uncertain。`provider.request_epoch` 是消费 included one-shot policy context 的 durable checkpoint；随后 `llm.requested` 声明 dispatch。`llm.responded` 或 `llm.errored` 终止 Turn epoch，compaction-summary outcome 终止 compaction epoch。Transport retry 保留同 epoch。Cancellation 后丢弃的 Provider response 通过 `llm.errored` 终止，不进入 transcript history。
13. **Observable definition 与 state 分离。** Project definition 跟随 Workspace，read-only Extension definition 跟随选中 Extension；generated run、Observation、delivery record 与 schedule cursor 跟随 Agent，并始终按全局 logical id 索引。
14. **Artifact reference 有界。** Durable byte 留在当前 Agent Artifact root 下，reference 是带 integrity metadata 的安全 root-relative path，Session-owned reference 限定到 target Session。Session scratchpad file 是可变 working material，不是 Artifact。
15. **Projection 不能成为 authority。** Runtime status、Browser delivery、log、pending-input observer、policy completion observer 与 continuation observer 报告已提交 lifecycle fact。它们不能 admit input、select Finish Policy、修改 effective Tool Result 或 complete Turn。必需 observer request checkpoint 是 durable commit boundary 自身的一部分；后续 best-effort observation 不能反转它。
