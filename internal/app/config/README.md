# Configuration

> English | [中文](README.zh.md)

The composition root supplies an immutable module inventory before parsing any
configuration layer. This package validates and merges declarations; it does
not select or construct product capabilities. Runtime loading, saving and
passive inspection use the same inventory, including Fleet import validation.

`preset` accepts `standard` (the default) or `minimal`. Standard enables every
capability declared in `internal/app/modulecatalog`; minimal enables only
`basic-file-tools`, `shell`, and `operating-context` by default. Explicit
`modules.<id>.enabled` switches override those defaults.

`input-tracking` follows these defaults: standard on, minimal off. Its
[input checklist](../../features/inputtracking/README.md) retains unchecked
inputs across disable/re-enable without changing pending delivery counts.

Preset and explicit switches merge independently through the existing config
layers and imports. Setting only a higher-layer preset preserves lower-layer
explicit switches. A higher explicit value replaces the same lower field;
an empty module envelope leaves it inherited. Managed Agent saves preserve the
submitted sparse YAML without expanding defaults. Unknown presets, module IDs,
and settings are rejected by the same configuration parser used for loading
and saving. Canonical module IDs use kebab-case.

`modules.worker-threads.max_depth` defaults to 1 and accepts only integer 1
or 2. Main has depth 0; each parent link adds one. This field merges separately
from `enabled`, including imports and Agent overlays. Explicit null or invalid
depths fail even when disabled; other modules do not accept this setting.
At the limit the Thread has no Worker module, while its own execution remains
controlled by the Agent-level switch. Existing deep history is retained and
host lifecycle operations remain available. Storage creation also obeys the
depth limit. Configuration changes use the existing Agent restart mechanism.

Tool modules assemble independently. A bare minimal preset serves three basic
file tools and three shell tools. Shell owns its sessions and syntax guidance.
The final runtime tool catalog determines descriptions, schemas, and recovery
advice: basic writes support long content when the complete chunked-write
workflow is unavailable, and skill-guide pointers require `skill_load`.
Fleet config updates validate declarations before publishing the Agent overlay
or import cache and restarting. `diagnose` validates before resource discovery.
Presets do not change Provider, model, Sandbox, auto-compaction, or core
persistence settings.

Main and Workers resolve the same effective module policy. Scope still limits
contributions: Observable management tools and external inputs belong to Main.
Minimal does not enable Worker execution; enable `worker-threads` explicitly
when that capability is needed. Reducing available tools and guidance reduces
the request content, but is not evidence of better model accuracy or latency.

Hooks and Skills declarations are parsed only after the final module switches
are known, so a higher-layer disablement can suppress damaged lower-layer
declarations. Ordinary YAML syntax and common configuration remain validated.
Disabled declarations are retained in the submitted YAML; enabling the module
restores its strict parsing and existing merge and trust rules.

Extension discovery requires `modules.extensions.enabled` and selection by
`extensions.allow`. Each resource also requires its hosting `skills`, `hooks`,
`mcp`, or `observables` module before its path is probed or content parsed.
Workspace resources depend only on their host, so disabling Extensions still
allows workspace MCP, Skills and Hooks. These switches govern external command
Hooks, independently of built-in module lifecycle callbacks. A selected
Extension's manifest and shared environment defaults remain Extension-owned,
even when its hosts are disabled; evaluating defaults does not prepare private
data directories. Enabled resources keep their existing validation, provenance,
conflict and child-process isolation rules.
