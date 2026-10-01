#!/usr/bin/env python3
"""Build and install local JueX clients without changing any running service."""
import argparse
import os
from pathlib import Path
import shutil
import subprocess
import tempfile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--prefix', type=Path, default=Path.home() / '.local')
    args = parser.parse_args()
    root = Path(__file__).resolve().parent.parent
    subprocess.run(['make', 'build-clients'], cwd=root, check=True)
    directory = args.prefix.expanduser().absolute() / 'bin'
    directory.mkdir(parents=True, exist_ok=True)
    for name in ('juex', 'juex-executor'):
        fd, temporary = tempfile.mkstemp(prefix='.'+name+'-', dir=directory)
        os.close(fd)
        try:
            shutil.copyfile(root / 'dist' / name, temporary)
            os.chmod(temporary, 0o755)
            os.replace(temporary, directory / name)
        finally:
            Path(temporary).unlink(missing_ok=True)
    print(f'Installed clients in {directory}')


if __name__ == '__main__':
    main()
