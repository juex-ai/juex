# Management client

> English | [中文](README.zh.md)

This CLI calls Management's public HTTP API. It owns only a private login
credential and the selected tenant; Agent state and execution remain in the
platform. The deployment operator uses `juex-management` for bootstrap and
provider configuration. Native devices use `juex-executor`.

Set `JUEX_SERVER` to the deployment's HTTPS origin. `juex login --email EMAIL
--password-stdin` reads a password from stdin and prints only the account.
Credentials are isolated by origin under the OS configuration directory.
`--session-file` selects an absolute file inside a private directory. HTTP
requires the explicit development flag `--insecure-http`. Logout revokes the
server session and removes the local credential. For a private deployment CA,
set `JUEX_CA_FILE` or `--ca-file` to its PEM file; hostname verification still applies.

`juex tenant list` and `juex tenant use ID` select a membership. A sole membership
is selected automatically. `--owner USER_ID` lets an authorized administrator
manage another member; all permission checks remain on the server.

`juex fleet show`, `juex agent list`, and `juex thread --agent ID list` discover
stable IDs. JSON configuration commands accept `--data-file PATH` or stdin;
the public API schema defines the payloads. Updates use the current version.
`juex request METHOD /tenants/...` exposes other public resource operations
without a second authorization or persistence implementation.

`juex thread --agent ID send THREAD_ID --text-file PATH --request-id REQUEST_ID`
queues durable input. Read results with `thread events THREAD_ID --after
SEQUENCE`. Input, Worker, compaction, and cleanup requests report their request
identity on failure. Reuse that identity after an uncertain response.
Irreversible cleanup additionally requires `--confirm` to repeat the target
Agent ID or Fleet owner ID.
