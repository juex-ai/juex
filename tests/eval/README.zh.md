# 评估 Harness

> [English](README.md) | 中文

此目录包含运行真实 Provider 或较长 multi-turn 行为的本地评估工具。确定性的跨平台 E2E test 放在 `tests/e2e`；Provider-config 选择与质量评估 helper 放在这里。

稳定的 Agent-facing 入口：

```bash
make verify-plan EXPLAIN=1
make verify-focused PLANNED=1
make verify-focused PKGS="./internal/app ./internal/runtime"
make verify-candidate RACE=1 WEB=1
make verify-final RACE=1 WEB=1 COMPACTION=1
```

它们委托给 `python -m tests.eval.juex_eval plan` 与 `verify`。底层 harness 入口位于 evaluation code 旁：

- `tests/eval/eval_scripts_test.go`
- `tests/eval/provider_model_smoke.sh`
- `tests/eval/compaction_eval.sh`
- `tests/eval/development_eval.sh`

`tests/eval/eval_scripts_test.go` 是此目录的 Go contract suite。它检查 Python module help surface、shell wrapper help、Provider-config selection、development-step flag、默认 report location 与 Schedule routing artifact contract：

```bash
go test ./tests/eval -count=1
```

Shell script 是 Python module 的薄 wrapper：

```bash
uv run --project . python -m tests.eval.juex_eval --help
```

## 验证 Tier

`plan` 与每个 verification tier 使用 `juex_eval/validation_plan.py` 中同一份 deterministic rule table。Candidate/final 默认检查从 `git merge-base origin/main HEAD` 到 `HEAD` 的变更；`--base <sha>` 精确使用该 commit。Focused planning 允许 dirty worktree，并合并 staged、unstaged 与 untracked path。Rename entry 保留两条 path，deleted entry 保留删除 path。每次运行写入 `plan.json` 与 `plan.md`，包含排序后的 changed file、匹配 rule ID、选中 gate、per-gate cause 与稳定 behavioral fingerprint。使用 `--explain` 或 `EXPLAIN=1` 输出 human-readable explanation。非 UTF-8 Git path byte 在 JSON 中可 round-trip，并在有效 UTF-8 Markdown report 中渲染为 `\xNN` escape。

`verify focused --planned` 只在显式 opt-in 后消费 dirty plan；无 scope 调用仍报错。Go path 选择自身 package，跨 boundary path 加入 `./tests/e2e`，frontend path 运行 `web-check` 加 binary build。Race-sensitive path 加入 `-race`。`PKGS=...` 仍是开发 loop 必需的 targeted alternative，不继承更宽的 diff-selected gate。仅文档或空 diff 可以不选择 focused code gate。未知非文档 path 使用完整 conservative plan，不产生空 scope。

`verify candidate` 在计划或准备 gate 前捕获完整 `HEAD` SHA、branch 和 porcelain status，随后要求 gate 后 worktree 仍位于该 clean SHA。它只运行一个完整 deterministic Go suite，再运行一个 executable build。Go suite 前使用共享且不覆盖的 `web-stub` target，使 fresh checkout 在不构建 frontend 时满足 Go embed contract。Planned race/web flag 自动应用；`RACE=1` 与 `WEB=1` 是 additive override。`WEB=1` 运行 `web-check`，同步 frontend asset 到 `internal/web/dist`，并调用 Go-only `build-go`，不重复构建 frontend。

`verify final` 应用同一 commit-bound clean-worktree contract。它先查找相同 SHA、record schema、candidate-plan fingerprint 与稳定 toolchain/environment fingerprint 的 passing candidate record。存在时，final 复用成功的 deterministic、build、web 与 race step，然后始终运行不重试的 build-tagged deterministic integration contract，再运行允许 retry 的 live integration 与 Provider smoke。Plan 加入可选 compaction gate，且绝不复用 final-only result。缺失或不兼容 candidate 会使 final 执行完整 plan，并记录精确 invalidation reason。只有 compaction、context projection、Provider replay 或 long-session 行为需要 live compaction quality gate 时，才设置 `COMPACTION=1` 作为 additive override。所有 tier 在首个失败步骤停止。底层 final CLI 支持 `--config`、`--selection-seed` 与 `--provider-timeout`，用于精确 live rerun。Candidate/final 也接受 `--run-id`；其 `--report-dir` override 是 report root，不是单个 run directory。Run ID 必须是仅含字母、数字、`_`、`-`、`.` 的安全 basename，且不能以 `.` 开头。

## Deterministic Capability Harness

`capability_harness.go` 为核心 Agent capability 提供 CI-safe scripted-provider eval，不调用真实 Provider。每个 `CapabilityCase` 创建隔离 workdir、注册真实 Builtin Tool、可选加入 eval-only Tool 与 Command Hook、运行 `runtime.Engine.Turn`，再从 `conversation.jsonl` 和 `events.jsonl` 计算稳定 report。

`contract_oracle.go` 负责 Go harness 的 deterministic artifact contract check。它解析 conversation/Event JSONL artifact，为必需 Tool use、TTY exec use、Tool output delta 与 structured shell result Event 报告稳定 pass/fail issue。Capability harness 是提供 artifact path 与 case-specific expectation 的 Adapter；oracle 不改变 production Runtime 行为。

运行：

```bash
go test ./tests/eval -run 'Capability' -count=1
```

初始 case 覆盖：

- File Tool：`read`、`write`、`edit`
- 搜索：`grep`
- Shell：`exec_command`
- 通过 eval-only guarded Tool 实现 permission/sandbox 风格拒绝与恢复
- Lifecycle Hook：`UserPromptSubmit` context injection 与 `Stop` continuation

新增 case 时创建 `CapabilityCase`，包含：

- `Files`：相对于隔离 workdir 的 fixture file
- `Script`：返回 deterministic `llm.Response` 的步骤
- 可选 `ExtraTools`：permission gate 等 eval-only probe
- 可选 `Hooks(workDir)`：必须通过真实 Hook runner 的 Command Hook
- `Assert`：检查 filesystem side effect、Event count、Tool metric 与 transcript text

每个 `CapabilityResult` 暴露：

- `success`：final text 含 `TASK COMPLETE`
- `provider_calls`：完成所需 scripted Provider Turn 数
- `tool_calls` 与 `error_tool_calls`：来自 transcript 的 Model-requested Tool usage
- `context_bytes`：持久 conversation JSONL byte，作为低成本 context-pollution proxy
- `tool_bytes`：持久化到 conversation history 的 Tool-result byte
- `elapsed_ms`：deterministic case 的墙上时钟耗时
- `events`：`events.jsonl` 中的 Event type count
- `tool_names`：per-Tool Call count
- `contract`：eval contract oracle 的 pass/fail detail

修改 Tool contract、sandbox/permission 行为、Hook、stop gate 或 context projection 时，使用这些 metric 做前后比较。Case 保持 deterministic 且不使用 credential；Live model 行为属于下面的 Provider smoke 与 compaction eval 命令。

Live model scope 来自已解析 Provider config。Candidate 按 `provider:model` 去重并排序；日常命令用记录的 seed 选择一个 candidate。

Provider smoke 或 compaction 投影选中 Provider/Model 前，会在持有与 Runtime 兼容的 `$JUEX_HOME/.locks/config-imports-cache.lock` 时加载全部 Home 与显式 source layer，包括每次 Last-Known-Good cache read。Eval 会拒绝含 v3 精确 schema 之外字段的 Runtime cache record，并且当 Runtime publication journal 正等待 Go 端 recovery 时拒绝读取。Remote import 在 redirect 与 body consumption 之间共用 Runtime 的五秒整体 deadline，redirect 不转发原资源的 conditional validator。Loader 随后在权限受限 work area 中实体化完整 merged source，并请 `juex doctor --offline` 验证完整 schema。因此未选 section 中的 unknown field 也会在 environment gate 失败，而不是被 projection 丢弃。

底层 `write-model-config` 命令在选择 Provider/Model 前使用同一 source-layer materialization 与 Juex validation。传入 `--juex` 或 `JUEX_BIN` 固定 validator；既没有本地 build 也没有 installed binary 时，repository helper 通过 `go run` 验证。Provider smoke 排除 effective Provider/Model capability 显式 `tools: false` 的 profile。Compaction 排除声明 context window 小于请求 eval window 的模型；省略声明时使用 Juex 默认 256k。每个选中的 Provider smoke model 使用同一严格 capability 与 Schedule-routing contract。

通用 selection 与 output flag 在各命令间有意保持一致：

- `provider_model_smoke.sh --only provider:model` 运行一个 live Provider smoke。
- `compaction_eval.sh --only provider:model` 运行一个 compaction eval；重复 flag 运行小型显式集合。
- `development_eval.sh --only provider:model` 传递 Provider smoke scope。
- `development_eval.sh --compaction-eval --compaction-only provider:model` 传递 compaction scope。
- `--selection-seed value` 从同一配置复现默认选择。
- `--all-models` 运行已解析配置中每个 eligible ref。
- `--report-dir` 覆盖各命令输出目录。

默认情况下 Provider smoke、development 与 compaction artifact 写入 `.tmp/reports/<report-kind>/<run-id>/`。Commit-bound candidate/final record 改用 `.tmp/reports/development-validation/<full-head-sha>/<run-id>/`。其 `record.json` 与 `record.md` 列出 schema、clean source snapshot、plan/environment fingerprint、candidate binary fingerprint、脱敏 Provider selection identity，以及每个 reused、executed、invalidated 或 fail-fast-not-run step。Execution state 与 terminal outcome 分离：`passed`、`flaky_pass`、`product_failure`、`environment_failure`、`provider_unavailable` 或 `transient_failure`。每个完成步骤记录 reason、命名 matching rule、merge-blocking decision、recommended action 与每次 attempt 的完整 log。Record plan fingerprint 组合 candidate-relevant Git-diff projection 与具体 candidate step plan，因此 final-only override 不使可复用 deterministic evidence 失效。相应 `plan.json` 与 `plan.md` 位于 record 旁。Candidate binary 缺失或变化会使复用失效，使 final 能重建 live smoke 所需 artifact。Environment fingerprint 包含 effective Go build flag、Workspace、experiment、toolchain、architecture、CGO/compiler setting、build 的 `git describe` 值与已解析 ripgrep executable fingerprint，防止不同 effective build/test input 间复用。目录按需创建。如果 candidate 与 final 显式共用一个 run ID，final 会在自身 `record.json`/`record.md` 旁把可复用来源保留为 `candidate-record.json`/`candidate-record.md`，使失败 live-gate retry 不重跑 deterministic plan。该 run ID 的 refreshed candidate 会原子替换旧 preserved candidate snapshot。

Report kind：

- `provider-model-smoke`
- `development-validation`
- `compaction-eval`

## Provider Smoke

构建 binary 后运行动态选择的本地 `provider:model` smoke：

```bash
make build
bash tests/eval/provider_model_smoke.sh --juex ./dist/juex
```

命令解析 `--config`、`JUEX_PROVIDER_CONFIG` 或原用户的 `~/.juex/juex.yaml`，用记录 seed 选择一个 eligible ref，并把该 `provider:model` 复制到隔离临时 workdir。随后用真实 compiled `juex` binary 运行两个 live Agent workflow。Capability workflow 要求模型对 case-local file 和 deterministic interactive installer command 使用 `read`、`write`、`edit`、`grep`、`exec_command` 与 `write_stdin`。独立 Schedule routing workflow 在不点名 creation Tool 的情况下请求每六小时循环执行 timed work。Run id 的 SHA-256 parity 为该运行中每个 Provider/Model row 确定性选择 empty 或 seeded-equivalent variant；选中 variant 记录到 JSONL 与 summary artifact。

该 smoke 有意比简单 Provider connectivity check 更严格。它解析 persisted `conversation.jsonl`、检查 filesystem side effect，并解析 `events.jsonl`。Python Adapter 调用 `juex_eval.contract_oracle` 完成 conversation/Event contract check。Passing run 要求：

- 所有必需 Tool-use block 均存在；
- 不使用 legacy `shell` 或 `shell_input` Tool；
- 存在 `tty:true` 的 `exec_command` call；
- `events.jsonl` 中没有 transient `tool.output_delta` record；
- `tool.completed` 上的 authoritative terminal content 有界，且包含 carriage-return progress、interactive prompt 与 completion token；
- 运行中的 `exec_command` 与完成它的 `write_stdin` 都在 `tool.completed.payload.result` 上有 structured shell result；
- 执行中途的 `write_stdin` interaction 能恢复运行进程；
- Command 成功完成，verification output 含 run token；
- 磁盘上出现预期 `write`/`edit` side effect。

Schedule routing subscenario 始终避开 Command-Observable route 与竞争 scheduler command。Variant-specific contract：

- `empty`：每个 `schedule_create` 前成功完成 `observable_list`，随后恰好持久化一个带 requested-id、`type: schedule`、`schedule_config`、`interval.every_seconds: 21600` 与请求 Observation content 的 entry。
- `seeded-equivalent`：初始已有一个不同 id 但 interval/content 符合请求的 Schedule；检查暴露等价 `schedule_config` 且 Runtime state 为 `running` 的原生 `observable_list` result；不产生成功 `schedule_create`，不调用 `observable_create`、`observable_delete` 或 `observable_stop`；最终 state 恰好保留该等价 Schedule。

`skill_load` 只是建议，不属于 outcome contract。无论指南省略、与 listing 并行加载或稍后加载，运行都可通过。偶然 inspection command 也不使正确结果失败；精确 persisted Schedule shape 是 authoritative routing outcome。Contract validator 同时支持 interval cadence 与 monthly calendar cadence（`timezone`、`monthly.days`、`monthly.times`）。默认 live Provider sweep 仍使用六小时 interval scenario；deterministic evaluation test 覆盖 monthly prompt、Tool input、seeded equivalence 与 persisted config contract。

Shell loop、detached interval sleep、`watch`、`crontab` 与 `systemd-run` 仍被拒绝，因为它们产生竞争 recurring side effect。允许额外 `observable_list`，包括 create 后验证。Empty variant 中每次 create attempt 前至少有一个 successful list result。若模型根据 failure hint 恢复，允许失败的 `schedule_create` attempt；最终必须恰好一个 create call 成功。Seeded-equivalent variant 不受 batching 影响：completion token 必须在 successful list result 后出现；mutation 与 final-state check 拒绝盲目 duplicate creation。Seeded variant 中允许失败 speculative `schedule_create`，条件是模型从 successful list result 恢复，并保持 seeded config 不变。

每次 Schedule retry 使用新 Workspace 与 Session。Transcript、Event、stdout、stderr、prompt、final `observables.json` 和 contract report 保留在 `cases/<provider_model>/schedule-routing/attempt-N/`。Seeded attempt 还保留 `seed-observables.json`，避免把 initial fixture 与 final state 混淆。只有 allowlist 中 transient Turn failure 可在 fresh attempt 消耗一次 `--retries 1` budget。Schedule contract failure 是 product failure，绝不 retry。Persistent failure 仍使选中 `provider:model` 失败；命令不会静默切换目标。Contract report 将 failure 分类为 Model capability failure 或 hard Runtime failure，任何 failure 都使 strict live gate 失败。

失败 `provider:model` 不是 skip。显式选择不存在的 Model、empty eligible default rotation、不可达 endpoint 与 Provider 显式报告 unavailable，都以 `provider_unavailable` 失败，而不是 product regression。保留 report，并说明问题属于配置、Provider capability、prompt-following 还是 Juex regression。

Live Provider/network command 只有在命名 allowlist rule 匹配 structured `retryable: true`、allowlisted transient HTTP status、allowlisted network signature 或有界 Provider timeout 后，才可重试恰好一次。从 candidate/final validation 调用的 Provider smoke 禁用内部 retry，使外层 step 拥有一次 retry。Standalone Provider smoke 只接受 `--retries 0` 或 `--retries 1`。Deterministic test、build、lint、race 与 contract assertion 绝不 retry。

只有更大变更要求覆盖每个 eligible configured Model 时才用 `--all-models`。Report 记录 selected ref、candidate ref、seed、resolved config path、脱敏 config hash 和精确复现命令。Hash 覆盖 Provider/Model identity、protocol、effective Tool/reasoning capability、thinking effort 与 effective context-window metadata；绝不复制 credential、header、query value 或 environment mapping。两个保留的非 secret Runtime override `PROVIDER_THINKING_EFFORT` 和 `PROVIDER_CONTEXT_WINDOW` 以规范化值参与 hash。Effective Provider endpoint 只贡献 opaque SHA-256 identity，绝不记录原 URI、user information、path 或 query。第二个 opaque profile identity 覆盖 effective capability、compat、header 与 query setting。Unknown Provider/Model field 保留在隔离配置中，使 Juex strict loader 拒绝相同 invalid source shape，而不是静默 normalize。缺失、不可读或 malformed local Provider config 以 `environment_failure` 和 action `fix_environment` 失败。格式正确但缺少 Provider/Model ID 或无 eligible Provider 的配置在 live request 前以 `provider_unavailable` 失败。

## Compaction 质量

Compaction evaluation 由 Operator 触发：

```bash
make build
tests/eval/compaction_eval.sh
```

Gold fact、scoring rubric、cache metric 与 report output shape 见 `docs/compaction/evaluation.zh.md`。这是 long-running Agent context retention 的项目级质量评估。普通 E2E test 覆盖 deterministic Runtime 行为；live compaction evaluation 用记录 seed 选择一个 eligible Provider-config ref，使日常验证成本低。Scorecard 还会把压缩后的 `Goal` content 与 `goal_state.json` 交叉检查，验证 `Next Steps` 中 unfinished Notes，并证明 Notes 在 compaction 后保持不变且能被复述。

## 开发记录

每个完成的开发任务都应留下 validation record：

```bash
bash tests/eval/development_eval.sh
```

Deterministic phase 复用 candidate planner，因此一次 `go test ./...` 已包含 `tests/e2e`，无需重复 standalone E2E。已有 live selection evidence 保留；JSON/Markdown record 还包含 outcome、rule、reason、retry attempt、`blocks_merge` 与 recommended action。

Compaction、context projection、Provider replay 或 long-session 变更使用 `--compaction-eval`。Record 链接 command log、`provider:model` smoke summary、Schedule routing coverage 与 scorecard，使后续 worker 能判断行为改善、持平或回归。
