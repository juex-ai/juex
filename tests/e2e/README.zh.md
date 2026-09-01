# Juex E2E 覆盖

> [English](README.md) | 中文

此目录保存跨 package 回归。Unit test 仍是边界 case 的主要位置；E2E test 证明 binary、config loader、Runtime、Provider Adapter、Session、Tool、MCP 和 Web API 仍能正确组合。

## Non-Live E2E

运行：

```bash
go test ./tests/e2e -count=1
```

| 范围 | 测试 | 保护内容 |
| --- | --- | --- |
| 完整 Runtime loop | `TestEndToEnd_FullStack` | Prompt source、Skill、MCP stdio Tool、Builtin read/write/edit/apply_patch/exec_command/grep、并行 Tool Call、Event JSONL、conversation JSONL。 |
| Projected Artifact read | `TestEndToEnd_ProjectedToolResultReadsThroughBuiltinArtifactURI` | 过大 Tool Result 外置为 read-only `artifact://` reference，由已注册 Builtin `read` Tool 通过当前 Agent Artifact store 解析。 |
| 默认 sandbox command path | `TestEndToEnd_OmittedSandboxConfigRestrictsExecCommandWrites`, `TestEndToEnd_OmittedSandboxConfigFailsClosedWithoutBackend`, `TestEndToEnd_OmittedSandboxConfigRejectsCommandHardLinks`, `TestEndToEnd_OmittedSandboxConfigAllowsContainedHardLinks`, `TestEndToEnd_OmittedSandboxConfigPreventsCreatingExternalHardLink` | 省略 sandbox config 时仍贯穿 config loading、App/Tool composition 与平台 backend；command write 保持在 Workspace/当前 AgentStateDir 内；backend 不可用与外部别名 writable file 在执行前 fail closed；内部 hard link 仍可用；sandbox command 不能创建新的跨 root hard link。 |
| Apply patch builtin | `TestEndToEnd_ApplyPatchBuiltinFlow` | Runtime 暴露 `apply_patch`，通过 Tool loop 应用 update/add，向 conversation JSONL 持久化紧凑 Tool result，并发出不回显 patch body 的 Tool Event。 |
| Chunked write builtin | `TestEndToEnd_ChunkedWriteBuiltinFlow` | Runtime 暴露 `write_begin` / `write_chunk` / `write_commit`，通过多个 Model Turn 组装长文件，持久化紧凑 Tool result 与 Tool Event，并向 Provider 发送摘要 chunk input 而不重放 chunk content。 |
| Tool failure ledger | `TestEndToEnd_ToolFailureLedgerRecordsAndStalesWithoutContinuation` | 失败 check 记录到 Runtime ledger，不注入 failure-ledger continuation；相关文件 mutation 把 failure 标为 stale；Events JSONL 持久化该流程。 |
| Tool failure state input | `TestEndToEnd_ToolFailureLedgerWithUserAgentsDisabledDoesNotHardBlock` | App 级 Runtime 在 `enable_user_agents_resources=false` 时仍记录 failure，允许在 unresolved failure 下完成而没有 hard gate，并把 failure 持久化为 Event 且不创建 inferred working memory。 |
| Model-owned Notes sidecar | `TestEndToEnd_NotesSurviveCompaction` | 通过 `update_notes` 改写的 Notes 在手动 compaction 后仍注入，持久化到 `notes.md`，并发出 `notes.updated`。 |
| Goal Tool 与 completion gate | `TestEndToEnd_GoalToolsContinueThenSucceed` | 模型通过 `create_goal` 创建 Session goal state；goal 为 `in_progress` 时 Builtin goal gate 排入一次 continuation；随后 `update_goal` 标记 success，Session 持久化 goal Event。 |
| 可移植 Runtime loop | `TestEndToEnd_FullStackPortable` | 注入 Fake shell profile 后的跨平台 prompt、Skill、MCP stdio、read/write/edit/grep、Event JSONL 与 conversation JSONL。 |
| Session resume | `TestEndToEnd_ResumeRoundTrip` | Resumed App Session 复用相同 Session id，并在下一 prompt 前重放此前 User/Assistant history。 |
| Windows history publication | `TestSessionHistoryBlockedReplacementPreservesActiveSelection` | 持久 reader sharing conflict 会保留之前选中的 Active Session，不做发布后 rollback；reader 关闭后 conditional selection 成功。 |
| Tool outcome crash recovery | `TestEndToEnd_DurableToolOutcomeResumesWithoutDuplicateExecution` | 持久 remote-Tool outcome 在下一 Provider request 前修复缺失 transcript result，精确保留一次结果且不重跑 Tool。 |
| Debug observability | `TestEndToEnd_DebugObservabilityArtifacts` | Tool success、Tool failure、manual compaction 与 finish attempt 的 debug Session artifact 会写入且可解析。 |
| Binary loading | `TestLiveBinary_LoadsSkillsAndMCP` | 编译后的 `juex` binary 通过 `juex run --dry-run --json` 加载项目 Skill 和真实 Python MCP Server。 |
| Extension binary loading | `TestLiveBinary_LoadsExtensionSkillsAndMCP` | 编译后的 `juex` binary 验证 `juex.extension.json`，再通过 `juex run --dry-run --json` 加载 `.juex/extensions/<name>/skills` 与 Extension `mcp.json`。 |
| 外部 Memory Extension | `TestExternalMemoryExtensionEnabledAndDisabled` | 安装的 Memory bundle 只在 enabled 时贡献 MCP Tool、Skill、可选 lifecycle Hook、私有 Extension data 与 `ext:memory` provenance。 |
| CLI Model override | `TestLiveBinary_ModelsFlagUsesUserGlobalProvider` | 编译 binary 可从空 workdir 使用 root `--models` 替换 user-global Provider config 的 Model chain。 |
| Multi-home config 与 state | `TestLiveBinary_NonDefaultHomesShareConfigAndIsolateWritableState` | 两个 compiled-binary instance 继承 default-home Provider 与 sandbox policy，提供不同 instance Fleet address，独立 override Model，并只在各自 effective home 下写入 registry、history、Session 与 debug log。 |
| Provider protocol | `TestLiveBinary_ProviderProtocolAndThinkingMatrix` | 编译 binary 把 config 路由到 OpenAI Responses、自定义 OpenAI Chat 与 DeepSeek-compatible Chat，包括 thinking-effort capability gate。 |
| CLI image attachment | `TestLiveBinary_CLIRunAttachmentSendsImageAndPersistsArtifact` | 编译 binary 读取绝对本地图片路径，通过 OpenAI Chat 发送混合文本与图片，持久化规范 Session media reference，并在源文件删除后仍可 replay。 |
| CLI non-vision attachment | `TestLiveBinary_CLIRunNonVisionAttachmentWarnsAndProjectsUnavailableText` | 编译 binary 在 stderr 告警、保持 stdout 可用、用明确 unavailable/no-guess Provider text 替换图片数据，并仍完成 Turn。 |
| CLI exec Tool | `TestLiveBinary_CLIRunExecCommandTool` | 编译 binary 运行 `juex run --debug --json`，从 `${JUEX_EXT_DATA_DIR}` 解析 Extension 默认值，把它暴露给普通 OpenAI Chat `exec_command` Tool Call，重放 Tool result，并持久化 transcript 与 debug artifact。 |
| Debug bundle CLI | `TestLiveBinary_BundleCreatesRedactedArchive` | 编译 binary 运行 `juex bundle --session ... --out ...`，写入 tar.gz archive，并验证 Session/env secret 已脱敏。 |
| Agent state isolation | `TestLiveBinary_IgnoresWorkspaceStateAndRebindsAgent` | 编译 binary 保持 Workspace Runtime-state path 不变且不进入 Agent state，保留 Workspace config，移动后重绑，并拒绝复制的 marker。 |
| Ephemeral identity isolation | `TestLiveBinary_EphemeralStateLifecycle` | 编译后的 `run` 与 `repl` 使用临时 state、支持保留后检查、保持已标记持久 state 逐字节不变，并阻止 read-only command 创建身份。 |
| Ephemeral listening | `TestLiveBinary_EphemeralListenEndpointAndCleanup` | 编译后的 `listen --ephemeral` 在 durable registry 外发布可达规范 endpoint、对 Fleet 不可见，并在 shutdown 后删除临时 state。 |
| Lifecycle Hook | `TestEndToEnd_CommandLifecycleHooks` | Command Hook 跨 App、config、Runtime、Session、Tool 与 Event JSONL 组合，实现 prompt context injection、pre-Tool denial 和 stop continuation。 |
| Web Turn API | `TestWeb_TurnRoundTripPersists` | Web Session creation、Turn submission、async completion 与 persisted transcript read。 |
| Web compaction cancellation | `TestWeb_InterruptCancelsCompactionWithoutPersistingMarker` | Manual compact 声明可中断；Web Stop 取消 Provider request，报告 `Compaction canceled`，且不在 transcript 留下 compact marker。 |
| Compaction reasoning budget | `TestEndToEnd_AnthropicCompactionRecoversFromReasoningBudgetExhaustion` | Streaming Anthropic Adapter 与 Runtime 从仅 reasoning 的 160-token response 通过一次有界 retry 恢复，保留 Goal/Notes input，只提交完整 text，并继续 Session。 |
| Web pending input | `TestWeb_CentralizedPendingInputLifecycle` | Web submission 通过 App 携带 Framework-owned start action，在活跃 Provider Call 期间 queue 第二个 input，把它 drain 到下一 request，并把两个 input 记录为 durable processed。 |
| Web Observables | `TestWeb_ObservablesStartAndSurfaceObservation` | Workspace Observable config 启动真实子进程、记录 Observation、交付到 Active Session，并通过 Web API 暴露 status。 |
| Schedule catch-up delivery | `TestWeb_ScheduleCatchUpAutomaticallySurfacesObservation`, `TestScheduleDeliveryVisibleRequiresBothSnapshots` | 自动 catch-up 到达 Provider，并等待 HTTP delivery snapshot 与 journal event，包括在连续 read 之间完成的 delivery。 |
| Fleet Web Session switch | `TestFleetWebNewSessionRejectsStaleEventReconnect` | 编译 binary、Agent listener 与 Fleet proxy 在旧 EventSource 重连时保持 `/new` Session active，同时保留 historical read。 |
| Fleet failed-Turn restart | `TestFleetRestartContinuesFailedTurnOnce` | 单个和批量 restart 在同一 Session 中继续真实失败的 Provider request 一次，不重复原 input；再次重启已完成 continuation 不产生新 request。 |
| Fleet Workspace environment | `TestFleetChildrenLoadIndependentWorkspaceDotenvOnRestart` | 两个编译 Fleet child 向 MCP 加载不同 Workspace `.env` 值、保持隔离，并只在对应 child restart 后采用变更值。 |
| Fleet Extension environment | `TestFleetChildrenLoadAgentScopedExtensionDefaultsOnRestart` | 两个编译 Fleet child 把一个选中 Extension 默认值解析到不同 Agent data directory，保持 process-lifetime snapshot，并只对 restarted child 应用 manifest edit。 |

`TestLiveBinary_LoadsSkillsAndMCP` 通过 `uv run --project <repo> python ...` 运行 Python Fake MCP Server。`mcp` SDK 依赖由仓库 `pyproject.toml` 与 `uv.lock` 管理，不使用 PEP 723 script header 或 `uvx`。

Pending-input readiness failure 会包含有界 Session Event-journal tail 和 goroutine snapshot。使用 terminal Event 和 blocked call site 区分早期 Turn error 与 IO/scheduling delay；一次通过的 rerun 本身不能确定此前 timeout 的原因。

## Live Integration

带 build tag 的 live integration test 默认不运行，因为它们使用 credential 与真实 Provider：

```bash
go test -tags=integration ./tests/e2e/... -run '^TestLiveConfigs_' -count=1 -v
```

它们从 `JUEX_PROVIDER_CONFIG` 或 `~/.juex/juex.yaml` 读取顶层 Model。设置 `JUEX_PROVIDER_SMOKE_ONLY=provider:model` 选择一个已配置 override；integration 要求完整 Model ref。uv 管理的 eval helper 会写入与 Provider smoke 相同的隔离最小 Provider/Model 配置。进程环境或源 YAML `environment.variables` 中非 selector 的 `PROVIDER_API_*` credential 和 tuning override 保持正常优先级。Live case 覆盖：

- 普通 completion；
- read-Tool use；
- 多步骤 write/edit/exec_command 工作流。

`TestIntegration_ExtensionObservableSandboxGrantsCurrentAgentStateDir` 使用平台 sandbox 证明 Extension Command Observable 可以写 Workspace 与当前 Agent 拥有的任何 state，但不能写其他 AgentStateDir。

保持 live prompt 客观且可自评分：应断言具体字符串或文件系统 effect，不断言主观回答质量。

Live Provider smoke、compaction quality evaluation 与 development validation record 位于 `tests/eval/`；见 `tests/eval/README.zh.md`。

## 覆盖规则

- 每项新行为都增加 unit test。
- 行为跨越 config、CLI、Runtime、Session、Web、Provider、MCP 或 filesystem boundary 时增加或更新 E2E。
- 除非目标明确是 Provider 质量，否则优先本地 Fake Provider/MCP Server，而不是 live credential。

## 最小运行矩阵

使用仍能覆盖变更行为的最小运行集合：

| 层 | Case 集合 | 何时运行 |
| --- | --- | --- |
| Go unit/package test | `make test` | 每次 production code 变更。 |
| Race suite | `make race` | Concurrency、shutdown、Runtime、MCP、Tool、Event、Session 或 Web 变更。 |
| Non-live E2E | `go test ./tests/e2e -count=1` | 跨 package boundary 的 CLI/Runtime/Session/Provider/Web 行为。 |
| Integration build tag | `make integration` | 先跑 deterministic tagged contract，再跑使用 `JUEX_PROVIDER_CONFIG` 或 `~/.juex/juex.yaml` 的 credential-backed `TestLiveConfigs_*` check。 |

当变更影响 eval harness、Provider smoke、compaction quality 或 development validation record 时，从 `tests/eval` 运行 evaluation-layer check。
