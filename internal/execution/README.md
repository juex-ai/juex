# Execution

> English | [中文](README.zh.md)

Execution owns environments, external operation identity and persistent results.
An Agent Activation does not own a device connection or the lifetime of its
processes. Location-dependent requests use an explicit environment; process and
connection handles remain bound to that environment and Agent.

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

Native devices initiate the encrypted connection. Pairing first binds the
owner's selected Agents/capabilities in the Dashboard, then requires a local
confirmation showing Tenant and account. Browser approval alone creates no
device credential. The device holds the credential independently of human
sessions. Each Tenant/User enrollment has its own private state directory.

`juex-execution serve` runs the platform service. `juex-executor --state
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

Hosted execution uses Docker's API with a pinned Linux image, cgroup v2 and
gVisor `runsc`; missing isolation fails startup. Each Agent has one durable
environment identity, network subnet, Workspace and Home. Listing environments
does not start containers. Pending operations start them on demand; unfinished
processes, MCP connections and unacknowledged results prevent idle reclamation.
The default idle timeout is five minutes. Environment row locks serialize
reclamation with new operation admission. Rejoining a Tenant preserves the
owned Workspace but never reauthorizes operations from an earlier epoch.

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
period, while operation identity remains to prevent replay.
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
