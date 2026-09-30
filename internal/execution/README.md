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

The outbound connector negotiates the protocol over TLS and rejects incompatible
versions. Reconnection preserves the engine. Platform grants can restrict the
locally approved Agent/capability ceiling; they cannot expand it. Revocation
stops related queued and running operations when received by the device.
