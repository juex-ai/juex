#!/usr/bin/env python3
"""Single-host JueX deployment operations. Run with uv run operator.py --help."""

import argparse
import contextlib
import datetime
import fcntl
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import secrets
import shutil
import subprocess
import time
import urllib.parse


SERVICES = ("management", "runtime", "memory", "calendar", "execution")
HERE = Path(__file__).resolve().parent


def run(args, *, output=None, input_file=None, timeout=120, check=True):
    result = subprocess.run([str(x) for x in args], stdin=input_file,
                            stdout=output if output is not None else subprocess.PIPE,
                            stderr=subprocess.PIPE, timeout=timeout)
    if check and result.returncode:
        # Arguments/environment may include operator secrets. Never echo them.
        raise RuntimeError(f"{Path(args[0]).name} failed ({result.returncode}): "
                           + result.stderr.decode(errors="replace")[-4000:])
    return result


def durable(path, data, mode=0o600):
    path = Path(path)
    tmp = path.with_name(path.name + ".new")
    with os.fdopen(os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_EXCL, mode), "wb") as f:
        f.write(data.encode() if isinstance(data, str) else data)
        f.flush()
        os.fsync(f.fileno())
    os.replace(tmp, path)
    sync_directory(path.parent)


def sync_directory(path):
    fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def write_json(path, value):
    durable(path, json.dumps(value, indent=2, sort_keys=True) + "\n")


def digest(path):
    with open(path, "rb") as f:
        return hashlib.file_digest(f, "sha256").hexdigest()


def absolute(value):
    path = Path(value)
    if not path.is_absolute() or path.resolve() != path or any(c in str(path) for c in "\n\r\"'$"):
        raise ValueError("paths must be absolute, canonical, and contain no shell/Compose substitutions")
    return path


def separate(*paths):
    for i, left in enumerate(paths):
        for right in paths[i + 1:]:
            if left == right or left in right.parents or right in left.parents:
                raise ValueError("deployment, workspace and backup destinations must not overlap")


def docker(config, *args, **kwargs):
    return run(["docker", "--host", "unix://" + config["socket"], *args], **kwargs)


def compose(config, *args, **kwargs):
    return docker(config, "compose", "--env-file", Path(config["root"]) / "compose.env",
                  "-f", Path(config["root"]) / "compose.yaml", *args, **kwargs)


def mount_identity(path):
    data = json.loads(run(["findmnt", "--json", "--mountpoint", path,
                           "--output", "UUID,FSTYPE,TARGET,OPTIONS"]).stdout)
    mounts = data["filesystems"]
    if len(mounts) != 1 or mounts[0]["fstype"] != "xfs" or mounts[0]["target"] != str(path):
        raise ValueError("Workspace must be a dedicated XFS mount")
    if not {"prjquota", "pquota"}.intersection(mounts[0]["options"].split(",")):
        raise ValueError("Workspace requires XFS project quota enforcement")
    return mounts[0]["uuid"]


def mount_device(path):
    source = run(["findmnt", "--noheadings", "--mountpoint", path, "--output", "SOURCE"]).stdout.decode().strip()
    device = Path(source).resolve()
    if not device.is_block_device():
        raise ValueError("Workspace must have a visible dedicated block device")
    return str(device)


def environment(path, values):
    for value in values.values():
        if "\n" in str(value) or "\r" in str(value) or "$" in str(value):
            raise ValueError("invalid deployment environment value")
    durable(path, "".join(f"{key}={value}\n" for key, value in values.items()))


def rebind_environment(path, values):
    # Preserve operator-added settings verbatim; replace only known bindings.
    kept = [line for line in path.read_text().splitlines(keepends=True)
            if line.split("=", 1)[0] not in values]
    durable(path, "".join(kept) + "".join(f"{key}={value}\n" for key, value in values.items()))


def render(config, password, master_key, preserve=False):
    root = Path(config["root"])
    prefix = config["platform_prefix"]
    base = {"JUEX_DATABASE_URL": f"postgres://juex:{password}@postgres:5432/juex?sslmode=disable",
            "JUEX_SERVICE_CERTS": "/run/juex/certificates", "JUEX_MAINTENANCE_DIR": "/run/juex/maintenance",
            "JUEX_MANAGEMENT_RPC": "management:8781", "JUEX_RUNTIME_RPC": "runtime:8782",
            "JUEX_EXECUTION_RPC": "execution:8783", "JUEX_MEMORY_RPC": "memory:8784",
            "JUEX_CALENDAR_RPC": "calendar:8785"}
    for service in SERVICES:
        env = dict(base)
        if service == "management":
            env.update(JUEX_PUBLIC_URL=config["public_url"], JUEX_MASTER_KEY=master_key)
        if service in ("management", "execution"):
            env["JUEX_TRUSTED_PROXIES"] = prefix + ".11"
        if service == "execution":
            env.update(JUEX_DATABASE_URL=f"postgres://juex:{password}@{prefix}.2:5432/juex?sslmode=disable",
                       JUEX_MANAGEMENT_RPC=prefix + ".10:8781", JUEX_BLOB_ROOT=str(root / "blobs"),
                       JUEX_HOSTED_CONFIG=str(root / "hosted.json"))
        (rebind_environment if preserve else environment)(root / "secrets" / (service + ".env"), env)
    environment(root / "compose.env", {
        "JUEX_PLATFORM_IMAGE": config["image"], "JUEX_DEPLOY_ROOT": root,
        "JUEX_WORKSPACE_ROOT": config["workspace"], "JUEX_DOCKER_SOCKET": config["socket"],
        "JUEX_WORKSPACE_DEVICE": mount_device(config["workspace"]),
        "JUEX_PLATFORM_PREFIX": prefix, "JUEX_HTTPS_PORT": config["https_port"],
        "JUEX_ACTIVE_THREADS": config["active_threads"],
        "JUEX_POSTGRES_IMAGE": config["postgres_image"], "JUEX_GATEWAY_IMAGE": config["gateway_image"],
    })
    hosted = {
        "backend": {"socket": config["socket"], "root": str(root / "control"),
                    "workspace_root": config["workspace"], "storage_identity": config["storage_identity"],
                    "guest_binary": str(root / "bin/juex-guest"), "image": config["hosted_image"],
                    "pool": config["hosted_pool"], "control": config["host_ip"] + ":8684",
                    "server": "https://execution:8684", "dns": config["dns"],
                    "protected": [prefix + ".0/24"], "allow": []},
        "key_file": str(root / "secrets/hosted.key"), "idle_seconds": 300,
        "memory_bytes": 768 << 20, "nano_cpus": 1000000000,
        "workspace_bytes": 2 << 30, "workspace_inodes": 131072,
    }
    if preserve:
        saved = json.loads((root / "hosted.json").read_text())
        for key in ("socket", "root", "workspace_root", "storage_identity", "guest_binary", "control"):
            saved["backend"][key] = hosted["backend"][key]
        saved["key_file"] = hosted["key_file"]
        hosted = saved
    write_json(root / "hosted.json", hosted)
    write_json(root / "deployment.json", config)


def prepare_postgres(config):
    # PostgreSQL 18 nests PGDATA under the mounted /var/lib/postgresql root.
    # Its entrypoint chowns PGDATA, not this private parent directory.
    identity = []
    for flag in ("-u", "-g"):
        value = docker(config, "run", "--rm", "--network", "none", "--entrypoint", "id",
                       config["postgres_image"], flag, "postgres").stdout.decode().strip()
        identity.append(int(value))
    os.chown(Path(config["root"]) / "postgres", *identity)


def prepare_logs(root):
    directory = root / "logs"
    directory.mkdir(mode=0o700, exist_ok=True)
    for name in (*SERVICES, "postgres", "gateway"):
        path = directory / name
        path.mkdir(mode=0o700, exist_ok=True)
        if name in SERVICES and name != "execution":
            os.chown(path, 10001, 10001)


def initialize(args):
    root, workspace = absolute(args.root), absolute(args.workspace)
    separate(root, workspace)
    if root.exists():
        raise ValueError("initialization requires a new deployment directory")
    url = urllib.parse.urlsplit(args.public_url)
    if url.scheme != "https" or not url.hostname or url.path or url.query or url.fragment or url.username:
        raise ValueError("public URL must be an HTTPS origin")
    prefix = args.platform_prefix
    platform = ipaddress.ip_network(prefix + ".0/24")
    hosted = ipaddress.ip_network(args.hosted_pool)
    if not platform.is_private or not hosted.is_private or hosted.prefixlen != 16 or hosted.overlaps(platform):
        raise ValueError("platform /24 and hosted /16 must be separate private IPv4 networks")
    if args.listen_port is not None and not 1 <= args.listen_port <= 65535:
        raise ValueError("invalid HTTPS bind port")
    ipaddress.IPv4Address(args.host_ip)
    for address in args.dns:
        ipaddress.IPv4Address(address)
    config = dict(root=str(root), workspace=str(workspace), socket=str(absolute(args.docker_socket)),
                  public_url=args.public_url, https_port=args.listen_port or url.port or 443, platform_prefix=prefix,
                  hosted_pool=str(hosted), host_ip=args.host_ip, dns=args.dns,
                  storage_identity=mount_identity(workspace), active_threads=10)
    for key in ("image", "hosted_image", "postgres_image", "gateway_image"):
        value = getattr(args, key)
        config[key] = docker(config, "image", "inspect", value, "--format", "{{.Id}}").stdout.decode().strip()
    assert_deployment_owner(config)
    root.mkdir(mode=0o700)
    for name in ("secrets", "certificates", "tls", "bin", "blobs", "control", "postgres"):
        (root / name).mkdir(mode=0o700)
    (root / "maintenance").mkdir(mode=0o755)
    # This inode is permanent. Only the drain marker is replaced atomically.
    os.chmod(root / "maintenance", 0o755)
    with open(root / "maintenance/admission.lock", "xb"):
        pass
    os.chmod(root / "maintenance/admission.lock", 0o644)
    durable(root / "maintenance/draining", "initial setup\n")
    password, master_key = secrets.token_hex(32), secrets.token_hex(32)
    durable(root / "secrets/postgres-password", password)
    durable(root / "secrets/master-key", master_key)
    durable(root / "secrets/hosted.key", secrets.token_bytes(32))
    docker(config, "run", "--rm", "--user", "0:0", "--network", "none", "--mount",
           f"type=bind,source={root / 'secrets'},target=/output", config["image"],
           "juex-management", "services", "init", "--directory", "/output/authority")
    for service in SERVICES:
        directory = root / "certificates" / service
        directory.mkdir(mode=0o700)
        for name in ("ca.pem", service + ".pem", service + ".key"):
            shutil.copyfile(root / "secrets/authority" / name, directory / name)
            os.chmod(directory / name, 0o600)
            os.chown(directory / name, 10001, 10001)
        os.chown(directory, 10001, 10001)
    for source, name in ((args.tls_certificate, "certificate.pem"), (args.tls_key, "key.pem")):
        shutil.copyfile(source, root / "tls" / name)
        os.chmod(root / "tls" / name, 0o600)
    container = docker(config, "create", config["image"], "/bin/true").stdout.decode().strip()
    try:
        for name in ("juex-guest", "juex-service-log"):
            docker(config, "cp", container + ":/usr/local/bin/" + name, root / "bin" / name)
    finally:
        docker(config, "rm", container)
    for name in ("compose.yaml", "nginx.conf"):
        shutil.copyfile(HERE / name, root / name)
    prepare_postgres(config)
    prepare_logs(root)
    render(config, password, master_key)
    print(json.dumps({"root": str(root), "status": "initialized_in_maintenance"}))


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
        yield


def drain(config, reason):
    durable(Path(config["root"]) / "maintenance/draining", reason + "\n")


def inventory(config, offline=False):
    result = compose(config, "run", "--rm", "--no-deps", "-T", "operator", "juex-management",
                     "maintenance-report", *(["--offline"] if offline else []), check=False)
    if not result.stdout:
        raise RuntimeError("maintenance report unavailable: " + result.stderr.decode(errors="replace")[-2000:])
    report = json.loads(result.stdout)
    write_json(Path(config["root"]) / "maintenance/report.json", report)
    if not report["ready"]:
        raise RuntimeError("maintenance not ready; inspect maintenance/report.json; no work was cancelled")
    if result.returncode:
        raise RuntimeError("maintenance report failed")
    return report


def firewall(config):
    # Only the trusted Execution controller is on the host network. Public
    # clients reach it through the TLS gateway; RPC/device ports stay private.
    chain = "JUEX-PLATFORM"
    ensure_xtables_lock()
    ipt = ["iptables", "-w", "5"]
    if run([*ipt, "-S", chain], check=False).returncode:
        run([*ipt, "-N", chain])
    rules = [["-s", "127.0.0.0/8", "-j", "ACCEPT"],
             ["-s", config["platform_prefix"] + ".0/24", "-j", "ACCEPT"],
             ["-s", config["hosted_pool"], "-p", "tcp", "--dport", "8684", "-j", "ACCEPT"],
             ["-j", "REJECT"]]
    existing = run([*ipt, "-S", chain]).stdout.decode().splitlines()
    count = sum(line.startswith("-A " + chain + " ") for line in existing)
    # Keep a leading deny while replacing only our own chain. A failed update
    # stays closed, and previous address ranges cannot retain stale access.
    run([*ipt, "-I", chain, "1", "-j", "REJECT"])
    for _ in range(count):
        run([*ipt, "-D", chain, "2"])
    for rule in rules:
        run([*ipt, "-A", chain, *rule])
    run([*ipt, "-D", chain, "1"])
    jump = ["-p", "tcp", "-m", "multiport", "--dports", "8683,8684,8783", "-j", chain]
    if run([*ipt, "-C", "INPUT", *jump], check=False).returncode:
        run([*ipt, "-I", "INPUT", "1", *jump])


def assert_deployment_owner(config):
    root = str(Path(config["root"]))
    ids = docker(config, "ps", "-aq", "--filter", "label=com.docker.compose.project=juex").stdout.decode().split()
    for identity in ids:
        item = json.loads(docker(config, "inspect", identity).stdout)[0]
        files = item["Config"]["Labels"].get("com.docker.compose.project.config_files", "").split(",")
        if files != [root + "/compose.yaml"]:
            raise ValueError("Docker already contains another juex deployment; use a separate daemon or retire it explicitly")
    ids = docker(config, "network", "ls", "-q", "--filter", "label=com.docker.compose.project=juex").stdout.decode().split()
    for identity in ids:
        item = json.loads(docker(config, "network", "inspect", identity).stdout)[0]
        if item.get("Labels", {}).get("ai.juex.deployment") != root:
            raise ValueError("Docker already contains another juex deployment network")


def up(config, reviewed=False):
    if (Path(config["root"]) / "maintenance/recovery-required.json").exists() and not reviewed:
        raise ValueError("recovery authority review required before starting services or opening the gateway")
    assert_deployment_owner(config)
    if (Path(config["root"]) / "maintenance/restore-incomplete").exists():
        raise ValueError("restore is incomplete")
    if mount_identity(config["workspace"]) != config["storage_identity"]:
        raise ValueError("Workspace identity changed; use the offline restore procedure")
    firewall(config)
    compose(config, "up", "-d", timeout=180)


def ensure_xtables_lock():
    path = Path("/run/xtables.lock")
    if not path.exists():
        try:
            fd = os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
            os.close(fd)
        except FileExistsError:
            pass
    if path.is_symlink() or not path.is_file():
        raise ValueError("host xtables lock must be a regular file")


def healthy(config):
    deadline = time.monotonic() + 90
    while True:
        result = compose(config, "exec", "-T", "management", "juex-management", "services", "check", check=False, timeout=15)
        if result.returncode == 0:
            return
        if time.monotonic() >= deadline:
            raise RuntimeError("services did not become healthy; maintenance retained")
        time.sleep(1)


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
    healthy(config)
    # Starting services does not release queued execution while draining exists.
    (root / "maintenance/draining").unlink(missing_ok=True)
    recovery.unlink(missing_ok=True)
    sync_directory(root / "maintenance")
    print(json.dumps({"status": "running"}))


def hosted_containers(config):
    ids = docker(config, "ps", "-aq", "--filter", "label=ai.juex.hosted.environment").stdout.decode().split()
    result = []
    for identity in ids:
        item = json.loads(docker(config, "inspect", identity).stdout)[0]
        environment = item["Config"]["Labels"]["ai.juex.hosted.environment"]
        expected = str(Path(config["root"]) / "control" / environment / "control")
        mounts = {m["Destination"]: m["Source"] for m in item["Mounts"]}
        if mounts.get("/var/lib/juex-control") != expected:
            # An unknown container touching this deployment is not safe to
            # ignore merely because its ownership label/mount changed.
            roots = (Path(config["root"]) / "control", Path(config["workspace"]))
            if any(root == Path(m["Source"]) or root in Path(m["Source"]).parents
                   for root in roots for m in item["Mounts"]):
                raise RuntimeError("hosted container ownership/mounts changed")
            continue
        for target, name in (("/workspace", "workspace"), ("/home/agent", "home")):
            if mounts.get(target) != str(Path(config["workspace"]) / environment / name):
                raise RuntimeError("hosted Workspace/Home mount changed")
        if item["State"]["Running"]:
            top = docker(config, "top", identity, "-eo", "uid,pid,args").stdout.decode().splitlines()
            if any(line.split()[0] != "0" for line in top[1:] if line.strip()):
                raise RuntimeError(f"hosted environment {environment} still has user processes; cancel explicitly before backup")
        result.append(item)
    return result


def stop_writers(config, hosted):
    # Timeout -1 forbids Docker's forced SIGKILL fallback. A stuck shutdown
    # makes this maintenance operation fail and leaves admission closed.
    ids = compose(config, "ps", "-q", "gateway", *SERVICES).stdout.decode().split()
    for identity in ids:
        docker(config, "stop", "--timeout", "-1", identity, timeout=90)
    for item in hosted:
        if item["State"]["Running"]:
            docker(config, "stop", "--timeout", "-1", item["Id"], timeout=90)
    for identity in ids + [item["Id"] for item in hosted]:
        state = json.loads(docker(config, "inspect", "--format", "{{json .State}}", identity).stdout)
        if state["Running"] or state.get("OOMKilled") or state["ExitCode"] not in (0, 143):
            raise RuntimeError("writer did not stop cleanly; backup not created")


def pack(source, archive, names=(".",)):
    run(["tar", "--numeric-owner", "--xattrs", "--acls", "--sparse", "--one-file-system",
         "-cpf", archive, "-C", source, *names], timeout=3600)
    os.chmod(archive, 0o600)
    with open(archive, "rb") as f:
        os.fsync(f.fileno())


def unpack(archive, destination):
    run(["tar", "--numeric-owner", "--xattrs", "--acls", "--sparse", "-xpf", archive,
         "-C", destination], timeout=3600)


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
    with exclusive(config, args.drain_timeout):
        inventory(config)
        hosted = hosted_containers(config)
        stop_writers(config, hosted)
        report = inventory(config, offline=True)
        data_dir.mkdir(mode=0o700)
        key_dir.mkdir(mode=0o700)
        with open(data_dir / "database.dump", "xb") as output:
            compose(config, "exec", "-T", "postgres", "pg_dump", "-U", "juex", "-d", "juex",
                    "--format=custom", "--no-owner", output=output, timeout=3600)
            output.flush()
            os.fsync(output.fileno())
        pack(root / "blobs", data_dir / "blobs.tar")
        pack(root / "control", data_dir / "control.tar")
        pack(workspace, data_dir / "workspaces.tar")
        pack(root, data_dir / "deployment.tar", ("deployment.json", "compose.yaml", "nginx.conf", "hosted.json", "bin"))
        pack(root, key_dir / "secrets.tar", ("secrets", "certificates", "tls"))
        docker(config, "image", "save", "-o", data_dir / "images.tar", config["image"],
               config["hosted_image"], config["postgres_image"], config["gateway_image"], timeout=3600)
        os.chmod(data_dir / "images.tar", 0o600)
        write_json(data_dir / "review.json", report)
        manifest = {"format": 1, "backup_id": identity, "complete": True,
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


def verified_bundle(directory):
    directory = absolute(directory)
    manifest = json.loads((directory / "manifest.json").read_text())
    if manifest.get("format") != 1 or manifest.get("complete") is not True or manifest.get("backup_id") != directory.name:
        raise ValueError("not a completed JueX backup")
    for name, expected in manifest["files"].items():
        if Path(name).name != name or (directory / name).is_symlink() or digest(directory / name) != expected:
            raise ValueError("backup checksum mismatch")
    return directory, manifest


def restore(args):
    source, manifest = verified_bundle(args.backup)
    recovery, key_manifest = verified_bundle(args.recovery)
    if key_manifest["backup_id"] != manifest["backup_id"] or key_manifest["files"]["secrets.tar"] != manifest["recovery_sha256"]:
        raise ValueError("recovery material does not match this backup")
    root, workspace = absolute(args.root), absolute(args.workspace)
    separate(root, workspace, source, recovery)
    if root.exists() or any(workspace.iterdir()):
        raise ValueError("restore requires a new deployment directory and an empty dedicated Workspace mount")
    storage_identity = mount_identity(workspace)
    assert_deployment_owner({"root": str(root), "socket": str(absolute(args.docker_socket))})
    ensure_xtables_lock()
    root.mkdir(mode=0o700)
    for name in ("maintenance", "blobs", "control", "postgres"):
        (root / name).mkdir(mode=0o755 if name == "maintenance" else 0o700)
    os.chmod(root / "maintenance", 0o755)
    with open(root / "maintenance/admission.lock", "xb"):
        pass
    os.chmod(root / "maintenance/admission.lock", 0o644)
    durable(root / "maintenance/draining", "disaster recovery\n")
    durable(root / "maintenance/restore-incomplete", manifest["backup_id"])
    unpack(source / "deployment.tar", root)
    unpack(recovery / "secrets.tar", root)
    unpack(source / "blobs.tar", root / "blobs")
    unpack(source / "control.tar", root / "control")
    config = json.loads((root / "deployment.json").read_text())
    config.update(root=str(root), workspace=str(workspace), storage_identity=storage_identity,
                  host_ip=str(ipaddress.IPv4Address(args.host_ip)), socket=str(absolute(args.docker_socket)))
    prepare_logs(root)
    docker(config, "image", "load", "-i", source / "images.tar", timeout=3600)
    prepare_postgres(config)
    render(config, (root / "secrets/postgres-password").read_text(), (root / "secrets/master-key").read_text(), preserve=True)
    compose(config, "up", "-d", "--wait", "postgres", timeout=180)
    with open(source / "database.dump", "rb") as input_file:
        compose(config, "exec", "-T", "postgres", "pg_restore", "-U", "juex", "-d", "juex",
                "--no-owner", "--exit-on-error", input_file=input_file, timeout=3600)
    compose(config, "run", "--rm", "--no-deps", "-T", "execution", "juex-execution", "restore-storage",
            "--previous-storage", manifest["storage_identity"], "--initialize", timeout=3600)
    unpack(source / "workspaces.tar", workspace)
    compose(config, "run", "--rm", "--no-deps", "-T", "execution", "juex-execution", "restore-storage",
            "--previous-storage", manifest["storage_identity"], timeout=3600)
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
    compose(config, "run", "--rm", "--no-deps", "-T", "operator", "juex-management", "recovery-revoke",
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


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", required=True, help="Absolute private deployment directory")
    commands = parser.add_subparsers(dest="command", required=True)
    init = commands.add_parser("init", help="Initialize a new deployment; never formats storage")
    for name in ("workspace", "image", "hosted-image", "public-url", "host-ip", "tls-certificate", "tls-key"):
        init.add_argument("--" + name, required=True)
    init.add_argument("--dns", action="append", required=True)
    init.add_argument("--listen-port", type=int, help="HTTPS bind port when different from the public origin")
    init.add_argument("--docker-socket", default="/var/run/docker.sock")
    init.add_argument("--platform-prefix", default="172.30.0")
    init.add_argument("--hosted-pool", default="172.31.0.0/16")
    init.add_argument("--postgres-image", default="postgres:18-bookworm")
    init.add_argument("--gateway-image", default="nginx:1.28-bookworm")
    commands.add_parser("up", help="Start services, preserving any maintenance marker")
    commands.add_parser("drain", help="Pause new work while preserving cancellation and receipts")
    commands.add_parser("report", help="Write a private recovery/readiness inventory")
    res = commands.add_parser("resume", help="Reopen admission after reviewing recovery when applicable")
    res.add_argument("--acknowledge-recovery", default="")
    revoke = commands.add_parser("recovery-revoke", help="Suspend a restored membership or revoke a device before reopening services")
    for name in ("tenant", "user", "device"):
        revoke.add_argument("--" + name, default="")
    back = commands.add_parser("backup", help="Drain and back up all state; incomplete backups never count as success")
    back.add_argument("--destination", required=True)
    back.add_argument("--recovery-destination", required=True, help="Separate private location for master keys and service identities")
    back.add_argument("--keep", type=int, default=7)
    back.add_argument("--drain-timeout", type=int, default=60)
    rec = commands.add_parser("restore", help="Restore onto an empty filesystem; execution remains disabled")
    for name in ("backup", "recovery", "workspace", "host-ip"):
        rec.add_argument("--" + name, required=True)
    rec.add_argument("--docker-socket", default="/var/run/docker.sock")
    args = parser.parse_args()
    if os.geteuid() != 0:
        parser.error("Linux operator commands require root for Docker, firewall and XFS ownership")
    os.umask(0o077)
    if args.command == "init":
        initialize(args)
        return
    if args.command == "restore":
        restore(args)
        return
    root = absolute(args.root)
    config = json.loads((root / "deployment.json").read_text())
    if config["root"] != str(root):
        raise ValueError("deployment path changed; use restore")
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
        elif args.command == "resume":
            resume(config, args.acknowledge_recovery)
        elif args.command == "recovery-revoke":
            revoke_recovery(config, args)
        elif args.command == "backup":
            backup(config, args)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, RuntimeError, OSError, subprocess.TimeoutExpired) as error:
        raise SystemExit(str(error)) from None
