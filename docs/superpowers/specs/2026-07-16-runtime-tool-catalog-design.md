# Runtime Tool Catalog Design

## Goal

Make the Runtime page an inspectable catalog of every tool exposed by the
workspace runtime. Builtin tools appear in stable domain groups. MCP tools
remain under their configured server. Every tool exposes its description,
top-level parameters, raw input schema, and effective hard timeout.

## Product contract

- Add a `Tools` section to `/runtime` after Provider and before MCP.
- Show builtin tools only in `Tools`, grouped in this order: `file`,
  `chunked_write`, `shell`, `search`, `skill`, `memory`, `session_state`, and
  `observable`.
- A group summary shows its display label, tool count, and tool-name preview.
  Expanding it shows the group tool rows.
- A tool row shows its name and effective timeout. Expanding it shows the
  description, a table derived from the top-level JSON Schema properties, and
  a separately expandable raw JSON Schema view.
- Replace the MCP table with per-server disclosure cards. A collapsed server
  card keeps source, status, tool count, command, and error visibility.
  Expanding a connected server shows its tools through the same tool-row
  module used by builtin groups.
- Failed or not-started MCP servers have no invented tools. Their existing
  status and error remain visible.
- The catalog is a workspace-level startup snapshot. It does not depend on an
  active session and does not refresh MCP tool discovery dynamically.
- Scratchpad is not a tool group because it has no dedicated tools.

## Domain language

- **Tool group** is the stable classification attached to a registered tool.
  `internal/tools` owns the value and registration metadata.
- **Session state** is the `session_state` tool group containing goal and
  notes tools. It is distinct from workspace Runtime state under `.juex/`.
- The tool registry remains the runtime catalog and dispatcher. Group metadata
  is catalog metadata and is not part of provider-facing `llm.ToolSpec`.

Add Tool group and Session state to the `juex-ddd` ubiquitous-language table.

## Approaches considered

### 1. Registry metadata with an app-level catalog projection (chosen)

Add a typed `Group` field to `tools.Tool`, stamp it at each existing
registration owner, and project registered metadata through
`app.RuntimeStatus`. Expose the MCP manager's already cached descriptors for
the per-server projection. This keeps classification beside the code that
owns each tool and keeps HTTP and React free of domain inference.

The metadata and handler binding must share one definition. The no-session
Runtime status path aggregates definitions only and never constructs shell,
session, or observable execution resources. A cross-check test compares that
projection with a real `app.New` registry so a newly registered definition
cannot silently disappear from the catalog.

### 2. Add `/api/runtime/tools`

Load schemas only when the user opens the catalog. This reduces the normal
Runtime payload but adds another transport, loading state, and synchronization
surface for a payload measured in tens of kilobytes. It is unnecessary until
the catalog becomes measurably large.

### 3. Derive groups and MCP servers from tool names in the web layer

This is smaller initially but makes naming conventions into hidden transport
policy, cannot classify every builtin reliably, and duplicates MCP parsing in
presentation code. It conflicts with the repository rule that shared domain
decisions live below transports.

## Architecture

### Tool registry

`internal/tools` adds a `ToolGroup` string value object and constants for the
nine known groups, including `mcp`. It also adds `ToolDefinition`, containing
name, group, description, schema, and timeout policy, plus helpers that bind a
definition to a string or structured-result handler. `Tool` keeps its current
shape for compatibility and can return its definition. The registry preserves
`Tool.Group` in `List()` and continues to omit group metadata from `Specs()`.

Builtin providers expose definitions and bind those same definitions to their
existing handlers. The skill, memory, goal/notes, observable, and MCP
registration owners do the same. Registration continues accepting an empty
group so test-only and embedded custom tools remain backward compatible; the
production catalog rejects missing or unknown groups, and the parity test
proves all JueX-owned tools are classified.

### MCP manager

`mcp.Manager.ToolDescriptors()` returns a deterministic, defensive deep copy
of the descriptors already cached during startup, keyed by server. It performs
no subprocess calls and returns an empty map for a nil or closed manager. A
successfully connected server remains present even when it advertises zero
tools, so map membership is the connection fact.

### Application catalog

`RuntimeCatalogService` owns two plain projections:

```go
type RuntimeToolsStatus struct {
    Count  int
    Groups []RuntimeToolGroupStatus
}

type RuntimeToolGroupStatus struct {
    Group string
    Tools []RuntimeToolInfo
}

type RuntimeToolInfo struct {
    Name        string
    Description string
    Schema      map[string]any
    Timeout     RuntimeToolTimeout
}

type RuntimeToolTimeout struct {
    Mode    string
    Seconds int
}
```

The catalog service aggregates the same definitions consumed by `app.New`; it does
not bind or dispatch handlers and therefore creates no shell manager, session,
memory store, or observable manager. MCP definitions are excluded from this
pass and supplied from the process manager descriptors.

The catalog service rebuilds the small builtin projection from immutable definitions
for each Runtime status request. `internal/tools` resolves effective timeout
metadata with the same normalization and cap used by registry calls.
Ordinary tools use `mode=bounded` with seconds; `exec_command` and
`write_stdin` use `mode=disabled`, meaning the generic hard timeout is disabled
while cancellation, process cleanup, and yield-window semantics still apply.
MCP tools use the registry's normalized bounded default.

`RuntimeMCPServerStatus` keeps `ToolCount` for collapsed summaries and gains a
`Tools` list. Connected state and `ToolCount` derive from the descriptor map
and tool list rather than a second count map. A server that failed startup
therefore reports its real error with an empty tool list.

### Web transport

`internal/web` extends the existing `GET /api/runtime` DTO. It does not add a
route or regroup tools. The server passes cached MCP descriptors and errors to
the app catalog service, then maps the returned plain types to JSON:

```json
{
  "tools": {
    "count": 28,
    "groups": [{"group": "file", "tools": [{"name": "read"}]}]
  },
  "mcp": {
    "servers": [{"name": "local", "tool_count": 1, "tools": [{"name": "echo"}]}]
  }
}
```

Tool objects also include `description`, `schema`, and a `timeout` object with
`mode` and `seconds`.

### Frontend

`frontend/src/types.ts` mirrors the new response shapes. A pure
`frontend/src/lib/runtime-tool-catalog.ts` module owns group labels, timeout
labels, stable schema formatting, and defensive extraction of top-level
parameters. It treats unknown or malformed schema fragments as display data,
never as trusted executable input.

`frontend/src/components/RuntimeToolCatalog.tsx` owns the reusable tool row and
detail rendering. `Runtime.tsx` supplies builtin groups and MCP server lists to
that module. Native `details`/`summary` elements provide keyboard-accessible
two-level disclosure without new state machinery. Raw schemas reuse the
existing JueX JSON code surface.

## Error handling

- A Runtime catalog construction error follows the existing `/api/runtime`
  error contract and returns `general_error`; it never starts a session to
  recover.
- Missing or malformed schema fields render an empty parameter table while
  preserving the raw JSON value.
- Missing MCP descriptors render zero tool rows; configured status, command,
  and startup error remain visible.
- Unknown or empty production tool groups fail catalog construction instead of
  being silently regrouped. Test-only custom registry tools may remain
  ungrouped because they do not enter the production catalog.

## Test plan

1. `internal/tools`: assert each default builtin provider stamps the expected
   group, definitions bind without metadata drift, and `Specs()` still
   contains no group field.
2. Tool registration owners: assert skill, memory, session-state, observable,
   and MCP tools carry their declared group.
3. `internal/mcp`: assert `ToolDescriptors()` is deterministic, preserves a
   connected zero-tool server, and callers cannot mutate the manager cache.
4. `internal/app`: assert group ordering, schemas, timeout modes, and parity
   of name/group/description/schema/effective timeout between catalog
   definitions and a real `app.New` registry.
5. `internal/web`: assert `/api/runtime` returns builtin groups and connected
   MCP tool details while failed servers keep an empty tool list.
6. `tests/e2e`: assert a fake MCP server's descriptor crosses the compiled web
   route.
7. Frontend unit tests: cover group/timeout labels, required parameters,
   arrays/enums/unions, malformed schema, and stable raw JSON formatting.
8. Frontend build and lint, Go package tests, full deterministic suite,
   development evaluation, local provider/model sweep, and a browser smoke of
   collapsed and expanded Tools/MCP states in light and dark color schemes.

## Non-goals

- Dynamic MCP rediscovery or hot-reload.
- Tool execution from the Runtime page.
- Nested JSON Schema form generation.
- A scratchpad group or synthetic scratchpad tools.
- A CLI catalog command or a second HTTP endpoint.
