# Builtin Tool Guides Implementation Plan

**Goal:** Move low-frequency builtin tool guidance into binary-embedded skills
while keeping a lean, reliable routing contract resident in every tool spec.

**Architecture:** The skill loader owns a reserved builtin catalog backed by
`embed.FS`. Existing skill tools expose that catalog without filesystem
extraction, and existing tool-definition owners replace prose-heavy Tier 2
metadata with hard guide pointers and compact schemas.

**Stack:** Go standard library, existing JueX skill/tool/runtime packages,
Markdown guide assets, Taskline workflow.

---

## Task 1: Embed and isolate builtin guide skills

**Files:**

- Add: `internal/skills/builtin/*/SKILL.md`
- Add: `internal/skills/builtin.go`
- Modify: `internal/skills/loader.go`
- Modify: `internal/skills/loader_test.go`

### Step 1: Write failing loader contract tests

Cover builtin discovery, private provenance, source/virtual path/raw content,
search/get/all, prompt exclusion, prompt-report exclusion, include/exclude
immunity, reload, collision failure, and fail-loud catalog parsing.

Run:

```bash
go test ./internal/skills -run 'Builtin|Prompt' -count=1
```

Expected: failures because no builtin catalog exists.

### Step 2: Implement the embedded catalog

- Embed the three guide files with `embed.FS`.
- Load them before filesystem directories with reserved names.
- Retain raw markdown, private builtin provenance, and prompt visibility
  metadata on `Skill`.
- Skip builtin entries during policy filtering and prompt rendering.
- Keep `All`, `Get`, and `Search` inclusive.

### Step 3: Verify the loader

```bash
gofmt -w internal/skills
go test ./internal/skills -count=1
```

---

## Task 2: Load and report builtin skills through existing interfaces

**Files:**

- Modify: `internal/app/skill_tools.go`
- Modify: `internal/app/app_test.go`
- Modify: `internal/app/runtime_status_test.go`
- Modify: compiled-binary loading tests under `tests/e2e`

### Step 1: Write failing application tests

Assert that `skill_search` reports `source=builtin` and a virtual path,
`skill_load` returns embedded markdown without filesystem guard rejection,
filesystem skills retain sandbox checks, and runtime status contains the
builtin guides but the system-prompt skills section does not.

### Step 2: Add the source-aware load path

For privately authenticated builtin skills, return retained raw content and an
exact URI-aware virtual directory. For other sources, preserve path validation
and live filesystem reads even when display metadata says `builtin`.

### Step 3: Verify app and binary loading

```bash
go test ./internal/app ./tests/e2e -run 'Skill|Runtime' -count=1
```

---

## Task 3: Slim Tier 2 tool metadata and write the guides

**Files:**

- Modify: `internal/observable/tools.go`
- Modify: `internal/observable/tools_test.go`
- Modify: `internal/runtime/goal_tools.go`
- Modify: `internal/runtime/notes_tools.go`
- Modify: `internal/runtime/*_tools_test.go`
- Modify: `internal/tools/builtin_chunked_write.go`
- Modify: `internal/tools/tools_test.go`
- Complete: the three builtin `SKILL.md` files

### Step 1: Write failing guidance and budget tests

Assert exact guide pointers on every Tier 2 description, preservation of
routing distinctions and schema structure, removal of schema descriptions and
constraints, and a combined estimated Tier 2 budget no greater than 1700
tokens. The measured pre-change baseline is 2433, so this enforces at least a
30% reduction; 800 was rejected as incompatible with the retained structure.

### Step 2: Replace resident prose

- Use compact purpose/routing text plus `MUST load the <name> skill before
  first use` on every Tier 2 tool.
- Remove descriptive and constraint schema fields but retain types, required
  lists, additional-property closure, and mutually exclusive branches.
- Put full workflows, constraints, defaults, failure recovery, and examples
  in the matching guide.

### Step 3: Verify metadata and handlers

```bash
gofmt -w internal/observable internal/runtime internal/tools
go test ./internal/observable ./internal/runtime ./internal/tools -count=1
```

---

## Task 4: Update stable documentation

**Files:**

- Modify: `README.md`
- Modify: `ARCHITECTURE.md`
- Modify: affected `internal/cli` tests for builtin entries in dry-run and
  doctor skill counts

Document the two tiers, builtin source and virtual paths, reserved names,
filter immunity, prompt exclusion, and source-aware loading. Remove wording
that says every skill is filesystem-backed or sandbox-read.

Verify headings, links, and stale statements with `rg` and `git diff --check`.

---

## Task 5: Full validation and delivery

Run:

```bash
go test ./... -count=1
go test ./... -race -count=1
make build
make integration
make development-eval
```

Run the real provider/model sweep from `~/.juex/juex.yaml`, including a prompt
that must load `juex-observables` before creating a Schedule. Record any
provider failure that occurs before tool routing separately from a guidance
regression. Smoke the runtime API on `0.0.0.0` and verify builtin skill status,
load output, and prompt exclusion.

Create Taskline Dev Notes, Test Report, and Review Report. Request independent
review, address actionable findings, push the branch, open a PR, wait for all
CI jobs and review threads, merge only when green, then confirm local main,
origin/main, Taskline Done state, and the next queue item.
