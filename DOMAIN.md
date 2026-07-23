# Juex Domain Model

This document is the canonical source for Juex product language, lifecycles,
and invariants. `ARCHITECTURE.md` maps these concepts to code modules,
interfaces, storage, and tests; it must not redefine their meaning.

## Product Boundary

Juex is one bounded context: a local, inspectable agent runtime that runs a
Resident or Ephemeral Agent in a Workspace, turns input into model and Tool
work, and keeps the resulting state available for continuation and inspection.

Go packages, CLI commands, HTTP routes, provider SDKs, process managers, and
the React application are modules or adapters inside that context. They are
not separate bounded contexts. External model services, MCP servers, operating
system service managers, and the user's filesystem remain outside the Juex
domain boundary.

## Ubiquitous Language

### Identity And State

| Term | Meaning |
| --- | --- |
| Agent runtime | The local system that admits input, builds model context, calls a Provider, executes Tool Calls, persists Session state, and emits Events. |
| Workspace | The project directory from which Juex loads work-local guidance and configuration and in which it stores the identity marker and workspace-rooted artifacts. |
| Juex home | The effective Juex-owned root selected by `JUEX_HOME`, defaulting to `~/.juex`; it scopes shared configuration, extensions, locks, and the resident Agent registry. |
| Resident Agent | A durable Agent identity bound one-to-one to a Workspace marker, stored in the Juex home registry, and visible to Fleet operations. Its identity-owned state survives Workspace moves. |
| Ephemeral Agent | A process-local Agent identity with private temporary Agent state. It uses the normal Workspace and user configuration/resources but has no Workspace marker, is not registered with Fleet, and is deleted on exit unless explicitly kept. |
| Workspace marker | `.juex/juex.local.json`, the narrow binding from a Workspace to its Resident Agent id. A marker is identity, not configuration or a copyable cache. |
| Agent Address | The value that binds a resolved Agent id to its identity-owned state directory and endpoint guard. Consumers use the address rather than deriving identity or Juex-home layout from directory names. |
| Agent home | The identity-owned state directory for one Resident Agent at `$JUEX_HOME/agents/<id>`, containing its registry record, Sessions, history, memory, logs, and generated Observable state. |
| Runtime Instance | One serving process incarnation for an Agent, identified independently from the Agent id and described by its instance id, process id, endpoint, start time, and binary version. Restarting changes the Runtime Instance without changing the Agent. |
| Workspace-local state | User-authored configuration and resources plus workspace-rooted artifacts under the Workspace. Observable definitions are workspace-local; generated Observable state is not. |
| Agent state | Runtime state owned by an Agent identity, including Session history, memory, logs, and generated Observable state. Resident Agent state lives in its Agent home; Ephemeral Agent state lives in its private temporary home. |
| Fleet | The control surface for Resident Agents registered under one effective Juex home. It projects binding and runtime health and manages lifecycle without owning user-authored Workspace content. |

### Sessions And Turns

| Term | Meaning |
| --- | --- |
| Session | A resumable, ordered conversation with identity, kind, transcript, Events, usage, model-owned working state, and a single-writer lock. |
| Primary Session | A Session eligible to be selected as the Resident Agent's active continuation target. |
| Side Session | A durable exploratory Session that is listed and resumable but never becomes the active Session. |
| Active Session | The selected Primary Session used by default for CLI, Web, and external-event continuation. |
| Turn | One user-originated or system-originated input processed through one or more Provider iterations and Tool Call batches until completion, cancellation, or error. |
| Pending input | Accepted user steering or external input queued while a Turn or compaction phase is active. It is durable, bounded, expiring, and admitted only at a safe Provider-iteration boundary. |
| Session state | Model-owned Goal and Notes for one Session, distinct from Agent state and from the runtime's observed execution status. |
| Goal | The model-owned completion contract for one Session, including its description, acceptance criteria, status, and continuation state. |
| Notes | The model-owned, bounded working Markdown for one Session. Notes survive compaction and are recited on every Provider request. |
| Session scratchpad | Session-local files for long drafts and intermediate work. They are managed explicitly, are not automatically placed in model context, and are removed with the Session. |
| Compaction | A policy-driven summary operation that appends a durable compact marker and changes future active context without deleting the original transcript. |
| Context projection | A request-time representation of large user input or Tool results using bounded previews and Artifact references. Projection preserves the durable source content and transcript contract. |

### Models, Tools, And Guidance

| Term | Meaning |
| --- | --- |
| Provider | A model service adapter that exchanges Juex's canonical messages, Tool definitions, usage, and stop reasons with an external model service. |
| Provider Profile | The resolved Provider identity, model, Protocol, endpoint and credential inputs, compatibility options, and Capability Set used for a request. |
| Protocol | A Provider wire contract, such as Anthropic Messages, OpenAI Responses, OpenAI Codex Responses, or OpenAI-compatible Chat. |
| Capability Set | Explicit gates describing which optional behaviors a Provider Profile supports, including tools, vision, streaming, reasoning controls/replay, and output-token control. |
| Tool | A named, schema-described operation available to the model. Builtin, skill, memory, Observable, model-state, and MCP tools share one runtime catalog and result contract. |
| Tool Group | A stable classification used to inspect and present related Tools without changing their names or execution contract. |
| Tool Call | A Provider-requested Tool operation identified within an assistant message. Its result is persisted in provider order and remains adjacent to the call in valid model context. |
| MCP Server | A configured stdio process that contributes Tools and may emit external notifications. One failed MCP Server does not disable healthy servers or builtin Tools. |
| MCP Notification | An external event from an MCP Server that is admitted as pending input or as a system-originated Turn. It is not user-authored input. |
| Skill | A Markdown instruction package discovered from configured resource scopes and made available to the model through prompt metadata and Tool access. |
| Memory Entry | Reusable Agent-owned context managed through memory Tools and stored with Agent state. It is distinct from work-local or user-global `AGENTS.md` guidance. |
| Prompt Section | A named part of the assembled system prompt, such as guidance, available Skills, Memory, runtime state, or shell context. |

### External Signals And Durable Content

| Term | Meaning |
| --- | --- |
| Observable | A workspace-defined external signal source with a shared lifecycle and durable generated state. |
| Command Observable | An Observable backed by a managed command whose parsed, filtered, and bounded output batches become Observations. |
| Schedule | An Observable backed by a one-shot, daily, or interval timetable and a pre-authored Observation payload. |
| Observation | A durable normalized signal emitted by an Observable, with source identity, content, attachments, delivery state, and target Session when admitted. |
| Event | A stable fact about runtime activity. Durable Events are committed to the Session journal before live delivery; explicitly transient Events exist only for current subscribers. |
| Artifact | Durable Workspace-local bytes addressed by a safe relative path plus integrity metadata. An Artifact reference is portable with the Workspace and does not imply that the bytes are model-visible. Observable-private oversized payload files in Agent state are generated implementation state, not Workspace Artifacts. |
| User Media | Session-scoped image input stored as an Artifact and represented in conversation by a validated media reference. Provider capabilities determine projection, not whether the durable reference exists. |

## Lifecycles

### Resident Agent Identity

1. A stateful command resolves the Workspace and effective Juex home.
2. A Workspace without a marker mints one Resident Agent id, publishes its
   Agent home, and writes the marker.
3. Later commands resolve the stored id to the same Agent Address. A missing
   registry entry fails loudly rather than silently minting a replacement.
4. A moved Workspace may rebind to the same Resident Agent after validation.
   A copied marker that still belongs to another live Workspace is rejected.
5. A serving process acquires the Agent Address guard and publishes a new
   Runtime Instance. Restart replaces that instance while preserving Agent
   identity and state.
6. Fleet stop and service removal preserve Agent state. Explicit Resident
   Agent removal is the destructive boundary and does not delete user-authored
   Workspace files.

### Session And Turn

1. Work attaches to the active Primary Session, creates a new Primary Session,
   creates a Side Session, or explicitly resumes a recorded Session.
2. Turn admission records the input and establishes one active execution
   boundary for that Session.
3. Each Provider iteration receives canonical context, may return ordered Tool
   Calls, and persists their ordered results before the next iteration.
4. Pending input drains only between Provider iterations. Completion closes the
   Turn only when no accepted input remains to continue it.
5. The transcript and durable Events remain the source for resume and
   inspection after completion, cancellation, failure, or process restart.

### Pending Input

1. Accepted input receives stable record and message ids, an expiry, and a
   durable `pending` record.
2. Admission marks the record before its message is appended to active context.
3. Successful processing is recorded so restart cannot execute the same input
   twice.
4. Expired input becomes inert. Queue overflow is rejected loudly.
5. Turn failure does not silently discard accepted input: retryable Provider
   failures may continue with it, while terminal failures preserve it in
   conversation history before ending the Turn.

### Observable And Observation

1. A Workspace defines a tagged Command Observable or Schedule.
2. Starting or manually running the source records generated run state in the
   Agent home.
3. Each accepted signal is normalized and durably recorded as an Observation
   before asynchronous delivery.
4. Delivery either queues it as pending input, delivers it to the active
   Primary Session, or records an explicit terminal state. Source deletion
   cannot erase the historical fact that an Observation existed.

### Compaction

1. Policy or an explicit request selects older provider-visible context while
   retaining a recent tail.
2. The summary request includes the current Goal and Notes as authoritative
   working state.
3. A successful summary is appended as a compact message with selection and
   usage metadata.
4. Future Provider requests use the latest compact marker plus retained
   messages; the persisted original transcript remains inspectable.

## Domain Invariants

1. **One identity binding.** One Workspace marker names one Resident Agent, and
   one live registry record points back to its bound Workspace.
2. **Identity is stored, not inferred.** Agent ids cannot be recomputed from a
   Workspace path or Agent-home basename. The Agent Address owns the mapping.
3. **Agent identity is distinct from a process.** An Agent id is not a Runtime
   Instance id. One Agent Address has at most one canonical serving instance,
   and control operations verify the exact Agent and Runtime Instance they
   target.
4. **Storage follows ownership.** Workspace-authored configuration, resources,
   Observable definitions, and Artifacts stay with the Workspace.
   Identity-owned Sessions, memory, history, logs, and generated Observable
   state stay with the Agent.
5. **Ephemeral work is isolated.** An Ephemeral Agent never creates, rebinds,
   migrates, or registers a Resident Agent identity.
6. **Only Primary Sessions activate.** A Side Session cannot replace the active
   Primary Session.
7. **Transcripts remain structurally valid.** Tool results preserve Provider
   order and match their Tool Calls; repair is explicit and recorded.
8. **Accepted input is durable.** Failure or cancellation may stop a Turn, but
   it must not silently lose input that admission already accepted.
9. **Provider details stop at the adapter.** Protocol-specific wire shapes do
   not redefine Session, Turn, Tool, or Event meaning.
10. **Capabilities are explicit.** Optional Provider behavior is enabled by the
   resolved Capability Set, not guessed from a model name at the call site.
11. **Events report facts.** A durable Event is committed only after the fact
    it represents and before live consumers treat it as authoritative.
12. **Observable definition and state are separate.** Definitions follow the
    Workspace; generated runs, Observations, delivery records, and schedule
    cursors follow the Agent.
13. **Artifact references are bounded.** Durable bytes remain Workspace-local,
    references are safe relative paths with integrity metadata, and User Media
    references are scoped to their target Session.
