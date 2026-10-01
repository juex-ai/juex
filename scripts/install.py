#!/usr/bin/env python3
"""Install the JueX platform client and native executor from a verified release."""
from __future__ import annotations

import argparse
import hashlib
import io
import json
import os
from pathlib import Path
import platform
import re
import shutil
import tarfile
import tempfile
from urllib.request import urlopen


def download(url: str) -> bytes:
    with urlopen(url, timeout=120) as response:
        return response.read()


def install(prefix: Path, version: str, base_url: str, system: str, arch: str) -> Path:
    if system not in {"linux", "darwin"} or arch not in {"amd64", "arm64"}:
        raise ValueError("release clients support Linux/macOS on amd64 or arm64")
    version = version.removeprefix("v")
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._+-]*", version):
        raise ValueError("invalid release version")
    name = f"juex_{version}_{system}_{arch}"
    archive = name + ".tar.gz"
    base_url = base_url or f"https://github.com/juex-ai/juex/releases/download/v{version}"
    hashes = download(base_url.rstrip("/") + "/checksums.txt").decode()
    expected = [line.split()[0] for line in hashes.splitlines()
                if len(line.split()) == 2 and line.split()[1].lstrip("*") == archive]
    if len(expected) != 1 or not re.fullmatch(r"[a-fA-F0-9]{64}", expected[0]):
        raise ValueError("release checksum is missing or ambiguous")
    payload = download(base_url.rstrip("/") + "/" + archive)
    if hashlib.sha256(payload).hexdigest() != expected[0].lower():
        raise ValueError("release checksum mismatch")
    binaries = {}
    with tarfile.open(fileobj=io.BytesIO(payload), mode="r:gz") as bundle:
        for binary in ("juex", "juex-executor"):
            matches = [m for m in bundle.getmembers() if m.name == f"{name}/bin/{binary}"]
            if len(matches) != 1 or not matches[0].isfile() or matches[0].size > 300 << 20:
                raise ValueError(f"invalid release binary: {binary}")
            with bundle.extractfile(matches[0]) as source:
                binaries[binary] = source.read()
    prefix = prefix.expanduser().absolute()
    releases, bin_dir = prefix / "lib/juex/releases", prefix / "bin"
    releases.mkdir(parents=True, exist_ok=True)
    bin_dir.mkdir(parents=True, exist_ok=True)
    directory = Path(tempfile.mkdtemp(prefix=name + "-", dir=releases))
    try:
        for name, content in binaries.items():
            path = directory / name
            with path.open("xb") as output:
                output.write(content)
                output.flush()
                os.fsync(output.fileno())
            path.chmod(0o755)
        for name in binaries:
            temporary = bin_dir / ("." + name + "-" + directory.name)
            temporary.symlink_to(directory / name)
            os.replace(temporary, bin_dir / name)
    except BaseException:
        # Retain a generation if a completed link already points into it.
        if not any((bin_dir / name).is_symlink() and (bin_dir / name).resolve().parent == directory for name in binaries):
            shutil.rmtree(directory)
        raise
    return directory


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--prefix", type=Path, default=Path.home() / ".local")
    parser.add_argument("--version", default=os.environ.get("JUEX_INSTALL_VERSION", ""))
    parser.add_argument("--release-base-url", default=os.environ.get("JUEX_INSTALL_RELEASE_BASE_URL", ""))
    args = parser.parse_args()
    version = args.version
    if not version:
        if args.release_base_url:
            parser.error("--version is required with --release-base-url")
        version = json.loads(download("https://api.github.com/repos/juex-ai/juex/releases/latest"))["tag_name"]
    system = platform.system().lower()
    arch = {"x86_64": "amd64", "aarch64": "arm64"}.get(platform.machine().lower(), platform.machine().lower())
    directory = install(args.prefix, version, args.release_base_url, system, arch)
    print(f"Installed juex and juex-executor: {directory}")
    print(f"Add {args.prefix.expanduser().absolute() / 'bin'} to PATH. Use juex --server https://YOUR-PLATFORM login --help.")


if __name__ == "__main__":
    main()
