# Management

> English | [中文](README.zh.md)

Management owns global users, tenants, memberships and each user's unique Fleet
within a tenant. Credentials are separate from the stable User identity. Its
PostgreSQL adapter owns the `management` schema; other services use its business
interfaces, never direct writes to these tables. App owns database connections
and composition. Management does not import the Home-based Fleet supervisor.

The current directory is the first platform foundation. It is not yet wired to
public HTTP, authentication, Web, or Runtime. `CreateUser` is an identity
provisioning primitive and `CreateTenant` is an operator operation; neither is a
public registration endpoint. Adapters must obtain actor IDs from authenticated
sessions. Roles supplied to membership operations are desired changes, not
proof of the caller's authority.

Every membership writer locks the Tenant row before reading current permissions
and checking the last active administrator. Membership changes, actor/owner
audit facts and outbox entries commit together. Per-membership versions preserve
the order of suspend/resume transitions. Outbox persistence records intent; it
does not prove downstream cancellation. Consumers must not discard a suspension
merely because a newer version re-enables access.

Invitation consumption requires the matching authenticated account and does not
verify email. Tokens are single-use, expiring, hashed, and replaced on reissue.
Only accepting a new invitation can restore a removed membership; its retained
Fleet is reused. Invitation creation returns its secret to the caller. The
future dashboard's persistent copy-link view requires encrypted secret storage;
the digest is not a recoverable invitation link.

`Fleet` authorizes a read only. Active tenant administrators can read retained
data for removed or suspended members; that read does not grant configuration
or execution rights. Delegated reads record both actor and owner. Execution and
device-grant authorization remain distinct future operations.

Schema setup is explicit through `postgres.Migrate`, transactionally serialized
and checksum-verified. Unknown or modified versions fail instead of being
silently repaired. Database integration cases in `tests/e2e` use the `postgres`
build tag and require `JUEX_TEST_POSTGRES_URL` pointing to a disposable test
server with database creation permission. Each case creates and drops its own
database. CI exercises these cases with PostgreSQL 18.
