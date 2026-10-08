"""Shared deployment transport, filesystem and subprocess operations."""

import hashlib
import ipaddress
import json
import os
from pathlib import Path
import subprocess
import ssl
import urllib.parse
import urllib.request

SERVICES = ("management", "runtime", "memory", "calendar", "execution")
HERE = Path(__file__).resolve().parent

def run(args, *, output=None, input_file=None, timeout=120, check=True, env=None, pass_fds=()):
    result = subprocess.run([str(x) for x in args], stdin=input_file,
                            stdout=output if output is not None else subprocess.PIPE,
                            stderr=subprocess.PIPE, timeout=timeout, env=env, pass_fds=pass_fds)
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


def pack(source, archive, names=(".",), tar="tar"):
    run([tar, "--numeric-owner", "--xattrs", "--acls", "--sparse", "--one-file-system",
         "-cpf", archive, "-C", source, *names], timeout=3600)
    os.chmod(archive, 0o600)
    with open(archive, "rb") as f:
        os.fsync(f.fileno())


def unpack(archive, destination, tar="tar"):
    run([tar, "--numeric-owner", "--xattrs", "--acls", "--sparse", "-xpf", archive,
         "-C", destination], timeout=3600)


def verified_bundle(directory):
    directory = absolute(directory)
    manifest = json.loads((directory / "manifest.json").read_text())
    if manifest.get("format") != 1 or manifest.get("complete") is not True or manifest.get("backup_id") != directory.name:
        raise ValueError("not a completed JueX backup")
    for name, expected in manifest["files"].items():
        if Path(name).name != name or (directory / name).is_symlink() or digest(directory / name) != expected:
            raise ValueError("backup checksum mismatch")
    return directory, manifest


def ingress_configuration(args):
    mode = args.ingress
    raw = args.public_url
    url = urllib.parse.urlsplit(raw)
    scheme = "http" if mode == "http" else "https"
    if (mode not in ("http", "https", "proxy") or any(c.isspace() for c in raw)
            or url.scheme != scheme or not url.hostname or url.path or url.query
            or url.fragment or url.username is not None or url.password is not None):
        raise ValueError(f"{mode} ingress requires a {scheme.upper()} origin")
    public_port = url.port
    if public_port is not None and not 1 <= public_port <= 65535:
        raise ValueError("invalid public origin port")
    certificate = bool(args.tls_certificate or args.tls_key or args.tls_ca or args.local_tls)
    if mode != "https" and certificate:
        raise ValueError("public certificates belong only to HTTPS ingress")
    if mode == "https":
        if args.local_tls and (args.tls_certificate or args.tls_key or args.tls_ca):
            raise ValueError("choose local TLS or supplied certificates")
        if not args.local_tls and not (args.tls_certificate and args.tls_key):
            raise ValueError("HTTPS ingress requires certificate/key or --local-tls")
    proxies = [str(ipaddress.ip_network(value, strict=True)) for value in args.proxy_cidr]
    if any(ipaddress.ip_network(value).prefixlen == 0 for value in proxies):
        raise ValueError("external TLS requires specific proxy addresses")
    if mode == "proxy":
        if not args.listen_port or not proxies:
            raise ValueError("external TLS requires --listen-port and --proxy-cidr")
        port = args.listen_port
    else:
        if proxies:
            raise ValueError("--proxy-cidr belongs only to external TLS ingress")
        port = public_port or (80 if mode == "http" else 443)
        if args.listen_port is not None and args.listen_port != port:
            raise ValueError("gateway bind port must match the public origin")
    if not 1 <= port <= 65535:
        raise ValueError("invalid gateway port")
    return dict(public_url=raw, ingress=mode, proxy_cidrs=proxies, https_port=port)


def ingress_insecure(config):
    return config.get("ingress", "https") == "http"


def ingress_management_flags(config):
    return ["--insecure-http"] if ingress_insecure(config) else []


def ingress_check_nginx(config, version):
    if config.get("ingress") == "proxy" and b"--with-http_realip_module" not in version:
        raise ValueError("external TLS ingress requires nginx http_realip_module")


def ingress_healthy(config):
    ca = Path(config["root"]) / "tls/ca.pem"
    context = ssl.create_default_context(cafile=str(ca) if ca.exists() else None)
    try:
        with urllib.request.urlopen(config["public_url"] + "/healthz", context=context, timeout=3) as response:
            return response.status == 200
    except OSError:
        return False


def ingress_gateway(config, template):
    mode = config.get("ingress", "https")
    if mode not in ("http", "https", "proxy"):
        raise ValueError("unknown public ingress mode")
    if mode == "https":
        return template
    template = "\n".join(line for line in template.splitlines()
                         if not line.strip().startswith("ssl_")) + "\n"
    template = template.replace("listen 443 ssl;", "listen 443;")
    if mode == "http":
        return template.replace("X-Forwarded-Proto https;", "X-Forwarded-Proto http;")
    proxies = config["proxy_cidrs"]
    if not proxies:
        raise ValueError("external TLS requires trusted proxy addresses")
    for value in proxies:
        network = ipaddress.ip_network(value, strict=True)
        if network.prefixlen == 0:
            raise ValueError("external TLS requires specific proxy addresses")
    # Access checks use the original TCP peer, before real-IP substitution.
    # Otherwise an allow rule would accidentally match the forwarded client.
    policy = "  geo $realip_remote_addr $juex_proxy_allowed {\n    default 0;\n"
    policy += "".join(f"    {value} 1;\n" for value in proxies) + "  }\n"
    template = template.replace("http {", "http {\n" + policy)
    headers = "".join(f"    set_real_ip_from {value};\n" for value in proxies)
    headers += "    real_ip_header X-Real-IP;\n    real_ip_recursive off;\n"
    headers += "    if ($juex_proxy_allowed = 0) { return 403; }\n"
    return template.replace("  server {", "  server {\n" + headers)
