# Builtin Tool Guides Design

## Goal

Reduce the always-resident provider context used by low-frequency builtin
tools without removing the instructions needed to use them correctly. Core
file, shell, search, skill, and memory tools keep their current descriptions.
The `observable`, `session_state`, and `chunked_write` groups move detailed
semantics, constraints, examples, and workflows into builtin guide skills
embedded in the JueX binary.

The change must preserve a self-contained single binary, deterministic skill
discovery, valid provider-facing schemas, existing handler validation, and the
runtime catalog's ability to explain every loaded resource.

## Product contract

Each low-frequency tool keeps a short resident description containing:

1. one sentence stating the tool's purpose;
2. the routing distinction needed before choosing it; and
3. a mandatory pointer to the exact guide skill name that must be loaded
   before the group's first use.

The builtin skills are:

| Skill | Tool group | Contents |
|---|---|---|
| `juex-observables` | `observable` | command versus schedule routing, lifecycle permanence, schema field semantics, constraints, and examples |
| `juex-session-state` | `session_state` | goal lifecycle, success/failure evidence, acceptance contracts, and concise working-note replacement |
| `juex-chunked-write` | `chunked_write` | begin/chunk/commit/abort workflow, provider-safe limits, indexes, checksums, and recovery |

The model discovers and loads these guides through the existing
`skill_search` and `skill_load` tools. There is no new tool, automatic skill
activation, deferred tool registration, or model-specific guidance depth.
Corrective instructions appended after tool misuse remain out of scope until
runtime evidence justifies them.

## Builtin skill loading

`internal/skills` embeds the three markdown files with `embed.FS`. Every
`Loader.Load` starts from this fixed catalog and then scans configured user,
extension, and project directories.

Builtin skills have these invariants:

- `Source` is `builtin`.
- `Path` is the stable virtual path
  `builtin://skills/<name>/SKILL.md`.
- Their raw embedded markdown and private builtin provenance are retained so
  `skill_load` does not depend on a filesystem extraction step. The public
  source label is never used as a sandbox trust signal.
- Names are reserved. A filesystem skill with the same name makes loading fail
  with the existing duplicate-skill error instead of overriding the guide.
- `skills.include` and `skills.exclude` apply only to filesystem skills.
  Required builtin guides cannot be hidden while tool descriptions still
  point at them.
- They participate in `All`, `Get`, `Search`, application skill tools, and the
  Runtime status catalog.
- They do not participate in `PromptSection` or prompt-budget omission
  reports. The tool descriptions are already their resident discovery path,
  so listing them again in `## Available Skills` would spend context twice.

Filesystem skills retain current precedence, filtering, sandbox checks, and
read-on-load behavior. `skill_load` returns retained embedded content and a
URI-aware virtual directory only when the loader's private provenance marks a
skill builtin. All other sources still pass the path guard and read the current
file, even if a caller supplied `builtin` as display metadata. Missing,
malformed, unexpected, or name-mismatched embedded guides fail startup loudly.

## Tool guidance tiers

Tier 1 remains unchanged: `file`, `shell`, `search`, `skill`, and `memory`.
`apply_patch` is already small and stays unchanged.

Tier 2 contains `observable`, `session_state`, and `chunked_write`. Each tool
definition keeps its name, group, normalized object shape, property types,
required lists, closed-object structure, and branch structure. Long prose,
examples, defaults, and constraints such as allowed values and ranges move to
the corresponding guide. Existing handler validation remains authoritative.

This means schema slimming removes descriptive and constraint metadata, not
the types, required fields, closed objects, or mutually exclusive recurrence
and filter branches needed to express the input shape.

## Interfaces and ownership

`internal/skills` owns embedding, reserved-name loading, prompt visibility,
and raw builtin content. `internal/app/skill_tools.go` owns the source-aware
read path because it already binds `skill_load` to the sandbox policy.

The existing tool registration owners keep their metadata:

- `internal/observable/tools.go`
- `internal/runtime/goal_tools.go`
- `internal/runtime/notes_tools.go`
- `internal/tools/builtin_chunked_write.go`

No parallel metadata registry is introduced. Runtime status continues to
project definitions from the live registry and skills from the loader.

## Budget and verification contract

Tests use `contextbudget.EstimateToolTokens`, the same estimator used by
runtime context reporting. The measured pre-change Tier 2 catalog is 2433
estimated tokens. The contract is at most 1700 tokens, a reduction of at least
30%, while all 15 tool descriptions retain the exact hard guide pointers.
The earlier 800-token target was rejected after measurement because the two
Observable creation schemas plus required structure already make it
unattainable. Structural tests assert that schemas retain types, required
fields, closed objects, and recurrence/filter branches while descriptive and
constraint metadata is absent.

The complete verification includes focused package tests, `go test ./...`,
race tests, a binary build, integration tests, development evaluation, and the
real local provider/model routing sweep. Runtime/API smoke verifies that all
three builtin skills appear with source `builtin`, can be loaded, and are not
duplicated in the system-prompt skill catalog.

## Documentation

`README.md` describes the builtin-guide discovery behavior and clarifies that
skill include/exclude settings control filesystem skills. `ARCHITECTURE.md`
records the two guidance tiers, embedded builtin source, virtual paths,
reserved names, prompt exclusion, and source-aware `skill_load` behavior.
