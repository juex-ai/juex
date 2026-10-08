#!/usr/bin/env python3
"""Operate a single-host JueX platform using native Host or Linux Hosted execution."""

import sys
sys.dont_write_bytecode = True

# This CLI's historical filename must not shadow Python's standard operator
# module when started by a minimal OS service environment.
if __name__ == "__main__":
    _script_directory = sys.path.pop(0)
    import operator
    sys.path.insert(0, _script_directory)
    del _script_directory

import argparse
import contextlib
import datetime
import fcntl
import json
import os
from pathlib import Path
import secrets
import shutil
import subprocess
import time

import host
import hosted
from common import (SERVICES, absolute, separate, durable, sync_directory, write_json, digest, verified_bundle)


def backend(config):
    value = config.get("backend", "hosted")
    if value not in ("host", "hosted"):
        raise ValueError("unknown deployment backend")
    return host if value == "host" else hosted


@contextlib.contextmanager
def exclusive(config, timeout=60):
    path = Path(config["root"]) / "maintenance/admission.lock"
    with open(path, "rb") as f:
        deadline = time.monotonic() + timeout
        while True:
            try:
                fcntl.flock(f, fcntl.LOCK_EX | fcntl.LOCK_NB)
                break
            except BlockingIOError:
                if time.monotonic() >= deadline:
                    raise RuntimeError("existing work is still active; maintenance retained, no work was killed")
                time.sleep(0.2)
        yield f.fileno()


def drain(config, reason):
    durable(Path(config["root"]) / "maintenance/draining", reason + "\n")


def inventory(config, offline=False):
    result = backend(config).management(config, "maintenance-report", *(["--offline"] if offline else []), check=False)
    if not result.stdout:
        raise RuntimeError("maintenance report unavailable: " + result.stderr.decode(errors="replace")[-2000:])
    report = json.loads(result.stdout)
    write_json(Path(config["root"]) / "maintenance/report.json", report)
    if not report["ready"]:
        raise RuntimeError("maintenance not ready; inspect maintenance/report.json; no work was cancelled")
    if result.returncode:
        raise RuntimeError("maintenance report failed")
    return report


def up(config, reviewed=False):
    root = Path(config["root"])
    if (root / "maintenance/recovery-required.json").exists() and not reviewed:
        raise ValueError("recovery authority review required before starting services or opening the gateway")
    if (root / "maintenance/restore-incomplete").exists():
        raise ValueError("restore is incomplete")
    backend(config).up(config, reviewed=reviewed)


def resume(config, acknowledgment):
    root = Path(config["root"])
    if (root / "maintenance/restore-incomplete").exists():
        raise ValueError("restore did not complete; admission remains closed")
    recovery = root / "maintenance/recovery-required.json"
    if recovery.exists():
        expected = json.loads(recovery.read_text())["review_sha256"]
        if expected == "pending-review" or acknowledgment != expected:
            raise ValueError("review recovered queues and authorities, then pass --acknowledge-recovery " + expected)
    up(config, reviewed=True)
    backend(config).healthy(config)
    # Starting services does not release queued execution while draining exists.
    (root / "maintenance/draining").unlink(missing_ok=True)
    recovery.unlink(missing_ok=True)
    sync_directory(root / "maintenance")
    print(json.dumps({"status": "running"}))


def prune_successful(destination, recovery, keep):
    completed = []
    for path in destination.glob("juex-*"):
        if path.is_dir() and not path.is_symlink() and (path / "manifest.json").is_file():
            value = json.loads((path / "manifest.json").read_text())
            if value.get("format") == 1 and value.get("backup_id") == path.name and value.get("complete") is True:
                peer = recovery / path.name
                if peer.is_dir() and not peer.is_symlink() and (peer / "manifest.json").is_file():
                    other = json.loads((peer / "manifest.json").read_text())
                    if (other.get("format") == 1 and other.get("complete") is True
                            and other.get("backup_id") == path.name
                            and other.get("files", {}).get("secrets.tar") == value.get("recovery_sha256")):
                        completed.append(path)
    for path in sorted(completed)[:-keep]:
        shutil.rmtree(path)
        shutil.rmtree(recovery / path.name)
    sync_directory(destination)
    sync_directory(recovery)


def backup(config, args):
    root, workspace = Path(config["root"]), Path(config["workspace"])
    destination, recovery = absolute(args.destination), absolute(args.recovery_destination)
    separate(root, workspace, destination, recovery)
    if args.keep < 1:
        raise ValueError("retain at least one successful backup")
    for path in (destination, recovery):
        path.mkdir(mode=0o700, parents=True, exist_ok=True)
        if path.stat().st_mode & 0o077:
            raise ValueError("backup destinations must be private (0700)")
    identity = "juex-" + datetime.datetime.now(datetime.UTC).strftime("%Y%m%dT%H%M%SZ-") + secrets.token_hex(4)
    data_dir, key_dir = destination / (".incomplete-" + identity), recovery / (".incomplete-" + identity)
    originally_draining = (root / "maintenance/draining").exists()
    if (root / "maintenance/restore-incomplete").exists():
        raise ValueError("cannot back up an incomplete restore")
    drain(config, "backup " + identity)
    with exclusive(config, args.drain_timeout) as lock_fd:
        inventory(config)
        backend(config).quiesce(config, lock_fd)
        report = inventory(config, offline=True)
        data_dir.mkdir(mode=0o700)
        key_dir.mkdir(mode=0o700)
        backend(config).snapshot(config, data_dir, key_dir)
        write_json(data_dir / "review.json", report)
        manifest = {"format": 1, "backup_id": identity, "complete": True,
                    "backend": config.get("backend", "hosted"),
                    "storage_identity": config["storage_identity"], "created_at": datetime.datetime.now(datetime.UTC).isoformat(),
                    "files": {p.name: digest(p) for p in sorted(data_dir.iterdir())},
                    "recovery_sha256": digest(key_dir / "secrets.tar")}
        write_json(key_dir / "manifest.json", {"format": 1, "backup_id": identity, "complete": True,
                                               "files": {"secrets.tar": manifest["recovery_sha256"]}})
        write_json(data_dir / "manifest.json", manifest)
        # A backup is successful only when both locations durably exist.
        key_dir.rename(recovery / identity)
        sync_directory(recovery)
        data_dir.rename(destination / identity)
        sync_directory(destination)
    if not originally_draining:
        resume(config, "")
    prune_successful(destination, recovery, args.keep)
    print(json.dumps({"status": "backup_complete", "backup": str(destination / identity),
                      "recovery_material": str(recovery / identity)}))


def restore(args):
    source, manifest = verified_bundle(args.backup)
    recovery, key_manifest = verified_bundle(args.recovery)
    if key_manifest["backup_id"] != manifest["backup_id"] or key_manifest["files"]["secrets.tar"] != manifest["recovery_sha256"]:
        raise ValueError("recovery material does not match this backup")
    config = backend(manifest).restore(args, source, manifest, recovery)
    root = Path(config["root"])
    report = inventory(config, offline=True)
    review_path = root / "maintenance/recovery-review.json"
    write_json(review_path, report)
    fingerprint = digest(review_path)
    write_json(root / "maintenance/recovery-required.json", {"backup_id": manifest["backup_id"], "review_sha256": fingerprint})
    (root / "maintenance/restore-incomplete").unlink()
    sync_directory(root / "maintenance")
    print(json.dumps({"status": "restored_in_maintenance", "review": str(review_path), "review_sha256": fingerprint}))


def revoke_recovery(config, args):
    root = Path(config["root"])
    required = root / "maintenance/recovery-required.json"
    if not required.exists():
        raise ValueError("offline recovery revocation requires a restored deployment awaiting review")
    value = json.loads(required.read_text())
    # Invalidate the old acknowledgment even if the command fails or this
    # process crashes after the database commits.
    value["review_sha256"] = "pending-review"
    write_json(required, value)
    backend(config).management(config, "recovery-revoke",
            "--tenant", args.tenant, "--user", args.user, "--device", args.device)
    fingerprint = review_recovery(config)
    print(json.dumps({"status": "authority_revoked", "review_sha256": fingerprint}))


def review_recovery(config):
    root = Path(config["root"])
    required = root / "maintenance/recovery-required.json"
    value = json.loads(required.read_text())
    report = inventory(config, offline=True)
    review = root / "maintenance/recovery-review.json"
    write_json(review, report)
    value["review_sha256"] = digest(review)
    write_json(required, value)
    return value["review_sha256"]


def require_backend_user(selected):
    if selected == "hosted" and (host.system() != "linux" or os.geteuid() != 0):
        raise ValueError("Hosted operator requires Linux root for Docker, firewall and XFS ownership")
    if selected == "host" and os.geteuid() == 0:
        raise ValueError("Host operator uses the owning non-root OS user")


def logs(config, service, tail):
    from collections import deque
    if service not in (*SERVICES, "postgres", "gateway") or not 1 <= tail <= 2000:
        raise ValueError("select a platform service and 1-2000 lines")
    lines = deque(maxlen=tail)
    directory = Path(config["root"]) / "logs" / service
    for path in sorted(directory.glob("juex-*.log")):
        if path.is_symlink() or not path.is_file():
            raise ValueError("unexpected service log path")
        with path.open(errors="replace") as stream:
            lines.extend(stream)
    print("".join(lines), end="")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", required=True, help="Absolute private deployment directory")
    commands = parser.add_subparsers(dest="command", required=True)
    def native_options(command):
        command.add_argument("--postgres-bin", help="PostgreSQL tool directory")
        command.add_argument("--nginx", help="nginx executable")
        command.add_argument("--tar", help="GNU tar executable (gtar on macOS)")
    for verb in ("init", "install"):
        init = commands.add_parser(verb, help="Initialize new owned state" if verb == "init" else "Initialize, start and optionally bootstrap the platform")
        init.add_argument("--backend", choices=("host", "hosted"), default="hosted")
        for name in ("workspace", "image", "hosted-image", "host-ip", "tls-certificate", "tls-key", "tls-ca", "bin-dir"):
            init.add_argument("--" + name)
        init.add_argument("--public-url", required=True)
        init.add_argument("--ingress", choices=("http", "https", "proxy"), default="https",
                          help="Explicit local HTTP, native TLS, or HTTPS terminated by an external proxy")
        init.add_argument("--proxy-cidr", action="append", default=[],
                          help="Trusted external TLS proxy address/CIDR; repeat for each proxy")
        init.add_argument("--local-tls", action="store_true", help="Generate a deployment CA; does not change system trust")
        init.add_argument("--dns", action="append")
        init.add_argument("--listen-port", type=int)
        init.add_argument("--docker-socket", default="/var/run/docker.sock")
        init.add_argument("--platform-prefix", default="172.30.0")
        init.add_argument("--hosted-pool", default="172.31.0.0/16")
        init.add_argument("--postgres-image", default="postgres:18-bookworm")
        init.add_argument("--gateway-image", default="nginx:1.28-bookworm")
        init.add_argument("--http-port", type=int, default=8680)
        init.add_argument("--device-port", type=int, default=8683)
        init.add_argument("--rpc-base", type=int, default=8781)
        init.add_argument("--active-threads", type=int, default=4)
        init.add_argument("--admin-email")
        init.add_argument("--tenant", default="Default")
        native_options(init)
    for verb in ("up", "down", "drain", "report", "status"):
        commands.add_parser(verb)
    res = commands.add_parser("resume", help="Reopen admission after any required recovery review")
    res.add_argument("--acknowledge-recovery", default="")
    log = commands.add_parser("logs", help="Read process logs without pruning or changing state")
    log.add_argument("--service", required=True, choices=(*SERVICES, "postgres", "gateway"))
    log.add_argument("--tail", type=int, default=200)
    manage = commands.add_parser("manage", help="Run the existing operator Management CLI with deployment configuration")
    manage.add_argument("arguments", nargs=argparse.REMAINDER)
    revoke = commands.add_parser("recovery-revoke")
    for name in ("tenant", "user", "device"):
        revoke.add_argument("--" + name, default="")
    for verb in ("backup", "upgrade"):
        command = commands.add_parser(verb)
        command.add_argument("--destination", required=True)
        command.add_argument("--recovery-destination", required=True)
        command.add_argument("--keep", type=int, default=7)
        command.add_argument("--drain-timeout", type=int, default=60)
        if verb == "upgrade":
            command.add_argument("--bin-dir")
            command.add_argument("--image")
            command.add_argument("--hosted-image")
    rec = commands.add_parser("restore", help="Restore empty owned state; admission remains closed")
    rec.add_argument("--backup", required=True)
    rec.add_argument("--recovery", required=True)
    rec.add_argument("--workspace")
    rec.add_argument("--host-ip")
    rec.add_argument("--docker-socket", default="/var/run/docker.sock")
    native_options(rec)
    worker = commands.add_parser("run-service", help=argparse.SUPPRESS)
    worker.add_argument("--service", required=True, choices=(*SERVICES, "postgres", "gateway"))
    args = parser.parse_args()
    os.umask(0o077)
    if args.command in ("init", "install"):
        require_backend_user(args.backend)
        if args.backend == "hosted":
            for field in ("workspace", "image", "hosted_image", "host_ip", "dns"):
                if not getattr(args, field):
                    parser.error("Hosted requires --" + field.replace("_", "-"))
        backend({"backend": args.backend}).initialize(args)
        if args.command == "init":
            return
    if args.command == "restore":
        _, manifest = verified_bundle(args.backup)
        require_backend_user(manifest.get("backend", "hosted"))
        if manifest.get("backend", "hosted") == "hosted" and (not args.workspace or not args.host_ip):
            parser.error("Hosted restore requires --workspace and --host-ip")
        restore(args)
        return
    root = absolute(args.root)
    config = json.loads((root / "deployment.json").read_text())
    if config["root"] != str(root):
        raise ValueError("deployment path changed; use restore")
    selected = backend(config)
    require_backend_user(config.get("backend", "hosted"))
    if args.command == "status":
        print(json.dumps(selected.status(config)))
        return
    if args.command == "logs":
        logs(config, args.service, args.tail)
        return
    if args.command == "run-service":
        if selected is not host:
            raise ValueError("native service command requires a Host deployment")
        import processes
        raise SystemExit(processes.serve(config, args.service, host.service_command(config, args.service), host.service_env(config, args.service)))
    with open(root / "maintenance/operator.lock", "a+b") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        if args.command == "up":
            up(config)
        elif args.command == "drain":
            drain(config, "operator maintenance")
            with exclusive(config):
                inventory(config)
        elif args.command == "report":
            if (root / "maintenance/recovery-required.json").exists():
                print(json.dumps({"review_sha256": review_recovery(config)}))
            else:
                inventory(config)
        elif args.command in ("resume", "install"):
            resume(config, args.acknowledge_recovery if args.command == "resume" else "")
            if args.command == "install" and args.admin_email:
                result = selected.management(config, "bootstrap", "--email", args.admin_email, "--tenant", args.tenant)
                durable(root / "secrets/bootstrap.json", result.stdout)
                print(json.dumps({"bootstrap_file": str(root / "secrets/bootstrap.json")}))
        elif args.command == "manage":
            values = args.arguments[1:] if args.arguments[:1] == ["--"] else args.arguments
            if not values:
                parser.error("manage requires a Management command")
            sys.stdout.buffer.write(selected.management(config, *values).stdout)
        elif args.command == "recovery-revoke":
            revoke_recovery(config, args)
        elif args.command == "backup":
            backup(config, args)
        elif args.command == "down":
            drain(config, "operator shutdown")
            with exclusive(config) as lock_fd:
                inventory(config)
                selected.quiesce(config, lock_fd)
                inventory(config, offline=True)
                selected.stop_database(config)
            print(json.dumps({"status": "stopped", "data_preserved": True}))
        elif args.command == "upgrade":
            drain(config, "operator upgrade")
            backup(config, args)
            config = selected.upgrade(config, args)
            resume(config, "")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, RuntimeError, OSError, subprocess.TimeoutExpired) as error:
        raise SystemExit(str(error)) from None
