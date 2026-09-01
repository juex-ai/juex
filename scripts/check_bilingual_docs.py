#!/usr/bin/env python3
"""Check bilingual pairing and navigation for tracked Markdown documents."""

from __future__ import annotations

import argparse
import re
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path, PurePosixPath


DEFAULT_WHITELIST = PurePosixPath("docs/bilingual-whitelist.txt")
TOP_SECTION_LINES = 12


@dataclass(frozen=True)
class CheckResult:
    errors: list[str]
    tracked_count: int
    pair_count: int
    whitelist_count: int


def _git(root: Path, *arguments: str) -> str:
    completed = subprocess.run(
        ["git", *arguments],
        cwd=root,
        check=False,
        capture_output=True,
        text=True,
    )
    if completed.returncode != 0:
        detail = completed.stderr.strip() or completed.stdout.strip()
        raise RuntimeError(f"git {' '.join(arguments)} failed: {detail}")
    return completed.stdout


def _tracked_markdown(root: Path) -> set[str]:
    output = _git(root, "ls-files", "-z", "--", "*.md")
    return {PurePosixPath(item).as_posix() for item in output.split("\0") if item}


def _load_whitelist(root: Path, relative_path: PurePosixPath) -> tuple[set[str], list[str]]:
    whitelist_path = root / relative_path
    if not whitelist_path.is_file():
        return set(), [f"whitelist file does not exist: {relative_path.as_posix()}"]

    entries: set[str] = set()
    errors: list[str] = []
    for line_number, raw_line in enumerate(
        whitelist_path.read_text(encoding="utf-8").splitlines(), start=1
    ):
        entry = raw_line.strip()
        if not entry or entry.startswith("#"):
            continue
        path = PurePosixPath(entry)
        if path.is_absolute() or ".." in path.parts or path.as_posix() != entry:
            errors.append(
                f"{relative_path.as_posix()}:{line_number}: invalid exact path: {entry}"
            )
            continue
        if not entry.endswith(".md"):
            errors.append(
                f"{relative_path.as_posix()}:{line_number}: whitelist entry is not Markdown: {entry}"
            )
            continue
        if entry in entries:
            errors.append(
                f"{relative_path.as_posix()}:{line_number}: duplicate whitelist entry: {entry}"
            )
            continue
        entries.add(entry)
    return entries, errors


def _english_path(chinese_path: str) -> str:
    return f"{chinese_path[:-len('.zh.md')]}.md"


def _chinese_path(english_path: str) -> str:
    return f"{english_path[:-len('.md')]}.zh.md"


def _top_section(document: Path) -> str:
    lines = document.read_text(encoding="utf-8").splitlines()
    start = 0
    if lines and lines[0].strip() == "---":
        for index, line in enumerate(lines[1:], start=1):
            if line.strip() == "---":
                start = index + 1
                break
    return "\n".join(lines[start : start + TOP_SECTION_LINES])


def _navigation_markdown(document: Path) -> str:
    top = re.sub(r"<!--.*?(?:-->|$)", "", _top_section(document), flags=re.DOTALL)
    visible_lines: list[str] = []
    fence_character = ""
    fence_length = 0

    for line in top.splitlines():
        fence = re.match(
            r"^\s*(?:(?:[-+*]|\d+[.)])\s+)?(`{3,}|~{3,})",
            line,
        )
        if fence:
            marker = fence.group(1)
            if not fence_character:
                fence_character = marker[0]
                fence_length = len(marker)
            elif marker[0] == fence_character and len(marker) >= fence_length:
                fence_character = ""
                fence_length = 0
            continue
        if fence_character:
            continue
        visible_lines.append(re.sub(r"`+[^`\n]*`+", "", line))

    return "\n".join(visible_lines)


def _has_peer_link(document: Path, peer_name: str) -> bool:
    target = re.escape(peer_name)
    pattern = re.compile(
        rf"(?<!!)\[[^\]\n]+\]\(\s*(?:\./)?{target}(?:#[^)\s]+)?"
        rf"(?:\s+\"[^\"]*\")?\s*\)"
    )
    return pattern.search(_navigation_markdown(document)) is not None


def audit_repository(
    root: Path,
    whitelist_path: PurePosixPath = DEFAULT_WHITELIST,
) -> CheckResult:
    root = root.resolve()
    tracked = _tracked_markdown(root)
    whitelist, errors = _load_whitelist(root, whitelist_path)

    for entry in sorted(whitelist - tracked):
        errors.append(f"whitelist entry is not a tracked Markdown file: {entry}")

    for entry in sorted(whitelist & tracked):
        peer = _english_path(entry) if entry.endswith(".zh.md") else _chinese_path(entry)
        if peer in tracked:
            errors.append(
                f"whitelist entry has a tracked language peer: {entry} (peer: {peer})"
            )

    pairs: set[tuple[str, str]] = set()
    for document in sorted(tracked - whitelist):
        if document.endswith(".zh.md"):
            english = _english_path(document)
            if english not in tracked:
                errors.append(f"missing English peer: {english} (for {document})")
                continue
            if english not in whitelist:
                pairs.add((english, document))
            continue

        chinese = _chinese_path(document)
        if chinese not in tracked:
            errors.append(f"missing Chinese peer: {chinese} (for {document})")
            continue
        if chinese not in whitelist:
            pairs.add((document, chinese))

    for english, chinese in sorted(pairs):
        english_path = root / english
        chinese_path = root / chinese
        if not _has_peer_link(english_path, PurePosixPath(chinese).name):
            errors.append(
                f"{english}: missing top link to {PurePosixPath(chinese).name}"
            )
        if not _has_peer_link(chinese_path, PurePosixPath(english).name):
            errors.append(
                f"{chinese}: missing top link to {PurePosixPath(english).name}"
            )

    return CheckResult(
        errors=sorted(errors),
        tracked_count=len(tracked),
        pair_count=len(pairs),
        whitelist_count=len(whitelist),
    )


def check_repository(root: Path) -> list[str]:
    return audit_repository(root).errors


def _repository_root(explicit_root: str | None) -> Path:
    if explicit_root:
        return Path(explicit_root).resolve()
    return Path(_git(Path.cwd(), "rev-parse", "--show-toplevel").strip())


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Check Git-tracked Markdown files for bilingual peers and links."
    )
    parser.add_argument("--root", help="repository root; defaults to the current Git root")
    parser.add_argument(
        "--whitelist",
        default=DEFAULT_WHITELIST.as_posix(),
        help="exact-path whitelist relative to the repository root",
    )
    arguments = parser.parse_args()

    try:
        result = audit_repository(
            _repository_root(arguments.root), PurePosixPath(arguments.whitelist)
        )
    except (OSError, RuntimeError, UnicodeError) as error:
        print(f"documentation bilingual check failed: {error}", file=sys.stderr)
        return 2

    if result.errors:
        print("documentation bilingual check failed:", file=sys.stderr)
        for error in result.errors:
            print(f"- {error}", file=sys.stderr)
        return 1

    print(
        "documentation bilingual check passed: "
        f"{result.pair_count} pairs, {result.whitelist_count} whitelisted paths, "
        f"{result.tracked_count} tracked Markdown files"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
