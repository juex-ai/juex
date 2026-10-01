# JueX Philosophy

> English | [中文](PHILOSOPHY.zh.md)

JueX makes persistent Agent work accessible through a service, with explicit
ownership, inspectable events and independently authorized execution.

- Keep the trusted orchestration loop small. User code runs in execution
  environments; application business policy belongs to its owning service.
- Bind state to stable identities. Machine paths and process lifetimes are
  infrastructure, not account, Fleet or Agent identity.
- Prefer explicit contracts. Durable acceptance, current permission checks,
  operation handles and unknown outcomes must remain visible to clients.
- Preserve one canonical model for providers. Adapters retain model-specific
  reasoning where appropriate without spreading SDK types through the system.
- Use narrow business interfaces. Shared PostgreSQL does not justify shared
  ownership or bypassing another service's authorization and lifecycle.
- Make Web and CLI clients of the same truth. Browser state never confirms
  completion, cancellation or deletion without a service receipt.
- Add abstractions for concrete workflows. The first deployment uses one host,
  PostgreSQL and Compose; additional brokers, identity services and deployment
  modes are introduced only when a product need justifies them.

Shared services reduce idle cost but require fencing and fair scheduling.
Full OS-user device access is useful but is explicitly different from hosted
sandbox isolation. Single-host maintenance downtime is acceptable; unverified
claims of zero data loss, exact external replay or process restoration are not.
