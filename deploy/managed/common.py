"""Private deployment filesystem and subprocess operations."""

import hashlib
import json
import os
from pathlib import Path
import subprocess

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
