# Input Tracking

> English | [中文](README.zh.md)

The `input-tracking` Module supplies a durable input checklist and the
`check_inputs` tool. Standard enables it; minimal disables it. Override either
preset with `modules.input-tracking.enabled`.

Framework registers direct user inputs at acceptance, including Main and
direct Worker inputs. Automated continuations, observations and notifications
are excluded. Each request receives the delivered unchecked inputs in acceptance
order. The model checks an input after handling it; partial work, failures,
waiting requests and still-applicable constraints remain unchecked. Questions
must be answered before checking, and other tools must finish in a previous
response. Checks are atomic across the requested IDs and idempotent within
the same Thread and work scope.

Framework owns `inputs.json`, original input content, check facts and recovery.
The Module owns no separate state file or disposable resource. A consuming Turn
can settle while its input remains unchecked: that record is a reminder, not
a new queued execution. Checking removes the reminder immediately; a file
record remains until execution recovery no longer needs it. Normal Turn
completion never checks an input. `pending_count` and `send --wait` retain
their delivery semantics.

Disabling removes the tool and reminders and stops registering new inputs.
Existing unchecked inputs are retained; re-enabling exposes them during the
next normal execution without backfilling disabled-period messages or waking
old work automatically. Compaction preserves the work scope and rebuilds
reminders from durable state. Host `/new` ends the previous checklist scope;
model `context_new` is rejected while unchecked inputs remain.

The current checklist accepts at most 256 unchecked inputs, rejecting new
tracked inputs before acknowledgement when full. Delivery TTL does not expire
tracked requests. Large content uses the ordinary input projection and artifact
read paths. Checklist previews share the compaction retention budget so reminders
cannot restore full long inputs after compression. Every input remains discoverable; context capacity failures are
reported rather than silently omitting checklist entries.

A check is the model's judgement, not proof that the work is correct. The
checklist does not add an automatic continuation or finish gate. Deterministic
coverage lives in Framework tests and `tests/e2e/input_tracking_test.go`;
the opt-in real-model A/B test is `TestLiveInputTrackingAB` with build tags
`integration,input_tracking_eval`. It records omissions, duplicate actions,
premature checks and call/token costs under `.tmp/reports/input-tracking/`.

Deployment is a clean break from the former pending-input filename and
Generation seeds. Before upgrading an existing Agent, settle or manually hand
off outstanding inputs and old context; no legacy reads or automatic migration
are provided. Merely disabling this Module does not reverse the storage change.
