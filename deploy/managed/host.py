"""Native platform deployment with private PostgreSQL and OS-owned jobs."""

import hashlib
import ipaddress
import json
import os
from pathlib import Path
from platform import machine, system as platform_system
import secrets
import shutil
import socket
import ssl
import sys
import time
import urllib.parse
import urllib.request
import uuid

from common import (HERE, SERVICES, absolute, digest, durable, environment, pack,
                    rebind_environment, run, separate, sync_directory, unpack, write_json)
import processes

BINARIES = ("juex", *("juex-" + name for name in (*SERVICES, "executor", "service-log")))
OPERATOR_FILES = ("operator.py", "common.py", "host.py", "hosted.py", "processes.py", "nginx.conf")
service_status = processes.service_status


def system():
    return {"Darwin": "darwin", "Linux": "linux"}.get(platform_system(), "unsupported")


def stage_release(root, source):
    source = absolute(source)
    hashes = {}
    for name in BINARIES:
        path = source / name
        if path.is_symlink() or not path.is_file() or path.stat().st_mode & 0o111 == 0:
            raise ValueError("release requires every platform binary as a regular executable")
        hashes[name] = digest(path)
    assets = source.parent / "deploy/managed"
    if not assets.exists():
        assets = HERE
    operator_hashes = {}
    for name in OPERATOR_FILES:
        path = assets / name
        if path.is_symlink() or not path.is_file():
            raise ValueError("release requires complete regular operator assets")
        operator_hashes[name] = digest(path)
    identity = hashlib.sha256(json.dumps([hashes, operator_hashes], sort_keys=True).encode()).hexdigest()[:24]
    directory = Path(root) / "releases" / identity
    release = {"id": identity, "bin": str(directory / "bin"), "sha256": hashes,
               "operator": str(directory / "operator"), "operator_sha256": operator_hashes}
    if directory.exists():
        verify_release({"release": release})
        return release
    directory.parent.mkdir(mode=0o700, exist_ok=True)
    staging = directory.with_name(".incomplete-" + identity)
    staging.mkdir(mode=0o700)
    (staging / "bin").mkdir(mode=0o700)
    (staging / "operator").mkdir(mode=0o700)
    for name in BINARIES:
        shutil.copyfile(source / name, staging / "bin" / name)
        if digest(staging / "bin" / name) != hashes[name]:
            raise ValueError("release source changed while copying")
        (staging / "bin" / name).chmod(0o500)
    for name in OPERATOR_FILES:
        shutil.copyfile(assets / name, staging / "operator" / name)
        if digest(staging / "operator" / name) != operator_hashes[name]:
            raise ValueError("operator source changed while copying")
        (staging / "operator" / name).chmod(0o400)
    write_json(staging / "release.json", release)
    staging.rename(directory)
    sync_directory(directory.parent)
    return release


def verify_release(config):
    release = config["release"]
    if set(release["sha256"]) != set(BINARIES):
        raise ValueError("incomplete platform release")
    for name, expected in release["sha256"].items():
        path = Path(release["bin"]) / name
        if path.is_symlink() or not path.is_file() or digest(path) != expected:
            raise ValueError("platform release checksum mismatch")
    if set(release["operator_sha256"]) != set(OPERATOR_FILES):
        raise ValueError("incomplete operator release")
    for name, expected in release["operator_sha256"].items():
        path = Path(release["operator"]) / name
        if path.is_symlink() or not path.is_file() or digest(path) != expected:
            raise ValueError("operator release checksum mismatch")


def install_operator(config):
    verify_release(config)
    for name in OPERATOR_FILES:
        durable(Path(config["operator"]) / name, (Path(config["release"]["operator"]) / name).read_bytes())


def executable(value, name):
    path = Path(value).resolve() if value else Path(shutil.which(name) or "/missing")
    if not path.is_file() or not os.access(path, os.X_OK):
        raise ValueError(f"missing dependency: {name}; provide its explicit executable path")
    return str(path)


def postgres_tools(directory):
    root = Path(directory).resolve() if directory else Path(shutil.which("postgres") or "/missing/postgres").parent
    for name in ("postgres", "initdb", "pg_isready", "pg_dump", "pg_restore", "createdb"):
        executable(str(root / name), name)
    version = run([root / "postgres", "--version"]).stdout.decode().strip().split()[2]
    return str(root), int(version.split(".")[0])


def service_env(config, name):
    root = Path(config["root"])
    result = {"PATH": config["path"], "HOME": str(Path.home()), "LANG": "en_US.UTF-8"}
    path = root / "secrets" / (name + ".env")
    if path.exists():
        for line in path.read_text().splitlines():
            if line and not line.startswith("#"):
                key, value = line.split("=", 1)
                result[key] = value
    return result


def binary(config, name):
    return str(Path(config["release"]["bin"]) / ("juex-" + name))


def management(config, *args, **kwargs):
    env = service_env(config, "management")
    for key in ("JUEX_MODEL_API_KEY", "JUEX_SMTP_CREDENTIAL"):
        if key in os.environ:
            env[key] = os.environ[key]
    return run([binary(config, "management"), *args], env=env, **kwargs)


def render(config, password, master_key, preserve=False):
    root = Path(config["root"])
    port = config["rpc_base"]
    base = {"JUEX_DATABASE_URL": "postgres://juex:" + password + "@/juex?" + urllib.parse.urlencode({"host": str(root / "socket"), "sslmode": "disable"}),
            "JUEX_MAINTENANCE_DIR": str(root / "maintenance")}
    for i, name in enumerate(SERVICES):
        base["JUEX_" + name.upper() + "_RPC"] = "127.0.0.1:" + str(port + i)
    for name in SERVICES:
        values = dict(base, JUEX_SERVICE_CERTS=str(root / "certificates" / name))
        if name == "management":
            values.update(JUEX_PUBLIC_URL=config["public_url"], JUEX_MASTER_KEY=master_key)
        if name in ("management", "execution"):
            values["JUEX_TRUSTED_PROXIES"] = "127.0.0.1"
        if name == "execution":
            values.update(JUEX_HOST_CONFIG=str(root / "host.json"), JUEX_BLOB_ROOT=str(root / "blobs"))
        (rebind_environment if preserve else environment)(root / "secrets" / (name + ".env"), values)
    host = {"backend": {"root": config["workspace"], "control_root": str(root / "control"),
                        "identity": config["identity"], "executable": binary(config, "executor"),
                        "server": config["public_url"], "ca_file": str(root / "tls/ca.pem") if (root / "tls/ca.pem").is_file() else ""},
            "key_file": str(root / "secrets/host.key"), "idle_seconds": 300}
    if preserve:
        saved = json.loads((root / "host.json").read_text())
        for key in ("root", "control_root", "identity", "executable"):
            saved["backend"][key] = host["backend"][key]
        saved["key_file"] = host["key_file"]
        host = saved
    write_json(root / "host.json", host)
    write_json(root / "deployment.json", config)
    template = (Path(config["release"]["operator"]) / "nginx.conf").read_text()
    template = template.replace("events {}", f'pid "{root}/run/nginx.pid";\nerror_log stderr;\nevents {{}}')
    temporary = "".join(f'  {name}_temp_path "{root}/run/nginx-{name}";\n'
                        for name in ("client_body", "proxy", "fastcgi", "uwsgi", "scgi"))
    template = template.replace("http {", "http {\n  access_log off;\n" + temporary)
    template = template.replace("listen 443 ssl;", f'listen 0.0.0.0:{config["https_port"]} ssl;')
    template = template.replace("/run/juex/tls", str(root / "tls"))
    template = template.replace("http://execution:8683", f'http://127.0.0.1:{config["device_port"]}')
    template = template.replace("http://management:8680", f'http://127.0.0.1:{config["http_port"]}')
    durable(root / "nginx.conf", template)


def initialize(args):
    root = absolute(args.root)
    if root.exists():
        raise ValueError("initialization requires a new deployment directory")
    if system() not in ("darwin", "linux") or os.geteuid() == 0:
        raise ValueError("Host platform requires a non-root macOS or Linux user")
    workspace = absolute(args.workspace or str(root.with_name(root.name + "-workspaces")))
    separate(root, workspace)
    if workspace.exists():
        raise ValueError("Host initialization requires a new workspace directory")
    url = urllib.parse.urlsplit(args.public_url)
    if url.scheme != "https" or not url.hostname or url.path or url.query or url.fragment or url.username:
        raise ValueError("public URL must be an HTTPS origin")
    if not args.bin_dir:
        raise ValueError("Host deployment requires --bin-dir from a complete platform release")
    if not args.local_tls and not (args.tls_certificate and args.tls_key):
        raise ValueError("provide TLS certificate/key or explicitly use --local-tls")
    pg, major = postgres_tools(args.postgres_bin)
    nginx = executable(args.nginx, "nginx")
    tar = executable(args.tar, "gtar" if system() == "darwin" else "tar")
    if b"GNU tar" not in run([tar, "--version"]).stdout:
        raise ValueError("backup requires GNU tar for sparse files, permissions and mount boundaries")
    https_port = args.listen_port or url.port or 8443
    if https_port != (url.port or 443):
        raise ValueError("native gateway port must match the public origin")
    ports = [https_port, args.http_port, args.device_port, *range(args.rpc_base, args.rpc_base + 5)]
    if len(set(ports)) != len(ports) or any(port < 1024 or port > 65535 for port in ports):
        raise ValueError("native service ports must be distinct unprivileged ports")
    for port in ports:
        with socket.socket() as probe:
            probe.bind(("0.0.0.0", port))
    if len(os.fsencode(str(root / "socket/.s.PGSQL.5432"))) >= 104:
        raise ValueError("deployment path is too long for the PostgreSQL Unix socket")
    root.mkdir(mode=0o700, parents=True)
    workspace.mkdir(mode=0o700, parents=True)
    for name in ("secrets", "certificates", "tls", "blobs", "control", "maintenance", "logs", "run", "socket", "operator"):
        (root / name).mkdir(mode=0o700)
    for name in (*SERVICES, "postgres", "gateway"):
        (root / "logs" / name).mkdir(mode=0o700)
    durable(root / "maintenance/admission.lock", "")
    durable(root / "maintenance/draining", "initial setup\n")
    durable(root / "maintenance/install-incomplete", "initial setup\n")
    identity = str(uuid.uuid4())
    config = {"format": 2, "backend": "host", "root": str(root), "workspace": str(workspace),
              "identity": identity, "storage_identity": identity, "os": system(), "arch": machine(),
              "python": str(Path(sys.executable).resolve()), "operator": str(root / "operator"),
              "path": os.environ.get("PATH", "/usr/bin:/bin"), "postgres_bin": pg, "postgres_major": major,
              "nginx": nginx, "tar": tar, "https_port": https_port, "http_port": args.http_port,
              "device_port": args.device_port, "rpc_base": args.rpc_base,
              "public_url": args.public_url, "local_tls": args.local_tls, "active_threads": args.active_threads,
              "release": stage_release(root, args.bin_dir)}
    install_operator(config)
    password, master = secrets.token_hex(32), secrets.token_hex(32)
    durable(root / "secrets/postgres-password", password)
    durable(root / "secrets/master-key", master)
    durable(root / "secrets/host.key", secrets.token_bytes(32))
    run([binary(config, "management"), "services", "init", "--directory", root / "secrets/authority"])
    for name in SERVICES:
        directory = root / "certificates" / name
        directory.mkdir(mode=0o700)
        for filename in ("ca.pem", name + ".pem", name + ".key"):
            shutil.copyfile(root / "secrets/authority" / filename, directory / filename)
            (directory / filename).chmod(0o600)
    if args.local_tls:
        local_certificate(config, url.hostname)
    else:
        for source, filename in ((args.tls_certificate, "certificate.pem"), (args.tls_key, "key.pem")):
            shutil.copyfile(source, root / "tls" / filename)
            (root / "tls" / filename).chmod(0o600)
        if args.tls_ca:
            shutil.copyfile(args.tls_ca, root / "tls/ca.pem")
            (root / "tls/ca.pem").chmod(0o600)
    render(config, password, master)
    run([Path(pg) / "initdb", "-D", root / "postgres", "-U", "juex", "--pwfile", root / "secrets/postgres-password",
         "--auth-local=scram-sha-256", "--auth-host=reject", "--encoding=UTF8", "--locale=C"])
    with (root / "postgres/postgresql.conf").open("a") as f:
        f.write("\nlisten_addresses = ''\nunix_socket_directories = '" + str(root / "socket") + "'\nunix_socket_permissions = 0700\n")
    run([config["nginx"], "-e", "stderr", "-t", "-p", root, "-c", root / "nginx.conf"])
    (root / "maintenance/install-incomplete").unlink()
    sync_directory(root / "maintenance")
    print(json.dumps({"root": str(root), "backend": "host", "status": "initialized_in_maintenance"}))


def local_certificate(config, hostname):
    root = Path(config["root"])
    openssl = executable("", "openssl")
    try:
        ipaddress.ip_address(hostname)
        san = "IP:" + hostname
    except ValueError:
        san = "DNS:" + hostname
    run([openssl, "req", "-x509", "-newkey", "rsa:3072", "-nodes", "-days", "3650", "-subj", "/CN=JueX local " + config["identity"],
         "-keyout", root / "secrets/local-ca.key", "-out", root / "tls/ca.pem"])
    run([openssl, "req", "-new", "-newkey", "rsa:3072", "-nodes", "-subj", "/CN=" + hostname,
         "-keyout", root / "tls/key.pem", "-out", root / "tls/request.pem"])
    durable(root / "tls/extensions.conf", "subjectAltName=" + san + ",DNS:localhost,IP:127.0.0.1\nextendedKeyUsage=serverAuth\n")
    run([openssl, "x509", "-req", "-in", root / "tls/request.pem", "-CA", root / "tls/ca.pem", "-CAkey", root / "secrets/local-ca.key",
         "-CAcreateserial", "-days", "365", "-extfile", root / "tls/extensions.conf", "-out", root / "tls/certificate.pem"])


def service_command(config, name):
    root = Path(config["root"])
    if name == "postgres":
        command = [str(Path(config["postgres_bin"]) / "postgres"), "-D", str(root / "postgres")]
    elif name == "gateway":
        command = [config["nginx"], "-e", "stderr", "-p", str(root), "-c", str(root / "nginx.conf"), "-g", "daemon off;"]
    else:
        port = config["rpc_base"] + SERVICES.index(name)
        command = [binary(config, name), "serve", "--listen", f"0.0.0.0:{port}"]
        if name == "management":
            command = [binary(config, name), "serve", "--listen", f'0.0.0.0:{config["http_port"]}', "--rpc-listen", f"0.0.0.0:{port}"]
        elif name == "execution":
            command += ["--device-listen", f'0.0.0.0:{config["device_port"]}']
        elif name == "runtime":
            command += ["--max-active-threads", str(config["active_threads"])]
    return [binary(config, "service-log"), "--directory", str(root / "logs" / name), "--", *command]


def up(config, reviewed=False):
    root = Path(config["root"])
    if (root / "maintenance/install-incomplete").exists():
        raise ValueError("installation is incomplete")
    verify_release(config)
    start_database(config)
    for name in (*SERVICES, "gateway"):
        processes.start(config, name)


def start_database(config):
    processes.start(config, "postgres")
    deadline = time.monotonic() + 30
    while run([Path(config["postgres_bin"]) / "pg_isready", "-h", Path(config["root"]) / "socket", "-U", "juex"], check=False).returncode:
        if time.monotonic() >= deadline:
            raise RuntimeError("owned PostgreSQL did not become ready")
        time.sleep(.2)
    # initdb creates postgres, not the platform business database.
    result = run([Path(config["postgres_bin"]) / "createdb", "-h", Path(config["root"]) / "socket", "-U", "juex", "juex"],
                 env=pg_env(config), check=False)
    if result.returncode and b"already exists" not in result.stderr:
        raise RuntimeError("cannot create the platform database")


def healthy(config):
    deadline = time.monotonic() + 90
    ca = Path(config["root"]) / "tls/ca.pem"
    context = ssl.create_default_context(cafile=str(ca) if ca.exists() else None)
    while True:
        if management(config, "services", "check", check=False, timeout=15).returncode == 0:
            try:
                with urllib.request.urlopen(config["public_url"] + "/healthz", context=context, timeout=3) as response:
                    if response.status == 200:
                        return
            except OSError:
                pass
        if time.monotonic() >= deadline:
            raise RuntimeError("services or public HTTPS gateway did not become healthy; maintenance retained")
        time.sleep(1)


def status(config):
    return {"backend": "host", "root": config["root"], "release": config["release"]["id"],
            "maintenance": (Path(config["root"]) / "maintenance/draining").exists(),
            "services": {name: service_status(config, name) for name in (*SERVICES, "postgres", "gateway")}}


def quiesce(config, lock_fd):
    for name in ("gateway", *SERVICES):
        processes.stop(config, name)
    env = service_env(config, "execution")
    run([binary(config, "execution"), "stop-hosts", "--lock-fd", str(lock_fd)],
        env=env, pass_fds=(lock_fd,), timeout=180)


def stop_database(config):
    processes.stop(config, "postgres")


def pg_env(config):
    return dict(service_env(config, "postgres"), PGPASSWORD=(Path(config["root"]) / "secrets/postgres-password").read_text())


def snapshot(config, data_dir, key_dir):
    root = Path(config["root"])
    with (data_dir / "database.dump").open("xb") as output:
        run([Path(config["postgres_bin"]) / "pg_dump", "-h", root / "socket", "-U", "juex", "-d", "juex", "--format=custom", "--no-owner"],
            env=pg_env(config), output=output, timeout=3600)
        output.flush()
        os.fsync(output.fileno())
    for source, filename, names in ((root / "blobs", "blobs.tar", (".",)), (root / "control", "control.tar", (".",)),
                                    (Path(config["workspace"]), "workspaces.tar", (".",)),
                                    (root, "deployment.tar", ("deployment.json", "host.json", "nginx.conf", "releases", "operator"))):
        pack(source, data_dir / filename, names, tar=config["tar"])
    pack(root, key_dir / "secrets.tar", ("secrets", "certificates", "tls"), tar=config["tar"])


def validate_restore(config, root, workspace):
    if config["os"] != system() or config["arch"] != machine():
        raise ValueError("Host recovery requires the same OS and architecture")
    if config["root"] != root or config["workspace"] != workspace:
        raise ValueError("Host recovery requires the same canonical deployment and workspace paths")


def restore(args, source, manifest, recovery):
    tar = executable(args.tar, "gtar" if system() == "darwin" else "tar")
    saved = json.loads(run([tar, "-xOf", source / "deployment.tar", "deployment.json"]).stdout)
    root = absolute(args.root)
    workspace = absolute(args.workspace or saved["workspace"])
    validate_restore(saved, str(root), str(workspace))
    separate(root, workspace, source, recovery)
    if root.exists() or workspace.exists():
        raise ValueError("Host restore requires absent deployment and workspace directories; preserve old state separately")
    if any(service_status(saved, name)["running"] for name in (*SERVICES, "postgres", "gateway")):
        raise ValueError("source platform is still active")
    root.mkdir(mode=0o700, parents=True)
    workspace.mkdir(mode=0o700, parents=True)
    for name in ("maintenance", "blobs", "control", "logs", "run", "socket"):
        (root / name).mkdir(mode=0o700)
    for name in (*SERVICES, "postgres", "gateway"):
        (root / "logs" / name).mkdir(mode=0o700)
    durable(root / "maintenance/admission.lock", "")
    durable(root / "maintenance/draining", "disaster recovery\n")
    durable(root / "maintenance/restore-incomplete", manifest["backup_id"])
    for archive, directory in ((source / "deployment.tar", root), (recovery / "secrets.tar", root),
                               (source / "blobs.tar", root / "blobs"), (source / "control.tar", root / "control"),
                               (source / "workspaces.tar", workspace)):
        unpack(archive, directory, tar=tar)
    config = json.loads((root / "deployment.json").read_text())
    pg, major = postgres_tools(args.postgres_bin or config["postgres_bin"])
    if major != config["postgres_major"]:
        raise ValueError("PostgreSQL major upgrade requires a separate migration")
    config.update(postgres_bin=pg, nginx=executable(args.nginx or config["nginx"], "nginx"),
                  python=str(Path(sys.executable).resolve()), tar=tar)
    verify_release(config)
    render(config, (root / "secrets/postgres-password").read_text(), (root / "secrets/master-key").read_text(), preserve=True)
    run([Path(pg) / "initdb", "-D", root / "postgres", "-U", "juex", "--pwfile", root / "secrets/postgres-password",
         "--auth-local=scram-sha-256", "--auth-host=reject", "--encoding=UTF8", "--locale=C"])
    with (root / "postgres/postgresql.conf").open("a") as f:
        f.write("\nlisten_addresses = ''\nunix_socket_directories = '" + str(root / "socket") + "'\nunix_socket_permissions = 0700\n")
    start_database(config)
    with (source / "database.dump").open("rb") as input_file:
        run([Path(pg) / "pg_restore", "-h", root / "socket", "-U", "juex", "-d", "juex", "--no-owner", "--exit-on-error"],
            env=pg_env(config), input_file=input_file, timeout=3600)
    return config


def upgrade(config, args):
    if not args.bin_dir:
        raise ValueError("Host upgrade requires --bin-dir")
    updated = dict(config, release=stage_release(Path(config["root"]), args.bin_dir))
    root = Path(config["root"])
    write_json(root / "previous-deployment.json", config)
    install_operator(updated)
    render(updated, (root / "secrets/postgres-password").read_text(), (root / "secrets/master-key").read_text(), preserve=True)
    return updated
