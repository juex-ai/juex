# Execution

> English | [中文](README.zh.md)

Execution owns environments, external operation identity and persistent results.
An Agent Activation does not own a device connection or the lifetime of its
processes. Location-dependent requests use an explicit environment; process and
connection handles remain bound to that environment and Agent.

Execution stores each Agent's versioned default environment and optional absolute
working directory. The public configuration API is proxied by Management; selecting
an environment requires an existing current grant and never expands it. Missing
or revoked defaults stay unavailable. An empty selection uses deployment-default
provisioning, while an explicit selection suppresses it. Configuration changes
affect new operations only, not retained handles or prepared requests. Host cwd
selection neither changes the executor's HOME nor creates OS isolation.

Execution also owns immutable file objects. PostgreSQL assigns IDs, ownership,
visibility and capacity reservations; the required `--blob-root` is an absolute,
private operator-owned directory, not business identity. Private attachments
belong to one Agent. Explicitly published Fleet artifacts can be read by other
Agents of the same Tenant/User/Fleet. Uploads remain private until both durable
bytes and database publication succeed. Files are limited to 256 MiB, transferred
in 256 KiB chunks with SHA-256 and durable byte cursors. The configurable pool
defaults to 20 GiB and admits at most 65,536 live objects, including empty files.
Deletion releases capacity only after physical cleanup; interrupted purges resume.
Fresh authority fences publication, including after hashing. Dashboard uploads,
downloads and references use this API; large bytes never enter model tool output.

Explicit file transfers publish from an authorized environment, import an
Artifact, or copy through an Artifact to another authorized environment. The
transfer and its first operation are admitted atomically; target admission is
atomic with attaching the published Artifact. Durable request identities and
completion events survive platform restarts and lost responses. Every chunk and
publication checks the original authority and device grant versions, so revoking
and restoring a grant cannot revive old work. Binary requests cannot bypass the
coordinator through the ordinary operation API. Storage exhaustion leaves a
visible durable wait; cancellation and a 24-hour extendable deadline bound it.
Active transfers retain their source Artifact against deletion. Cancellation
records a discard separately from acknowledgment of received source bytes; an
already published destination is never silently undone.

The platform service owns the `execution` PostgreSQL schema. Management and
Runtime call its Kitex API over mutually authenticated TLS; device administration
is restricted to Management's service identity. Current Management authority
is checked before admission and dispatch. Membership/Agent execution epochs
invalidate old work; the separate membership removal epoch prevents previous
device grants from returning when a user rejoins a Tenant.
Runtime admission also carries the original Turn's authority epochs; refreshing
current permissions cannot legitimize a delayed call from before revocation.
Environment and operation changes commit durable outbox facts in the resource
transaction. Only Runtime consumes and acknowledges them, by event identity.

Runtime can durably cancel a prepared request before admission reaches Execution.
Cancellation decisions are scoped to Tenant/User/Fleet/Agent and the original
environment/request identity. Admission checks them in its resource transaction;
timeouts, service restarts and later reauthorization cannot revive that work.
Only Runtime's authenticated RPC identity can reserve such a cancellation; public
Dashboard cancellation requires an existing operation or transfer. Confirmation
means Execution owns delivery, not that an external effect has been undone.

Native devices initiate the encrypted connection. Pairing first binds the
owner's selected Agents/capabilities in the Dashboard, then requires a local
confirmation showing Tenant and account. Browser approval alone creates no
device credential. The device holds the credential independently of human
sessions. Each Tenant/User enrollment has its own private state directory.

`juex-execution serve --blob-root /var/lib/juex/execution/blobs` runs the platform service. `juex-executor --state
/absolute/private/directory pair --server https://platform.example` enrolls a
Linux/macOS device; `run` maintains its foreground connection. An HTTPS reverse
proxy routes `/device/` to Execution and the Dashboard/API to Management.

The same enrollment supports `start`, `stop`, `status`, `logs --tail 200` and
`autostart enable|disable`. Background mode uses Linux `systemd --user` or macOS
`launchd`; autostart is explicitly enabled and runs at OS user login. Linux
requires an available user service manager; the CLI does not enable lingering or
request root access. Disabling autostart leaves the running executor unchanged.
Keep the executable at its installed absolute path. Status verifies the process
incarnation, and stop waits for engine shutdown. Foreground runs remain owned by
their terminal. Private service logs rotate daily or at 2 MiB, retain at most
eight files and expire after seven days; operation recovery records are separate.

The native engine runs as the current Linux/macOS user. Its default working
directory is not a sandbox. Shell and stdio MCP can exercise that user's OS
permissions; capability gates select exposed operations, not independent OS
security boundaries. Hosted isolation is the responsibility of the verified
container backend. Platform model and service credentials are not inherited by
child processes.

Binary transfer uses a private immutable source capture and a separately staged
destination. Chunks retain durable cursors across connection loss; command slots
are used only for capture or final import, not while receiving bytes. Imports
verify the complete size and SHA-256 before atomic publication and never replace
an existing path. File bytes have a separate 1 GiB device reservation pool and
never become command output. Source captures expire only after separate file
acknowledgment; acknowledging the small operation result does not release them.
An executor restart leaves unfinished transfers unknown without replay.

Deployment-managed Host and Hosted environments share one lifecycle. Each Agent
has one durable environment identity, Workspace and Home. Listing environments
does not start executors. Pending operations start them on demand; unfinished
processes, MCP connections and unacknowledged results prevent idle reclamation.
The default idle timeout is five minutes. Lifecycle locks fence dispatch during
reclamation while new operations commit to the durable queue. Rejoining a Tenant preserves the
owned Workspace but never reauthorizes operations from an earlier epoch.

`juex-execution serve --host-config /absolute/operator-config.json` enables Host
provisioning on Linux/macOS. It is mutually exclusive with `--hosted-config`.
The operator supplies an installed executor, private separate workspace/control
roots, a stable deployment identity and enrollment key, and a reachable device
endpoint. HTTPS verifies the configured CA; local HTTP requires explicit opt-in.
Host starts independent native services, so restarting Execution leaves their
processes and journals intact. Per-Agent HOME supplies local package locations
without changing the platform process environment or granting OS isolation.
Only empty unowned roots may be initialized; existing ownership, credentials and
journals must match. Missing provisioned state requires explicit recovery.

Hosted execution uses Docker's API with a pinned Linux image, cgroup v2 and
gVisor `runsc`; missing isolation fails startup. Each environment owns a network
subnet in addition to its persistent Workspace and Home.

The trusted guest control process runs as root; every file tool, shell, PTY and
MCP child runs as UID/GID 1000. Private enrollment and recovery state are outside
that user's access. The root filesystem is read-only, with persistent Workspace
and Home and a bounded temporary directory. The guest receives no platform
database, provider, RPC or Docker credentials. Execution derives its enrollment
credential from a separate 32-byte private key; PostgreSQL stores only its hash.

Workspace and Home share an XFS project quota on a dedicated filesystem,
separate from control state. PostgreSQL owns the immutable storage UUID, project
ID and hard limits; existing data is never silently recreated after provisioning.
Both byte and inode limits survive container replacement. The backend validates
mount identity, enforcement and inheritance before starting any container.
See the [hosted deployment recipe](../../deploy/hosted/README.md).

`juex-execution serve --hosted-config /absolute/operator-config.json` enables the
backend. The operator configuration describes the Docker socket, pinned image,
guest binary, private storage root, IPv4 address pool, DNS resolvers, protected
platform networks and exact LAN endpoint exceptions. The dedicated hosted TLS
listener accepts only hosted connections at `/device/connect`, using the
Execution service certificate; its configured control exception must match
that listener. Pairing and Management APIs are not exposed there.

Network policy is installed before starting a guest. Public IPv4 egress is
allowed; host addresses, metadata, private/reserved networks, platform networks
and other Agent subnets are denied. LAN exceptions name an IPv4 address,
transport and port and cannot override platform/Agent protection. IPv6 is
blocked. A read-only resolver file avoids Docker's embedded loopback DNS,
which is unreachable from gVisor; configured resolvers receive only TCP/UDP 53.
The backend owns only its named bridge rules, never the host's default policy.

A private, exclusively locked journal belongs to one environment enrollment.
An operation commits its identity and request before starting. Retransmission
returns that operation; conflicting reuse is rejected. Restart marks unfinished
operations unknown and never repeats their external effects. Network loss alone
does not cancel a command. Cancellation records intent before signalling; the
observed terminal result remains separate from that request.

The platform queue defaults to a 24-hour offline wait without a Runtime slot.
Cancellation and waiting deadlines are durable even while the device is absent.
Every reconnect queries the original operation; a connection epoch fences old
writers. The first connection binds its journal identity. Replacing that journal
quarantines the enrollment and marks dispatched outcomes unknown, so a fresh
local directory cannot accidentally replay an old command. Revoked credentials
can only receive cancellation and upload results, never new work.

Output is durable before its byte cursor advances. Oversized command output has
an explicit truncation flag. MCP notification storage exhaustion closes the
connection rather than continuing to discard notifications. New operations
reserve result capacity; unacknowledged results and unknown recovery records are
not rotated. Acknowledged settled output expires after the configured retention
period, while operation identity remains to prevent replay. The native and hosted
engines check retention at startup and at least hourly, including while disconnected.
The platform retains acknowledged settled output for seven days and reserves
up to 512 MiB per environment for results; saturation rejects new operations.
Output is transported as bytes, preserving offsets even for non-UTF-8 content.
For Runtime-owned MCP connections, retention also waits for Runtime's durable
consumption cursor. Partial consumption cannot expire the remaining notification
output; the seven-day retention starts after complete consumption and settlement.
Individual MCP notification records are bounded at 1 MiB, including the newline;
oversized records close the connection with a visible error.

The outbound connector negotiates the protocol over TLS and rejects incompatible
versions. Reconnection preserves the engine. Platform grants can restrict the
locally approved Agent/capability ceiling; they cannot expand it. Revocation
stops related queued and running operations when received by the device.

Permanent Agent cleanup cancels owned operations and transfers depending on its
Artifacts, including imports running in another Agent's environment. Their
original identities remain until real settlement; unknown results stay unresolved.
Externally paired devices and explicitly selected user directories are never
erased. Unreceived source captures are explicitly discarded before result
acknowledgment. Managed Host retains its connector and journal until cancellation
is confirmed, then removes its service and marked owned directories. Stopping a
native executor cannot settle unknown external processes. Hosted containers and
networks stop before Workspace, Home and private control data are removed.
Every retry checks ownership; deletion can resume after a database rollback.
Minimal outcome receipts remain after private payloads are erased.

Operation and Artifact audit facts default to 90 days, configurable through `--audit-days`. Retention does not delete operation identities, unknown outcomes, cancellation decisions, file metadata or business output.

Hooks use the Shell capability and the same durable operation journal as other
commands. They accept explicit argv and bounded JSON stdin, retain stdout and
stderr separately, and enforce time and output limits. Exit two is a policy
result, not an execution failure. Output overflow fails explicitly; interrupted
or missing results are never interpreted as permission to continue. A frozen
device authorization version fences both admission and dispatch after revocation.

## Execution-side extensions

Put `juex.extension.json` in an explicit directory on an authorized environment, then use the Agent's Extensions panel to inspect and select resources. Inspection reads only bounded regular manifest/skill files beneath that directory, under the same OS identity as other file operations. It never launches commands or creates extension state.

```json
{
  "manifest_version": 2,
  "name": "example",
  "version": "1.0.0",
  "environment": {"CACHE_DIR": "${JUEX_EXT_DATA_DIR}/cache"},
  "skills": [{"id": "guide", "path": "SKILL.md", "description": "Use the example scripts"}],
  "observables": [{
    "id": "watch",
    "command": ["/bin/sh", "watch.sh"],
    "options": {"parser": {"type": "jsonl", "content_field": "message"}, "batch": {"interval_seconds": 5}}
  }]
}
```

Commands inherit declared defaults and resource-specific overrides; reserved `JUEX_EXT_DIR` and `JUEX_EXT_DATA_DIR` are supplied last. Those two placeholders expand in declared environment values, including PATH. Native data lives beneath the connector's working directory in `.juex-extensions/<environment>/<agent>/<binding>`. Hosted data lives in persistent Agent Home under `.local/share/juex/extensions/<environment>/<agent>/<binding>`; its helper prepares directories and resolves commands as UID 1000. Bindings have stable, separate data directories, but native OS-user permissions are not a sandbox. Configuration removal never deletes native files.

Text observers persist complete UTF-8 chunks without waiting for newline. JSONL lines are limited to 64 KiB; output-pool exhaustion fails visibly. A JSONL attachment field accepts at most 16 objects containing `path`, optional `name` and `media_type`. Paths resolve against the declared command directory and still require current file access. Declared filters use one `contains` or `regex` selector and optional kind/severity overrides. Exit notification is `never` by default, or `always` / `nonzero`. Consumers must acknowledge durable output before its retention interval begins; sleeping or restarting Runtime never restarts an observer process.
