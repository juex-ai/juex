# ADR-0003: Managed Agent Platform

> English | [中文](0003-managed-agent-platform.zh.md)

## Context

The Home-based registry couples business identity, discovery and lifecycle to a
local machine. Multiple users and remote execution devices require identities
and permissions independent of paths and processes. Memory and Calendar need
their own durable ownership while Agents sleep.

## Decision

Keep the existing monorepo and Go module. The platform is natively multi-tenant
with a single-tenant deployment default. A global User has tenant Memberships;
each `(Tenant, User)` owns one Fleet. Tenant roles are administrator and member.
Administrators may act on their tenant's resources with actor/owner audit, but
cannot enlarge or revive an owner's remote-device grants. Operator duties have
separate CLI access rather than a third dashboard role.

Management owns identity, membership, resource definitions and authorization.
One shared Runtime hosts replaceable Agent Activations with independent Main
and Worker state. Memory and Calendar own their Fleet-scoped business state.
Execution/Connections owns execution environments, operations and connections
that must survive Activation idleness. Internal service contracts use Kitex;
Web and management CLI use the public HTTP/event boundary.

PostgreSQL owns business records, pending inputs, events, usage and leases.
Transactions and outboxes establish durable acceptance before publication.
Services own their schemas and write interfaces. Platform-managed Blob storage
and persistent hosted Workspace volumes hold large files; paths do not supply
business identity or service discovery.

Linux deployment uses Docker Compose. Hosted Agent execution uses OCI/gVisor
with no automatic weaker fallback. Linux/macOS remote devices connect outbound
under explicit owner authorization. Full OS-user execution is distinct from a
hosted sandbox; cwd is not a permission boundary. Models select from authorized
environment identities and cannot grant themselves access. Offline operations
wait durably; unknown external outcomes are not blindly replayed.

Built-in email/password authentication keeps stable User identity separate from
credentials. Invitations grant Membership, not mailbox verification. Models and
their secrets come from the deployment operator; usage belongs to Tenant/User
and the actual provider/model, including administrator delegation.

The new platform starts with fresh state and selected manual data transfer.
There is no old Home, API or configuration compatibility layer. All public entrypoints use these managed service boundaries.

## Alternatives and consequences

- Extending the Home-scoped Fleet supervisor would retain path-based identity
  and mix process management with tenant authority. Management is a separate
  group; App composes it without upward dependencies from the domain services.
- A new repository would discard useful Provider, Thread, tool, UI and test
  assets. The monorepo retains those assets while replacing their persistence
  and deployment boundaries. Services remain independently buildable.
- Per-Agent service processes and a generic repository/RBAC framework add cost
  before the required boundaries work. Shared trusted services and narrow
  business operations keep ownership explicit; user code runs in execution
  environments.
- Single-host downtime is acceptable. Recovery requires durable state and
  fencing, not promises to restore process memory. Capacity, gVisor workload
  compatibility and complete backup restoration require measured evidence.
