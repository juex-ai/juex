# Management

> English | [中文](README.zh.md)

Management owns global users, tenants, memberships and each user's unique Fleet
within a tenant. Credentials are separate from the stable User identity. Its
PostgreSQL adapter owns the `management` schema; other services use its business
interfaces, never direct writes to these tables. App owns database connections
and composition. Management does not import the Home-based Fleet supervisor.

The authenticated Management HTTP service and Web use this directory.
`CreateUser` is an identity
provisioning primitive and `CreateTenant` is an operator operation; neither is a
public registration endpoint. Adapters must obtain actor IDs from authenticated
sessions. Roles supplied to membership operations are desired changes, not
proof of the caller's authority.

Every membership writer locks the Tenant row before reading current permissions
and checking the last active administrator. Membership changes, actor/owner
audit facts and execution epochs commit together. Restoring access never rolls
back an epoch; Runtime, Execution and applications recheck it to invalidate old
work even when suspension and restoration occur between their checks. Audit
facts default to 90 days, configurable by the operator with `--audit-days`.
Retention never removes authority epochs, business history or recovery receipts.

Invitation consumption requires the matching authenticated account and does not
verify email. Tokens are single-use, expiring, hashed, and replaced on reissue.
Only accepting a new invitation can restore a removed membership; its retained
Fleet is reused. Invitation creation returns its secret to the caller. Its
encrypted copy backs the persistent copy-link view; the digest cannot recover it.

Passwords use bounded Argon2id hashing; human sessions and capability tokens are
stored as digests. Invitation registration cannot reset an existing account.
Bootstrap and operator recovery have no public HTTP route. Password recovery
revokes human sessions independently of device credentials and Agent execution.
Invitation acceptance is not email verification; SMTP recovery requires a
separately verified email address.

Mail is transactionally queued with an encrypted body. The worker records SMTP
acceptance separately from invitation status. Retries retain the message ID;
SMTP cannot guarantee exactly-once delivery after an ambiguous disconnect.
Confirmed delivery erases the queued capability body. Public API responses never
contain password hashes, provider keys or the platform encryption key.

`Fleet` authorizes a read only. Active tenant administrators can read retained
data for removed or suspended members; that read does not grant configuration
or execution rights. Delegated reads record both actor and owner. Execution and
device-grant authorization are distinct operations.

Fleet settings own model defaults. Memory and Calendar own their application
enablement and execution epochs; the dashboard reads and configures each service
through its authenticated API instead of maintaining duplicate flags.

Management owns the Agent capability policy. Omitted policy preserves the stored
value on configuration edits; an explicit empty disabled list allows all groups.
Tightening the policy and advancing the Agent execution epoch commit together.
Runtime freezes the policy per Turn and intersects it with fresh authority;
Execution and application owners enforce their own operations through the same
authority snapshot. A per-Agent application restriction does not change Fleet
application enablement or erase application history.

Dynamic instruction sources are explicitly enabled and default to off. An
optional global path belongs to the selected execution environment. Omitting
this setting preserves it during other configuration edits; disabling or changing
an active source advances the Agent execution epoch with the configuration write.

Models are deployment-owned. Tenant catalogs inherit by default or use an
explicit allowlist; an empty allowlist grants nothing. Fleet defaults inherit
the platform default, and Agents may override them. Operators configure flat,
ordered fallback lists. Snapshot admission and each credential resolution check
current resource authority in one Management transaction. Per-model and tenant
access epochs prevent revoke/restore from reviving an old Turn's grant; unchanged
candidates remain usable. API keys may rotate without changing the frozen
route. Changes to endpoint, protocol, limits or private provider options advance
the model epoch, including an account change followed by restoration. Private
headers/query parameters, thinking and capability/compatibility options are
encrypted with the model and returned only to Runtime after fresh admission;
public catalogs and frozen Turns contain no copies of those values. Provider
labels do not override explicit protocols. Managed Codex requires a fixed,
explicit `ChatGPT-Account-ID`; token rotation never chooses the account implicitly.
An unauthenticated local OpenAI-compatible endpoint requires explicit `none`
authentication and sends no Authorization header or invented API key.
Admission ends before the external request: revocation blocks
later admissions, not an already dispatched request.

The normal request output cap is separate from its positive context reservation.
A zero cap keeps the adapter/provider default; it does not promise unlimited
output or guarantee that the provider uses only the reserved tokens. Anthropic
requires at least its 4096-token adapter default in that reservation. Both values
are frozen into model plans; changing either invalidates earlier candidates.

Schema setup is explicit through `postgres.Migrate`, transactionally serialized
and checksum-verified. Unknown or modified versions fail instead of being
silently repaired. Database integration cases in `tests/e2e` use the `postgres`
build tag and require `JUEX_TEST_POSTGRES_URL` pointing to a disposable test
server with database creation permission. Each case creates and drops its own
database. CI exercises these cases with PostgreSQL 18.

The persistent Inbox belongs to the resource owner within each Tenant. Only that
signed-in user may read its notifications, change read state or configure delivery;
delegated Fleet administration does not grant personal Inbox access. Application,
Fleet and event identity deduplicate frozen payloads. Completion notifications are
opt-in; reminders and attention events are visible by default. Preferences affect
future notifications. Each business email attempt checks current membership,
verified current address and preferences. It contains only an authenticated Inbox
link, never application evidence or conversation content. SMTP retries retain their
message identity but cannot promise exactly-once remote delivery.

Permanent cleanup is a durable, owner-scoped job for an archived Agent or a removed member’s Fleet. Management freezes the target identity, fences every participant before erasing data, and retries each acknowledged phase independently. Service tombstones reject late writes and recreation. Platform data removal and outstanding external stops are separate results. Usage and audit survive; a purged Fleet is replaced only when its owner accepts a new invitation. Historical backups expire under the operator’s retention policy.

Execution-side extensions are Agent-owned configuration snapshots bound to an authorized environment and explicit directory. Import accepts an Execution inspection receipt, never browser-supplied manifest or skill bodies. Current file grants and their version must still match. Configuration writes validate the complete derived Hook set. Resource removal, binding disablement and embedded Hook disablement advance the execution epoch; installed device files remain untouched.
