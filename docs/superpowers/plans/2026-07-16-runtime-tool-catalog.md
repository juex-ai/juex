# Runtime Tool Catalog Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Expose a complete, grouped builtin and per-server MCP tool catalog on the Runtime page, including descriptions, parameter schemas, and unambiguous effective timeout metadata.

**Architecture:** Every executable tool binds a shared `tools.ToolDefinition` to its handler, so production registration and read-only catalog projection have one metadata source. `internal/app` aggregates builtin definitions and cached MCP descriptors into plain runtime status types; `internal/web` maps those types to the existing endpoint; React renders native two-level disclosures through one reusable tool-detail module.

**Tech Stack:** Go 1.24, React 19, TypeScript 6, Vite, Tailwind CSS, native `details`/`summary`, Node test runner.

---

## File map

- `internal/tools/registry.go`: tool group, definition, binding, and effective-timeout contracts.
- `internal/tools/builtin_*.go`: builtin definitions and real handler bindings.
- `internal/{memory,observable,runtime,mcp}` and `internal/app/skill_tools.go`: package-owned definitions and registrations.
- `internal/mcp/manager.go`: defensive process-level descriptor snapshot.
- `internal/app/runtime_status.go`: catalog grouping and Runtime status projection.
- `internal/web/runtime.go`: HTTP DTO mapping for builtin and MCP tools.
- `frontend/src/lib/runtime-tool-catalog.ts`: pure labels and JSON Schema parameter projection.
- `frontend/src/components/RuntimeToolCatalog.tsx`: shared catalog disclosure rendering.
- `frontend/src/pages/Runtime.tsx`: Tools section and MCP server disclosure composition.
- `.agents/skills/juex-ddd/SKILL.md`, `ARCHITECTURE.md`, `DESIGN.md`, `frontend/README.md`: stable vocabulary and interface documentation.

### Task 1: Define tool metadata once and bind it at every registration owner

**Files:**

- Modify: `internal/tools/registry.go`
- Modify: `internal/tools/builtin_file.go`
- Modify: `internal/tools/builtin_chunked_write.go`
- Modify: `internal/tools/builtin_shell.go`
- Modify: `internal/tools/builtin_search.go`
- Modify: `internal/app/skill_tools.go`
- Modify: `internal/memory/memory.go`
- Modify: `internal/runtime/goal_tools.go`
- Modify: `internal/runtime/notes_tools.go`
- Modify: `internal/observable/tools.go`
- Modify: `internal/mcp/client.go`
- Modify: `internal/mcp/manager.go`
- Test: `internal/tools/tools_test.go`
- Test: `internal/app/app_test.go`
- Test: `internal/memory/memory_test.go`
- Test: `internal/runtime/goal_tools_test.go`
- Test: `internal/runtime/notes_tools_test.go`
- Test: `internal/observable/tools_test.go`
- Test: `internal/mcp/client_test.go`

- [ ] **Step 1: Write failing metadata and timeout tests**

Add assertions that default builtin names map to the fixed group taxonomy, that
skill/memory/goal/notes/observable/MCP registrations carry their package group,
and that provider-facing `Registry.Specs()` still exposes only name,
description, and schema. Add a timeout test with this contract:

```go
bounded := EffectiveToolTimeout(ToolDefinition{}, 90)
if bounded.Mode != ToolTimeoutModeBounded || bounded.Seconds != 90 {
    t.Fatalf("bounded timeout = %+v", bounded)
}
disabled := EffectiveToolTimeout(ToolDefinition{TimeoutPolicy: ToolTimeoutDisabled}, 90)
if disabled.Mode != ToolTimeoutModeDisabled || disabled.Seconds != 0 {
    t.Fatalf("disabled timeout = %+v", disabled)
}
```

- [ ] **Step 2: Run focused tests and verify RED**

Run:

```bash
go test ./internal/tools ./internal/app ./internal/memory ./internal/runtime ./internal/observable ./internal/mcp -run 'Tool(Group|Definition|Timeout)|Register.*Tools' -count=1
```

Expected: compilation or assertion failures because `ToolGroup`,
`ToolDefinition`, bindings, and group metadata do not exist.

- [ ] **Step 3: Add the registry contracts**

Implement this public shape without replacing the existing `Tool` fields, so
test-only composite literals remain source compatible:

```go
type ToolGroup string

const (
    ToolGroupFile         ToolGroup = "file"
    ToolGroupChunkedWrite ToolGroup = "chunked_write"
    ToolGroupShell        ToolGroup = "shell"
    ToolGroupSearch       ToolGroup = "search"
    ToolGroupSkill        ToolGroup = "skill"
    ToolGroupMemory       ToolGroup = "memory"
    ToolGroupSessionState ToolGroup = "session_state"
    ToolGroupObservable   ToolGroup = "observable"
    ToolGroupMCP          ToolGroup = "mcp"
)

type ToolDefinition struct {
    Name           string
    Group          ToolGroup
    Description    string
    Schema         map[string]any
    TimeoutPolicy  ToolTimeoutPolicy
    TimeoutSeconds int
}

type ToolTimeoutMode string

const (
    ToolTimeoutModeBounded  ToolTimeoutMode = "bounded"
    ToolTimeoutModeDisabled ToolTimeoutMode = "disabled"
)

type EffectiveTimeout struct {
    Mode    ToolTimeoutMode
    Seconds int
}
```

Add `ToolDefinition.Bind(Handler) Tool`,
`ToolDefinition.BindResult(ResultHandler) Tool`, `Tool.Definition()`, and
`EffectiveToolTimeout(def, defaultSeconds)`. Reuse the registry's existing
normalization and cap in that function; keep `TimeoutSecondsFor` behavior
unchanged.

- [ ] **Step 4: Make each package own definitions and bind real handlers**

Move each production tool's name/group/description/schema/timeout fields into
one definition function or definition slice in its current owning package.
Constructors bind their current handlers to those definitions. Export only the
definition slices needed by `internal/app`:

```go
func DefaultBuiltinToolDefinitions(opts BuiltinDefinitionOptions) []ToolDefinition
func ToolDefinitions() []tools.ToolDefinition // memory and observable packages
func GoalToolDefinitions() []tools.ToolDefinition
func NotesToolDefinitions() []tools.ToolDefinition
```

Keep shell descriptions profile-aware through `BuiltinDefinitionOptions`.
Stamp both MCP registration paths with `ToolGroupMCP`. Do not infer groups
from tool names and do not make group required in generic `Registry.Register`.

- [ ] **Step 5: Run focused tests and verify GREEN**

Run the command from Step 2. Expected: all selected package tests pass.

- [ ] **Step 6: Run package suites and commit**

Run:

```bash
gofmt -w internal/tools internal/app/skill_tools.go internal/memory internal/runtime/goal_tools.go internal/runtime/notes_tools.go internal/observable/tools.go internal/mcp
go test ./internal/tools ./internal/app ./internal/memory ./internal/runtime ./internal/observable ./internal/mcp -count=1
git diff --check
git add internal/tools internal/app/skill_tools.go internal/memory internal/runtime/goal_tools.go internal/runtime/notes_tools.go internal/observable/tools.go internal/mcp
git commit -m "feat: classify runtime tools"
```

Expected: Go package tests pass and the commit contains only definition,
binding, and focused test changes.

### Task 2: Project builtin and MCP definitions through `/api/runtime`

**Files:**

- Modify: `internal/mcp/manager.go`
- Modify: `internal/mcp/client_test.go`
- Modify: `internal/app/runtime_status.go`
- Modify: `internal/app/runtime_status_test.go`
- Modify: `internal/web/runtime.go`
- Modify: `internal/web/server.go`
- Modify: `internal/web/runtime_test.go`
- Modify: `tests/e2e/web_test.go`

- [ ] **Step 1: Write failing MCP snapshot and app catalog tests**

Test that `Manager.ToolDescriptors()`:

- preserves a connected server with zero advertised tools;
- sorts copied descriptors by name;
- deep-clones nested `InputSchema` maps and slices;
- returns an empty map for nil or closed managers.

In `runtime_status_test.go`, assert this exact known group order:

```go
wantGroups := []tools.ToolGroup{
    tools.ToolGroupFile,
    tools.ToolGroupChunkedWrite,
    tools.ToolGroupShell,
    tools.ToolGroupSearch,
    tools.ToolGroupSkill,
    tools.ToolGroupMemory,
    tools.ToolGroupSessionState,
    tools.ToolGroupObservable,
}
```

Also build a real `app.New` with the stub provider and compare every non-MCP
definition with the status catalog by name, group, description, normalized
schema, and effective timeout.

- [ ] **Step 2: Write failing web and e2e contract tests**

Extend `/api/runtime` tests to require:

```json
{
  "tools": {"count": 27, "groups": []},
  "mcp": {"servers": [{"tool_count": 1, "tools": [{"name": "echo"}]}]}
}
```

The fake MCP descriptor must preserve its description and input schema.
Failed MCP startup must return `tools: []` on that server. The e2e test should
exercise the real route with the existing fake MCP process.

- [ ] **Step 3: Run focused tests and verify RED**

Run:

```bash
go test ./internal/mcp ./internal/app ./internal/web ./tests/e2e -run 'ToolDescriptors|Runtime.*Tool|RuntimeStatus|WebRuntime' -count=1
```

Expected: failures because manager descriptors and Runtime DTO fields are not
implemented.

- [ ] **Step 4: Implement defensive MCP descriptor snapshots**

Add:

```go
func (m *Manager) ToolDescriptors() map[string][]ToolDescriptor
```

Hold the read lock while cloning. Clone arbitrary JSON-compatible schema maps,
arrays, and scalar values recursively. Sort each returned descriptor slice by
name without mutating the manager cache. Preserve map keys for connected
zero-tool servers.

- [ ] **Step 5: Implement the app catalog projection**

Add plain runtime status types matching the design document:

```go
type RuntimeToolsStatus struct {
    Count  int
    Groups []RuntimeToolGroupStatus
}

type RuntimeToolInfo struct {
    Name        string
    Description string
    Schema      map[string]any
    Timeout     RuntimeToolTimeout
}
```

Aggregate package definitions without binding handlers. Reject empty, `mcp`,
or unknown groups in the builtin projection. Sort groups by the fixed taxonomy
and tools by name. Translate `tools.EffectiveTimeout` to
`RuntimeToolTimeout{Mode, Seconds}`. In MCP status, use descriptor-map
membership as connected state and derive `ToolCount` from the projected list.

Replace `RuntimeStatusOptions.MCPToolCounts` with cached descriptor input. Do
not create a session or execution manager. Keep configured failed servers and
their errors.

- [ ] **Step 6: Extend the existing web DTO**

Add JSON DTOs with `tools`, `groups`, tool `schema`, and timeout
`{mode,seconds}`. Pass `mcp.Manager.ToolDescriptors()` to the app status
service. Remove the active-session registry count fallback from the Runtime
route; the serve-process manager is the catalog source of truth.

- [ ] **Step 7: Run focused and package tests, then commit**

Run:

```bash
gofmt -w internal/mcp internal/app/runtime_status.go internal/app/runtime_status_test.go internal/web tests/e2e/web_test.go
go test ./internal/mcp ./internal/app ./internal/web ./tests/e2e -count=1
git diff --check
git add internal/mcp internal/app/runtime_status.go internal/app/runtime_status_test.go internal/web tests/e2e/web_test.go
git commit -m "feat: expose runtime tool catalog"
```

Expected: all selected packages pass; no new HTTP route exists.

### Task 3: Render shared builtin and MCP tool disclosures

**Files:**

- Create: `frontend/src/lib/runtime-tool-catalog.ts`
- Create: `tests/frontend/runtime-tool-catalog.test.ts`
- Create: `frontend/src/components/RuntimeToolCatalog.tsx`
- Modify: `frontend/src/types.ts`
- Modify: `frontend/src/pages/Runtime.tsx`
- Modify: `tests/frontend/runtime-page.test.ts`

- [ ] **Step 1: Write failing pure projection tests**

Test these exported functions before creating them:

```ts
runtimeToolGroupLabel("chunked_write") === "Chunked Write"
runtimeToolTimeoutLabel({ mode: "bounded", seconds: 60 }) === "60s timeout"
runtimeToolTimeoutLabel({ mode: "disabled", seconds: 0 }) === "tool managed"
runtimeToolParameters(schema)
formatRuntimeToolSchema(schema)
```

`runtimeToolParameters` must sort top-level properties by name, mark names in
`required`, and format primitive, enum, array, nested object, and `oneOf`
types. Missing/non-object `properties` yields `[]`. Raw formatting must never
throw; circular or unsupported input yields a readable fallback.

Extend the AST-based Runtime page test to require heading order `Provider`,
`Tools`, `MCP`, `Skills`, `Hooks` and a shared `RuntimeToolList` reference in
both builtin and MCP render paths.

- [ ] **Step 2: Run frontend tests and verify RED**

Run:

```bash
pnpm --dir frontend test -- runtime-tool-catalog runtime-page
```

Expected: module-not-found or assertion failures for the new catalog helpers
and page section.

- [ ] **Step 3: Implement the TypeScript DTO and pure helpers**

Add `RuntimeToolInfo`, `RuntimeToolGroup`, and `RuntimeToolTimeout` interfaces.
Use `Record<string, unknown>` for schemas. Add optional-safe defaults only for
old in-memory responses; the Go response remains the source of truth.

Implement the pure helper module without React or DOM dependencies so the Node
test runner can import it directly.

- [ ] **Step 4: Implement the shared disclosure module**

`RuntimeToolCatalog.tsx` exports:

```tsx
export function RuntimeToolList({ tools, empty }: RuntimeToolListProps)
export function RuntimeToolGroups({ groups }: { groups: RuntimeToolGroup[] })
```

Use native `details`/`summary` for group and tool levels. The expanded tool
body renders description, timeout, a four-column parameter table (Name, Type,
Required, Description), and a nested raw-schema disclosure using the existing
JSON `CodeBlock`. Keep focus styles and chevrons consistent with current
Runtime prompt disclosures.

- [ ] **Step 5: Compose the Runtime page**

Insert Tools after Provider. Replace the MCP table with server disclosure
cards that keep server/source/status/tool count/command/error in the summary
or immediately visible metadata and render `RuntimeToolList` on expansion.
Do not show MCP tools in the builtin groups. Preserve the empty and error
states.

- [ ] **Step 6: Run frontend verification and commit**

Run:

```bash
pnpm --dir frontend test
pnpm --dir frontend lint
pnpm --dir frontend build
git diff --check
git add frontend/src/lib/runtime-tool-catalog.ts tests/frontend/runtime-tool-catalog.test.ts frontend/src/components/RuntimeToolCatalog.tsx frontend/src/types.ts frontend/src/pages/Runtime.tsx tests/frontend/runtime-page.test.ts
git commit -m "feat: render runtime tool catalog"
```

Expected: all frontend tests pass, ESLint exits 0, and Vite builds without a
bundle-size warning.

### Task 4: Align stable domain, architecture, and UI documentation

**Files:**

- Modify: `.agents/skills/juex-ddd/SKILL.md`
- Modify: `ARCHITECTURE.md`
- Modify: `DESIGN.md`
- Modify: `frontend/README.md`

- [ ] **Step 1: Update the ubiquitous language**

Add rows with these meanings and owners:

```markdown
| Tool group | Stable classification of registered tools for runtime inspection: file, chunked_write, shell, search, skill, memory, session_state, observable, or mcp | `internal/tools`, registration owners |
| Session state | Model-owned goal and working notes for one session, exposed through the session_state tool group and never through workspace Runtime state | `internal/runtime`, `internal/session` |
```

- [ ] **Step 2: Update architecture and frontend docs**

Document `ToolDefinition`, group omission from provider specs, effective
timeout mode, `Manager.ToolDescriptors()`, and the expanded `/api/runtime`
contract in `ARCHITECTURE.md`. Update DESIGN Runtime detail to specify builtin
group and MCP server disclosures and shared tool detail rendering. Add the new
helper/component paths to `frontend/README.md`.

- [ ] **Step 3: Check docs and commit**

Run:

```bash
rg -n "Tool group|Session state|ToolDefinition|ToolDescriptors|RuntimeToolCatalog" .agents/skills/juex-ddd/SKILL.md ARCHITECTURE.md DESIGN.md frontend/README.md
git diff --check
git add .agents/skills/juex-ddd/SKILL.md ARCHITECTURE.md DESIGN.md frontend/README.md
git commit -m "docs: document runtime tool catalog"
```

Expected: every term has one stable meaning; no changelog prose or stale
runtime-page wording remains.

### Task 5: Full verification and development evidence

**Files:**

- Create/update: `.tmp/reports/*` through project evaluation tooling only
- Create: Taskline `Dev Notes`, then `Test Report` during the stage workflow

- [ ] **Step 1: Review the complete diff against the design**

Compare every product-contract bullet in
`docs/superpowers/specs/2026-07-16-runtime-tool-catalog-design.md` with code and
tests. Confirm no MCP tool appears in builtin groups, zero-tool connections are
connected, failed servers expose no invented tools, and scratchpad is absent.

- [ ] **Step 2: Run deterministic verification**

Run:

```bash
gofmt -w internal tests/e2e
go test ./... -race -count=1
pnpm --dir frontend test
pnpm --dir frontend lint
pnpm --dir frontend build
make build
git diff --check
```

Expected: every command exits 0.

- [ ] **Step 3: Run project development evaluation and provider sweep**

Run `make development-eval` and the narrower real local provider/model sweep
required by `.agents/skills/juex-localtest/SKILL.md` for a web/runtime change.
Record exact commands, models, pass counts, and any unavailable provider in the
Taskline Test Report. Do not treat an unavailable external provider as a local
pass.

- [ ] **Step 4: Browser smoke the rebuilt binary**

Start the rebuilt server on `0.0.0.0` with an unused port and
`--unsafe-bind-any`. In a real headless browser, verify:

- Tools groups start collapsed and expose tool rows when expanded;
- a builtin tool exposes parameters, raw schema, and timeout;
- an MCP server exposes its tools through the same row module;
- failed/empty states remain legible;
- layout does not horizontally overflow at desktop and narrow viewport widths;
- light and dark color schemes retain readable contrast.

Capture screenshots under `.tmp/reports/` and stop the server afterward.

- [ ] **Step 5: Commit any verification-driven fixes**

If verification changes tracked code, repeat the affected checks and commit
only those fixes with a conventional message. Do not commit `.tmp` runtime
artifacts.
