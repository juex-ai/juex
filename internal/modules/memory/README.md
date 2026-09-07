# Memory

> English | [中文](README.zh.md)

Memory owns durable Agent knowledge in `modules/memory/` under the Agent state
directory. Main and Worker Threads share that directory; different Agents do
not. `standard` enables Memory and `minimal` disables it. Set
`modules.memory.enabled` explicitly to override the preset. Memory works with
Extensions, MCP, Skills, and Hooks disabled and provides its own brief guidance.
App resolves relative embedding paths before supplying the absolute Agent scope.

Entry Markdown files are authoritative. Their YAML frontmatter contains name,
single-line description, type (`user`, `feedback`, `project`, or `reference`),
creation time, and update time. Same-name writes preserve creation time. Search
uses literal substrings with Unicode simple case folding across metadata and
body; it does not expand characters such as `ß` into `ss`. Malformed or unreadable
individual entries are skipped; a directory read failure is an operation error.
Physical directory and regular-entry boundaries reject symlinked storage.

Every operation coordinates through one stable filesystem transaction lock.
Separate Module instances read the shared files without a cache. Lock waiting
honors cancellation, and the lock file remains in place after use. Entries are
atomically published before rebuilding the derived `MEMORY.md` index. A later
index or durability failure reports that the entry was already saved or deleted;
it cannot roll back authoritative knowledge. Thread start and successful
compaction rebuild the index through ordinary Module policies. Maintenance
failures are observable but nonfatal; cancellation and policy checkpoint failures
still propagate.

Construction, tool catalogs, and guidance perform no Memory file work. Disabled
Memory contributes no tools, guidance, or maintenance. Knowledge survives `/new`,
shutdown, disabling, and removal of the Module; only explicit `memory_delete`
removes entries. Prompt guidance never injects the index or all entry bodies.
Memory has no automatic extraction, legacy tool aliases, or data migration.
