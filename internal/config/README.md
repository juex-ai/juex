# Configuration

> English | [中文](README.zh.md)

`preset` accepts `standard` (the default) or `minimal`. Standard enables every
capability declared in `internal/modulecatalog`; minimal enables only
`basic-file-tools`, `shell`, and `operating-context` by default. Explicit
`modules.<id>.enabled` switches override those defaults.

Preset and explicit switches merge independently through the existing config
layers and imports. Setting only a higher-layer preset preserves lower-layer
explicit switches. A higher explicit value replaces the same lower field;
an empty module envelope leaves it inherited. Managed Agent saves preserve the
submitted sparse YAML without expanding defaults. Unknown presets, module IDs,
and settings are rejected by the same configuration parser used for loading
and saving. Canonical module IDs use kebab-case.

Effective configuration is not evidence of available tools. The sealed runtime
catalog remains authoritative. The current bundled tool and Thread context
factories reject independent switches they cannot yet honor before constructing
resources; a bare minimal preset is therefore not yet a runnable six-tool mode.
Fleet config updates reject unsupported combinations before publishing the
Agent overlay or import cache and restarting. `diagnose` reports the same
composition error before resource discovery.
Extension discovery gating and the built-in Memory factory are separate work.
Presets do not change Provider, model, Sandbox, auto-compaction, or core
persistence settings.
