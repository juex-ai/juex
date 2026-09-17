# Independent Fleet Services

> English | [中文](README.zh.md)

Fleet manages service processes and discovery. Features own business APIs and
storage; App composes them. Ordinary Agent Modules remain process-local.
`../service` owns OS service registration and has a separate responsibility.

Only the owning `JUEX_HOME/juex.yaml` and its imports define `fleet.services`.
A custom Home never inherits default-Home service definitions. Workspace and
Agent layers cannot define Fleet settings. Restart Fleet management to reload
definitions, then restart an affected service to apply its command or opaque
`config`. No implementation is implicitly enabled.

```yaml
fleet:
  services:
    example:
      mode: managed
      enabled: true
      command: [/absolute/path/to/service-implementation]
      network: unix
```

The executable implements the lease/readiness and typed control contract.
App acquires `services.Acquire` before opening business storage and holds it
through graceful drain. It recovers storage, opens the listener, registers
business RPC alongside `serviceendpoint.ControlServer`, then writes the
candidate through `Lease.Ready`. Fleet passes Home/Fleet/service/instance in
the launch environment. Memory business composition is delivered separately.

Use `juex fleet services list`, `status`, `start`, `stop`, `restart` and `logs`.
Commands emit JSON except logs; HTTP exposes the same operations under
`/api/services`. Management startup does not wait for optional services;
management shutdown leaves them running. There is no service Web screen.

## Ownership and recovery

- `fleet.json` stores stable Fleet identity. Each launch gets a new instance.
  PID and process start identity are diagnostic; stop uses exact-identity RPC
  and confirms writer lease release.
- Explicit stop persists before RPC and survives manager restart. Enablement
  is separate: disabling stops managed writers, incomplete shutdown remains
  visible, and re-enabling respects an explicit stop. Automatic recovery has
  three persisted launch attempts until explicit start/restart resets them.
  Reconciliation runs every five seconds.
- A lifecycle lock serializes management. A service holds its writer lease
  throughout its lifetime. Intent replacement holds the writer lock; children
  reread intent after acquiring their lease, fencing delayed old launches before
  storage access. An occupied lease without verified readiness never authorizes
  another writer.
- After recovery, a service writes an instance-specific candidate. Fleet checks
  Fleet/service/instance over RPC before atomic public discovery publication.
  Manager restart can adopt the candidate after a pre-publication crash.
  Cleanup checks the inspected instance.

Persistent state is under `services/<id>/`; discovery is
`run/services/<id>.json` and contains no business data. Local defaults use Unix
sockets (TCP on Windows). Explicit TCP supports port zero and publishes the
actual address. Long Unix paths use a hashed Fleet-scoped temporary path.

External definitions require `mode: external`, `network` and `address` and
forbid command/config. The endpoint must identify the owning Fleet/service.
Fleet verifies and publishes its current instance but never starts or stops it.
Disabling removes local discovery; logs remain with the external deployment.

## Clients

Inject an explicit Home/Fleet `serviceendpoint.Resolver`; never search another
Fleet. Construction works offline and calls can resolve again after failure
or replacement. Shared Kitex typed Thrift control has a two-second call bound,
checks identity on every request/response and never retries mutations blindly.
Business clients must validate the same expected identity on actual business
RPC requests; a separate probe cannot verify a pooled business connection.

Identity checks prevent accidental misrouting, not impersonation. The OS user
and network are trusted. Services own accepted work, idempotency and graceful
drain. Fleet supplies no universal queue, business proxy or Module host.

Explicit Unix socket paths are never unlinked before binding, because another
Fleet could own them. A stale fixed socket after an ungraceful exit requires
inspection; default instance-specific sockets avoid that reuse ambiguity.
