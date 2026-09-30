# Execution

> English | [中文](README.zh.md)

Execution owns environments, external operation identity and persistent results.
An Agent Activation does not own a device connection or the lifetime of its
processes. Location-dependent requests use an explicit environment; process and
connection handles remain bound to that environment and Agent.

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

Output is durable before its byte cursor advances. Oversized command output has
an explicit truncation flag. MCP notification storage exhaustion closes the
connection rather than continuing to discard notifications. New operations
reserve result capacity; unacknowledged results and unknown recovery records are
not rotated. Acknowledged settled output expires after the configured retention
period, while operation identity remains to prevent replay.

The outbound connector negotiates the protocol over TLS and rejects incompatible
versions. Reconnection preserves the engine. Platform grants can restrict the
locally approved Agent/capability ceiling; they cannot expand it. Revocation
stops related queued and running operations when received by the device.
