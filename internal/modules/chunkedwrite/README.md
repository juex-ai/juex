# Chunked Write

> English | [中文](README.zh.md)

Each enabled Thread Module owns one in-memory write manager. Startup recovers
active buffers from current-Generation owned execution facts and matching tool
arguments. Commit and abort facts remain terminal even if later result policies
change presentation or fail. The tools layer validates paths, checksums and
atomic publication; it does not interpret Thread history.

Close and disable release buffers. `/new` clears them after the Generation
transition commits. No private resource files are created. Committed user files
and journals are retained; re-enabling can recover active work still represented
in the current Generation.

Provider folding is request-only. The Module selects its completed tool pairs
and summary anchors; Framework enforces ownership, protocol validity and budgets.
Disabled Modules do not restore buffers or fold historical chunks. Generic
Provider projection still preserves valid tool pairing and context limits.
If the current output budget cannot fit the summaries, folding is skipped and
ordinary projection handles the original tool pairs.
