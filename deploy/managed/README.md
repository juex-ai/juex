# Managed deployment

English | [简体中文](README.zh.md)

The operator manages five platform services, PostgreSQL and a public gateway.
Choose **Host** for macOS/Linux native directories and Shell, or **Hosted** for
Linux gVisor isolation and XFS quotas. The backend is fixed at initialization;
startup never falls back to a different execution mode.

## macOS/Linux Host

Use Python 3.11+, PostgreSQL server/client tools of the same major version,
nginx, OpenSSL and GNU tar (`gtar` on macOS). Install dependencies normally;
PostgreSQL's runtime libraries and shared resources must be present, not only
its executables. Dependency installation is separate from creating JueX state.
No Docker, runsc or XFS is required. Run as the owning non-root user with a
working macOS launchd session or Linux `systemd --user` manager.

Extract and verify the matching `juex_platform_VERSION_OS_ARCH.tar.gz` release,
then run from its directory (use `--bin-dir /absolute/repo/dist` for local builds):

```sh
python3 deploy/managed/operator.py --root /absolute/private/juex-platform install \
  --backend host --workspace /absolute/private/juex-workspaces \
  --bin-dir /absolute/release/bin --ingress http --public-url http://machine.example:8080 \
  --postgres-bin /absolute/postgresql/bin --nginx /absolute/bin/nginx \
  --tar /absolute/bin/gtar --admin-email admin@example.com
```

Local and NetBird installations explicitly select HTTP; no public certificate or
browser CA installation is needed. Use the same HTTP origin and `--insecure-http`
for CLI/device clients. The one-use admin setup link is saved in private
`secrets/bootstrap.json`.

Initialization refuses existing deployment/workspace directories. It creates
its own socket-only PGDATA, nginx prefix, logs and uniquely named OS services;
it never adopts a system PostgreSQL or nginx service. Keep canonical paths short
enough for PostgreSQL's Unix socket. Unfinished initialization retains
`maintenance/install-incomplete` and cannot start; preserve its evidence and
retry in new directories after fixing the reported dependency/configuration.

The public gateway binds `0.0.0.0` for LAN/NetBird clients. Its Management HTTP
and device upstreams bind loopback; RPC requires service mTLS. Host Agents run as the
same OS user: separate Home/Workspace directories are not a security boundary.
Native jobs start in the owning user's service session; this does not promise
macOS operation before login. The environment/PATH captured during installation
must contain the tools the Agents need.

## Linux Hosted

Only Execution receives the Docker socket. Hosted workloads use `runsc`;
trusted platform services use `runc`.

Use Docker Engine with Compose, installed `runsc`, Python 3.11+, GNU tar,
`iptables`, `findmnt`, and `xfsprogs`. Prepare a **dedicated XFS filesystem**
mounted with `prjquota,nosuid,nodev`, owned by root with mode 0700. It must be
separate from the deployment directory. The operator never formats disks.
Provide a Hosted base image, two unused private IPv4 ranges, and an address reachable
from Hosted containers. Hosted DNS must be explicit. Choose storage outside
user-controlled directories.

Build the platform image with `docker build -f deploy/managed/Dockerfile -t
juex-platform:VERSION .`. Base-image arguments permit an operator-controlled
registry mirror. Build the supported Python/Node Hosted image with
`docker build -f deploy/managed/Dockerfile.hosted -t juex-hosted:VERSION .`.
Install user dependencies under persistent Home/Workspace; system packages belong
in a versioned image recipe. Pull PostgreSQL and gateway images before setup;
initialization records immutable local image IDs. Keep those exact images for
rollback. Copy this directory to `/opt/juex`.

```sh
sudo python3 /opt/juex/operator.py --root /var/lib/juex-management init \
  --workspace /srv/juex-workspaces --image juex-platform:VERSION \
  --hosted-image juex-hosted:VERSION --ingress http \
  --public-url http://machine.example:8080 --host-ip 172.30.0.1 --dns 1.1.1.1
sudo python3 /opt/juex/operator.py --root /var/lib/juex-management resume
```

The default platform bridge is `172.30.0.0/24`; Hosted allocations use
`172.31.0.0/16`. Override them if they overlap existing routes. Only the public gateway
port binds publicly to `0.0.0.0`. Execution's host-network endpoints are fenced
by the operator's firewall; database and other RPC ports are not published.
Directly running `docker compose up` bypasses the operator's recovery checks:
use `operator.py up/resume` for service startup.

The gateway uses platform address `.11`. Management and Execution trust only
that proxy for `X-Real-IP`, which both gateway routes overwrite with the actual
client address so authentication and device rate limits remain per client.
An external production proxy uses the explicit ingress policy below; the service
trust remains limited to nginx. Restore rebinds it to the platform subnet.

Issue the first-admin setup link with `sudo python3 /opt/juex/operator.py --root
/var/lib/juex-management manage bootstrap --email admin@example.com`. The operator
applies the deployment's transport policy to every Management command. The link
is a secret one-use URL; deliver it privately. Copy-link invitations remain
available without SMTP.
Model credentials are provisioned through the Management operator CLI. Do not
expose the private deployment files or service certificates to agents.

To configure SMTP, set the password in a temporary exported
`JUEX_SMTP_CREDENTIAL` variable without saving it in shell history, then run:

```sh
sudo --preserve-env=JUEX_SMTP_CREDENTIAL python3 /opt/juex/operator.py \
  --root /var/lib/juex-management manage smtp seal --address smtp.example.com:587 \
  --from juex@example.com --username mailer
unset JUEX_SMTP_CREDENTIAL
```

Save the emitted `JUEX_SMTP_CONFIG=...` line in `secrets/management.env`,
replacing any existing value, then use `operator.py up` to recreate Management
with the new setting. This encrypts the entire SMTP configuration with the
deployment master key; never persist the password input. Rerun to replace
credentials, or remove the setting and recreate Management to disable mail.
Existing queued mail remains durable. Keep the master key in the separate
recovery archive described below; restore requires the matching key.

Ordinary process logs are in `logs/SERVICE/juex-*.log`: seven-day retention,
10 MiB per file and seven files per service. Idle services expire logs hourly.
Docker's duplicate process log storage is disabled. Business receipts, usage
and audit records have separate database retention contracts.

## Production HTTPS

Use `--ingress proxy --public-url https://juex.example.com --listen-port 8080
--proxy-cidr 127.0.0.1/32` when Caddy runs on the same Host machine. Caddy owns
public certificates and renewal; the private nginx hop uses HTTP. Session cookies
remain Secure and origin checks use the HTTPS public URL. nginx must include
`http_realip_module`. A minimal Caddyfile is:

```caddyfile
juex.example.com {
  reverse_proxy 127.0.0.1:8080 {
    header_up X-Real-IP {remote_host}
  }
}
```

Allow only the proxy's actual TCP source addresses. In Hosted, Docker port
forwarding can change that source: inspect the gateway peer and pass its exact
address with `--proxy-cidr`; do not trust an entire client subnet. nginx checks
the original peer before accepting Caddy's client address, then overwrites both
service routes' identity headers. The gateway must not be directly accessible
from untrusted networks. Caddy must be running and its public origin reachable
before JueX `install` or `resume` can pass its public health check.

For TLS directly in nginx, use `--ingress https` and provide `--tls-certificate`
and `--tls-key`; a private issuer also needs `--tls-ca`. Host additionally supports
`--local-tls`, which generates a deployment CA without changing OS/browser trust.
Clients must explicitly trust that CA. Internal service mTLS and Hosted guest TLS
are independent of all three public ingress modes. Backup/restore preserves the
selected ingress; external Caddy configuration and certificates remain the
operator's responsibility. Changing an enrolled Host origin requires an explicit
device migration, not editing the origin in place.

## Shared operation

Use `operator.py --root ROOT status` and `logs --service SERVICE` for read-only
inspection. `down` drains work and stops owned services while preserving data;
`resume` starts them and reopens admission after health checks. `up` starts
services without clearing maintenance. Run Host commands as the owning user;
Hosted requires Linux root. The installed Host entry point is
`ROOT/operator/operator.py` and does not depend on a source checkout.

`manage` runs the existing Management operator CLI with deployment settings,
for example `manage model put --help` or `manage smtp seal --help`.
`JUEX_MODEL_API_KEY` and `JUEX_SMTP_CREDENTIAL` are passed only when provided in
the operator's environment. After changing service environment configuration,
use `down` then `resume` so the running process receives it.

## Consistent backup

```sh
sudo python3 /opt/juex/operator.py --root /var/lib/juex-management backup \
  --destination /backup/juex-data --recovery-destination /secure-backup/juex-keys
```

Both destinations must be private (0700), separate from the deployment and
workspace and from each other. Use independent storage appropriate to the
failure being protected against. The default keeps seven **complete pairs**;
a data archive without its matching secret archive cannot be restored.

Backup marks admission draining, waits for admitted model/application work,
checks external operations and Hosted user processes, then gracefully stops
service writers and owned Host executors or idle Hosted guests. Cancellation and receipts remain
available while draining. Busy, unknown or uncleanly stopped work makes backup
fail and retains maintenance; it is never silently killed. Inspect
`maintenance/report.json` and service logs, settle/cancel the original work
explicitly, then retry or `resume`.
Receipts from a confirmed permanently destroyed Hosted environment remain in the
review inventory with their original outcomes, but no longer block backup.

The bundle covers PostgreSQL, Blob, managed Workspace/Home, execution journals,
configuration and the pinned native release or container images. Master keys, service identities and TLS
material are written only to the separate recovery location. File checksums
and paired manifests identify success. Queued work retains its original IDs.
External-device files and process memory are not platform backup contents. Host
backup cannot fence unrelated OS processes: do not edit its Home/Workspace from
outside JueX during backup. A timed-out native stop retains a stop marker and
maintenance; it does not force-kill work. Explicit resume clears that marker.

For Linux Hosted daily backups, install `juex-backup.service` and `juex-backup.timer` under
`/etc/systemd/system`. Create root-only `/etc/juex/backup.env` containing
`JUEX_DEPLOY_ROOT`, `JUEX_BACKUP_DESTINATION`, and `JUEX_RECOVERY_DESTINATION`,
then run `systemctl daemon-reload` and `systemctl enable --now
juex-backup.timer`. The timer runs at 03:00 local time with up to five minutes
of jitter and catches missed runs. Monitor failures with `systemctl status
juex-backup.service`; a failed backup can intentionally leave maintenance on.

## Whole-system recovery

For Host, stop the source with `down`, preserve its directories separately, and
restore to the same now-absent canonical deployment/workspace paths on the same
OS and architecture. Keep the PostgreSQL major version unchanged; major upgrades
require a separate migration. Host restore accepts dependency path overrides.

For Hosted, use a new deployment directory and an empty dedicated XFS mount. The target
Docker daemon must not contain another `juex` Compose deployment; the operator
refuses to replace it. Retire an old deployment explicitly or use another
host/daemon before recovery.

```sh
sudo python3 /opt/juex/operator.py --root /var/lib/juex-recovered restore \
  --backup /backup/juex-data/juex-BACKUP-ID \
  --recovery /secure-backup/juex-keys/juex-BACKUP-ID \
  --workspace /srv/juex-recovered-workspaces --host-ip 172.30.0.1
```

Hosted restore validates all checksums, loads pinned images, restores the database,
initializes XFS project quotas **before** extracting Workspace/Home, then
checks the complete trees and limits. It preserves operator SMTP, LAN allowlist
and resource settings; only machine-specific bindings change. Ordinary runtime
logs start fresh. An incomplete restore stays closed and must not be resumed.

Only PostgreSQL runs after a successful restore. Inspect
`maintenance/recovery-review.json`, including membership and device authorities
and queued work. Revoke stale access before opening the public gateway:

```sh
sudo python3 /opt/juex/operator.py --root /var/lib/juex-recovered recovery-revoke \
  --tenant TENANT-ID --user USER-ID
sudo python3 /opt/juex/operator.py --root /var/lib/juex-recovered recovery-revoke \
  --device DEVICE-ID
sudo python3 /opt/juex/operator.py --root /var/lib/juex-recovered resume \
  --acknowledge-recovery REVIEW-SHA256
```

Membership revocation suspends access without deleting data and preserves the
last active administrator. Each revocation refreshes the required review hash. If a revocation fails or
is interrupted, run `report` to inspect the offline database again and obtain
a fresh hash before resuming.
`up` cannot bypass this acknowledgment. Reopening starts services, checks their
mTLS health, then releases the admission gate; it does not recreate lost remote
process memory or replay unknown commands.

## Verification and upgrades

Use the repository's [local-test skill](../../.agents/skills/juex-localtest/SKILL.md)
for verification. Real Host acceptance additionally exercises the owning OS
service manager and public gateway; Hosted acceptance needs Linux Docker,
gVisor and quota-backed XFS. Unit tests or compilation do not prove either.

Run the new release's operator with `upgrade --bin-dir /absolute/new-release/bin`
for Host, or `upgrade --image IMAGE --hosted-image IMAGE` for Hosted; both require
`--destination` and `--recovery-destination` for a complete pre-upgrade backup.
Upgrades drain work, stop owned executors, retain the old release, pin the new
binaries/operator assets or images, then resume. Failure leaves admission closed.
An old binary is not a safe schema downgrade: use the matching paired backup for
rollback. Coordinate platform/Web and device protocol versions.

The first managed release starts with new state. Import selected legacy material
through an explicit migration; never point managed services at an old local Home.
