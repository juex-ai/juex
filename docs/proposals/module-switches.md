**JueX Module Switches: Original Design Inventory**

> English | [中文](module-switches.zh.md)

Status: the preset and disablement design has shipped, including [PR #535](https://github.com/juex-ai/juex/pull/535) and [PR #536](https://github.com/juex-ai/juex/pull/536). Updated: 2026-09-08. The 18-module inventory below records the original discussion; the [current configuration contract](../../internal/app/config/README.md) is authoritative and also includes the subsequently added input-tracking module.

Following the minimal-mode discussion on 2026-09-06, this proposal defines 18 Module switches using `modules.<id>.enabled`. `preset` supports `minimal` and the proposed name `standard`. Explicit switches override the preset; repeated explicit switches retain the existing configuration-layer precedence.

A Module owns its feature's tools, automatic context, execution policies, and resource lifecycle. Disabling it prevents tool registration, new feature context, feature policy execution, and construction or recovery of private resources. Retention follows resource meaning: delete current Goal/Notes working state when their Modules are disabled or removed; retain Scratchpad files, durable Memory knowledge, user configuration, and history. Framework cleanup uses resource ownership without starting disabled Modules or parsing their state payloads. A feature switch is not a file-access permission.

The table shows preset defaults, not unconditional resource startup. Extension allowlists, MCP server definitions, and Hook configuration still constrain what is loaded.

| Module | Tools | Context contribution | Hooks, policies, and resource lifecycle | standard | minimal |
| --- | --- | --- | --- | --- | --- |
| `basic-file-tools` | `read`, `write`, `edit` | No separate persistent section; descriptions and results suggest advanced tools only when effectively available | File reads, writes, edits, required path checks, and Artifact readback | On | On |
| `shell` | `exec_command`, `write_stdin`, `list_shell_sessions` | Shell syntax, session usage, and active-session information | Asynchronous commands, TTY, stdin, polling, session management, and shutdown | On | On |
| `apply-patch` | `apply_patch` | Patch-format guidance in the tool definition; no separate persistent section | Patch validation and application | On | Off |
| `chunked-write` | `write_begin`, `write_chunk`, `write_commit`, `write_abort` | On-demand guides, result suggestions, and history folding for active or completed writes | Active-write state, recovery, validation, commit, abort, and cleanup | On | Off |
| `file-search` | `grep` | Tool description; no separate persistent section | Workspace search | On | Off |
| `operating-context` | None | Brief runtime information such as cwd, OS, and current time | Context only; does not control process environment, dotenv, or Sandbox | On | On |
| `agents-md` | None | Automatically load global and project AGENTS.md into the system prompt | Locate and read guidance files; disabling this does not prevent explicit reads through `read` | On | Off |
| `skills` | `skill_search`, `skill_load` | Available Skills index; full body returned on explicit load | Skill discovery, indexing, filtering, prompt budgets, and loading | On | Off |
| `scratchpad` | No dedicated tools; reuse file tools or Shell | Scratchpad path and usage guidance; no automatic file-body injection | Prepare the Thread working directory and contribute its path; retain it across Generations; when disabled, do not create or advertise it, and do not delete existing files | On | Off |
| `goal` | `get_goal`, `create_goal`, `update_goal` | Runtime Goal contract, necessary continuation prompts, and compaction state | Goal storage, completion/continuation policy, clearing on context reset, and deletion of goal_state.json on Module disablement or removal | On | Off |
| `notes` | `update_notes` | Runtime Notes and compaction state | Notes storage, content budget, clearing on context reset, and deletion of notes.md on Module disablement or removal | On | Off |
| `memory` | `memory_search`, `memory_write`, `memory_delete` (proposed builtin names) | Necessary Module-owned guidance; bodies returned on demand through tools, without injecting all memories | Agent-scoped durable knowledge, Markdown entries and a rebuildable index; maintain the index through ThreadStart/PostCompact lifecycles; disable tools, guidance, and maintenance while retaining knowledge | On | Off |
| `context-control` | `context_new`, `context_compact` | Capacity reminders and guidance for model-controlled context changes | Accept model requests for Generation transitions and compaction; does not own underlying Generation persistence | On | Off |
| `worker-threads` | `thread_create`, `thread_list`, `thread_status`, `thread_send`, `thread_subscribe`, `thread_stop`, `thread_archive` | Subscribed Worker results and notifications; no additional fixed system section required | Worker execution, subscriptions, result delivery, stopping, and cleanup; not a storage switch for every Thread on disk | On | Off |
| `observables` | `observable_list`, `observable_create`, `schedule_create`, `observable_start`, `observable_stop`, `observable_delete`, `observable_observations` | Observation inputs and on-demand guides, rather than a fixed persistent prompt section | Command/schedule producers, definitions, state, records, and Main delivery | On | Off |
| `mcp` | Dynamic server-declared tools | Tool schemas/results and MCP Notification inputs | MCP connections, local processes, catalogs, calls, notifications, Agent-scoped sharing, and shutdown | On | Off |
| `hooks` | No direct model tools | Additional context, continuation prompts, and compaction guidance returned by external command Hooks | Parse Hook resources and execute ThreadStart, UserPromptSubmit, PreToolUse, PostToolUse, Stop, PreCompact, and PostCompact commands | On | Off |
| `extensions` | No direct tools; delegate resources to their consuming Modules | No direct persistent prompt; Skills, Hooks, MCP, and other consuming Modules contribute content | Composition-time plugin discovery, manifests, resource declarations, environment defaults, and private data paths; disablement takes effect before discovery or parsing | On | Off |

`minimal` enables only `basic-file-tools`, `shell`, and `operating-context` by default. They expose six tools: `read`, `write`, `edit`, `exec_command`, `write_stdin`, and `list_shell_sessions`. Ordinary requests contain only brief environment/Shell guidance and necessary conversation content. Explicitly enabling other Modules adds their tools and context to the effective composition.

Naming recommendation: use `kebab-case` for Module IDs, following existing `builtin-tools`, `worker-threads`, and `context-control`; retain existing naming for configuration fields. `basic-file-tools` identifies the basic file operations, while `file-search` distinguishes file-content search from Skill or MCP discovery. YAML supports both `_` and `-`; choosing `-` is a consistency decision. One `shell` Module retains the three existing tools and session protocol, enabled in both presets, without adding a tool.

`operating-context` is extracted from the current `thread-context`. Scratchpad and active-Shell guidance move to their respective Modules. File-tool workflows can then retain working-directory information without enabling Shell.

`hooks` controls external command Hooks from configuration and Extensions, not Framework lifecycle interfaces. Goal's FinishPolicy follows the `goal` switch, and chunked-write history projection follows `chunked-write`. Disabling `hooks` must not disable every builtin Module's lifecycle behavior.

Extension resources require both their source and consumer to be enabled: the plugin is selected by `extensions.allow`, the `extensions` Module is enabled, and the relevant `skills`, `hooks`, `mcp`, or `observables` Module is enabled. Disabling `extensions` does not disable workspace-native Skills, Hooks, or MCP. When `hooks` is disabled, even an enabled Extension must not read, parse, or execute its Hook resources. Only enabled consumers process their specific resources.

A subsequent decision brings Memory back from `juex-extensions/extensions/memory` as an independent Go Module. Its switch owns tools, necessary guidance, index maintenance, and resources without depending on extensions, mcp, skills, or hooks. Builtin lifecycle callbacks are separate from external command hooks. Preserve current on-demand retrieval and explicit write/delete behavior; do not restore MemorySlot or add vector storage, automatic extraction, or Dream workflows. Enabled in standard and disabled in minimal are the proposed defaults. Disabling retains durable knowledge for reuse on re-enablement. Retire the old Extension distribution and provide manual knowledge-cutover instructions in a separate deliverable, without automatic data migration.

Advanced-tool suggestions depend on effective capabilities rather than preset names. For example, `standard` with chunked writes disabled must not have `write` recommend chunked writes; `minimal` with chunked writes enabled may recommend them. When Skills are disabled, other features retain sufficient basic guidance and do not recommend an unavailable `skill_load`.

This iteration does not add Module switches for the following:

- Automatic compaction, summary generation, and tool-output budgets retain Runtime configuration and generic mechanisms. Disabling `context-control` removes model-facing tools and capacity reminders; host `/new`, `/compact`, and configuration-enabled automatic compaction remain available. Goal/Notes-specific summary contributions follow their own Module switches.
- Thread, Input, Turn, Generation, Journal, Usage, durable events, cancellation, and recovery remain core lifecycle responsibilities.
- Sandbox, basic process environment, model selection/fallback, and Provider adapters retain their existing configuration and are not implicitly changed by presets.
- Separate media, failure-ledger, and generic status-presentation Modules are not among these 18 items. Existing foundations remain; the audit's later boundary recommendations are not commitments to add those configuration options.

The current `builtin-tools` splits into the first five entries. `thread-context` splits into contributions from `operating-context`, `scratchpad`, and `shell`. `project-guidance` is proposed to become `agents-md`. Other existing Modules retain their feature identities, and `extensions` becomes a new composition-time Module. Implementation uses the new configuration contract without forwarding old names.

Original target configuration example; see the current configuration contract for supported names:

```yaml
preset: minimal

modules:
  agents-md:
    enabled: true
  hooks:
    enabled: false
```

This document is a proposal inventory. It does not change runtime configuration or product code. See the [minimal-mode audit](minimal-mode-audit.md) for current implementation evidence.

Web UI belongs to complete Module disablement too: Goal/Notes status entries and live subscriptions, plus Scratchpad panel entries and file-tree requests, must disappear with their Modules. Delete current Goal/Notes state files on Module disablement or removal; retain Scratchpad files and history. See [Module UI extension research](module-ui-research.md) for proposed slots, state projections, and composition; these remain design suggestions.
