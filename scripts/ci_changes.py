"""Select the documentation-only CI path from a complete GitHub event diff."""

import json
import os
from pathlib import Path
import re
import subprocess
import sys


# These Markdown files are runtime resources, not just documentation.
RUNTIME_PATHS = (
    b"internal/features/skills/builtin/",
    b"internal/entrypoints/webassets/dist/",
)


def docs_only(event_name: str, event: dict, root: Path) -> bool:
    try:
        if event_name == "pull_request":
            base = event["pull_request"]["base"]["sha"]
            head = event["pull_request"]["head"]["sha"]
            separator = "..."
        elif event_name == "push":
            base, head = event["before"], event["after"]
            separator = ".."
        else:
            return False
        if any(not re.fullmatch(r"[0-9a-f]{40}", sha) or sha == "0" * 40
               for sha in (base, head)):
            return False
        # Disable rename detection so a code-to-Markdown rename includes the
        # deleted code path. NUL delimiters preserve spaces/newlines in names.
        diff = subprocess.run(
            ["git", "diff", "--name-only", "--no-renames", "-z",
             f"{base}{separator}{head}", "--"],
            cwd=root, check=True, capture_output=True,
        ).stdout
        paths = [path for path in diff.split(b"\0") if path]
        return bool(paths) and all(
            path.endswith(b".md") and not path.startswith(RUNTIME_PATHS)
            for path in paths
        )
    except (KeyError, TypeError, OSError, subprocess.CalledProcessError):
        print("Unable to determine changed paths; running full CI.", file=sys.stderr)
        return False


def main() -> None:
    result = False
    try:
        event = json.loads(Path(os.environ["GITHUB_EVENT_PATH"]).read_text())
        result = docs_only(os.environ["GITHUB_EVENT_NAME"], event, Path.cwd())
    except (KeyError, OSError, ValueError):
        print("Unable to read the GitHub event; running full CI.", file=sys.stderr)
    with Path(os.environ["GITHUB_OUTPUT"]).open("a") as output:
        output.write(f"docs_only={str(result).lower()}\n")
    print("Documentation-only change." if result else "Full CI required.")


if __name__ == "__main__":
    main()
