# JueX Architecture

> English | [中文](ARCHITECTURE.zh.md)

The monorepo contains one Go module, a React dashboard and independently runnable
services. [DOMAIN.md](DOMAIN.md) defines meaning; source schemas, generated
contracts and tests define exact fields and routes.

## Service boundaries

```text
Web / management CLI ── HTTPS ── Management
Remote executors ── outbound HTTPS ── Execution
Management / Runtime / Execution / Memory / Calendar ── authenticated Kitex RPC
Each service ── its own PostgreSQL schema
Execution ── Blob storage, Docker/runsc guests and device connections
```

[Management](internal/management/README.md) owns identity, configuration and
fresh authority. [Runtime](internal/managedruntime/README.md) owns orchestration,
durable conversation and model usage. [Execution](internal/execution/README.md)
owns tools, connections and files. [Memory](internal/memory/README.md) and
[Calendar](internal/calendar/README.md) own independent application state.
Services do not query each other's tables to bypass their interfaces.

`internal/app/managed` composes services, provider factories and narrow adapters.
`internal/entrypoints` adapts Cobra, public HTTP and private RPC.
`internal/foundation` holds shared protocol/value types and infrastructure;
`internal/providers` implements the canonical LLM boundary. Service packages do
not import App or each other. Generated RPC clients remain explicit and use
service-specific mutual TLS; network reachability alone grants no authority.

## Execution and recovery

PostgreSQL owns identities, queues, events, leases, usage and reliable outboxes.
Acceptance and durable identity commit together. Lease epochs fence stale workers;
revocation epochs prevent delayed inputs from regaining authority. Recovery queries
original external operation IDs and never guesses whether an unknown action ran.

Runtime admits fair owner queues into bounded shared model slots. Waiting tools
release those slots. Activations expire independently of Agents; Execution keeps
connections and background work alive. Model calls and user code are separate:
only trusted Runtime obtains deployment model credentials.

Execution is the sole service with Docker engine access. Hosted guests use runsc,
UID 1000, explicit resource limits, XFS project quotas and a restricted network.
Workspace/Home survive guest rebuilds. Authorized native devices execute as their
OS user, with durable local operation journals, output cursors and acknowledgments.
Device credentials and platform authority are separately revocable.

Large files use immutable Blob IDs with ownership metadata. Explicit chunked
transfers verify size and SHA-256. User shell paths never act as service discovery
or platform business identity. Secrets are encrypted in Management and injected
only into the authorized connection/process scope.

## Clients and applications

One HTTPS origin serves the dashboard, resource APIs and device transport. The
public API is the common boundary for Web and CLI. The React app uses generated
Management types and shared UI components; server records own durable truth.
Assistant text remains conversation content; tools and reasoning are disclosures.

Memory and Calendar work through their own transactions and outboxes. Their model
jobs use ordinary scoped Runtime Workers and share owner scheduling/usage. An
application credential cannot become a general user session or read arbitrary
conversation history. Disabling an application preserves its business records.

## Deployment

The [operator workflow](deploy/managed/README.md) coordinates the five services,
PostgreSQL and HTTPS gateway. Only Execution gets the engine socket; private RPC
and database ports are fenced. Platform binaries and Web release together.
Device protocol versions are negotiated explicitly.

Maintenance drains admission and checks in-flight operations before stopping
writers. Whole-system backup pairs the database, Blob, Workspace/Home, journals,
recipes and pinned images with separate secret recovery material. Restore remains
closed until authority review is acknowledged. Process memory and remote user
files are outside that recovery boundary.
