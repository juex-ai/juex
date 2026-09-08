# Input Tracking Proposal

> English | [中文](input-tracking.zh.md)

Status: implemented in [PR #538](https://github.com/juex-ai/juex/pull/538). Updated: 2026-09-08. The proposal below records the design discussion; the [Input Tracking Module](../../internal/features/inputtracking/README.md) defines the shipped behavior, limits, and upgrade requirements.

Add a simple durable checklist to the existing input mechanism: Framework registers inputs automatically, recitation presents unchecked inputs, and the model checks them off after handling them. This proposal does not replace the current DOMAIN or ARCHITECTURE contracts.

## 1. Add Only One Check Marker

Each tracked input has two possibilities:

- **Unchecked:** still needs the model's attention, whether unstarted, partially complete, temporarily blocked, or containing an effective constraint.
- **Checked:** handled appropriately and no longer needs a reminder.

A nullable `checked_at` is sufficient: null means unchecked, and Framework writes the timestamp when the tool commits successfully. The model neither maintains timestamps nor chooses work states, error categories, or disposition types.

Reuse existing input IDs, original content, provenance, attempts, and errors. Tracking eligibility is Framework metadata established at acceptance; untracked must remain distinct from unchecked.

Do not add open/blocked/resolved or completed/incorporated/superseded/cancelled state sets, progress fields, evidence fields, or relationship graphs. Conversation, Goal, Notes, and existing Tool results continue to express progress.

## 2. Relationship To pending_input And Modules

Existing `pending_input` owns durable acceptance, queueing, injection, execution attempts, and recovery. The new checklist only records whether the model still needs a reminder, sharing the same `input_id` and original input.

| Component | Responsibility |
| --- | --- |
| Runtime / Framework | Automatic registration, input storage, check commits, current-set retention, execution recovery, and lifecycle boundaries. |
| `input-tracking` module | One check tool, per-request recitation, and lightweight check-state presentation. |
| Goal / Notes | Long-term objectives and working notes respectively, without responsibility for registering every input. |

Retain the proposed independently switchable module: standard enabled and minimal disabled by default, still subject to review. Disabling it leaves delivery intact, removes its tool and recitation, and stops tracking new inputs. Existing unchecked records remain. Re-enabling presents them on the next normal execution, without backfilling disabled-period history or starting old work automatically.

Runtime still owns the unified current-state store, using the agreed `inputs.json` name to express that the former `pending_inputs.json` now includes both execution-pending and unchecked inputs. Do not copy originals into a separate Notes ledger or build another delivery state machine.

Framework acceptance, queue order, step-boundary injection, and recovery semantics remain unchanged. Compute `pending_count` from the actual delivery queue, not the number of records in `inputs.json`. A successful check removes the input from recitation; remove its file record only when execution recovery no longer needs it. Checking does not cancel an active Turn.

Existing internal `processed` means the input has been written to history, not checked by the model. Use `checked_at` for the new marker without reusing that meaning.

Initially track direct user conversation inputs accepted while the module is enabled: the first input when idle, inputs during execution, and user inputs directly addressed to Workers. Trusted entrypoint metadata identifies provenance. System continuations, Tool results, Observations, MCP Notifications, and automatic Worker notifications do not automatically enter this checklist.

## 3. Expose Only One Model Tool

Proposed name: `check_inputs`. Its only argument is the input IDs to check, optionally in a batch:

```json
{"input_ids": ["I101", "I103"]}
```

Do not expose `get_inputs` or a general `update_inputs`. Recitation already supplies current unchecked inputs, so a query tool is unnecessary. Checking does not require states, revisions, reasons, progress, or evidence references.

The tool can check only tracked inputs delivered to the model in the current Thread and work scope. It cannot create, edit, or delete inputs or check undelivered messages. Rechecking an input succeeds idempotently; missing or out-of-scope IDs fail explicitly. Validate the whole batch before committing it atomically to avoid ambiguous partial success.

Return the confirmed IDs. Framework records the check time, Turn, and invocation association automatically, without extra model arguments.

Initially omit an uncheck tool. If the user identifies an error or omission, that new message becomes an unchecked input while the original check history remains.

## 4. When To Check An Input

Checked means appropriately handled, rather than merely seen. It does not require treating every message as an independent task.

| Situation | Rule |
| --- | --- |
| “Implement export and write tests” | Check only after both implementation and tests are complete, not halfway through. |
| “What is the current progress?” | Check after answering this question; leave the original implementation input unchecked. |
| “Change the format to JSON” | Work against the amended requirement and check after applying it, not merely acknowledging receipt. |
| “Do not merge the PR this time” | Keep unchecked until the applicable work ends so the constraint remains in recitation. |
| Failure or waiting for the user | Keep unchecked and explain the obstacle through existing conversation. |
| User explicitly cancels or fully replaces an earlier requirement | After handling that instruction, the model may check the old input that no longer requires execution; no cancellation or supersession subtype. |

Keep originals unchanged and present them chronologically. Guidance makes later user amendments authoritative rather than mechanically executing withdrawn wording. The model must not cancel work merely to empty the list. A partial amendment does not finish the entire original task.

Framework validates identity, scope, and ordering; the model still judges whether handling is sufficient. Do not add mandatory evidence parameters or a separate verifier model initially, and do not claim a check proves correctness.

For questions, save the answer before committing the check. If an answer and tool call share a model response, Framework persists the answer first. Never check before relying on an answer that has not yet been generated. Accept a short additional call when checking requires another step.

## 5. Recitation Supplies All Unchecked Information

Every Provider request automatically includes the IDs and content of all delivered, unchecked inputs in the current scope. Existing queueing handles undelivered messages; they do not appear early in the checkable list.

```text
These inputs remain unchecked. Preserve existing work and apply later amendments.
Call check_inputs after handling them. Unchecked does not mean no work was done.

[I101] Implement export and write tests.
[I104] Do not merge the PR this time.
[I105] Add invalid-input tests.
```

Reuse original message content or its existing input projection instead of generating another semantic summary that could omit requirements. Attachments and large content use existing media, spool, and tool-read mechanisms; this module adds no query protocol.

With no query tool, the initial checklist must not require pagination to discover inputs. Include recitation explicitly in context budgeting. Use existing compaction/content projection first, then report capacity problems if it still cannot fit, rather than silently hiding unchecked IDs or requirements.

Checked inputs are no longer repeatedly injected as work. Their originals and check facts remain in history, and compaction summaries must respect those facts. Rebuild recitation from durable state on each request instead of appending snapshots as new user messages or elevating source instruction priority.

Initially do not select historical completed items for automatic redisplay or maintain a separate effective-constraint state. Constraints needing continuing reminders simply remain unchecked.

## 6. Essential Persistence And Recovery Rules

1. **Register at acceptance.** One durable acceptance commit stores the input and, when tracked, its empty check marker before acknowledging success. Do not rely on the model writing Notes or an asynchronous callback to create records later.
2. **Dequeuing does not check an input.** Normal Turn completion, API success/failure, and compaction cannot write `checked_at`. Existing execution settlement continues while unchecked records are retained independently.
3. **Commit checks before publishing.** Follow existing Generation-fact commit, current-state update, and publication ordering. Reconcile the crash window between the committed check and state file by ID without repeating tools.
4. **A reminder is not requeueing.** Recitation reminds the model about delivered, unchecked inputs without replaying them as new user messages. Keep existing execution recovery, restoring completed Tool outcomes and avoiding blind retries of unknown outcomes.
5. **Bound retention.** Keep inputs needing delivery/execution recovery plus unchecked inputs in the current scope. Reject before acceptance when full. Delivery TTL must not silently erase accepted unchecked requirements. Checked inputs no longer involved in execution recovery may leave the current set while history remains.

Ordinary stop, exhausted Provider retries, and module disablement retain unchecked records and respect existing suspension/resumption controls. Process restart does not introduce another continuation mechanism.

Host `/new` explicitly ends the old checklist's active scope without marking its unchecked inputs as checked or deleting history. Inputs accepted after the boundary belong to the new scope. Compaction retains the same work scope. Model `context_new` cannot implicitly clear a checklist with unchecked items; use compaction instead.

Storage changes follow the project's clean-break policy: no legacy aliases, dual reads, or automatic migration. Inspect outstanding old inputs and arrange manual handoff before implementation cutover. This proposal does not perform that cutover or delete existing data.

## 7. No Separate Automatic Continuation Gate Initially

A binary checklist cannot automatically distinguish forgotten tasks, work waiting for the user, and still-effective constraints. Unchecked items therefore neither unconditionally prevent a Turn from ending nor trigger unlimited continuations.

Recitation reminds the model during every request. Existing Goal continuation retains its own rules. Remove the earlier independent FinishPolicy, three-attempt no-progress detector, and Goal/input continuation arbitration design.

This ensures unchecked information does not silently disappear after dequeuing or compaction, but does not guarantee immediate completion of every input. If evaluation still shows frequent termination with runnable work outstanding, discuss finish checks separately rather than preemptively adding blocked states and more tools.

## 8. Presentation, Verification, And Research

Web may show a check beside an input, with failures handled by existing error presentation. Assistant prose remains plain conversation text. Add no task board, disposition categories, or semantic-completion wait mode. `send --wait` still means the consuming Turn settled.

Acceptance tests should cover registration of initial and mid-run inputs, independent checks across multiple inputs, immutable originals, idempotent and scope-safe checks, retention across errors/compaction, answer-before-check ordering, recovery without action replay, module disable/re-enable and work-scope boundaries, and large checklists without silent omission. Cross-module behavior requires e2e tests, visible Web changes require browser verification, and implementation follows the [local validation skill](../../.agents/skills/juex-localtest/SKILL.md).

Model evaluations reproduce the original five scenarios, comparing existing behavior, Notes reminders, and this proposal. Report omissions, duplicate actions, false checks, unchecked-input retention after recovery, and additional calls/tokens. Do not promise unmeasured improvement; specifically check whether the model still ends prematurely.

The following research was performed on 2026-09-07; upstream was not researched again for this simplification:

- [Pi's loop](https://github.com/earendil-works/pi/blob/9767ba275f3e9a5ee0f5c5342249b629ab1b2282/packages/agent/src/agent-loop.ts) and [compaction template](https://github.com/earendil-works/pi/blob/9767ba275f3e9a5ee0f5c5342249b629ab1b2282/packages/coding-agent/src/core/compaction/compaction.ts): delivery timing and summarized progress are distinct from per-input checks.
- [Codex input loop](https://github.com/openai/codex/blob/5ecb3afd1bf405149e2159bfda50093b0c1b5fab/codex-rs/core/src/session/turn.rs), [Plan](https://github.com/openai/codex/blob/5ecb3afd1bf405149e2159bfda50093b0c1b5fab/codex-rs/core/src/tools/handlers/plan.rs), and [Goal](https://github.com/openai/codex/blob/5ecb3afd1bf405149e2159bfda50093b0c1b5fab/codex-rs/ext/goal/src/spec.rs): the inspected paths do not form an automatic per-input registration and checking protocol.
- [Manus](https://manus.im/blog/Context-Engineering-for-AI-Agents-Lessons-from-Building-Manus) supports todo recitation to reduce attention drift; [Anthropic](https://www.anthropic.com/engineering/effective-harnesses-for-long-running-agents) notes that models can still check items prematurely.
- [Lost in the Middle](https://arxiv.org/abs/2307.03172) and [LLMs Get Lost In Multi-Turn Conversation](https://arxiv.org/abs/2505.06120) motivate the problem without directly validating this design.
