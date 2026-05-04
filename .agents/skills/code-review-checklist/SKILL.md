---
name: code-review-checklist
description: Apply when reviewing or self-reviewing code changes. Walk through correctness, tests, error handling, security, and naming.
type: model-invocable
---

# Code review checklist

When asked to review (or before claiming a change is done):

1. **Correctness** - does it do what was asked? Edge cases handled?
2. **Tests** - is there a test that would have caught a regression? Run them.
3. **Error handling** - errors surface to the caller; no silent swallows.
4. **Security** - no command injection, no leaking secrets, no path traversal.
5. **Naming + comments** - identifiers describe intent; comments explain *why*.
6. **Diff size** - smallest change that solves the problem; no drive-by edits.

After this checklist, summarise findings as bullets and suggest concrete fixes.
