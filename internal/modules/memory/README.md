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
creation time, and update time. Same-name writes preserve creation time.
Names exclude `MEMORY` and Windows device basenames on every platform, including
device names followed by a dot and suffix. Search uses literal substrings with
Unicode simple case folding across metadata and
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

## Switch from the retired Memory Extension

The `juex-extensions` repository no longer distributes the Memory bundle. Its
installer leaves an existing installation and Agent-private data untouched.
Switch one Agent at a time; keep the old installation and knowledge as a backup.

1. Resolve the registered Agent with `juex agent list`. Stop it with
   `juex agent stop --agent <agent-id>`. Run
   `juex agent config --agent <agent-id>` to locate its sparse configuration.
2. Edit that configuration so the effective `extensions.allow` list excludes
   `memory` and retains every other desired Extension. Set
   `modules.memory.enabled: true`. Remove any separately configured copies of
   the old Memory MCP, Skill, or Hooks too. Keep the Agent stopped until the
   configuration and file copy are complete, avoiding two Memory providers.
3. Copy only selected compatible UTF-8 Markdown entries from
   `$JUEX_HOME/agents/<agent-id>/extensions/memory/` into
   `$JUEX_HOME/agents/<agent-id>/modules/memory/`, creating the destination if
   needed. Preserve each entry's frontmatter and body; its filename stem must
   equal `name`, and its metadata and name must satisfy the current tool schema.
   Do not copy `MEMORY.md`, locks, temporary files, or symlinks. Check existing
   destination names, including case variants, and reconcile conflicts manually
   without overwriting knowledge. Leave the original files in place.
4. Run `juex agent start --agent <agent-id>`, then open Main or send a request to
   activate its Thread. Thread startup rebuilds the new index from the copied
   entries. Ask the Agent to use `memory_search` to retrieve a known fact and
   confirm the Runtime shows the three built-in Memory tools and the intended
   other Extensions. Index maintenance errors are observable and require
   inspection; a running Agent alone does not prove the copy succeeded.

`JUEX_HOME` defaults to `~/.juex`. This is an operator procedure; installing or
upgrading Juex does not copy, convert, or delete the old knowledge automatically.
