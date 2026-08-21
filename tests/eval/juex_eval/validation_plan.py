"""Deterministic Git-diff-driven validation planning."""

from __future__ import annotations

import hashlib
import json
import pathlib
import subprocess
from dataclasses import dataclass
from typing import Any, Iterable


SCHEMA_VERSION = 1
CANDIDATE_FLAGS = ("race", "web")
FINAL_FLAGS = ("compaction", "integration", "provider-smoke")
CONSERVATIVE_PACKAGES = ("./...",)
CONSERVATIVE_CANDIDATE_FLAGS = CANDIDATE_FLAGS
CONSERVATIVE_FINAL_FLAGS = FINAL_FLAGS


@dataclass(frozen=True)
class ChangedFile:
    status: str
    path: str
    old_path: str | None = None

    def normalized(self) -> ChangedFile:
        return ChangedFile(
            status=_normalized_status(self.status),
            path=_normalized_path(self.path),
            old_path=_normalized_path(self.old_path) if self.old_path else None,
        )

    def as_dict(self) -> dict[str, Any]:
        return {"status": self.status, "path": self.path, "old_path": self.old_path}


@dataclass(frozen=True)
class MatchedRule:
    rule_id: str
    description: str
    files: tuple[str, ...]
    focused_packages: tuple[str, ...] = ()
    candidate_flags: tuple[str, ...] = ()
    final_flags: tuple[str, ...] = ()

    def as_dict(self) -> dict[str, Any]:
        return {
            "rule_id": self.rule_id,
            "description": self.description,
            "files": list(self.files),
            "focused_packages": list(self.focused_packages),
            "candidate_flags": list(self.candidate_flags),
            "final_flags": list(self.final_flags),
        }


@dataclass(frozen=True)
class ValidationPlan:
    mode: str
    base_sha: str
    head_sha: str
    dirty: bool
    changed_files: tuple[ChangedFile, ...]
    matched_rules: tuple[MatchedRule, ...]
    focused_packages: tuple[str, ...]
    candidate_flags: tuple[str, ...]
    final_flags: tuple[str, ...]
    fingerprint: str

    def as_dict(self) -> dict[str, Any]:
        return {
            "schema_version": SCHEMA_VERSION,
            "mode": self.mode,
            "base_sha": self.base_sha,
            "head_sha": self.head_sha,
            "dirty": self.dirty,
            "changed_files": [row.as_dict() for row in self.changed_files],
            "matched_rules": [row.as_dict() for row in self.matched_rules],
            "focused_packages": list(self.focused_packages),
            "candidate_flags": list(self.candidate_flags),
            "final_flags": list(self.final_flags),
            "fingerprint": self.fingerprint,
        }


@dataclass
class _RuleAccumulator:
    description: str
    files: set[str]
    focused_packages: set[str]
    candidate_flags: set[str]
    final_flags: set[str]


RULE_DESCRIPTIONS = {
    "frontend": "Frontend source changes require the web gate and live web-runtime coverage.",
    "embedded-web": "Embedded web assets cross the frontend, Go embed, server, and binary boundary.",
    "go-package": "Go source changes require the containing package tests.",
    "cross-boundary": "Cross-package runtime paths require the deterministic end-to-end suite.",
    "race-sensitive": "Concurrent or shared-state paths require race validation.",
    "live-runtime": "Provider, protocol, CLI, session, tool-call, or web-runtime paths require live gates.",
    "compaction": "Compaction behavior requires the live compaction evaluator.",
    "conservative-harness": "Build, module, CI, or validation-harness changes require the full plan.",
    "documentation-only": "Documentation-only changes are explained without inventing code gates.",
    "conservative-unknown": "Unclassified paths require the full conservative plan.",
    "explicit-cli-override": "Explicit CLI flags conservatively replace focused scope or add gates.",
    "final-baseline": "Final verification always requires live integration and provider smoke.",
}

CROSS_BOUNDARY_PREFIXES = (
    "cmd/juex/",
    "internal/app/",
    "internal/cli/",
    "internal/config/",
    "internal/fleetweb/",
    "internal/llm/",
    "internal/mcp/",
    "internal/providerreadiness/",
    "internal/runtime/",
    "internal/session/",
    "internal/tools/",
)

RACE_PREFIXES = (
    "internal/agentstate/",
    "internal/app/",
    "internal/chunkedwrite/",
    "internal/endpoint/",
    "internal/events/",
    "internal/fleet/",
    "internal/fleetweb/",
    "internal/homestore/",
    "internal/mcp/",
    "internal/observable/",
    "internal/runtime/",
    "internal/session/",
    "internal/statusstream/",
    "internal/tools/",
    "internal/web/",
)

LIVE_PREFIXES = (
    "cmd/juex/",
    "frontend/",
    "internal/app/",
    "internal/cli/",
    "internal/config/",
    "internal/fleetweb/",
    "internal/llm/",
    "internal/mcp/",
    "internal/providerreadiness/",
    "internal/runtime/",
    "internal/session/",
    "internal/tools/",
    "internal/web/",
    "tests/e2e/",
)

CONSERVATIVE_EXACT_PATHS = {
    ".golangci.yml",
    ".goreleaser.yml",
    "CLI_CONFIG",
    "Makefile",
    "go.mod",
    "go.sum",
    "mise.toml",
    "pyproject.toml",
    "uv.lock",
}

CONSERVATIVE_PREFIXES = (
    ".github/workflows/",
    "release/",
    "scripts/",
    "tests/eval/",
)


def plan_for_changes(
    mode: str,
    changed_files: Iterable[ChangedFile],
    *,
    base_sha: str,
    head_sha: str,
    dirty: bool,
    repo_root: pathlib.Path | None = None,
) -> ValidationPlan:
    if mode not in {"focused", "candidate", "final"}:
        raise ValueError(f"unsupported validation plan mode: {mode}")
    changes = _merge_changed_files(changed_files)
    accumulators: dict[str, _RuleAccumulator] = {}

    def add(
        rule_id: str,
        changed: ChangedFile,
        *,
        packages: Iterable[str] = (),
        candidate: Iterable[str] = (),
        final: Iterable[str] = (),
    ) -> None:
        row = accumulators.setdefault(
            rule_id,
            _RuleAccumulator(RULE_DESCRIPTIONS[rule_id], set(), set(), set(), set()),
        )
        row.files.add(_display_path(changed))
        row.focused_packages.update(packages)
        row.candidate_flags.update(candidate)
        row.final_flags.update(final)

    for changed in changes:
        paths = tuple(path for path in (changed.path, changed.old_path) if path)
        matched = False

        if all(_is_documentation_path(path) for path in paths):
            add("documentation-only", changed)
            continue

        if any(path.startswith("frontend/") for path in paths):
            add("frontend", changed, candidate=("web",), final=("integration", "provider-smoke"))
            matched = True

        if any(path.startswith("internal/web/") for path in paths):
            add(
                "embedded-web",
                changed,
                packages=("./internal/web", "./tests/e2e"),
                candidate=("race", "web"),
                final=("integration", "provider-smoke"),
            )
            matched = True

        go_packages = _go_packages_for_change(changed, repo_root)
        if go_packages:
            add("go-package", changed, packages=sorted(go_packages))
            matched = True

        if any(path.startswith(CROSS_BOUNDARY_PREFIXES) for path in paths):
            add("cross-boundary", changed, packages=("./tests/e2e",))
            matched = True

        if any(path.startswith(RACE_PREFIXES) for path in paths):
            add("race-sensitive", changed, candidate=("race",))
            matched = True

        if any(_is_live_runtime_path(path) for path in paths):
            add("live-runtime", changed, final=("integration", "provider-smoke"))
            matched = True

        if any(_is_compaction_path(path) for path in paths):
            add("compaction", changed, final=("compaction", "integration", "provider-smoke"))
            matched = True

        if any(_is_conservative_harness_path(path) for path in paths):
            add(
                "conservative-harness",
                changed,
                packages=CONSERVATIVE_PACKAGES,
                candidate=CONSERVATIVE_CANDIDATE_FLAGS,
                final=CONSERVATIVE_FINAL_FLAGS,
            )
            matched = True

        if not matched:
            add(
                "conservative-unknown",
                changed,
                packages=CONSERVATIVE_PACKAGES,
                candidate=CONSERVATIVE_CANDIDATE_FLAGS,
                final=CONSERVATIVE_FINAL_FLAGS,
            )

    baseline = accumulators.setdefault(
        "final-baseline",
        _RuleAccumulator(RULE_DESCRIPTIONS["final-baseline"], set(), set(), set(), set()),
    )
    baseline.files.add("<verification contract>")
    baseline.final_flags.update(("integration", "provider-smoke"))

    matched_rules = tuple(
        MatchedRule(
            rule_id=rule_id,
            description=row.description,
            files=tuple(sorted(row.files)),
            focused_packages=tuple(sorted(row.focused_packages)),
            candidate_flags=tuple(sorted(row.candidate_flags)),
            final_flags=tuple(sorted(row.final_flags)),
        )
        for rule_id, row in sorted(accumulators.items())
    )
    focused_packages = tuple(sorted({item for row in matched_rules for item in row.focused_packages}))
    if "./..." in focused_packages:
        focused_packages = ("./...",)
    candidate_flags = tuple(sorted({item for row in matched_rules for item in row.candidate_flags}))
    final_flags = tuple(sorted({item for row in matched_rules for item in row.final_flags}))
    fingerprint = _plan_fingerprint(
        changes,
        matched_rules,
        focused_packages,
        candidate_flags,
        final_flags,
    )
    return ValidationPlan(
        mode=mode,
        base_sha=base_sha,
        head_sha=head_sha,
        dirty=dirty,
        changed_files=changes,
        matched_rules=matched_rules,
        focused_packages=focused_packages,
        candidate_flags=candidate_flags,
        final_flags=final_flags,
        fingerprint=fingerprint,
    )


def with_cli_overrides(
    plan: ValidationPlan,
    *,
    focused_packages: Iterable[str] = (),
    race: bool = False,
    web: bool = False,
    compaction: bool = False,
) -> ValidationPlan:
    packages = tuple(sorted({str(item).strip() for item in focused_packages if str(item).strip()}))
    has_focused_override = bool(packages)
    candidate_flags = set(plan.candidate_flags)
    final_flags = set(plan.final_flags)
    if packages:
        if "./..." in packages:
            packages = ("./...",)
    else:
        packages = plan.focused_packages
        if race:
            candidate_flags.add("race")
        if web:
            candidate_flags.add("web")
        if compaction:
            final_flags.update(CONSERVATIVE_FINAL_FLAGS)
    override = MatchedRule(
        rule_id="explicit-cli-override",
        description=RULE_DESCRIPTIONS["explicit-cli-override"],
        files=("<command line>",),
        focused_packages=packages if has_focused_override else (),
        candidate_flags=tuple(sorted(flag for flag in candidate_flags if flag not in plan.candidate_flags)),
        final_flags=tuple(sorted(flag for flag in final_flags if flag not in plan.final_flags)),
    )
    has_override = bool(override.focused_packages or override.candidate_flags or override.final_flags)
    rule_rows = [*plan.matched_rules]
    if has_override:
        rule_rows.append(override)
    matched_rules = tuple(sorted(rule_rows, key=lambda row: row.rule_id))
    candidate = tuple(sorted(candidate_flags))
    final = tuple(sorted(final_flags))
    fingerprint = _plan_fingerprint(
        plan.changed_files,
        matched_rules,
        tuple(packages),
        candidate,
        final,
    )
    return ValidationPlan(
        mode=plan.mode,
        base_sha=plan.base_sha,
        head_sha=plan.head_sha,
        dirty=plan.dirty,
        changed_files=plan.changed_files,
        matched_rules=matched_rules,
        focused_packages=tuple(packages),
        candidate_flags=candidate,
        final_flags=final,
        fingerprint=fingerprint,
    )


def collect_plan(repo_root: pathlib.Path, mode: str, *, base: str | None = None) -> ValidationPlan:
    repo_root = repo_root.resolve()
    head_sha = _git_text(repo_root, ["rev-parse", "HEAD"], "git rev-parse HEAD failed")
    dirty = bool(_git_text(repo_root, ["status", "--porcelain", "--untracked-files=all"], "git status failed"))
    if mode in {"candidate", "final"}:
        if dirty:
            raise ValueError(f"{mode} validation plan requires a clean worktree")
        base_sha = _resolve_base(repo_root, base, head_sha)
        changes = _diff_changes(repo_root, [base_sha, head_sha])
    elif mode == "focused":
        base_sha = _resolve_commit(repo_root, base) if base else head_sha
        changes: list[ChangedFile] = []
        if base:
            changes.extend(_diff_changes(repo_root, [base_sha, head_sha]))
        changes.extend(_diff_changes(repo_root, ["--cached", head_sha]))
        changes.extend(_diff_changes(repo_root, []))
        changes.extend(ChangedFile("U", path) for path in _untracked_paths(repo_root))
    else:
        raise ValueError(f"unsupported validation plan mode: {mode}")
    return plan_for_changes(
        mode,
        changes,
        base_sha=base_sha,
        head_sha=head_sha,
        dirty=dirty,
        repo_root=repo_root,
    )


def write_plan(output_dir: pathlib.Path, plan: ValidationPlan) -> tuple[pathlib.Path, pathlib.Path]:
    output_dir.mkdir(parents=True, exist_ok=True)
    json_path = output_dir / "plan.json"
    markdown_path = output_dir / "plan.md"
    json_path.write_text(json.dumps(plan.as_dict(), ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    markdown_path.write_text(render_markdown(plan), encoding="utf-8")
    return json_path, markdown_path


def render_markdown(plan: ValidationPlan) -> str:
    lines = [
        "# Validation Plan",
        "",
        f"- Schema version: `{SCHEMA_VERSION}`",
        f"- Mode: `{plan.mode}`",
        f"- Base SHA: `{plan.base_sha}`",
        f"- Head SHA: `{plan.head_sha}`",
        f"- Dirty: {str(plan.dirty).lower()}",
        f"- Fingerprint: `{plan.fingerprint}`",
        "",
        "## Changed Files",
        "",
    ]
    if plan.changed_files:
        for changed in plan.changed_files:
            rename = f" (from `{changed.old_path}`)" if changed.old_path else ""
            lines.append(f"- `{changed.status}` `{changed.path}`{rename}")
    else:
        lines.append("- None.")
    lines.extend(["", "## Matched Rules", ""])
    if plan.matched_rules:
        for rule in plan.matched_rules:
            lines.append(f"- `{rule.rule_id}`: {rule.description}")
            lines.append(f"  - Files: {', '.join(f'`{path}`' for path in rule.files)}")
            gates = [
                *(f"focused:{item}" for item in rule.focused_packages),
                *(f"candidate:{item}" for item in rule.candidate_flags),
                *(f"final:{item}" for item in rule.final_flags),
            ]
            lines.append(f"  - Gates: {', '.join(f'`{gate}`' for gate in gates) or 'none'}")
    else:
        lines.append("- None; the diff is empty.")
    lines.extend(["", "## Selected Gates", ""])
    lines.extend(_gate_explanation("Focused package", plan.focused_packages, plan.matched_rules, "focused_packages"))
    lines.extend(_gate_explanation("Candidate flag", plan.candidate_flags, plan.matched_rules, "candidate_flags"))
    lines.extend(_gate_explanation("Final flag", plan.final_flags, plan.matched_rules, "final_flags"))
    if not plan.focused_packages and not plan.candidate_flags and not plan.final_flags:
        lines.append("- No code gates selected (empty or documentation-only diff).")
    return "\n".join(lines) + "\n"


def _gate_explanation(
    label: str,
    values: tuple[str, ...],
    rules: tuple[MatchedRule, ...],
    field: str,
) -> list[str]:
    lines: list[str] = []
    for value in values:
        causes = [rule for rule in rules if value in getattr(rule, field)]
        rendered = "; ".join(
            f"`{rule.rule_id}` via {', '.join(f'`{path}`' for path in rule.files)}" for rule in causes
        )
        lines.append(f"- {label} `{value}`: {rendered}")
    return lines


def _merge_changed_files(changed_files: Iterable[ChangedFile]) -> tuple[ChangedFile, ...]:
    by_path: dict[str, tuple[set[str], set[str]]] = {}
    for raw in changed_files:
        changed = raw.normalized()
        statuses, old_paths = by_path.setdefault(changed.path, (set(), set()))
        statuses.update(changed.status)
        if changed.old_path:
            old_paths.add(changed.old_path)
    return tuple(
        ChangedFile(
            status="".join(sorted(statuses)),
            path=path,
            old_path=sorted(old_paths)[0] if old_paths else None,
        )
        for path, (statuses, old_paths) in sorted(by_path.items())
    )


def _plan_fingerprint(
    changed_files: tuple[ChangedFile, ...],
    matched_rules: tuple[MatchedRule, ...],
    focused_packages: tuple[str, ...],
    candidate_flags: tuple[str, ...],
    final_flags: tuple[str, ...],
) -> str:
    projection = {
        "changed_files": [row.as_dict() for row in changed_files],
        "matched_rules": [row.as_dict() for row in matched_rules],
        "focused_packages": list(focused_packages),
        "candidate_flags": list(candidate_flags),
        "final_flags": list(final_flags),
    }
    return "sha256:" + hashlib.sha256(
        json.dumps(projection, sort_keys=True, separators=(",", ":")).encode("utf-8")
    ).hexdigest()


def candidate_fingerprint(plan: ValidationPlan) -> str:
    candidate_rules = [
        {
            "rule_id": row.rule_id,
            "files": list(row.files),
            "candidate_flags": list(row.candidate_flags),
        }
        for row in plan.matched_rules
        if row.candidate_flags
    ]
    projection = {
        "changed_files": [row.as_dict() for row in plan.changed_files],
        "candidate_rules": candidate_rules,
        "candidate_flags": list(plan.candidate_flags),
    }
    return "sha256:" + hashlib.sha256(
        json.dumps(projection, sort_keys=True, separators=(",", ":")).encode("utf-8")
    ).hexdigest()


def _normalized_status(status: str) -> str:
    letters = "".join(character for character in status.upper() if character.isalpha())
    return letters or "M"


def _normalized_path(path: str) -> str:
    normalized = str(path).replace("\\", "/").removeprefix("./")
    if not normalized or normalized.startswith("/") or ".." in pathlib.PurePosixPath(normalized).parts:
        raise ValueError(f"invalid repository-relative path: {path}")
    return normalized


def _display_path(changed: ChangedFile) -> str:
    return f"{changed.old_path} -> {changed.path}" if changed.old_path else changed.path


def _go_package(path: str) -> str | None:
    parent = pathlib.PurePosixPath(path).parent.as_posix()
    if parent == ".":
        return "./"
    return f"./{parent}"


def _go_packages_for_change(
    changed: ChangedFile,
    repo_root: pathlib.Path | None,
) -> set[str]:
    packages: set[str] = set()
    deleted = "D" in changed.status
    if changed.path.endswith(".go"):
        if deleted:
            packages.add(_existing_go_package_or_full(changed.path, repo_root))
        else:
            package = _go_package(changed.path)
            if package:
                packages.add(package)
    if changed.old_path and changed.old_path.endswith(".go"):
        old_package = _go_package(changed.old_path)
        new_package = _go_package(changed.path) if changed.path.endswith(".go") else None
        if old_package != new_package:
            packages.add(_existing_go_package_or_full(changed.old_path, repo_root))
    return packages


def _existing_go_package_or_full(path: str, repo_root: pathlib.Path | None) -> str:
    package = _go_package(path)
    if package and repo_root is not None:
        directory = repo_root / pathlib.PurePosixPath(path).parent
        if directory.is_dir() and any(directory.glob("*.go")):
            return package
    return "./..."


def _is_live_runtime_path(path: str) -> bool:
    lowered = path.lower().replace("-", "_")
    return path.startswith(LIVE_PREFIXES) or any(
        marker in lowered for marker in ("provider", "protocol", "reasoning", "tool_call")
    )


def _is_compaction_path(path: str) -> bool:
    lowered = path.lower()
    return (
        "compact" in lowered
        or "context_projection" in lowered
        or path.startswith("internal/runtime/contextbudget/")
    )


def _is_conservative_harness_path(path: str) -> bool:
    return path in CONSERVATIVE_EXACT_PATHS or path.startswith(CONSERVATIVE_PREFIXES)


def _is_documentation_path(path: str) -> bool:
    name = pathlib.PurePosixPath(path).name
    documentation_names = {
        "AGENTS.md",
        "ARCHITECTURE.md",
        "CLAUDE.md",
        "DESIGN.md",
        "DOMAIN.md",
        "LICENSE",
        "NOTICE",
        "PHILOSOPHY.md",
        "README.md",
    }
    return (
        path.startswith("docs/")
        or name in documentation_names
    )


def _resolve_base(repo_root: pathlib.Path, base: str | None, head_sha: str) -> str:
    if base:
        return _resolve_commit(repo_root, base)
    return _git_text(repo_root, ["merge-base", "origin/main", head_sha], "git merge-base origin/main HEAD failed")


def _resolve_commit(repo_root: pathlib.Path, ref: str | None) -> str:
    if not ref:
        raise ValueError("base commit is required")
    return _git_text(repo_root, ["rev-parse", "--verify", f"{ref}^{{commit}}"], f"cannot resolve base commit: {ref}")


def _diff_changes(repo_root: pathlib.Path, revisions: list[str]) -> list[ChangedFile]:
    output = _git_bytes(
        repo_root,
        ["diff", "--name-status", "-z", "--find-renames", *revisions, "--"],
        "git diff failed",
    )
    fields = output.split(b"\0")
    if fields and not fields[-1]:
        fields.pop()
    changes: list[ChangedFile] = []
    index = 0
    while index < len(fields):
        status = fields[index].decode("utf-8", "surrogateescape")
        index += 1
        kind = status[:1]
        if kind in {"R", "C"}:
            if index + 1 >= len(fields):
                raise ValueError("malformed rename/copy entry from git diff")
            old_path = fields[index].decode("utf-8", "surrogateescape")
            new_path = fields[index + 1].decode("utf-8", "surrogateescape")
            index += 2
            changes.append(ChangedFile(kind, new_path, old_path))
        else:
            if index >= len(fields):
                raise ValueError("malformed entry from git diff")
            path = fields[index].decode("utf-8", "surrogateescape")
            index += 1
            changes.append(ChangedFile(kind, path))
    return changes


def _untracked_paths(repo_root: pathlib.Path) -> list[str]:
    output = _git_bytes(
        repo_root,
        ["ls-files", "--others", "--exclude-standard", "-z"],
        "git ls-files failed",
    )
    return sorted(
        field.decode("utf-8", "surrogateescape")
        for field in output.split(b"\0")
        if field
    )


def _git_text(repo_root: pathlib.Path, args: list[str], error: str) -> str:
    completed = subprocess.run(
        ["git", *args],
        cwd=repo_root,
        check=False,
        capture_output=True,
        text=True,
    )
    if completed.returncode:
        detail = completed.stderr.strip()
        raise ValueError(f"{error}: {detail}" if detail else error)
    return completed.stdout.strip()


def _git_bytes(repo_root: pathlib.Path, args: list[str], error: str) -> bytes:
    completed = subprocess.run(
        ["git", *args],
        cwd=repo_root,
        check=False,
        capture_output=True,
    )
    if completed.returncode:
        detail = completed.stderr.decode("utf-8", "replace").strip()
        raise ValueError(f"{error}: {detail}" if detail else error)
    return completed.stdout
