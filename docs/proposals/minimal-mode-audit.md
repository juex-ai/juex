**JueX Minimal Mode: Module Disablement Audit and Refactoring Inventory**

> English | [中文](minimal-mode-audit.zh.md)

Audit date: 2026-09-06. Source baseline: `2b0c1bb`, workspace `/Users/hejinhai/git/project/juex`. This audit inspected source and ran isolated probes without modifying product code, user configuration, or running Agents. New Module names, interfaces, and mode configuration below are proposals, not implemented features.

The existing Module framework can run with zero tools and an empty ordinary-request system prompt. However, configuration alone cannot yet produce a usable composition of `read + write + edit` plus the three existing Shell tools. The main gaps are coarse Module boundaries, feature preparation before registration, and feature-specific policies in Framework/Foundation. The lifecycle framework does not need replacement; focused Module splits and a few narrow interfaces are needed.

The subsequent discussion is captured in the [proposed 18 Module switches](module-switches.md), including proposed `basic-file-tools`, `file-search`, and `agents-md` names, one Shell Module retaining the three existing tools, and `standard`/`minimal` defaults. That inventory describes the target design; current-state tables in this report describe the audited implementation.

Disablement has two acceptance levels. Reducing model context requires removing tool schemas, system guidance, runtime messages, and error suggestions. Complete Module disablement also prevents resource construction, private-state reads, process startup, feature-state recovery, and feature-state publication. Disabling or removing Goal/Notes also deletes their time-sensitive current-state files. This is Framework cleanup based on resource ownership, without starting disabled Modules or reading their state payloads. Required history persistence, protocol validity, cancellation, and recovery must remain intact.

**Observed behavior**

The probe executed an ordinary conversation request using a stub Provider in an empty temporary workspace, with no external Skills, AGENTS.md, Extensions, or MCP servers configured.

| Composition | Tools received by Provider | System bytes | Internal ToolSpec JSON bytes | Additional observation |
| --- | ---: | ---: | ---: | --- |
| Current Module defaults | 34 | 843 | 14,620 | Request history contained user input and one runtime-context message |
| Only `builtin-tools` enabled | 12 | 0 | 5,951 | Includes chunked writes, patch, grep, and three Shell tools |
| All 11 known Modules disabled | 0 | 0 | 2, meaning `[]` | Ordinary conversation succeeded; chunked-write manager and Scratchpad directory still existed |

These are isolated-probe byte counts, not tokenizer measurements, production configuration sizes, or real small-model performance results. The 843-byte prompt does not represent a complete production prompt; project guidance, user Skills, Notes, and other resources add content. An empty ordinary request with every Module disabled does not imply that compaction requests contain no hardcoded guidance.

A second probe disabled all Modules while allowing a test Extension with an intentionally malformed `hooks.yaml`. `app.New` still failed with `hooks: parse .../hooks.yaml: yaml: line 1: did not find expected ',' or ']'`. Hooks disablement therefore does not cover plugin resource parsing before registration.

**Current model tools and owners**

| Module | Tools | Minimal-mode treatment | Current disablement |
| --- | --- | --- | --- |
| `builtin-tools`: basic files | `read`, `write`, `edit` | Keep | Can only toggle with the rest of the Module |
| `builtin-tools`: patch | `apply_patch` | Disable | Internal `DisableApplyPatch` exists, but production configuration has no independent switch |
| `builtin-tools`: chunked writes | `write_begin`, `write_chunk`, `write_commit`, `write_abort` | Disable | No independent Module switch |
| `builtin-tools`: search | `grep` | Disable; use Shell for search | No independent Module switch |
| `builtin-tools`: Shell | `exec_command`, `write_stdin`, `list_shell_sessions` | Keep all three tools in one shell Module | The session protocol relies on these tools together; enable and disable them together |
| `skills` | `skill_search`, `skill_load` | Disable | Tools and automatic Skill context can be disabled; other tools still refer to `skill_load` |
| `worker-threads` | `thread_create`, `thread_list`, `thread_status`, `thread_send`, `thread_subscribe`, `thread_stop`, `thread_archive` | Disable | Existing switch prevents tool and Worker-manager construction |
| `observables` | `observable_list`, `observable_create`, `schedule_create`, `observable_start`, `observable_stop`, `observable_delete`, `observable_observations` | Disable | Existing switch disables the Manager and command/schedule producers; MCP Notifications are a separate input source |
| `goal` | `get_goal`, `create_goal`, `update_goal` | Disable | Tools, live Goal context, and automatic continuation can be disabled |
| `notes` | `update_notes` | Disable | Tools, live Notes context, and Module-state operations can be disabled |
| `context-control` | `context_new`, `context_compact` | Disable model-initiated operations and capacity reminders | Existing switch does not control host `/new`, `/compact`, or automatic compaction |
| `mcp` | Dynamic tools from connected servers | Disable | Existing switch is checked by App/Web startup |

The empty configuration exposes 34 static tools; external MCP tools are additional. Registration sources: [runtime_modules.go](../../internal/app/runtime_modules.go#L117), [BuiltinProviders](../../internal/tools/builtin.go#L70), [Worker tools](../../internal/app/worker_threads.go#L1195), and [Observable tools](../../internal/observable/tools.go#L123).

**Other context and capabilities**

| Capability | Current switch or owner | Finding |
| --- | --- | --- |
| Global/workspace AGENTS.md autoload | `project-guidance` | Already modular. Disabling it prevents its file reads and prompt contributions; no new loader abstraction is needed |
| External Skill discovery, indexing, and prompts | `skills` | Loaded inside the Module factory, with include/exclude and prompt budgets. Three builtin guides are hidden, on-demand resources, not full bodies permanently injected into the system prompt |
| Scratchpad guidance | `thread-context` | Can be disabled with the whole Module, but also loses cwd, OS, time, and active Shell state. Prompt disablement is not storage disablement |
| Scratchpad directory and paths | Thread storage and several RuntimeContext fields | Core code creates the directory and carries a dedicated path. Full pluggability requires Module ownership; existing files must remain untouched when disabled |
| cwd, OS, time, Shell usage | `thread-context` | Coupled to Scratchpad. Minimal guidance belongs to Shell/operating-context contributors |
| Active Shell session guidance | `thread-context` | Should follow the Shell Module to avoid suggestions after Shell is disabled |
| Per-iteration Goal/Notes recitation | Respective Modules, projected as `runtime_message` | Already disableable. Measuring only the system prompt misses these messages |
| Context-capacity reminders | `context-control`, projected as `runtime_message` | Already disableable; capacity estimation and overflow handling remain Runtime responsibilities |
| Hooks at ThreadStart, input, tool, finish, and compaction phases | `hooks` | Execution policies are modular, but YAML/Extension loading occurs outside disablement checks and remains a startup dependency |
| Extension discovery, manifests, environment defaults, and resource sources | `extensions.allow`, App preprocessing | `allow: []` selects no Extensions, but no `extensions` Module master switch exists. Disabling all Modules does not deselect allowed Extensions |
| Currently external Memory and similar plugins | Extension Hook/Skill/MCP resources | Memory was distributed as an Extension at audit time; a subsequent decision brings it back as an independent builtin memory Module. Other third-party Extensions retain process isolation |
| MCP Notifications | MCP connection and App notification gate | Separate from the Observable Manager. Disabling `observables` does not disable notifications from enabled MCP; minimal mode should disable both |
| Automatic compaction, summary model, overflow recovery | `compaction` configuration and Runtime | Configurable, but not the same Module switch. Keep necessary small-window budget protection while removing optional feature guidance from summaries |
| Long-input/tool-output externalization and `artifact://` readback | Runtime projection, Artifact Store, `read` | Distinct from Scratchpad. Output externalization can operate with automatic compaction disabled and protects small windows; preserve readback in the six-tool mode |
| Image reads and attachments | `read` and Provider media adapters | No independent Module. Descriptions can follow model capabilities; full media disablement is additional work, not a prerequisite for six tools |
| Tool-error classification, recovery, diagnostic events | Runtime failure ledger | No Module switch, with read/grep/Hook special cases. No separate persistent system section was found; lower priority than tool/context separation |
| Model selection/fallback, authentication, environment, Sandbox | Basic configuration/execution services | Do not all need Module conversion for minimal mode. A single model can be expressed through the model list; preserve execution safety and cancellation |
| Thread, Input, Turn, Generation, Usage, history, SSE/Web/CLI | Framework/Foundation and host interfaces | Core execution and user operations, not additional model tools. Minimal mode must preserve persistence and the control plane |

Key sources: [AGENTS.md and Thread context](https://github.com/juex-ai/juex/blob/2b0c1bbdc2e55741d680e858e79c635899bf9cf1/internal/modules/promptcontext/module.go#L28), [hidden builtin guides](../../internal/skills/builtin.go#L79), [runtime-context messages](../../internal/runtime/active_context.go#L76), [resource preprocessing](../../internal/app/resource_refs.go#L72), and [Extension environment merging](../../internal/app/agent_runtime.go#L76).

**Required changes for a usable six-tool mode**

P0 means necessary for the target, P1 means necessary for complete Module separation, and P2 means later cleanup. These are implementation priorities, not incident severities.

| ID | Priority / strength | Problem, ownership, and direction | Acceptance focus |
| --- | --- | --- | --- |
| A1 | P0 / Strong | Split `builtin-tools` into `basic-file-tools`, `apply-patch`, `chunked-write`, `file-search`, and `shell`. Reuse providers where useful, but give each feature its own factory and resources | Effective Provider tools are exactly six for unmodified minimal mode, without filtering names after registration |
| A2 | P0 / Strong | Keep the existing Shell session protocol and `exec_command`, `write_stdin`, and `list_shell_sessions` together in shell. The Module owns session resources, guidance, and cleanup; add no tool | Commands longer than the yield interval retain final output; exit status, timeout, cancellation, and child cleanup are verified |
| A3 | P0 / Strong | `write` always recommends chunked writes and declares `maxLength: 2000`. Separate short-write limits/recommendations from the basic contract and define a complete path without chunked writes | Basic file tools can independently write reasonably sized files; descriptions/schemas do not reference disabled tools |
| A4 | P0 / Strong | Extract Scratchpad and active-session guidance from `thread-context`; operating-context contributes cwd/platform/time, and shell contributes minimal syntax and session guidance | No Notes, write_begin, or grep suggestions remain when Scratchpad is disabled; execution location stays clear |
| A5 | P0 / Strong | Add an Extension-loading Module master switch before Discover, manifests, Hook files, and environment-default processing. Keep `extensions.allow` for selecting plugins when enabled | Disabled Extensions cannot block startup, configuration validation, or status with malformed/conflicting resources, and contribute no environment defaults |
| A6 | P0 / Strong | Generate descriptions and optional advanced suggestions from effective configured/composed capabilities. Basic tools keep complete basic guidance and errors; they neither read YAML themselves nor depend on advanced implementations | Supported combinations never suggest unavailable tools or absent state Modules; check all required tools, not only write_begin |
| A7 | P0 / Strong | Add `preset` with `minimal` and proposed `standard`; merge presets and explicit switches across layers before letting explicit values override preset defaults | New optional Modules do not enter minimal automatically. A higher-layer preset alone does not erase lower-layer explicit switches; repeated explicit switches retain layer precedence |

A1–A3 sources: [builtin grouping](../../internal/tools/builtin.go#L70), [file-tool switches/schema](../../internal/tools/builtin_file.go#L16), and [Shell sessions](../../internal/tools/builtin_shell.go#L16). A6 sources: [error guide injection](../../internal/runtime/loop.go#L1570) and [Group-to-Skill mapping](../../internal/tools/registry.go#L49). A7 source: [default enablement](../../internal/config/modules.go#L21).

Discussion update, 2026-09-06: the user confirmed that advanced suggestions should follow effective enablement, and that `preset` supplies defaults overridden by explicit switches. The following concrete semantics are proposed, not implemented:

- Use `standard` / `minimal`. `standard` preserves current default behavior, and omission selects standard. It does not bypass Extension allowlists, resource configuration, or other enablement constraints. Reserve “default” for the concept of an omitted selection.
- `minimal` enables only basic-file-tools, shell, and operating-context by default, excluding newly added optional Modules. Explicitly enabling AGENTS.md, Skills, or others is allowed; overridden configurations need not remain six-tool compositions. The strict six-tool acceptance criterion applies to the unmodified preset.
- Merge preset selection and explicit switches separately. The last explicit preset wins; the last explicit value of each switch wins. Then resolve each switch from its explicit value, falling back to the chosen preset.
- Thus Home `skills.enabled: true` survives Workspace `preset: minimal` alone. Workspace must explicitly set `skills.enabled: false` to disable it, consistently with explicit-switch precedence.
- Do not expand presets into the explicit modules map while reading individual YAML layers. Preserve the distinction between user settings and preset defaults, including sparse Agent configuration saves.
- Unknown preset names are configuration errors. Neither preset implicitly selects a model or enables Extensions excluded by the allowlist.

Example proposed configuration:

```yaml
preset: minimal

modules:
  agents-md:
    enabled: true
  notes:
    enabled: false
```

Tool construction/execution consumes effective capabilities, not `preset == minimal`. Minimal with chunked writes explicitly enabled should offer their suggestions; standard with them disabled should not. Keep `write` descriptions, schema limits, and failure guidance consistent rather than removing suggestions while leaving a contract that cannot work independently. Results always contain basic success/failure information; advanced suggestions are optional additions.

**Further separation: no registered Module, no feature behavior**

| ID | Priority / strength | Current behavior and proposed ownership | Why a switch alone is insufficient |
| --- | --- | --- | --- |
| B1 | P1 / Strong | `app.New` constructs and restores the chunked-write manager unconditionally. Move it into its Module; active writes belong to the correct Thread state | Even with builtin-tools disabled, the manager is allocated and history traversed; existing records may cause file checks |
| B2 | P1 / Strong | Runtime recognizes `chunkedwrite.Event` and stores `llm.Block.ChunkedWrite`; both Runtime and LLM projections directly fold chunked-write history. Move this to Module-owned result contributions/history projection | Feature-specific algorithms remain in the core path after tools disappear; moving construction is insufficient |
| B3 | P1 / Strong | Remove unconditional Scratchpad directory creation and dedicated context fields; let a ThreadResource prepare and contribute the path, retaining existing files | Prompt disablement works, full storage disablement does not. Existing generic Thread Dir is sufficient; no Scratchpad-specific lifecycle is needed |
| B4 | P1 / Strong | Defer Hook parsing/semantic validation until effective composition is known, or skip feature-resource processing in an explicit disabled branch; prevent lower-layer validation from defeating later disablement | Factory checks occur too late. Basic syntax errors in the main configuration YAML should still be reported |
| B5 | P1 / Strong | Goal/Notes implementations, compaction-state interfaces, and dedicated getters remain in Runtime. First make compaction guidance conditional, then move concrete state policies out | Live-state disablement works, but summary guidance still unconditionally discusses Goal contracts and Notes; new state features still require core edits |
| B6 | P1 / Worth exploring | Define a shared Framework phase or admission gate for external-input activation after the Main recovery barrier is established; connect Observable/MCP adapters | StartRuntime precedes Thread recovery. App still explicitly calls Observable.StartAll and MCP gate.Activate |
| B7 | P1 / Strong | Let tools declare execution constraints; Framework enforces them without inferring serialization from UI groups | isSerializedToolCall hardcodes ThreadState/WorkerThread groups, so another Module cannot independently declare the same constraint |
| B8 | P1 / Strong | Base read-only checks, diagnostics, and status on effective capabilities; at least fix diagnose loading/checking disabled Skills/MCP | Startup honors switches while diagnostics may fail or perform non-offline MCP readiness checks; entry points disagree |
| B9 | P1 / Strong | Replace Goal/Notes/Scratchpad-specific Thread API, event projection, and page wiring with Module state contributions and UI slots. Go publishes effective UI contributions and Web assembles them centrally | Complete disablement covers state reads, API capabilities, subscriptions, and UI; hiding buttons or removing model tools alone does not meet the added Web scope |
| B10 | P2 / Worth exploring | If failure classification/ledger is optional diagnostic policy, move tool-name cases into policy/observation while retaining core error results and persistence | A ledger is created each Turn and knows features; do not pluginize all generic error handling merely for the first six-tool release |
| B11 | P1 / Strong | Modules declare ownership and retention for private resources; Framework cleans current Goal/Notes state on disablement/removal, separately from normal resource Close | Cold starts, inactive Threads, and removal of an implementation must not leave revivable working state. Cleanup requires no Module instance, payload parsing, or goal/notes-specific core cases |

Evidence locations:

- B1: [construction](../../internal/app/app.go#L340), [recovery call](../../internal/app/app.go#L682), [recovery implementation](../../internal/tools/chunked_write.go#L40).
- B2: [Runtime result recognition](../../internal/runtime/loop.go#L1668), [Runtime history folding](../../internal/runtime/context_projection.go#L129), [Provider folding](../../internal/llm/provider_projection.go#L44), [feature-specific Block field](../../internal/llm/types.go#L89).
- B3: [Thread creation](../../internal/thread/store.go#L168), [dedicated context field](../../internal/runtime/module/registry.go#L33).
- B4: [Extension Hook loading](../../internal/app/resource_refs.go#L363), [configuration-layer parsing](../../internal/config/config.go#L973).
- B5: [state interfaces](../../internal/runtime/compaction_summary.go#L20), [fixed summary guidance](../../internal/runtime/contextbudget/summary.go#L74), [state queries](../../internal/runtime/thread_state_modules.go#L8).
- B6: [explicit activation](../../internal/app/app.go#L692), [recovery barrier](../../internal/app/pending_recovery.go#L293).
- B7: [group-based serialization](../../internal/runtime/loop.go#L1551).
- B8: [MCP diagnostics](../../internal/cli/doctor.go#L495), [Skills diagnostics](../../internal/cli/doctor.go#L602).
- B9: [runtime status composition](../../internal/app/runtime_status.go#L257).
- B10: [error classification](../../internal/runtime/tool_failure.go#L117).
- B11: [current enabled-only cleanup contract retaining disabled files](../../internal/runtime/module/lifecycle.go#L41). This behavior must change; see [working-state lifecycle](module-ui-research.md#working-state-deletion-and-retention) for target semantics.

**Lifecycle interface assessment**

Existing ToolProvider, ContextProvider, Runtime/ThreadResource, Quiesce/Close, TurnInputPolicy, ToolPolicy, FinishPolicy, ThreadStartPolicy, CompactionPolicy, and ContextRenewalCleaner/Observer are sufficient for much of the work. They already support independent AGENTS.md, Skills, Goal, and Notes tool/context contributions and filter disabled factories before construction. Scratchpad guidance, basic-tool splitting, and disabling Extension discovery do not require rewriting Engine lifecycles.

The following boundaries need additions or clarification:

1. **Provider history projection.** ContextProvider adds sections but cannot transform existing message sequences. ToolPolicy results contain only text and IsError, not structured chunked-write facts. Removing B2's special cases requires generic structured result contributions and constrained Module projection over message copies before Provider requests. Framework preserves original durable facts; projections cannot mutate the Journal, must preserve valid tool-use/result pairs, and require budget recalculation over the final request. Whether these need two interfaces should follow the smallest concrete implementation need.
2. **Compaction state and summary validation.** Existing CompactionPolicy can supply guidance and first absorb unconditional feature prose. It lacks generic structured summary state and a complete candidate-summary validation interface. Preserving exact Goal fields and unfinished Notes items calls for owned, budgeted compaction contributions and constrained pre-commit validation, not another FeatureState field in core.
3. **Composition-time resources.** Extensions must supply environment defaults and Skill/MCP/Hook/Observable resources before consumers are constructed. RuntimeResource start/close cannot naturally express these inputs, and sealed sets cannot accept later tool registration. Select resource-source factories from effective Modules, obtain read-only declarations with provenance, then validate and inject them into downstream factories in App. A narrow composition object is sufficient initially; a global service locator is unnecessary. Loading plugins only in StartRuntime does not solve the ordering problem.
4. **Resource preparation versus input activation.** Startup/shutdown order works, but accepting external inputs only after establishing recovery barriers still uses feature-specific App wiring. A shared Ready/activation contract or explicitly injected admission gate is sufficient; no universal callback bus or dependency DAG is needed.
5. **Declared execution constraints.** Framework owns serialized execution, but Tool/Module declarations should identify which tools require it rather than implicit Group names. Feature-owned ToolPolicy or text contributions should own guide suggestions; current ToolPolicy can carry error hints without a universal callback.
6. **Private-state retirement.** Normal Close releases process resources without deleting working state needed after restart. Module disablement/removal is a separate configuration-activation event: record resource ownership and retention when resources are created, then let Framework idempotently delete declared disposable state without constructing the Module, even if its implementation has been removed. Re-enable with empty state; retain history and retained-resource files.

The added Web scope requires the narrow state/UI contribution seam in B9; it need not generalize every diagnostic view. Builtin-guide resource contributions can be introduced as needed. Minimal mode should not first build an all-purpose plugin framework. Sealed registration already fits configuration changes followed by Agent restart; this request does not require hot plugging.

**Added scope: bring Memory back**

The current source is `juex-extensions/extensions/memory`: Python MCP provides search, write, and delete; a Skill provides guidance; SessionStart/PostCompact command Hooks rebuild the index. Move these responsibilities into one builtin Go memory Module using generic lifecycles, independent of external command hooks or MCP/Skills/Extension loading. Disablement stops memory reads, writes, maintenance, and guidance while retaining durable knowledge; the disposable Goal/Notes policy does not apply. Preserve on-demand retrieval and explicit writes without adding automatic memory, vector retrieval, or a dedicated MemorySlot. Delivery includes retirement of the old Extension distribution, manual cutover instructions, and regression verification. The historical 34-tool audit is not the new standard tool count.

**Implementation order and acceptance**

First deliver A1–A7, B1, B3, B4, and B11: a usable six-tool composition, disabled Extension/Scratchpad capabilities, and no unavailable-tool suggestions. Minimal mode is a preset over compiled Module composition and uses ordinary assembly.

Next deliver B2 and B5–B8: narrow history/compaction interfaces, common external-input activation and diagnostic boundaries, and removal of concrete feature policies from core. Include B9 with the added Web pluggability scope and defer B10. See [Module UI extension research](module-ui-research.md) for state, API, and mounting boundaries.

Acceptance must cover actual behavior:

- Capture Provider requests through real App/Engine to prove exactly six schemas. Inspect system, runtime_message, descriptions, and errors, not just Registry counts.
- Execute read → write → edit → exec_command workflows and finish session operations through write_stdin / list_shell_sessions, including long files, long-running commands, timeout/cancellation, large-output externalization, and readback.
- Exercise `/new`, compaction, restart, and Pending Input recovery under the minimal composition, preserving durable state and ordering.
- Place malformed disabled-feature resources/state on disk; prove no payload parsing or disabled-Module construction/startup. Delete current Goal/Notes state by ownership; retain Scratchpad, configuration, and history. Missing files are a successful cleanup; failures are observable and retryable.
- Verify create Goal/Notes → apply Module disablement → /new → re-enable yields empty state. Normal exit/restart with the Module still enabled preserves state. Cover Threads without a running Module instance.
- Switch a Thread with old chunked-write records and Hook messages to minimal configuration and define history retention. Keeping history does not re-enable Modules; reducing context must not corrupt audit history or tool pairing. A new Context Generation can provide clean minimal context.
- Independently disable Skills, Notes, Scratchpad, chunked writes, patch, and MCP, checking other enabled features for dangling guides or suggestions.
- Regress ordinary-mode tools, MCP/Observable recovery barriers, and reverse shutdown; Main/Workers inherit effective composition consistently.
- Separately benchmark real target small models for first-token latency, task success, error recovery, valid tool calls, and context usage. This audit did not measure model performance; fewer tools alone do not prove better results.

Existing architecture tests allow semantic coupling such as B2 because they primarily inspect imports and classify `internal/chunkedwrite`, `internal/tools`, and `internal/llm` as Foundation. They cannot catch Goal/Notes implementations in the same package or tool-name branches. Add cross-package behavior tests and proportionate ownership rules, not tests whose only purpose is proving old names absent. See [boundary_test.go](../../internal/architecture/boundary_test.go#L36).

There is also a documentation scope mismatch: [ARCHITECTURE.md](../../ARCHITECTURE.md) says Feature disablement prevents construction, side effects, and publication, while the current guarantee mainly covers registered factories, not Extension preprocessing, the chunked-write manager, or Scratchpad. Update the architecture boundaries and configuration guidance alongside implementation, maintaining language peers. This audit did not directly rewrite the accepted product contract.

**Initial audit validation record (not validation of the proposed changes)**

- Existing factory-filtering, reverse-startup-rollback, App disablement, and tests/e2e ModuleLifecycle tests passed.
- Architecture and all three internal/modules package suites passed: `mise exec -- go test ./internal/architecture ./internal/modules/... -count=1`.
- Isolated probes used go test overlays without adding tests to the repository. The [probe source](/private/tmp/juex_minimal_mode_audit_test.go) and [overlay](/private/tmp/juex_minimal_audit_overlay.json) remain reproducible while their temporary files exist: `mise exec -- go test -overlay /private/tmp/juex_minimal_audit_overlay.json ./internal/app -run '^TestMinimalAudit' -v -count=1`.
- Inspected Module/config, App/resource discovery, builtin tools, Skills/Hooks/MCP/Observables, Thread storage, Runtime/LLM projection, compaction, diagnostics, and status. No full-repository test run, browser regression, or real model-service evaluation was performed.
