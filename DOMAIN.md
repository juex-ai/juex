# JueX Domain Model

> English | [中文](DOMAIN.zh.md)

This document defines product vocabulary and invariants. Implementation boundaries
belong in [ARCHITECTURE.md](ARCHITECTURE.md).

## Identity and authority

A User is a global account with stable identity, independent of login credentials.
A Tenant has administrator/member Memberships. One User may join multiple Tenants;
each `(Tenant, User)` owns exactly one retained Fleet and any number of Agents.
Invitations grant Membership and do not prove mailbox ownership. No concurrent
mutation may remove, suspend or demote the last active tenant administrator.

An administrator manages resources only inside that Tenant, within the owner's
existing authority. Actor and resource owner remain distinct in audit and usage.
Reauthorization does not revive old execution epochs or cancelled work. A human
login session and a device credential are separate identities.

## Ownership

| Owner | Durable responsibility |
| --- | --- |
| Management | Accounts, tenants, membership, Fleet/Agent definitions, model policy, encrypted credentials and audit. |
| Runtime | Inputs, Main/Worker Threads, Turns, context checkpoints, model attempts, events, usage and activation leases. |
| Execution | Environments, grants, operations, connections, immutable files, explicit transfers and result acknowledgments. |
| Memory | Fleet knowledge, evidence, proposals, review jobs and deletion constraints. |
| Calendar | Fleet schedules, trigger jobs and delivery identity. |

An Agent owns a permanent Main and independent Workers. A Worker records its
parent; cancellation of that parent does not undo already accepted independent
work. Application jobs use scoped ordinary Workers. Cross-Agent collaboration
is explicit and confined to the same owner and Fleet.

An Activation is replaceable runtime capacity, not the Agent's identity.
Inputs commit before acceptance is returned. Request IDs deduplicate admission;
the next assistant message is not assumed to correspond one-to-one with an Input.
Context compaction preserves durable history and a checkpoint. Worker archival
requires idle work and retains readable history.

## Execution environments

Each Agent can select an authorized environment and working directory as its
default, or use the deployment-provided environment. Host environments use
Linux/macOS directories and the OS user's capabilities; hosted environments have
persistent Workspace/Home and gVisor-enforced limits. A cwd is a default location,
not a filesystem permission boundary. Selecting a default never grants access.
New operations freeze the selected location; prepared requests and process or
connection handles retain their original environment. An unavailable default
never causes automatic substitution.

The deployment owns automatically provisioned environments, whether Host or
Hosted. Each has stable per-Agent identity, Home and Workspace. An externally
paired device remains user-owned; native execution alone does not imply platform
ownership or permission to delete its files.

A network disconnect does not imply process termination. Offline requests wait
durably without consuming model slots. Unknown outcomes are visible and never
silently repeated or redirected. Online revocation rejects new work and requests
cancellation; offline cancellation remains pending until confirmed. Local process
memory and arbitrary remote files are not restored from platform backups.

File transfer is explicit and records both environments. Immutable Artifact
references permit sharing within one Fleet; there is no automatic synchronization
or cross-tenant sharing. Credentials belong only to authorized service/process
scopes and shared model secrets never enter user execution environments.

## Lifecycle and retention

Suspension blocks execution while preserving data. Agent archive and member
removal retain readable resources; restoration checks current authority. Removal
requires reinvitation. Rejoining reuses a retained Fleet; after permanent cleanup,
a fresh empty Fleet is created. Cleanup has durable stages and explicit failures.
Platform data removal and remote process-stop confirmation remain distinct.
Remote user files are never automatically erased.

Memory and Calendar may be disabled without deleting their data. Disabling stops
new agent/application activity; Calendar does not replay missed disabled triggers.
Permanent deletion is separate. Usage and minimal audit survive resource cleanup;
ordinary process logs do not substitute for durable operation receipts.

## Models and usage

The operator supplies models. Fleet defaults and Agent overrides choose from the
current authorized catalog. Each Turn freezes its model/fallback plan, while every
new provider call checks current permissions and credential revocation. Fallback
uses only configured candidates. Actual provider/model attempts own their usage,
including Workers, application work and compaction. Input plus output is total;
cached input is a subset, and missing usage is unknown rather than zero.

Usage belongs to the resource's Tenant/User even during delegated administration.
Daily/monthly aggregates retain their reporting timezone period; changing timezone
does not reinterpret history. Raw detail defaults to 90 days; aggregates remain.
