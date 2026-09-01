# Juex 架构

> [English](ARCHITECTURE.md) | 中文

> 实现指南。请结合 `DOMAIN.zh.md`（规范产品语言与不变量）、`PHILOSOPHY.zh.md`（产品与工程原则）及 `DESIGN.zh.md`（Web UI）阅读。本文说明代码如何组织：Module、interface、data flow、storage 与 test strategy。
>
> 原则：以**覆盖 v0.1 全部 must-have 的最简单 prototype**作为首个发布版本。

---

## 1. 端到端目标

分层 runtime-status state machine 及 snapshot + cursor boundary 见 [`docs/runtime-status.zh.md`](docs/runtime-status.zh.md)。Runtime 完成：

```text
CLI 输入 prompt
  -> 由 AGENTS.md + Skill + Module context + bounded runtime section 组装 system prompt
  -> 调用 Anthropic 或 OpenAI-compatible LLM
  -> 独立 Tool Call 并行，model-owned state call 按 Provider 顺序执行
  -> 持久化 conversation 并发出 Event
  -> 追加 JSONL 到 $JUEX_HOME/agents/<agent-id>/sessions/<id>/
```

---

## 2. 仓库布局

```text
juex/
├── cmd/juex/main.go              # CLI entry 与 bootstrap import
├── .agents/skills/               # project-local Skill
├── frontend/                     # React + Vite
├── internal/
│   ├── app/                      # process composition、slash command、Session attachment、Turn admission
│   ├── agentstate/               # resident/ephemeral identity、marker、registry、address
│   ├── artifact/                 # 安全 Artifact storage 与 integrity
│   ├── usermedia/                # Session image upload 与 media-reference policy
│   ├── eventmedia/               # external-event attachment validation/admission
│   ├── cli/                      # Cobra CLI
│   ├── version/                  # ldflags build metadata
│   ├── config/                   # YAML、import/LKG、shell、Codex auth
│   ├── environment/              # dotenv、immutable snapshot、propagation metadata
│   ├── cancellation/             # user/signal/runtime-restart typed cause
│   ├── errorclass/               # runtime error classification/public wording
│   ├── extensions/               # Home/Workspace Extension discovery
│   ├── homestore/                # crash-safe lock、atomic replace、directory sync
│   ├── providerreadiness/        # Provider selection/credential/connectivity checks
│   ├── chunkedwrite/             # canonical chunk lifecycle fact/state
│   ├── bundle/                   # portable debug tar.gz
│   ├── events/                   # Event envelope、Bus、Catalog、durable sink
│   ├── eventcatalog/             # stable schema/codec/validation/replay policy
│   ├── hooks/                    # trusted lifecycle command hook
│   ├── observable/               # source adapter、Observation lifecycle/store/tool
│   ├── observability/            # redacted Session log
│   ├── fleet/                    # Agent registry health/lifecycle policy
│   ├── fleetservice/             # launchd/systemd/Termux registration
│   ├── fleetweb/                 # Fleet API/SSE/proxy/SPA
│   ├── processidentity/          # process incarnation fingerprint
│   ├── processmetrics/           # RSS/CPU sampling
│   ├── llm/                      # canonical Message/Block、Profile、adapter
│   ├── toolevents/               # Tool Event contract/constructor
│   ├── statusapi/                # runtime status transport contract
│   ├── statusstream/             # snapshot/cursor replay/fan-out
│   ├── tools/                    # registry 与 builtin
│   ├── modules/                  # trusted Feature Module adapter
│   ├── mcp/                      # official SDK adapter
│   ├── skills/                   # SKILL.md loader
│   ├── frontmatter/              # shared YAML frontmatter parser
│   ├── prompt/                   # system prompt assembly
│   ├── session/                  # transcript、metadata、history、lock
│   ├── runtime/                  # Turn loop、pending input、context、compaction
│   ├── endpoint/                 # Agent listener/URI/runtime.json
│   ├── sandbox/                  # command/file policy
│   ├── netbootstrap/             # minimal environment DNS/TLS fallback
│   └── web/                      # Agent HTTP API/SSE/embed
├── tests/e2e/                    # cross-package 与 integration
├── tests/eval/                   # live Provider/quality evaluation
├── .github/workflows/            # CI、integration、release
├── docs/superpowers/             # 历史 spec/plan
├── .goreleaser.yml
├── scripts/                      # installer、ripgrep、build
├── release/ripgrep-assets.tsv
├── Makefile
├── pyproject.toml / uv.lock
├── go.mod / go.sum
├── README.md / DOMAIN.md / PHILOSOPHY.md / ARCHITECTURE.md / DESIGN.md
├── AGENTS.md / CLAUDE.md -> AGENTS.md
├── mcp.json.example
└── juex.yaml.example
```

Package unit test 与源码同目录。跨 package 产品 test 在 `tests/e2e/`，evaluation contract 与 live helper 在 `tests/eval/`；两者属于同一 Go module，可 import `internal/...`。

## 2.1 Module 权责图

Juex 是一个 bounded context；下列是实现 Module，不是 context。`DOMAIN.md` 定义概念，本文确定实现决策的位置。

| Module | 负责 | 不负责 |
|---|---|---|
| `agentstate` | Resident/Ephemeral Agent identity、Workspace marker/registry、Agent Address、rebind/copy detection、registry-boundary delete | Home storage、endpoint、Fleet lifecycle、Session content |
| `homestore` | portable advisory lock、home lock layout、atomic replacement、directory sync | identity、endpoint、Fleet policy、多文件 service transaction |
| `endpoint` | local binding、URI parse/dial、exact runtime identity publish/probe、instance shutdown、maintenance guard | HTTP route、Agent Address、Fleet registry、spawn |
| `fleet` | registry binding/health、per-Agent lock、reconcile、start/stop/restart、log、config replace、remove/GC | Browser DTO、native service、endpoint scheme、任意 Workspace content |
| `fleetservice` | user-level launchd/systemd/Termux definition/transaction | individual Agent、Fleet address policy、CLI presentation |
| `fleetweb` | Fleet HTTP/SSE、roster DTO、directory browser、verified proxy、SPA | registry/process policy、single-Agent route、frontend policy |
| `processidentity` | 跨平台 PID incarnation/start identity | liveness、runtime schema、Fleet policy、metric |
| `processmetrics` | RSS/cumulative CPU sample 与 interval derivation | poll cadence、health、DTO、format、persistence |
| `extensions` | ordered root discovery、allow filtering、winner selection、source/resource/requirement projection | allowlist inheritance、resource parsing/runtime registration/execution |
| `config` | YAML layering/import/LKG、allowlist、environment order、Provider input、path/policy projection | Extension scan、dotenv grammar、global env mutation、Provider semantic、Turn/HTTP |
| `environment` | name validation、dotenv、immutable snapshot/default/overlay/value-free metadata、single-workspace activation | config discovery、Extension discovery、subprocess/runtime/presentation |
| `providerreadiness` | selection、credential、construction、connectivity readiness | Protocol semantic、fallback、CLI formatting |
| `llm` | canonical message/block、Provider/Profile/Protocol/Capability、wire adapter、transport retry、model health | model-chain fallback、Session、Tool、transport DTO |
| `provenance` | Request Epoch、digest、safe Provider descriptor、bounded dedupe/journal reduction | call timing、credential、journal、UI |
| `runtime` | Turn、Provider iteration、Tool ordering、pending input、fallback/retry、context/compaction、fact emission | SDK retry、Session discovery、MCP lifecycle、transport parse |
| `runtime/module` | Module identity/capability/sealed set、ownership validation、typed policy、resource order/cleanup | concrete feature、Extension discovery、dynamic plugin、Session attach |
| `session` | identity/kind、journals/metadata/history/active/usage/scratchpad/lock | prompt、Provider、Tool、attachment orchestration |
| `cancellation` | typed user/signal/restart cause 与 signal-aware context | Stop admission、Turn reaction、DTO |
| `errorclass` | timeout/cancel/auth/permission/connectivity/endpoint/retry classification | retry decision、cause source、rendering |
| `statusapi` | transport-neutral status DTO/projection/current-only activity adapter | transition、persistence、routing、Fleet replay |
| `statusstream` | replaceable snapshot、bounded cursor replay、sequential stream、coalescing、cleanup | projection、cursor extraction、SSE、Fleet generation |
| `events` | generic Event/Catalog interface、normalization、sync subscribe、commit-before-delivery | stable schema、producer vocabulary、journal、UI |
| `eventcatalog` | builtin schema registry/codec/validation/durability/browser/replay policy | Extension vocabulary、dispatch/storage/projection |
| `toolevents` | Tool Event name/payload/constructor | execution、schema/replay、dispatch/storage |
| `observability` | redacted human-readable Session log | authoritative state、decision、Web |
| `tools` | registry/dispatch、builtin adapter、normalization/output hygiene | chunk lifecycle、wire quirk、Session、Observable/MCP lifecycle |
| `modules/builtintools` | Runtime builtin contribution 与 shell resource | dispatch、sandbox policy、Session state |
| `modules/promptcontext` | project guidance、Session operating context、shell/scratchpad context | framework assembly/order、Skill discovery |
| `modules/skills` | Runtime Skill Tool 与 bounded catalog context | discovery、prompt order、Extension |
| `chunkedwrite` | canonical lifecycle fact 与 deterministic derivation | schema/dispatch、filesystem、Event transport |
| `hooks` | trusted config/match/bounded command/fact | phase ordering、deny interpretation、Tool |
| `sandbox` | model-triggered file policy、writable roots、blocked path、backend/probe/wrapping | AgentStateDir、Shell lifecycle、config、trusted process、approval |
| `observable` | Command/Schedule spec/source validation、adapter、lifecycle、store/delivery | Extension discovery、active Session、Turn admission、Provider、UI |
| `eventmedia` | external attachment validation/size/blocked path/content address | scheduling、MCP transport、user upload |
| `mcp` | official SDK adapter、config、stdio/HTTP session、header、Tool、readiness/notification/diagnostic | protocol framing、Turn policy、active Session、Web |
| `skills` | frontmatter、metadata、catalog/compression/budget | final prompt assembly、execution、dispatch |
| `prompt` | validated typed Module context 的 framework prompt assembly | context collection、order、wire、persistence |
| `artifact` | rooted path safety、atomic bytes、content address、bounded read/integrity | media format、Provider encoding、preview、retention |
| `usermedia` | image validation/limit/Session namespace/reference | byte storage、multipart parse、Provider encode |
| `app` | config/resource resolution、Module composition/wiring、Session attach、admission、external delivery、slash command | capability policy、Cobra/HTTP parse、SDK |
| `cli` | Cobra grammar/flag/presentation/exit category | runtime policy、persistence、Fleet lifecycle |
| `web` | single-Agent HTTP/SSE、DTO、Session cache、cancel/read-only view | domain、Protocol、Fleet policy |
| `frontend` | transcript assembly、visual/interaction、DTO mirror | backend lifecycle/policy/storage/runtime decision |

### 依赖规则

1. 共享决策置于 transport 下层；CLI/HTTP 只 parse/render，admission/Turn/queue 在 App/runtime/session，error 在 `errorclass`。
2. `agentstate` 独占 identity → location；调用方不得用目录 basename 推导 ID/Home path。
3. `homestore` 独占 Home mutation mechanics；policy 留在 identity/endpoint/Fleet/service。
4. 三层方向严格：Foundation 不 import Framework/Feature；Framework 可定义 Agent/Session/Turn/Module contract 但不 import concrete Feature；Feature 可依赖 Framework/Foundation。
5. Provider adapter 只在边界翻译 wire；共享含义进入 canonical LLM value。
6. Config 负责 resolve，不负责 govern；Runtime 收 resolved value。
7. App 是 composition root：构造前过滤 disabled factory、注入 typed dependency；Framework 负责 indexing/order/publication/cleanup，Feature 负责行为。
8. Session 独占 persistence/active metadata，CLI/Web 不复制规则。
9. Event 是 fact，不是 repair command；authoritative state 先改，durable fact 先 commit 再 live delivery，只有显式 transient 可绕 journal。
10. Artifact safety 只有一个 boundary；Media/projection 留 format policy，但 bytes 安全交给 Store。
11. Frontend 镜像 backend read model；正确性规则不在 React 重写。
12. Retry boundary 明确：LLM adapter 负责单次 transport/API/stream retry；runtime 负责 model chain、pending continuation、Turn retry。

架构强制保持轻量：import-only test 检查稳定层级；constructor、narrow interface、unexported state 与 sealed set 表达 ownership，由 owner/feature test 覆盖 registration/publication/replacement/cleanup，不维护全源码 analyzer。

### 2.2 Module set 与生命周期

Module 是编译进 Juex 的 trusted in-process Feature value，只有一个稳定 `module.ID`，注册一次，可实现多个 narrow typed capability。Runtime set 包含 Builtin Tools、project guidance、Skills 及启用的 Side Session/Observable/MCP；Session set 包含 context、Goal、Notes、Hooks 与 caller module。

```go
type Module interface { ID() ID }
type ToolProvider interface { Tools(context.Context, ToolContext) ([]tools.Tool, error) }
type ContextProvider interface { Context(context.Context, ContextRequest) ([]ContextSection, error) }
type TurnInputPolicy interface { ApplyTurnInput(context.Context, TurnInputRequest) (TurnInputDecision, error) }
type ToolPolicy interface { ApplyTool(context.Context, ToolPolicyRequest) (ToolPolicyDecision, error) }
type FinishPolicy interface { EvaluateFinish(context.Context, FinishRequest) (FinishDecision, error) }
```

Provider 返回 value，不修改 serving registry。Framework 先 freeze identity/order/index，再启动 scoped resource，最后 materialize Tool；Runtime/Session union 完整验证后才一次发布 registry。重复/非法 Module ID、Tool name、context key 会同时报告 owner；sealed set 只能取 defensive snapshot。

Runtime factory 仅收 immutable runtime identity/path；Session factory 仅收 immutable Session identity/path。Composition root 在调用 constructor 前过滤 `enabled:false`，只注入显式 dependency。生命周期：

1. Resolve config 与 Extension reference。
2. Filter/construct/register/freeze Runtime set。
3. 按注册顺序 start resource，再 materialize/validate Tool；失败逆序关闭已启动 resource，并 join cleanup error。
4. Attach/lock Session。Startup/standalone `new_primary` 可更新 active history；resident replacement 只 prepare+lock，不先改 history。构造/freeze/start/validate Session candidate 后才可发布。
5. 从 sealed catalogs 构造完整 registry，与 Session、prompt builder、dependency 一起通过 Engine replacement transaction 原子发布；失败保留旧 bundle。
6. Commit 后逆序 quiesce/close 旧 Session set；post-commit cleanup 仅诊断，绝不回滚/删除新 Session。
7. Shutdown 先停 admission、quiesce Runtime（Session delivery 仍可用），再 quiesce/close Session、release persistence、逆序 close Runtime。Deferred quiesce 可 wait/retry；尝试所有 cleanup 并带 Module/phase join error。

Context request 标明 `session_start`、`turn_preparation`、`provider_iteration`，携带 cancellation 与只读 identity。Section 保留 key/source/path/owner/scope/purpose，并验证 system-prompt vs runtime-message projection 及 bounded/unbounded budget。Goal/Notes 使用稳定 runtime-message ID；guidance/Skills/scratchpad/shell/context 使用 system prompt。Status 从 sealed catalog 投射 Tool/owner。

Extension 与 Runtime Module 是不同边界：Extension 只是 Skill、MCP、Hook、Observable、env/private data bundle，不加载 Go plugin/dynamic library；source 为 `ext:<name>`，mutable data 位于 `JUEX_EXT_DATA_DIR`。

Turn input policy 只在 durable admission 与 transcript repair 后运行；Tool policy 在完整 batch declared 且各 call durably started 后运行；Finish policy 按 Module 顺序全部 evaluate 后，首个仍有效 continuation 胜出，Framework pending input 是最终 completion authority。Observer 只接 committed fact，不改 flow。Framework 不提供 untyped callback、priority、dependency DAG 或 dynamic registration。

---

## 3. 核心接口

### 3.1 LLM Provider

Canonical type 位于 `internal/llm`：`Message{ID,Role,Model,Kind,Blocks}`，Block 为 text/image/reasoning/tool_use/tool_result 等；`ProviderProfile` 包含 ID/Type/Protocol/BaseURL/APIKey/Model/ThinkingEffort/Header/Query/Capabilities/Compat；`Provider.Complete` 与 optional `ProviderWithOptions.CompleteWithOptions` 返回 canonical `Response`。`CompleteOptions` 支持 Purpose、MaxOutputTokens、CachePolicy、RetryObserver、OnDelta、StreamIdleTimeout；`OnDelta` 只发 text/reasoning。

Public custom protocol 为 `anthropic/messages`、`openai/responses`、`openai/chat`；`openai-codex/responses` 仅供 `openai-codex` preset。Preset 固定 protocol：openai=Responses、openai-codex=Codex Responses、anthropic=Messages、deepseek=Chat。未知 custom provider 必须显式 protocol；custom Chat 默认启用 reasoning effort，endpoint 拒绝时显式关闭。

`config` resolve `ProviderSelection`，`llm.NewProvider` 构造 adapter，`providerreadiness` 做 selected-runtime/credential/hello 检查。Streaming adapter 把 fragment 投射为 neutral delta，最终仍返回 canonical Response；idle watchdog 默认 90s，可 override/disable。`capabilities.streaming:false` 走 blocking。

SDK type 只在 adapter。HTTP SDK 对 recoverable network、408/409/429/5xx 最多 retry 10；普通 request error 立即返回。Codex SSE 对已开始的 stream-read error 有第二层分类 retry，context cancel/deadline 不 retry；delta 可丢弃且无 side effect，所以发过 delta 后仍可 replay，并以 `llm.retry` 记录 provider/model/transport/attempt/delay/reason/exhaustion。Semantic `response.failed` 不 retry。Responses/Codex SSE 的 stream-idle 最多 replay 一次完整 request，最终错误说明 two-attempt budget，并分类 deadline timeout。

Responses wire tool-call ID 限 64 字符，adapter 映射为稳定 hash，matching call/result 共用映射而 canonical history 不变。统一 projection helper 在编码前 compaction、校验 Tool transcript、按 capability filter Tool/reasoning replay、normalise schema、用 lifecycle fact 折叠 committed chunk write，并修复 argument JSON fallback；protocol-specific struct/cache/decoding 留在 adapter。Persisted transcript repair 在 session/runtime boundary；非法内容到 wire edge 仍 fail loud。

Malformed stream event 包装为 `StreamParseError`（kind、provider:model、event type、optional block index、bounded raw preview）。Anthropic streaming 以 SDK `message_start` usage 为准；兼容 endpoint 只在 start input usage 仍为 zero 时用 `message_delta` non-zero value 填充，绝不覆盖标准值。

Capability 决定 wire feature：Tools off 时省略 specs/history；reasoning effort/replay off 时不发相应 field。OpenAI-compatible Chat 可 replay `reasoning_content`/`reasoning`/`thinking`，Anthropic replay thinking block，Responses 存 reasoning item ID + encrypted content。Codex 本地保存 reasoning output，但 `store=false` 时不 replay 不持久的 item ID。Anthropic 配 effort 时使用 adaptive thinking + `output_config.effort`；empty effort 启用 adaptive 默认。DeepSeek 用 Chat `reasoning_effort`，默认只 replay `reasoning_content`。

### 3.2 Tool

`ToolDefinition` 唯一定义 name/group/description/schema/timeout，再 bind Handler；`Normalized` 统一 object schema。Timeout 为 `bounded` 或 `disabled`，前者显示 capped seconds，后者表示 Tool 自管 lifecycle。`Registry.Specs` 不向 Provider 暴露 group/timeout。Group 固定为 `file`、`chunked_write`、`shell`、`search`、`skill`、`session_state`、`observable`、`mcp`。

Skill 是 Markdown resource，不是 executable Tool。Prompt 含紧凑 catalog，`skill_search` 搜索全部 loaded entry，`skill_load` 取全文。低频 `chunked_write`/`session_state`/`observable` 只在 schema 留 purpose + builtin guide pointer；调用不依赖先读指南。发生 error 后 runtime 在持久化/failure ledger 前给 model-visible result 追加 remediation，但已发 Tool Event/structured classification 保留原始 error。

Builtin：`read`、`write`、`edit`、`apply_patch`、`write_begin/chunk/commit/abort`、`exec_command`、`write_stdin`、`list_shell_sessions`、`grep`、`skill_search`、`skill_load`。`BuiltinOptions` 注入 WorkDir/Shell/SessionManager/Search/Sandbox/default timeout/DisableApplyPatch/providers。

File Tool 相对 WorkDir。`apply_patch`/chunked write 只接受 workspace-contained path，拒绝 symlink escape，并保存 normalized relative identity；`PathGuard` 再执行 blocked path。Directory grep 不 follow symlink，显式单文件 symlink 可搜索 target。Chunk manager per-registry in-memory，可从 canonical lifecycle fact + matching input 恢复；commit/abort 后 Provider history 仅保留 compact summary，缺 fact 的旧 transcript 不猜状态。Begin/commit 都重新校验 symlink boundary；Event 只持久化 normalized relative path。

Runtime hard timeout 不进入 Tool schema，默认可被 Tool metadata override、上限 300s；`exec_command`/`write_stdin` 自管 lifecycle，`yield_time_ms` 只定义观察窗口。Timeout 作为普通 error result，保留 bounded captured stdout/stderr。Unix cancellation/cleanup 终止 process group。Deadline error 统一为 timeout，Event 带 `error_kind:timeout`、`timed_out:true`、`raw_cause`；user cancellation 为 `cancelled by user`；SIGINT 为 interrupted，SIGTERM/SIGHUP 为 terminated，并保留 signal details。

`exec_command` 总通过 shared session manager 启动，只等 yield；仍运行则返回 numeric `session_id`。`write_stdin` poll/write/interrupt；`list_shell_sessions` 可恢复 ID，默认只列 running。Active session 以 bounded metadata/command summary 进入后续 prompt，不含 output。Empty poll 自有 observation window，不因 `runtime.tool_timeout` 小而 kill。

Sandbox enabled 时，process 必须经 runner 包装或 fail closed；`write_stdin` 只操作 creation-time session。Linux/macOS 把 `TMPDIR` 置于 AgentStateDir，host temp read-only，workspace 可写。`blocked_paths` 同时约束 command backend 与 file Tool；bubblewrap 对不存在的 blocked path 无法 mask 时 fail closed，不创建 host mountpoint。

### 3.3 Event

`events.Event` 是 open envelope（ID、Type、Version、Timestamp、SessionID、TurnID、Data、Transient）。Catalog 定义 stable schema/codec/validation/durable/browser/replay policy。Bus 的 `Emit` 顺序为 normalize/validate → durable sink commit → synchronous projector → asynchronous live adapter。Durable append 失败时不 projection/delivery；Required request Event 在 Provider/Hook/Tool 外部 side effect 前 commit。Transient 不写 journal，SSE 无 `id`，不会推进 replay cursor。

Runtime-status projection 先运行，Web 随后把 committed Event 与该 exact status snapshot 合为 `BrowserEvent`。Replay 以 durable event ID 为 public cursor，按 JSONL 顺序重建 status 后过滤；Tool terminal state absorbing，terminal 后 late output 不进 browser。`ReadCommitted` 在 commit barrier 下捕获固定 byte prefix，释放 barrier 后 decode，保证 replay snapshot 与 publish 顺序可比较而不长期阻塞 disk read。

### 3.4 Guidance 与外部 Memory

`internal/prompt` 直接读取 optional user-global、project root、project resource dir 的 AGENTS.md hierarchy；它是稳定 guidance，与 Memory 实现独立。

First-party Memory 是 `juex-extensions` 的普通 external bundle：Skill 提供 guidance，MCP 提供 search/write/delete，Hook 维护 index。Core 只看 source=`ext:memory` 的 generic resource，没有 Memory store/Tool group/prompt branch/in-process Module；mutable entry/index 在 Agent-private `JUEX_EXT_DATA_DIR`。

Session/Extension data 位于 `$JUEX_HOME/agents/<id>/`；project resource 位于 `.agents`。默认加载 `~/.agents`，除非 `enable_user_agents_resources`/flag 关闭。Project MCP/Skill 同名覆盖 user；AGENTS.md 按顺序拼接。此 switch 不影响 Extension，后者由独立 `extensions.allow` 选择。

### 3.5 Session

Session 拥有 ID/Alias/Kind(`primary|side`)/Active/Dir/History/TokenUsage/ContextUsage；`Info` 的时间以 UTC RFC3339 进入 JSON。

`conversation.jsonl` 与 `events.jsonl` 是独立 versioned journal，每条含 kind/session/contiguous sequence。Writer 先编码完整 batch，只追加 newline-terminated record，再 sync；write/sync 失败 truncate+sync 回旧 offset。Replay 会丢弃并持久截断 incomplete final record；unknown version、wrong identity、sequence gap、完整 corrupt record 为 hard error。Metadata/checkpoint/history/rewrite 通过 `homestore.WriteFileAtomic`（sync temp、atomic replace、sync parent）。

Bus 是 app runtime durable boundary。Tool batch 先 commit `llm.responded` + 全部 ordered `tool.requested`，各 call 在首个可能产生副作用的 Hook/handler 前 commit `tool.running`，terminal Event 含 exact Provider-visible result，并在 result message 与下一 Provider request 前 commit。`Close` 停止新 commit、drain live delivery、sync journal，重复调用结果稳定。

Resume 统一用 `LoadWithOptions`。启用 repair 时，用 durable Tool facts 修复 unresolved assistant `tool_use`：已有 terminal outcome 恢复 exact result；仅 declared 写 `TOOL_NOT_STARTED`；started 无 terminal 写 `TOOL_OUTCOME_UNKNOWN` 并发 `tool.outcome_unknown`，绝不自动 retry。Ordered repair batch 先入 conversation，再入 `transcript.repaired`；若事件 commit 中断，下轮识别已持久 synthetic result，只补 evidence。Catalog-backed Bus 在拿到 lifetime lock 后执行。

Usage 从 `llm.responded` Event 恢复进 Session Info。Status read 通过 `ReplayEventsWithCatalog` 流式 reduce，只保留 bounded status history。Raw `ReplayEvents`/`ReadEvents` 只作 storage adapter；跨 Module semantic consumer 使用 `WithCatalog`。

`conversation.jsonl` 始终 canonical/inspectable。`session.json` 的 bounded checkpoint 含 transcript fingerprint、content SHA-256、turn/preview、latest compact marker byte、explicit retained pre-compact location、repair safety，并以 versioned checksum 覆盖。Matching safe checkpoint 只读 retained row + active suffix；recent paging 校验 sealed compact row 后从尾部反向扫描，不重建 suffix index。Invalid/stale/missing fallback full scan，并在下次 append 更新。Windows 额外验证 content digest；平台无稳定 file identity/change time 时 fail closed。

Resident Session 在 append 前比较 open file 与 canonical path、hash adopted prefix；`conversation.lock` 串行化 fingerprint check、append、metadata replace。发现 external complete suffix 时 canonical rescan 并 adopt；识别 own batch 即便被 external append 位移，也不把已 committed batch 报失败。Canonical append 已确认后，metadata/history refresh failure 变为 resident retry obligation，而不是诱导 caller duplicate；下次 mutation/Close 修复。Event journal 与之独立。

Unresolved Tool Call 令 checkpoint repair-unsafe；后续 result 仅在 hidden prefix 已验证时恢复 safe，否则 full scan。`session.json` 用正 epoch millisecond `started_at_ms/last_active_at_ms`；创建同时设置，每次成功 append 更新 activity/checkpoint。时间不从 Session ID 或 mtime 推断；缺 owned timestamps 的 pre-release directory 不列表，直接 load 返回 `ErrSessionTimeUnavailable`。

每个 persisted Session 有 `scratchpad/`。Eager session 随 transcript 创建，lazy session 首次 persistent append 创建，load 确保存在。Session package owns path/delete boundary。App 删除前 preflight Session + `artifacts/sessions/<id>`，先 commit Session deletion；Artifact cleanup 失败返回 typed partial failure，可重试删除 orphan namespace，不影响 Agent-level artifact。

`observability` 从 Bus 写 redacted `logs/juex.log`/`debug.log`；structured diagnosis 直接用 events journal。每个 Agent 的 `history.json` 为 `{active_id,sessions}` derived cache，entry 只含 ID、transcript turn/preview 与 `{size,mtime_ns,change_id}` fingerprint；canonical metadata/usage 留在 session/event。Change identity 用 file identity + ctime/Windows ChangeTime，平台不可靠则缓存 fail closed。

只有一个 active primary；`run`/`repl`/`listen` 默认 attach，`--new`/`/new` 创建并切换。Side durable/listed，但从不 active，Web 不可 turn。普通 activity 只在仍选中时刷新 cache，旧 live Session 的 late append 不得覆盖新 activation。

App attachment 选择/记录 target，并返回 `attach_active|new_primary|new_side|resume` lock mode。优先有效 active primary，其次其他 disk primary，再新建。Standalone new_primary 先持久选择后拿 lock；resident replacement 用 narrower prepare path，只 create+lock，在 runtime publication + Session-start policy 成功后才 compare-and-set history。History 使用 process-owned OS lock，不按 mtime 过期；activation 与 conditional candidate delete 共用 root guard。

App lifetime lock 为 `sessions/<id>/session.lock`。Startup 用 AgentStateDir `.locks/sessions/` root guard 串行 selection/load/create/lifetime lock，再用 per-Session guard cleanup；delete 同时拿两者。Stale lock 只有在 PID dead、PID reuse，或 unreadable 且足够旧时清理；无法读 process start 的平台对 live PID fail safe。

Web new Session 为 lazy：`POST /api/sessions` 先分配 in-memory active primary，首次 append 才创建 conversation/scratchpad；读 scratchpad 不持久化。CLI run/repl 仍 eager。

`session.List` 严格扫描，`LoadInfo` 返回 summary+messages；`ListWithHistory` 用 fingerprint-valid cache，并从 event tail 最多后读 8 MiB 取 usage，缺失字段保持 unset。Stale summary full scan 后在 history lock 下 recheck fingerprint 并 repair cache，不改 active_id。Recent page 独立用 checkpoint/reverse reader并保持 Tool pair boundary；inactive response 的 summary/revision/page 来自同一 snapshot，避免 concurrent append 混代。

### 3.6 App + Runtime

`app.New(Options)` 组合 Config、optional injectable Provider、ModelCandidates/Health、WorkDir、MCP manager/flags、ResumeDir、Alias、SessionMode、LazySession；App 暴露 Run、RunWithAttachments、REPL、Close。`run`/`repl` 各自拥有 process-local MCP manager；`listen` 先保证 active primary，再创建 shared process MCP manager。HTTP listener 可先启动，但 Session open 等待 MCP warmup，使所有 Web Session 复用 manager。

`App.AdmitTurn` 把 input 分类为 started、queued、command-completed、conflict、rejected、error。App adapter 只保留 command/compaction exclusion，普通 start-vs-queue、durable state、retry 与 Framework Turn identity 交给 `Engine.ReceivePendingInput`。Manual compact 的 `turn.admitted` 带 `operation:compact`，即使 pre-compact Hook 失败也不丢 queued input。

新 main input 在返回 started 前写 non-replayable `accepting` intent（`origin:turn` + stable message ID）；Engine 建立 active Turn、commit `turn.admitted` 后 promotion 为 `admitted`。Promotion 前失败尽力 drop；即使 compensation 失败，除非 matching durable admission Event 证明 crash 发生在 commit 与 promotion 之间，否则 accepting 不恢复。已有 replayable pending record 在同一 checkpoint 前始终保持 replayable。Policy 可改 content 但不可改 Framework identity；append 后标 processed。Crash 后未处理 admitted record 会重新走 policy/projection，不绕 rejection/transform。

Pending journal 首次建立 latest/replayable index，后续 transition 只更新 index 并校验 file fingerprint。Session attachment 用 committed admission ID + complete transcript reconciliation，返回 opaque recovery handle；App 在 startup barrier 后依次执行，normal restore 按 acceptance order drain，避免重复 message。

Engine 包含 Provider、ModelCandidates/Health、Tools、Bus、Session、Prompt、`MaxPendingInputs`（默认 16）、`ContextWindow`（默认 256000）、Compaction policy。Session switch 原子发布 `SessionRuntimeSnapshot`：Session、scratchpad-aware Prompt、persistent queue、sealed Session Modules、Tool registry、Hook path 同代。Goal/Notes 由 Session Module Store 拥有，不是 Engine field。`ReplaceSessionRuntime` 与 Turn/compaction 串行，并拒绝 active reservation 或 in-memory pending input。

App replacement coordinator 先 prepare/lock candidate，不改 history；在 reader 仍见 old Session 时构造/start/validate candidate Modules/registry。拿 App write lock 后 capture Engine checkpoint，安装 bundle、重定向 Event sink/recorder、执行 Session-start policy；active history compare-and-set 是最后 fallible pre-commit gate。成功后才发布 App Session/status/chunk state/lock。失败恢复 Engine、sink、recorder 与 exact previous history；只有 candidate 非 active 才删 persistence。Commit 后才释放 old resource，cleanup error 不回滚新 authority。Lock order：App lifecycle → Engine Turn mutex → Engine Session-runtime lock → Pending mutex → Session/store。

#### Durable Turn lifecycle ownership

| 关注点 | Control owner | Durable authority |
|---|---|---|
| Admission | App 分类/执行，runtime 决定 start/queue 与 identity，再走 input policy | pending journal、`turn.admitted`、transcript |
| Pending input | runtime receive/state/recovery/drain/process/completion | `pending_input.jsonl` + stable message IDs |
| Tool | runtime 排 batch/policy，tools 执行，toolevents 构造 payload | `llm.responded`、完整 ordered requested、running/input-resolution、含 exact result 的 terminal |
| Session replace | App transaction/lock，runtime snapshot，session history/lock/journal | private candidate journals；active history final gate；App ref commit |
| Finish | runtime lifecycle；module typed policy | assistant response、selected state、durable continuation、terminal Turn event |

新 main input 的 durable restart authority 是 pending journal + conversation + cataloged Event，不是 transport state。Finish 只在 `llm.responded` durable 后 evaluate 全部 ordered policy；Framework 先 durable-admit selected continuation，才通知 observer；`finishActiveTurnIfNoPending` 是最终 completion gate，之后才能 commit `turn.completed`。`PolicyObserver.Requested` 可 fail closed；其他 observer 只报告。

`TurnMessageWithID` 是稳定 entry；internal `turn_lifecycle.go` 明确 context/provider/tool/finish/closure phase。Provider candidate 使用 process-local circuit breaker：30s、1m、2m、5m cooldown + single-request half-open。LLM owns breaker，runtime owns replay/preflight/`llm.fallback`/optional `model_change`。`notify_model_changes` 默认 false；成功 switch 时 notice 与 response 原子 append，失败 attempt 无 notice。Fallback 可发生在 provisional delta 后，因为 delta 不能含 executable Tool；browser 在 fallback 清 abandoned projection。

Turn 无 per-request-count 或 wall-clock cap；以正常无 pending 完成、parent/user cancel、既有 provider/tool/context error 或 compaction failure 结束。`llm.requested.iter` 只观测。Engine 只有一个 active operation cancel-cause，可由 Web Stop 取消 Web/MCP/Observation 发起的 work。User cancel 统一 `cancelled by user`；process signal 保留 SIGINT/SIGTERM/SIGHUP。

Compaction pure budget 位于 `runtime/contextbudget`，Engine 保留 lock/provider/Event/calibration。Retain recent direct/mcp_event/observation，排除 model_change/system_notice，保持 active Tool Call/Result closed suffix；canonical transcript 不变。Independent Tool call 并发，`get_goal/create_goal/update_goal/update_notes` 等 model-owned call 按 Provider order 串行，result 按原顺序回贴。

Active Turn 可 queue user/critical external input，最多 `MaxPendingInputs`，overflow loud reject，仅在下次 Provider call 前 drain。Journal 记录 stable ID/state/timestamp/attempt/expiry。Restart 恢复 unexpired pending/admitted，忽略 unproven accepting，跳过 transcript 已有 ID，并标 processed。Startup barrier 阻止新 Turn 抢先。General transport/timeout failure 且有 pending 时 drain 后继续同一 Turn；user Stop/auth/permission 等 terminal failure 把 accepted input 入 transcript 后结束，不标 dropped。

### 3.7 CLI（Cobra）

CLI 树包括：`init`、`doctor`、`run`、`repl`、`sessions list/show/continue/activate/context/compact/delete`、`listen`、`fleet serve/install/uninstall/status/start/stop/restart/logs/gc`、`bundle`、`version`。Root-local `--version/-v` 输出 short build line，非 persistent；`juex version -v` 仍是 subcommand verbose。

`init` 用保守 YAML node edit 写/合并并经 config 验证；`doctor` 只读，把 provider readiness、shell、MCP、Skill、value-free environment 汇总，`--offline` 跳过 Provider/remote MCP 网络。`bundle` 是 `internal/bundle` 的薄 adapter；package 负责 collection/tar/manifest hash/runtime/config/env/optional artifact/redaction。Runtime env value 永远是 mandatory redact input，即使 `--redact=false`；manifest 不 hash 自己。

Root 把 Ctrl-C/SIGTERM/Unix SIGHUP 写入 cause-aware context。普通 cancel 与 signal 在 stderr/JSON 中分别保留规范 wording/details。`run --attach` 可重复、从 workdir resolve，Session 确定前完成 validation，之后把已验证 bytes 写 artifact 而不重读 source；无 prompt 可 image-only。Dry-run 只校验 metadata。REPL `/attach` 暂存到下个 ordinary prompt；local status/compact 不清，Session switch 成功后清。Vision disabled 产生 non-blocking structured warning。

Workspace flags `--config/-C/--models` 不出现在 Fleet help，提前传入仍被 guard 拒绝。Fleet 允许 `--enable-user-agents-resources`、`--debug`、`--log-level`、`--verbose`。Cobra tree 是 command/flag inventory source of truth；全 subtree rejected flag 必须从 help 移除。

每个 executable command 注解 agent-state policy：run/repl/listen=`mint`，sessions/bundle=`existing`，independent=`none`；缺 annotation fail。`ResolveExisting` 只读验证 marker/registry/binding，不建 lock、不改 excludes、不 migrate/rebind；移动 Workspace 必须先运行 stateful command。

`--ephemeral` 与 dry-run scratch 用 `AgentStateNone` 后绑定 private temp `agents/<random-id>`，endpoint lock 在 temp root，真实 Home 仅用于 config/Extension discovery，Fleet 不扫描；退出删除，`--keep` 保留并报告。`cmd/juex/main.go` 只 bootstrap + `os.Exit(cli.Execute())`。

### 3.8 Agent Endpoint

`endpoint.Listen` 拿 `$JUEX_HOME/.locks/endpoints/<agent-id>.lock`，lock 前后验证 Agent dir，不重建缺 registry；优先 `<state>/api.sock`，只删 confirmed stale socket，AF_UNIX 不可用时 fallback ephemeral loopback TCP。HTTP server 启动后显式 publish `runtime.json`，Close 只 conditional-remove 自己的 record。Identity 含 Agent ID、crypto-random instance ID、PID、endpoint、runtime start、optional OS process fingerprint、binary version。Linux fingerprint=boot ID + raw start ticks；fingerprint/version 仅双方都有时参与 compare，兼容旧 record。

URI 只允许 `unix:///absolute/path/api.sock` 或 numeric loopback `tcp://127.0.0.1:<port>`。Target 提供 proxy-free transport/client；client 无 global timeout以支持 SSE。`Probe` 校验 `/api/identity` 完整 identity；`RequestShutdown` 把 exact identity 发 `/api/control/shutdown`，mismatch 拒绝。Maintenance guard 用于 stale cleanup/GC；Fleet 不直接 signal recorded PID。

### 3.8.1 Fleet Supervisor

Fleet 把 binding（bound/orphaned/invalid）与 runtime health（healthy/stopped/unhealthy/ambiguous）正交投射。`fleet serve` 拿 Home `fleet.lock`，reconcile、只 adopt exact endpoint、只删 confirmed stale runtime、启动 enabled autostart，再 bind browser。Detached child 执行当前 binary `-C <workspace> listen`，继承 effective Home，stdout/stderr 进 `logs/fleet.log`；Supervisor/browser 退出不停止 child。

Lifecycle action 拿 `.locks/fleet/<id>.lock`。Start 等 child exact identity healthy；Stop 只请求 instance-bound shutdown，不发 signal。Restart 先读 status，记 active/pending-drain 或 selected failed Turn；必须有 acknowledged identity-bound graceful shutdown。Replacement healthy 后确认 same Session/Turn：active work 应以 `runtime_restart` cancel，failed work 保持同 error kind；仅 idle replacement 接收唯一 `system_notice` continuation。Completed/user-cancelled/superseded 不继续。Status/continuation failure 只诊断，Stop 不继续。

Add 对 absolute workspace 走 marker rules；Disable 先 Stop 再保存 false，Enable 不自动 Start。Remove 确认、Stop、拿 endpoint guard、atomic rename registry dir、只删仍 matching marker。GC 仅 definite revalidated orphan。Fleet command 直接 resolve Home，不 mint cwd identity。Status 显示 recorded binary version，version skew 只 warn，不隐式 restart。

### 3.8.2 Fleet Service Registration

Address precedence：explicit `--addr` → field-wise merge `~/.juex/juex.yaml` 与 distinct `$JUEX_HOME/juex.yaml` → `127.0.0.1:5839`。Instance 可显式 false 覆盖 default-home unsafe true；empty instance addr 继承 default addr。Non-loopback 必须 explicit `--unsafe-bind-any`，或 addr 也来自 Home config 时使用其 unsafe setting；explicit addr 不继承 permission。

Install 先 atomic persist explicit addr/unsafe，再安装；`--restart-agents` 后续串行只选 snapshot 中 enabled+bound+healthy，逐项报告且不因单项失败中止。Service definition 不带 addr，让 config restart 生效。替换前验证 existing definition 是 Juex。Install resolve executable/Home，按 Home 派生 safe service ID，atomic write，多文件失败 rollback。Uninstall 即便 definition missing 也查 native manager，先停并确认 supervisor，再删除。

macOS LaunchAgent 用 `AbandonProcessGroup`；systemd user 用 `KillMode=process`；Termux 用 run/log script + `down` sentinel，安装 replacement 后显式 restart，删除确认 `sv status down`。这样 service manager 不杀 detached Agent。Platform path 在 native user dir，Home-derived name 区分实例。

### 3.8.3 Fleet Web Backend

`fleetweb` 负责 loopback listener、JSON route/status mapping、SPA fallback、single-level dir browser 与 proxy；handler 只 delegate Fleet，不直接读 registry/process。Dir browser 拒绝 symlink、默认隐藏 dot dir、不递归；create 只在 validated absolute non-symlink parent 下创建一个 single-component 空目录，conflict 明示。`--unsafe-bind-any` 明确把 trusted host filesystem mutation boundary 扩展给 remote client。

Roster concurrent+bounded enrich healthy Agent 的 `/api/status`；失败不影响 Fleet process health。`/api/fleet/events` 聚合 typed snapshot，subscriber 对每个 healthy Agent 共享一个 upstream，periodic reconcile 只发现 lifecycle。Process metric 单核满载=100%，不除 core、不 clamp；只有 Agent process+endpoint exact healthy 才 attach usage，failure 不改 health。Fleet process 用独立 sampler `/api/fleet/status`。

Proxy 每次转发前 re-read/probe healthy endpoint，支持 Unix/numeric-loopback，strip prefix、保留 query/response、不 retry、立即 flush SSE，dial failure 502。无 healthy endpoint 时可用 read-only state 仅服务 persisted active lookup、session list/detail/context/scratchpad/media GET；Turn/event/runtime/workspace/mutation 禁止 fallback。

Config PUT 先按 effective user config 验证 workspace replacement，Fleet lock 覆盖 preflight/write/stop/start；restart 失败时 valid config 仍保留。GET/PUT 把所有 env value 替为 `[REDACTED_ENV]`；PUT 表示保留 existing，existing 不存在则拒绝。要写 literal 必须 YAML `!juex/literal "[REDACTED_ENV]"`，写前移除 control tag。

### 3.9 Web Layer

`juex listen` 总启动 canonical Agent endpoint 并写 identity-owned runtime.json，默认无额外 TCP；显式 `--addr` 才加 loopback JSON/SSE，非 loopback 还需 unsafe。Canonical `APIHandler` unknown route 为 404；explicit TCP `Handler` 对 non-API GET/HEAD 给 fleet-browser pointer，unknown API 仍 404。只有 Fleet mount SPA。Startup 确保 active primary、启动 listener、publish endpoint，再 warm shared MCP + active Session。每个 Session 一个 App，Web broadcaster 是 durable sink 的 live adapter；journal append 成功后才发 SSE，slow client buffer-full 5s 后 drop。

`GET /api/sessions/active` 只读 active ID、允许 lazy in-memory primary，否则只验证 conversation+metadata，不扫 transcript。Active selection 与 create/activate/delete/new 串行，但不阻塞 selected Session restoration。Detail 可在 Event replay warmup 时读 persisted projection；live route 等 App ready。

List/detail 合并 lazy in-memory Session。List 用 validated history summary cache。Detail 默认返回 latest compact marker + tail，或 bounded recent window；`before` + capped `limit` 分页。若 boundary 落在连续 Tool Result 中，最小向前扩展到 matching Tool Call（只跨 UI-only Policy trace），pagination metadata 用 expanded start；orphan result 保持 output-only。`created_at` 从 canonical message ID 派生，不加进 canonical Message/JSONL；live projection 用同一 ID timestamp，fallback Event time。

只有 active primary 接受 Web `POST /turns`；inactive primary 先 activate，side 只读。CLI 可 continue side 而不 active。

Active Primary 拥有 in-process Side Session manager，Tool 为 create/list/status/send/subscribe/stop。每个 child 有独立 App/Session/Engine/Bus/lock/scratchpad/queue，复用 Agent resources/MCP，绑定 Primary Goal/Notes，不启动 Observable、不递归 Side Tool。Child 可写共享 Goal，但只有 Primary 跑 completion gate。若 subscribed child 正运行或 terminal result 已接受但未进入 Provider-visible processing，Goal gate 可暂时完成当前 Turn 而不 synthetic continuation；handoff 在 persistence/admission retry/queue 中保持，drain/promote 回调按 durable ID 清除。

Create/idle send async start，busy send 走 durable queue，subscription 默认 on。Subscribed terminal result 转为 user-role `side_session` message，经同一 delivery interface start/queue。Exhausted delivery 记 `notification_error` 并发 `side_session.notification_failed`。Stop 关闭不删 durable Session；关系 process-local，App close/new 停止 children，restart 不重建。

Live-only route 只在 disk App ID 仍等于 active_id 时 resolve。Stale EventSource reconnect 对 inactive 返回 conflict，不切 Session；history/context/scratchpad/status 可 read-only。HTTP handler 只 validate/decode/render/cache/SSE，admission/runtime 留在 App。`webTurnTransport` 内聚 running/status/pending/interrupt/goroutine/reset，interrupt 先走 App cancel boundary。

Runtime `StatusStore` 按 runtime-status 文档投射 layered state。Status snapshot 含 durable cursor；status SSE 从 cursor 恢复 full snapshot。Browser transcript stream 的每个 Event 带对应 authoritative status。Frontend live projection 只负责 transcript optimistic/queue/compact/tool delta/final assembly，不重建 lifecycle；controller 从 transcript response 的 earlier cursor 打开 stream，并独立 calibration status。Reconnect 重校准；任一路失败不阻塞另一路，较新的 stream snapshot invalidates older refresh。

Server 在 durable commit barrier 后 capture journal descriptor/fixed prefix，dedupe replay 与 live handoff。Cursor 是 opaque durable Event ID；empty 表示无 position，若需从头用 namespace 外的 `?replay=journal-start`，不可占用 extension 可能生成的 ID。Stable persisted message ID 用于 initial/live/refresh dedupe；Tool 用 globally unique tool-use ID。Session route 生命周期内 initial cursor 稳定，唯一例外是 empty-journal placeholder 被首个真实 cursor 替换。

Agent API 可直接通过 `/api/...` 访问，也可经 Fleet proxy 的 `/agents/<id>/api/...` 访问。完整 Fleet browser、management 与 Agent contract 为：

| Method | Path | 用途与行为 |
|---|---|---|
| GET | `/healthz` | readiness probe |
| GET | `/` | Fleet roster SPA 入口 |
| GET | `/agents/<id>` | 选中 Agent 的 Sessions SPA route |
| GET | `/agents/<id>/sessions/<session-id>` | 选中 Agent 的 conversation SPA route |
| GET | `/agents/<id>/history` | 选中 Agent 的 history SPA route |
| GET | `/agents/<id>/runtime` | Runtime Overview SPA route |
| GET | `/agents/<id>/runtime/extensions` | Extensions SPA route |
| GET | `/agents/<id>/runtime/observables[/<observable-id>]` | Observable list/detail SPA route |
| GET | `/agents/<id>/runtime/logs` | bounded Agent logs SPA route |
| GET | `/agents/<id>/runtime/config` | Agent config SPA route |
| GET | `/settings` | Fleet settings SPA route |
| GET | `/assets/*` | embedded JS/CSS/font asset |
| GET | `/api/agents` | Fleet roster JSON；healthy Agent 尽力附带 live activity |
| GET | `/api/fleet/status` | resident Fleet process RSS 与 interval CPU |
| POST | `/api/agents` | 注册 absolute Workspace，可设置 metadata 并启动 |
| GET | `/api/fs/dirs?path=&show_hidden=` | 服务端单层目录浏览 |
| POST | `/api/fs/dirs` | 在已浏览的 absolute parent 下创建一个空 child dir |
| POST | `/api/agents/<id>/start\|stop\|restart` | Agent lifecycle action |
| POST | `/api/agents/<id>/enable\|disable` | 保存可逆 enabled state；disable 同时 stop |
| DELETE | `/api/agents/<id>` | 确认、stop 并有意删除 registered Agent state |
| GET | `/api/agents/<id>/logs?lines=N` | bounded combined log tail |
| GET, PUT | `/api/agents/<id>/config` | 读取或 validate/write/restart config，env value 按 redaction contract 往返 |
| GET | `/api/sessions` | JSON Session list |
| GET | `/api/sessions/active` | lightweight active primary ID lookup，不扫描 transcript |
| GET | `/api/status` | authoritative selected-Agent runtime-status snapshot |
| GET | `/api/status/events` | resumable selected-Agent runtime-status SSE |
| POST | `/api/sessions` | 创建 active primary Session |
| GET | `/api/sessions/<id>` | JSON transcript window + safe `event_cursor`；`?before=&limit=` 读取旧页且保持 Tool pair boundary |
| DELETE | `/api/sessions/<id>` | 删除 Session 并从 history 移除 |
| POST | `/api/sessions/<id>/activate` | 把 primary Session 设为 active |
| GET | `/api/sessions/<id>/context` | 单个 Session 的 active Provider context |
| GET | `/api/sessions/<id>/scratchpad` | active/persisted Session 的 scratchpad-only tree |
| POST | `/api/sessions/<id>/compact` | 追加 manual compact summary marker |
| POST | `/api/sessions/<id>/attachments` | 验证并存储 Session-scoped image upload |
| POST | `/api/sessions/<id>/turns` | 启动 text、image 或 mixed Turn；仅 active primary 可写 |
| POST | `/api/sessions/<id>/interrupt` | 取消当前 Turn/active operation |
| GET | `/api/sessions/<id>/status` | authoritative layered status snapshot + durable Event cursor |
| GET | `/api/sessions/<id>/status/events` | cursor 后的 resumable full-status snapshot SSE |
| GET | `/api/sessions/<id>/events` | BrowserEvent SSE；`?since=<cursor>` 从该 durable Event 后恢复，`?replay=journal-start` 从 journal 起点 replay；blank/absent `since` 表示无 resume position 且不 replay |
| GET | `/api/observables` | 列 Workspace Observable 与 runtime status |
| POST | `/api/observables` | 创建并启动 tagged Command Observable 或 Schedule |
| GET | `/api/observables/<id>` | Observable status + recent Observations |
| POST | `/api/observables/<id>/run` | 发一个 durable Schedule Observation，不改变 lifecycle |
| POST | `/api/observables/<id>/start` | 启动 stopped/exited Observable |
| POST | `/api/observables/<id>/stop` | 停止 running Observable |
| DELETE | `/api/observables/<id>` | 删除 project-owned spec 并 stop source；Extension definition 返回 `409` 且不 mutation |
| GET | `/api/observables/<id>/observations` | recent Observation history |
| GET | `/api/files/tree` | Web sidebar 的 WorkDir file tree |
| GET | `/api/files/content?path=<path>` | 单个 WorkDir file 的 bounded text preview 或 image metadata；拒绝越界 path |
| GET | `/api/files/raw?path=<path>` | bounded-to-WorkDir image bytes |
| GET | `/api/media?root=artifact\|workspace&path=<path>` | 显式 root 的 image bytes；verified content-addressed Artifact 使用 immutable cache，mutable Workspace file 使用 revalidation |
| GET | `/api/runtime` | App-assembled Provider、grouped builtin/MCP Tool、shell、Hook、system prompt、Skill status 的 Web DTO |
| GET | `/api/status` | selected-Agent runtime snapshot，含 idle/working compatibility field |
| GET | `/api/status/events` | selected-Agent runtime-status SSE |
| GET | `/api/fleet/events` | Fleet aggregate `agent.status` SSE |

### 3.10 Observables

`observable` 有 shared Observation kernel + Command/Schedule adapter。Command 管 process 并把 parsed/filtered/bounded output 变 Observation；Schedule 管 timetable/catch-up/pause/pre-authored payload。Kernel 统一 transition、durable state、source-event idempotency、tracked delivery、Event、list/start/stop/delete/history。

Schedule recurrence strict union：`once|daily|monthly|interval`。Monthly day=1..31，local time 必须 IANA timezone；不存在日期/DST gap 无 occurrence，DST fold 只取较早 UTC。UTC timestamp 进入既有 source-event ID/cursor/catch-up/recovery/delivery。

Persisted/API strict tagged union：command 必须 `command_config`，schedule 必须 `schedule_config`。非法 entry 作为 per-entry issue，不 rewrite；valid sibling 可启动，但全部 issue 修复前禁止 config edit。Model creation Tool 分为 `observable_create` 与 `schedule_create`，其余 lifecycle source-agnostic；list 返回 cloned read-only schedule config。Frontend mirror DTO，不复制 validation。

App 合并 writable project file 与 ordered read-only Extension refs；ID collision 报双方 source。Invalid extension entry 带 source error，不阻塞 project edit。只有 project entry 可 Save/Delete；Extension mutation typed conflict 且不 stop/delete/write。

Extension Command 获得 installation dir、Agent data dir、deferred prepare，直接 argv 启动无 shell；注入 authoritative WORKDIR/JUEX_WORKDIR/JUEX_EXT_DIR/JUEX_EXT_DATA_DIR。Project/Extension Command sandbox writable root 均含 Workspace+AgentStateDir。Project 拒绝 Extension variable；Schedule 不起 subprocess。

Web-only manual Schedule `Manager.RunOnce` 产生 unique `schedule:<id>:manual:<random>` Observation，经 tracked delivery，但不建 run、不写 cursor、不改 pause/running。成功 201，unsupported/unavailable 409，unknown 404；无 model-facing Tool。

---

## 4. 单个 Turn 的 Data Flow

```text
user input -> Engine.Turn -> emit turn.started
  -> Prompt sections: AGENTS hierarchy + Skill + Module context + Tool specs + operating context
  -> emit llm.requested -> Provider.Complete -> emit llm.responded
  -> 无 tool_use: Session.Append -> turn.completed -> return text
  -> 有 tool_use: declared ordered batch；独立 call 并发，Session-state/Side 保持 Provider 顺序
       -> requested/delta/completed/errored -> ordered tool_result append -> 下一次 LLM
```

---

## 5. 配置

默认 Home 为 `~/.juex`；writable Home 为 `JUEX_HOME` 或默认 Home。先读 `~/.juex/juex.yaml`，仅当 effective Home canonical 不同时再读 `$JUEX_HOME/juex.yaml`；前者是 distinct instance 的只读 base，write/lock/Fleet/Agent state 只进 effective Home。Workspace config 为 `<WorkDir>/.juex/juex.yaml`；若 WorkDir 自身是 `.juex`，则为 `<WorkDir>/juex.yaml`。

主要配置：ordered `imports`、完整替换的 `models` chain、user-agent resource switch、`environment`、Skill budget/filter、Module enable、Extension allowlist、shell profile、Provider/Profile/Model/Capability/Compat、Hook、runtime queue/timeout/output/policy、compaction、tool-output externalization。Known Provider preset 为 openai/openai-codex/anthropic/deepseek；custom 必须 protocol。

关键默认值与约束：

| Field | Contract |
|---|---|
| `imports[].source` | declaring file 前按顺序应用 direct local/HTTP(S)，relative path 相对声明文件；import 不得再含 imports；`--config` 只能 local |
| `models` | ordered `provider:model`，首个 primary、后续 fallback；nearer layer 完整替换，包括 explicit empty |
| `enable_user_agents_resources` | 默认 true，支持常用 bool spelling；false 只忽略 `~/.agents` 三类资源，不影响 Extension |
| `environment.load_dotenv` | 默认 true，只启动时读固定 `<WorkDir>/.env`；missing 允许，malformed fail |
| `environment.variables` | immutable runtime env string map；必须 portable name，拒绝 Juex-owned name |
| `extensions.allow` | exact case-sensitive list；omitted inherit，explicit replace，无 effective setting 则不选 Extension |
| `skills.prompt_budget_chars` | 默认 8000，并受 model context policy 上限 |
| `skills.include/exclude` | merge 后 filter filesystem Skill；include non-empty 时 ignore exclude；builtin guide 不受影响 |
| `modules.<id>.enabled` | layered bool，默认 enabled；unknown ID/field fail。Runtime ID：builtin-tools/project-guidance/skills/side-sessions/observables/mcp；Session ID：session-context/goal/notes/hooks |
| `shell.profile` | auto/powershell/cmd/bash/zsh/sh/git-bash/wsl/custom；binary override 必须验证，不 fallback；custom 才允许 family/args/path style |
| `providers[].capabilities` | Tools、vision、streaming、reasoning effort/replay、max output token gate |
| `thinking_effort` | low/medium/high/xhigh/max；非法值 load fail |
| `context_window` | 默认 256000 |
| `hooks.commands` | event、optional Tool filter、argv；timeout 默认 10s、最大 300s；每 stream output 默认 65536 bytes |
| `runtime.pending_input_ttl` | user steering 默认 15m |
| `runtime.external_event_ttl` | MCP/external 默认 24h |
| `runtime.tool_timeout` | non-shell 默认 60s、上限 300s，不进 Tool schema |
| `runtime.max_output_tokens` | optional normal-turn cap，省略使用 Provider default |
| `show_builtin_policy_traces` / `notify_model_changes` | 均默认 false |
| `compaction.enabled` | 控制 auto/manual compaction |
| `compaction.instructions` | 持久 summary focus；位于 per-request instruction 与 successful `PreCompact` stdout 之前 |
| `reserve_tokens` | 可比默认 70% context-window threshold 更早触发 |
| `keep_recent_tokens` | 可收紧默认 5/64 context-window recent direct/MCP/Observable budget |
| `summary_model` | compaction 首选 candidate；失败沿 normal chain，无 conversation model-change notice |
| `summary_max_tokens` | 可收紧默认 0.5% window；semantic retry 最多 2 倍 |
| `tool_result_max_chars` | 可收紧 summary input 中每 Tool Result 默认 0.5% window |
| `user_input_inline_max_bytes` | 超限写 `artifacts/sessions/<id>/user-inputs/`，Provider 只见 stable preview |
| `compaction.user_input_preview_head_bytes` | externalized user input 保留在 inline preview 中的 leading byte 数 |
| `compaction.user_input_preview_tail_bytes` | externalized user input 保留在 inline preview 中的 trailing byte 数 |
| `max_auto_failures` | 连续 auto-compaction failure 后 pause proactive compaction |
| `tool_output.inline_max_bytes` | 独立于 compaction，超限写 `tool-results/`；head/tail preview 总和不超过 effective budget |
| `tool_output.preview_head_bytes` | optional leading-byte preview ceiling；head/tail 均省略时平分 effective inline budget |
| `tool_output.preview_tail_bytes` | optional trailing-byte preview ceiling；两者总和不超过 effective inline budget |

YAML precedence（后胜）为 defaults → default-home imports/file → distinct instance imports/file → workspace imports/file → explicit imports/`--config` → supported env → CLI。Root `--models` 完整替换 YAML chain，并胜过 `PROVIDER_API_ID/PROTOCOL/MODEL`；Base/Key/Thinking/Window 等 non-conflicting env 仍应用。`PROVIDER_API_MODEL` 只替换 selected provider 内 model ID，保留 tail。所有 ref 必须 unique 且 resolve。

每个 declaring YAML 先 strict parse，再按声明顺序读取 direct imports、用既有 per-field apply 构造 isolated candidate，最后应用 declaring document；任何 read/parse/scope/apply error 丢弃 candidate。Remote 只允许 HTTP(S) 200/conditional 304，deadline 5s、body 1MiB、redirect 最多 3；diagnostic 不含 body/query。Redirect 去 Referer 和原 validator，避免 token/ETag 泄漏；单次 load 对重复 remote identity 只 fetch 一次。

完整 config 通过 semantic/env/shell/auth 后，remote content 以 0600 atomic cache 到 `$JUEX_HOME/cache/config-imports/<source-digest>-<declaring-digest>-<context-digest>.json`，记录 identity、safe metadata、validator、time、content SHA。Transient network/408/429/5xx 可用不超过 7 天的 digest-valid LKG 并标 stale；其他 HTTP、expired/tampered cache、invalid new 200 fail 且不替换旧 LKG。

Pending cache record 以 home lock + 0600 transaction journal 成组发布；任一失败 rollback 已替换 target。下一 reader 发现 prepared journal 回滚，committed generation 保留；reader 从首个 LKG read 到完整 load 一直持 lock，不混 generation。Workspace config write 在同一 transaction 中 journal previous workspace bytes + cache targets，atomic replace 后 publish cache，失败/next prepared recovery 同时还原 workspace 与 cache。Read-only validation 不 publish。Fleet-only reader 无 workspace，只从完整 runtime LKG context 的交集选择“最旧 record 最新”的一致 context，绝不发布 fresh content。Doctor 只显示 source/freshness/digest/time。

Child-process env precedence：Extension manifest default → default-home YAML → instance-home YAML → Workspace `.env` → workspace YAML → explicit config YAML → inherited process env → child-local MCP/Observable → Juex-owned injection。Dotenv 是 data，不做 expansion/command/reload，empty inherited 保留。Runtime-bearing CLI 只为 in-process provider/SDK 激活 YAML+dotenv+inherited snapshot；App 显式传递 Extension defaults 给 child，绝不 mutation process env。一个 process 只激活一个 Workspace snapshot。Provider/model map 按 ID merge，但 `models` list 完整替换；`shell` object-level override，workspace `shell:{}` 重置 auto。

### Lifecycle Hook

Hook event：`SessionStart`、`UserPromptSubmit`、`PreToolUse`、`PostToolUse`、`PreCompact`、`PostCompact`、`Stop`。Home/instance location trusted；project/explicit config 必须 `hooks.trusted:true`。Extension Hook 继承 winner trust/context。Hook 无 shell，argv 直接执行，stdin 为 JSON，使用 resolved immutable env + reserved identity；timeout/output bounded。

SessionStart/UserPromptSubmit/PreToolUse/PreCompact/Stop 是 typed policy/gate：request Event 先 durable，Hook failure/deny 按 phase fail closed 或产生规范 continuation；PostToolUse/PostCompact 是 observation，不能改 flow。Hook stdout 只在契约允许时作为 context/decision data，stderr 仅诊断。Policy fact 统一 `policy.requested/started/completed/errored` 并保留 Hook event/name/source；conversation-visible trace 由 `show_builtin_policy_traces` 控制。Goal completion gate 与 pending input 始终是 Framework 最终 authority。

Compaction 会先依据 immutable prompt/tool/context fit 决定，summary request 使用 bounded recent selection 与 authoritative Goal/Notes state。Configured instruction、per-request instruction、successful PreCompact stdout 依次合并。Candidate chain + 一次更高 output budget 的 semantic retry 全部失败后才 fail；parent cancel 不 fallback，不 commit marker。Manual/auto 共享 active operation cancellation；成功 marker commit 后才 retire cancel function，PostCompact 只 observational。

Session-local `notes.md` 由 `update_notes` 整体重写，无 `get_notes`；UTF-8、最多 2048 字符、secret-like redaction、atomic replace。非空 Notes 每次 Provider request 在 Goal 后作为 `runtime-notes` 重建，不进 conversation，因而 survive compaction。Read/validation error 保持 slot，给出 recovery message，且同一 uninterrupted incident 只发一次 `notes.errored`；成功 read/update 清 incident。

Scratchpad 是大容量补充：prompt 只提供 absolute path（若在 Workspace 内再提供 relative chunk-write path），不自动 recite bytes；模型用既有 file Tool 管理。它是 mutable Session work，与 immutable content-addressed Artifact 分离。`goal_state.json` 是 model-owned operational contract，由 create/update_goal 修改并显示 status；`continuation_count` 记录 gate continuation，`status_reason` 仅解释。

Manual compact/context 通过 CLI、slash 与 Web API 提供。Summary snapshot Goal/Notes，structured JSON 保留 multiline field；fit 先保 authoritative state 再舍 transcript。Usage 与 compact Event 记录 retry/fallback epoch。OpenAI-compatible 使用 per-Session stable prompt cache key，Anthropic 对 stable system/tool section 加 ephemeral cache-control。Auto compaction 达 failure threshold 后发 `context.compact.skipped`；MCP notification 前 auto-compaction failure 仍处理 notification，ordinary user Turn 继续 fail loud。

---

## 6. 文件系统约定

```text
~/.agents/
├── AGENTS.md
├── mcp.json
└── skills/<name>/SKILL.md

~/.juex/juex.yaml                  # distinct effective Home 的只读 base

$JUEX_HOME/
├── juex.yaml
├── extensions/<name>/
│   ├── juex.extension.json
│   ├── hooks.yaml
│   ├── mcp.json
│   ├── observables.json
│   └── skills/<skill>/SKILL.md
├── fleet.lock
├── .locks/endpoints/<id>.lock
├── .locks/fleet/<id>.lock
└── agents/<id>/
    ├── agent.json / runtime.json / api.sock / history.json
    ├── .locks/sessions/
    ├── logs/fleet.log
    ├── artifacts/{event-media,read-media,sessions/<id>/}
    ├── extensions/<name>/
    ├── observables/
    └── sessions/<id>/

<WorkDir>/
├── AGENTS.md
├── .agents/{AGENTS.md,mcp.json,skills/<name>/SKILL.md}
└── .juex/{juex.local.json,extensions/<name>/,juex.yaml,observables.json}
```

Session subtree 包含 metadata、conversation/event journal、lock、pending journal、Notes、scratchpad、Goal 与 logs。`JUEX_HOME` scope writable config/Extension install/Fleet/Agent state；distinct Home 可读 default base/Extension，但绝不向 default Home 写 instance state。Ephemeral Agent 复用 shape，但置于 private temp root，非 effective Home、Fleet 不扫描、默认退出删除。

### 6.1 Artifact Storage

`artifact.Store` 接受 `<AgentStateDir>/artifacts` + root-relative logical path，返回 stable relative ref + SHA-256 + byte count。用 `os.Root`、same-dir temp + atomic replacement；read 可校验 integrity，超过 caller limit 时立即停止，不先整文件入内存；escape/symlink 拒绝。Image detection/resize 属于 read Tool，encoding 属于 Provider，preview 属于 context；retention/GC 独立。

### 6.2 User Media

`usermedia` 验证 HTTP body/CLI path、dimension/integrity、per-Turn image limit，并只允许目标 Session 的 `sessions/<id>/media/` namespace；bytes 委托 Artifact。Web/CLI 都在 Turn 前存储并返回 `llm.MediaRef`，App admission 再验证并转 canonical image block，防止任意 workspace path 注入。Vision-disabled Profile 不阻塞 Turn，返回 `attachment_vision_unavailable`；canonical history 保留 media，Provider projection 变 metadata + cannot-view/do-not-guess instruction。

Extension selection 从 default Home、distinct instance Home、Workspace 低到高。先按 logical name 选 whole winner，再 strict validate exact-case manifest；winner invalid 不 fallback。Manifest v1 要求目录同名 + SemVer；duplicate key/invalid known field reject，unknown field 在支持边界 ignore。Requirement 仅信息，不 parse/install/gate；Web 只把 safe absolute HTTP(S) 变 link。Agent env defaults 低于其他 env source，解析 Juex placeholder、拒绝 dangerous/unresolved conflict，并用于 startup/status/MCP/redaction。

Resource collision：Extension MCP/Skill/Hook/Observable 不得与已有或其他 Extension 同名。Status 从已选 descriptor 投射 metadata/scope/path/count/requirement/value-free env status，不 rescan。Source 为 `ext:<name>`。Allowed work-local Extension 可执行 Command Observable，Sandbox 才是 filesystem capability boundary。每个 Extension data dir 为 `<AgentStateDir>/extensions/<name>`；discovery 不创建，local MCP/Hook/Observable 真正启动前才以 safe 0700 prepare，并注入 JUEX_EXT_DIR/JUEX_EXT_DATA_DIR。

Web primary tab 只有 Chat/Runtime；Runtime nested route 为 Overview/Extensions/Observables/Logs/Config。Workspace marker 通过 Git user excludes 全局忽略，不改 project `.gitignore`。Read-only existing resolution 不 rebind moved workspace。

---

## 7. MCP

Production client 是 official Go SDK 的 thin adapter。Local 用 `CommandTransport`，remote 用 `StreamableClientTransport`；SDK 负责 JSON-RPC/init/negotiation/reconnect，Juex 负责 config/header injection/neutral Tool/process lifecycle/custom notification/diagnostic。Static header 只发给 configured origin，redirect 不带 credential。

`mcpServers` 遵循 Claude core transport：type omitted/stdio 为 stdio；remote 只接受 `http|streamable-http` + url + optional headers。Header 支持 `${VAR}`/`${VAR:-default}`，来自 immutable env，format/JSON/log/error 全 redacted。Legacy SSE、Claude WebSocket、interactive OAuth、`headersHelper` 明确 fail，不静默 ignore。

Custom `notifications/claude/channel` 在 SDK reject 前拦截。stdio 用 Connection.Read decorator；Streamable HTTP 保留 concrete SDK connection/session state，通过 successful SSE body filter dispatch custom notification，同时保留 event ID/retry priming，其余 event framing 不变。

Tool 名为 `mcp__<server>__<tool>`。Process-level Manager 可给多个 Session registry 注册 proxy，Session close 不关 MCP。Descriptor deep-copy，server 内按 Tool name sort；connected zero-tool 的 map membership 保留以区别未连接。

Claude notification 保留完整 params，作为 `mcp_event` user message 进入普通 Turn，结构化展示 server/method/type/content/meta/selected params。`params.attachments` 走 Workspace/AgentStateDir validation，relative 以 Workspace 为基准，bytes 先复制到 content-addressed `event-media`，invalid 在 text 中明示。run/repl 指向唯一 primary，listen 指向 history active primary；Side 不声明 channel capability。

stdio stdout 只允许 JSON-RPC，server log 必须 stderr。Runtime catalog 从 active sealed Runtime/Session Module 组装 provider/shell/module/prompt/hook/skill/grouped Tools/MCP；URL 与 startup error 去 query，Tool 显示 normalized schema 与 semantic timeout。Status 读 catalog 时持 App publication lease，不混 replacement generation，不构造 descriptor-only shadow Module。

User-global、Extension、project config 都走同一 Manager/Module contribution。Project 同名覆盖 user；Extension collision reject。Runtime startup 与 doctor 的 readiness 分 selection → credentials → connectivity；auth/permission=credentials，wrong endpoint=selection，transport/DNS/TLS/timeout/rate/server=connectivity。

Local MCP 启动前注入/展开 absolute WORKDIR/JUEX_WORKDIR，server env override global 后再由 reserved value 最终覆盖。Extension 再注入 JUEX_EXT_DIR/JUEX_EXT_DATA_DIR，data dir 真正启动前安全创建；remote 不获 process env，值不进 header/global snapshot。Physical path 必须保持在 Agent Extension data root，拒绝 symlink。

---

## 8. Skill（最小实现）

Skill 位于 `.agents/skills/<name>/SKILL.md`，frontmatter 至少含 name/description/type，正文为完整指导。Startup 顺序：先 load fixed embedded builtin guide，再 scan user/Extension/project；parse；保留 builtin name，按 precedence merge，应用 filesystem include/exclude；生成 budgeted available catalog；模型用 `skill_search` 找 omitted entry、`skill_load` 取全文。

Project 覆盖 user；Extension/builtin name strict collision reject。Status source 为 builtin/user/project/`ext:<name>`。Builtin virtual path 为 `builtin://skills/<name>/SKILL.md`，由 private provenance 授权；filesystem path 受 sandbox。Builtin guide 不占 prompt catalog/budget，但仍出现在 All/search/load/dry-run/doctor/status。无 vector retrieval 或自动 activation。

---

## 9. 构建、发布与 CI

### Make target

| Target | 作用 |
|---|---|
| `verify-plan` | 由 Git diff 派生 plan.json/md，可解释 gate cause |
| `verify-focused` | dirty worktree 可用；明确 PKGS 或 PLANNED；准备不覆盖真实 asset 的 Web stub 与 pinned rg |
| `verify-candidate` | 前后 clean；由 plan + additive RACE/WEB 运行一次 deterministic suite + binary build |
| `verify-final` | 前后 clean；复用 candidate plan，始终 live integration + 一个 selected Provider smoke，必要/显式时 compaction |
| `test` / `race` | caller env 下 provision rg 后 Go suite（race 强制 count=1） |
| `ripgrep` / `lint` | verified rg；golangci-lint |
| `build` / `build-go` / `web-stub` | 完整 Web+Go；已有 embed 的 Go-only；仅缺失时 stub |
| `cross` / `snapshot` / `release-dry` | 7 managed archive；GoReleaser snapshot；非发布 release |
| `integration` / `provider-smoke` / `development-eval` | build-tagged contract+credential live；seeded capability/Schedule smoke；redacted development record |

GoReleaser v2 产出 darwin amd64/arm64、linux amd64/arm64/armv7、windows amd64/arm64。Linux arm64 pinned rg 是 glibc，installer 在 arm64 musl 拒绝；Termux 只装 static Juex 到 `$PREFIX/bin` 并用 native `pkg install ripgrep`，不装 managed rg/package manifest。ARMv6 不支持。

Archive 含 stamped Juex、target rg 15.1.0、license、`juex-package.json`，size/SHA pin 在 `release/ripgrep-assets.tsv`，最终 `checksums.txt` 覆盖 archive。Before hook 先 build frontend。POSIX installer 校验 checksum，装 immutable generation 到 `<prefix>/lib/juex/releases`，atomic switch command symlink；同版本 reinstall 用 unique suffix，旧 generation 保留。Windows 保持 generation layout，复制 exe 后才写 `current.txt`。

Installer 以新 binary 检测已有 user Fleet service；缺失 service 只有 `INSTALL_FLEET_SERVICE=1` 才安装。Released installer 不 restart detached Agent，只报 skew；source local installer 刷新已有 service 时才传 `--restart-agents`。Service probe/refresh/status failure 是 post-install warning，不作 binary install failure。Runtime resolution：显式 `JUEX_RG` > verified managed payload；unpackaged source 才 fallback PATH。

### CI workflow

- `ci.yml`：push/PR + manual Windows benchmark。Frontend 独立 `make web-check`；lint + `goreleaser check`；Ubuntu/macOS race suite；Windows 把 ordinary/web/e2e/eval 分 runner 并以稳定 aggregate gate 汇总，parallelism/concurrency 限 2 CPU；manual benchmark 上传 secret-free env + `go test -json`。
- `integration.yml`：manual Anthropic/OpenAI matrix，从 secret 写 temp config 并导出 `JUEX_PROVIDER_CONFIG`，运行 `make integration`。
- `release.yml`：tag `v*`，准备 frontend toolchain，`goreleaser release --clean`。
- 本任务新增 `documentation` gate，通过 `make docs-check` 校验双语 pair/whitelist/top reciprocal link。

---

## 10. Test Strategy

每个 package 有 `_test.go`；`tests/e2e` 覆盖产品 cross-package，`tests/eval` 覆盖 harness。重点包括 architecture import direction、Event catalog/order、frontmatter、version、全部 Tool/timeout/session、Module sealing/atomic registry/lifecycle rollback、Feature contribution、MCP config/cancel、Skill precedence/collision、prompt hierarchy、Session journal/history/delete、runtime parallel/serial/cancel/fallback、observability redaction/signal、netbootstrap、App/CLI composition、binary smoke、Web/pending/live Provider，以及 eval oracle/provider selection/Schedule routing/verification orchestration。

稳定验证入口是 `make verify-focused/candidate/final`。`make integration` 继承 caller HOME/CODEX_HOME/JUEX_HOME/tool state，从 explicit `JUEX_PROVIDER_CONFIG` 或 caller config 选 Provider，再使用 case-local runtime state；`JUEX_PROVIDER_SMOKE_ONLY` 可选一个 ref，selection 后清 selector env 避免漂移，但保留 endpoint/credential/thinking/window override。

`make provider-smoke` 从 explicit/env/original Home resolve，过滤 Tools capability=false，按 recorded seed 选一个 ref，在 isolated binary 中验证 capability + Schedule routing，写 redacted `.tmp/reports/provider-model-smoke/`。Empty/seeded variant 分别验证 create 前空表或已存在等价不同 ID 且不 duplicate/stop；两者都拒绝 Command Observable route并验证 tagged config。只有 provider matrix migration/full audit 才用 `--all-models`。

`make verify-final` 是完整 merge-candidate gate。Standalone record 用 `make development-eval`。Compaction/context/provider replay/long-session 变化必须运行 `tests/eval/compaction_eval.sh`，说明见 `docs/compaction/evaluation.zh.md`。

---

## 11. 与早期设计的差异

| 决策 | 早期偏好 | 当前实现 | 原因 |
|---|---|---|---|
| LLM client | official SDK | official SDK | 与设计一致 |
| MCP client | mark3labs/mcp-go | official Go SDK + Juex adapter | SDK 管 protocol/transport，Juex 管 product policy/diagnostic |
| Event dispatch | channel + pool | synchronous map | 暂无 async listener 需求 |
| Frontmatter | yaml.v3 | handwritten | 只有 top-level string field |
| Config | viper/koanf | small YAML loader | 字段有限、precedence 可预测 |
| CLI | stdlib flag | Cobra | subcommand UX、persistent flag、help |

---

## 12. 一句话总结

**Juex 是一个 Go binary：提供 Cobra CLI、React Web UI、Builtin/MCP Tool、AGENTS.md/Skill/Extension loading、同步 Turn loop、AgentStateDir JSONL persistence、Workspace-local artifact/config、Event Bus、GoReleaser 跨平台发布与 GitHub Actions CI。** 优先标准库，Module 足够小，便于测试与解释。
