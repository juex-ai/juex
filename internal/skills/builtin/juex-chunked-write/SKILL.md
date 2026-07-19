---
name: juex-chunked-write
description: Guide for safe long-file writes with begin, chunk, commit, and abort.
type: builtin-guide
---
# JueX Chunked Writes

Load this guide when you need detailed long-file write workflows, constraints,
or examples. Correct tool calls do not require a prior guide load. Use the
ordinary `write` tool for generated content up to 2000 characters; use this
workflow for longer files.

## Workflow

1. Call `write_begin` with the working-directory-relative `path`. `mode` is
   `overwrite` by default or `create` when the destination must not exist.
2. Keep the returned `write_id` and call `write_chunk` with zero-based,
   contiguous `index` values. `content` must be the actual file text. Each
   chunk is at most 2000 characters and 4000 bytes. An optional `sha256` is the
   lowercase hexadecimal digest of that chunk.
3. Call `write_commit` with the same `write_id`. Optional
   `expected_chunks` validates the count and optional `sha256` validates the
   assembled file. Commit writes a temporary file and renames it atomically.
4. Call `write_abort` when abandoning an unfinished session.

Never send summary fields such as `content_omitted`, `content_bytes`,
`content_chars`, or `content_sha256` instead of `content`. Tool results are
compact acknowledgements and do not echo chunk contents. If a call is rejected
for size, split that content into smaller sequential chunks and retry from the
same required index. Replaying an identical chunk at an already accepted index
is idempotent; different content at that index is an error.
