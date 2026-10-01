# Managed deployment

English | [简体中文](README.zh.md)

This is the official single-host Linux deployment: one Management dashboard,
shared Runtime/Memory/Calendar/Execution services, PostgreSQL, and an HTTPS
gateway. Only Execution receives the Docker socket. Hosted workloads use
`runsc`; trusted platform services use `runc`.

## Prerequisites

Use Docker Engine with Compose, installed `runsc`, Python 3.11+, GNU tar,
`iptables`, `findmnt`, and `xfsprogs`. Prepare a **dedicated XFS filesystem**
mounted with `prjquota,nosuid,nodev`, owned by root with mode 0700. It must be
separate from the deployment directory. The operator never formats disks.
Provide a TLS certificate/key valid for the public HTTPS hostname, a
Hosted base image, two unused private IPv4 ranges, and an address reachable
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
  --hosted-image juex-hosted:VERSION --public-url https://juex.example.com \
  --host-ip 172.30.0.1 --dns 1.1.1.1 \
  --tls-certificate /secure/fullchain.pem --tls-key /secure/privkey.pem
sudo python3 /opt/juex/operator.py --root /var/lib/juex-management resume
```

The default platform bridge is `172.30.0.0/24`; Hosted allocations use
`172.31.0.0/16`. Override them if they overlap existing routes. Only the HTTPS
port binds publicly to `0.0.0.0`. Execution's host-network endpoints are fenced
by the operator's firewall; database and other RPC ports are not published.
Directly running `docker compose up` bypasses the operator's recovery checks:
use `operator.py up/resume` for service startup.

Issue the first-admin setup link with `docker compose --env-file
/var/lib/juex-management/compose.env -f /var/lib/juex-management/compose.yaml exec
management juex-management bootstrap --email admin@example.com`. It is a secret
one-use URL; deliver it privately. SMTP settings belong in
`secrets/management.env`; copy-link invitations remain available without SMTP.
Model credentials are provisioned through the Management operator CLI. Do not
expose the private deployment files or service certificates to agents.

Ordinary process logs are in `logs/SERVICE/juex-*.log`: seven-day retention,
10 MiB per file and seven files per service. Idle services expire logs hourly.
Docker's duplicate process log storage is disabled. Business receipts, usage
and audit records have separate database retention contracts.

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
service writers and idle Hosted guests. Cancellation and receipts remain
available while draining. Busy, unknown or uncleanly stopped work makes backup
fail and retains maintenance; it is never silently killed. Inspect
`maintenance/report.json` and service logs, settle/cancel the original work
explicitly, then retry or `resume`.

The bundle covers PostgreSQL, Blob, Hosted Workspace/Home, execution journals,
configuration and pinned images. Master keys, service identities and TLS
material are written only to the separate recovery location. File checksums
and paired manifests identify success. Queued work retains its original IDs.
External-device files and process memory are not platform backup contents.

For daily backups, install `juex-backup.service` and `juex-backup.timer` under
`/etc/systemd/system`. Create root-only `/etc/juex/backup.env` containing
`JUEX_DEPLOY_ROOT`, `JUEX_BACKUP_DESTINATION`, and `JUEX_RECOVERY_DESTINATION`,
then run `systemctl daemon-reload` and `systemctl enable --now
juex-backup.timer`. The timer runs at 03:00 local time with up to five minutes
of jitter and catches missed runs. Monitor failures with `systemctl status
juex-backup.service`; a failed backup can intentionally leave maintenance on.

## Whole-system recovery

Use a new deployment directory and an empty dedicated XFS mount. The target
Docker daemon must not contain another `juex` Compose deployment; the operator
refuses to replace it. Retire an old deployment explicitly or use another
host/daemon before recovery.

```sh
sudo python3 /opt/juex/operator.py --root /var/lib/juex-recovered restore \
  --backup /backup/juex-data/juex-BACKUP-ID \
  --recovery /secure-backup/juex-keys/juex-BACKUP-ID \
  --workspace /srv/juex-recovered-workspaces --host-ip 172.30.0.1
```

Restore validates all checksums, loads pinned images, restores the database,
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

`uv run python -m unittest discover -s deploy/managed` checks operator recovery
boundaries. Repository PostgreSQL E2E tests cover draining and offline authority
revocation; actual deployment acceptance additionally requires Linux Docker,
gVisor and XFS backup/restore, including file and quota checks.

Coordinate server and Web versions. Before replacing pinned image IDs, take a
complete backup and retain the old deployment recipe. An incompatible device
protocol requires a device CLI upgrade. The first managed release starts with
new state; selected old material is migrated manually. Do not point the managed
services at an old local Home directory.
