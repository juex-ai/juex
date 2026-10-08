# JueX

> English | [中文](README.zh.md)

JueX is a managed Agent platform. One deployment serves multiple tenants and
users through a single Management dashboard. Each user has one Fleet per tenant;
Agents run in a shared durable Runtime and use deployment-managed Host or Hosted
environments, or explicitly authorized remote devices. The default deployment has
one tenant.

## Deploy

Follow the [deployment guide](deploy/managed/README.md) for macOS/Linux Host
or Linux Hosted, including PostgreSQL, a public gateway and persistent storage. The operator
creates the first administrator's one-use setup link and provisions model
credentials. Administrators invite members; users sign in with email/password.

Closing the browser does not stop accepted work. Memory and Calendar are
independent Fleet applications and continue according to their own lifecycle.

## Clients

Release archives contain `juex` and `juex-executor` for Linux/macOS, amd64/arm64.
Separate `juex_platform_*` archives include all five services and the operator.
Download an archive and verify its release checksum, or use the Python 3.11+
installer from a checked-out release:

```sh
python3 scripts/install.py --version VERSION
export JUEX_SERVER=https://juex.example.com
juex login --email user@example.com --password-stdin
juex tenant list
juex fleet show
juex agent list
```

The login command reads the password from standard input. Do not place it in
command arguments. CLI session credentials are private and scoped to the public
origin. See [client CLI](internal/entrypoints/clientcli/README.md) and command help.

Enroll a remote Linux/macOS device with an explicit private state directory:

```sh
juex-executor --state /absolute/private/device-state pair --server https://juex.example.com
juex-executor --state /absolute/private/device-state run
```

Approve the request in the dashboard and confirm the grant locally. Device
execution uses the current OS user's permissions. See
[Execution](internal/execution/README.md) for background mode, grants and recovery.

## Develop

Use the versions pinned in `mise.toml`, then `mise exec -- make build`.
`make build-clients` builds only the two user clients; `make build-go` builds all
service/client binaries using existing embedded Web assets. `make install-local`
installs clients without starting or restarting services.

Verification follows the [local-test skill](.agents/skills/juex-localtest/SKILL.md).
Database tests require an isolated PostgreSQL role that can create databases;
live tests additionally require an explicit private model fixture. Frontend setup
is in [frontend/README.md](frontend/README.md).

## Project map

- [DOMAIN.md](DOMAIN.md): identities, ownership, lifecycles and invariants.
- [ARCHITECTURE.md](ARCHITECTURE.md): service boundaries and persistence.
- [PHILOSOPHY.md](PHILOSOPHY.md): principles and trade-offs.
- [DESIGN.md](DESIGN.md): dashboard interactions and visual contract.
- [Managed platform ADR](docs/adr/0003-managed-agent-platform.md): architecture rationale.
