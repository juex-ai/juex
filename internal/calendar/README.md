# Calendar

[简体中文](README.zh.md)

Calendar owns shared Fleet schedules, occurrence history, application enablement
and notification outboxes. Management resolves human and Agent authority; Runtime
admits explicit Agent work as ordinary, budgeted Workers. Reminder occurrences
deliver an Inbox notification without calling a model. The typed private SDK is
in `rpc`; user and Agent entry points cannot supply an arbitrary execution scope.

The PostgreSQL adapter serializes each Fleet's bounded state under one short row
lock. Rule edits, occurrence identity and outbox facts commit together; network
calls happen outside that lock. State is limited to 1,000 schedules, 100 unfinished
occurrences, 50,000 retained occurrences and 64 MiB per Fleet. Capacity rejection
does not discard an unfinished or unknown operation. Other Fleets remain independent.

An occurrence freezes its target, instruction, schedule version and authority.
Rule changes and reassignment affect future dispatch only. Dispatch queries the
original Runtime receipt before admission, and retries the same identity after a
lost reply. Persisted occurrences remain recoverable regardless of age. A Worker
with an unknown external outcome is retained for attention and never replaced.
Model completion and external settlement are separate: a completed Worker can
still own running background operations. Calendar keeps querying their original
identities and can cancel them after completion or application disablement.
Cancellation remains requested until Runtime confirms settlement. Concurrent
delivery attempts are fenced so an older response cannot replace a newer result.

Fault recovery chooses the latest unrecorded occurrence within the schedule's
lateness window (24 hours by default, configurable from 1 to 1,440 minutes).
Expired occurrences are recorded as missed. Manual pause/resume and application
disable/enable only schedule future occurrences. Disabled data remains readable
to authorized humans; Agent access and new triggers stop. Target or authority
revocation pauses a schedule until explicit resume with fresh authority. Archiving
keeps definitions and history; restoring a definition leaves it paused.

`recurrence` is pure date calculation. Fixed intervals retain a persisted anchor.
Gregorian missing dates and daylight-saving gaps are skipped; ambiguous wall
times fire only at their first instant. Chinese lunar conversion is serialized
around the dependency's mutable cache. Search is bounded to ten future years.
Lunar one-shot dates normalize to an absolute instant at admission. The module
does not read a user's local filesystem or execute user code.

Completion, reminder and attention notices use independent Main and Inbox
acknowledgements. Main notices are context for the next input, not a new wake.
Notification preferences and verified-email delivery belong to Management.

Agent cleanup pauses its schedules and preserves shared definitions and occurrence history. Undelivered work is fenced; uncertain external effects stay explicitly unresolved. New and resumed schedules check their target against cleanup tombstones inside the Fleet transaction. Whole-Fleet cleanup erases Calendar state and prevents late initialization.
