# Calendar

[简体中文](README.zh.md)

Calendar owns shared Fleet schedules, occurrence history, application enablement
and notification outboxes. Management resolves human and Agent authority; Runtime
admits explicit Agent work as ordinary, budgeted Workers. Reminder occurrences
deliver an Inbox notification without calling a model. Main triggers instead
deliver an input into the selected Agent's retained Main context. Their accepted
receipt proves durable delivery, not completion. Before acceptance, cancellation
fences admission; afterwards the input follows ordinary Main authority and
cancellation rules. Calendar disable does not retract accepted Main inputs. The typed private SDK is
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

The default recovery policy chooses the latest unrecorded occurrence within the
schedule's lateness window (24 hours by default, 1 to 1,440 minutes). Expired
occurrences are recorded as missed. The none policy skips only unprepared
instants at or before the recovery boundary. Continuous timer delays retain the
original due instant without applying a recovery lateness window. Prepared
deliveries always retain their identity. A single scheduler session holds a
PostgreSQL advisory lock; all occurrence and clock writes use that same
connection. Acquisition establishes a fixed database-time recovery boundary. A
second instance cannot reset it, and a disconnected leader cannot commit stale
scheduling writes. Delivery and notification remain independently retryable. Manual pause/resume and application
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
acknowledgements. Main notices are context for the next input, not a new wake; the explicit Main
trigger mode is the separate waking input. Its receipt notice goes only to Inbox.
Notification preferences and verified-email delivery belong to Management.

Agent cleanup pauses its schedules and preserves shared definitions and occurrence history. Undelivered work is fenced; uncertain external effects stay explicitly unresolved. New and resumed schedules check their target against cleanup tombstones inside the Fleet transaction. Whole-Fleet cleanup erases Calendar state and prevents late initialization.
