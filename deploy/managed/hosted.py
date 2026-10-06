"""Linux Docker/gVisor/XFS deployment primitives."""

import ipaddress
import json
import os
from pathlib import Path
import secrets
import shutil
import time
import urllib.parse

from common import (SERVICES, HERE, absolute, separate, durable, environment, rebind_environment, write_json, run, pack, unpack, sync_directory)

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
        for key in ("socket", "root", "workspace_root", "storage_identity", "guest_binary", "control", "image"):
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
    config = dict(backend="hosted", format=2, root=str(root), workspace=str(workspace), socket=str(absolute(args.docker_socket)),
                  public_url=args.public_url, https_port=args.listen_port or url.port or 443, platform_prefix=prefix,
                  hosted_pool=str(hosted), host_ip=args.host_ip, dns=args.dns,
                  storage_identity=mount_identity(workspace), active_threads=args.active_threads)
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



def management(config, *args, **kwargs):
    environment = []
    for name in ("JUEX_MODEL_API_KEY", "JUEX_SMTP_CREDENTIAL"):
        if name in os.environ:
            environment.extend(("-e", name))
    return compose(config, "run", "--rm", "--no-deps", "-T", *environment, "operator", "juex-management", *args, **kwargs)


def quiesce(config, lock_fd):
    stop_writers(config, hosted_containers(config))


def stop_database(config):
    ids = compose(config, "ps", "-q", "postgres").stdout.decode().split()
    for identity in ids:
        docker(config, "stop", "--timeout", "-1", identity, timeout=90)


def snapshot(config, data_dir, key_dir):
    root, workspace = Path(config["root"]), Path(config["workspace"])
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

def restore(args, source, manifest, recovery):
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
    return config

def status(config):
    result = compose(config, "ps", "--all", "--format", "json")
    return {"backend": "hosted", "root": config["root"],
            "services": [json.loads(line) for line in result.stdout.decode().splitlines() if line.strip()]}


def upgrade(config, args):
    if not args.image or not args.hosted_image:
        raise ValueError("Hosted upgrade requires --image and --hosted-image")
    updated = dict(config)
    for key in ("image", "hosted_image"):
        updated[key] = docker(config, "image", "inspect", getattr(args, key), "--format", "{{.Id}}").stdout.decode().strip()
    guests = hosted_containers(config)
    for item in guests:
        state = json.loads(docker(config, "inspect", "--format", "{{json .State}}", item["Id"]).stdout)
        if state["Running"] or state.get("OOMKilled") or state["ExitCode"] not in (0, 143):
            raise RuntimeError("hosted guest did not stop cleanly; upgrade refused")
    # Ensure verifies the pinned image and guest digest. Recreate only the
    # stopped container; the owned network, Home, Workspace and journal survive.
    for item in guests:
        docker(config, "rm", item["Id"])
    root = Path(config["root"])
    container = docker(updated, "create", updated["image"], "/bin/true").stdout.decode().strip()
    try:
        for name in ("juex-guest", "juex-service-log"):
            docker(updated, "cp", container + ":/usr/local/bin/" + name, root / "bin" / name)
    finally:
        docker(updated, "rm", container)
    render(updated, (root / "secrets/postgres-password").read_text(),
           (root / "secrets/master-key").read_text(), preserve=True)
    return updated
