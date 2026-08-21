"""Commit-bound candidate and final verification records."""

from __future__ import annotations

import hashlib
import json
import pathlib
import platform
import re
import shlex
import subprocess
import sys
import time
from dataclasses import dataclass
from typing import Any, Iterable, Protocol


SCHEMA_VERSION = 1
REPORT_KIND = "development-validation"
CANDIDATE_RECORD_NAME = "candidate-record.json"
GO_ENV_FINGERPRINT_KEYS = (
    "GOOS",
    "GOARCH",
    "GO386",
    "GOAMD64",
    "GOARM",
    "GOARM64",
    "GOMIPS",
    "GOMIPS64",
    "GOPPC64",
    "GORISCV64",
    "GOWASM",
    "CGO_ENABLED",
    "CC",
    "CXX",
    "CGO_CFLAGS",
    "CGO_CPPFLAGS",
    "CGO_CXXFLAGS",
    "CGO_LDFLAGS",
    "GOENV",
    "GOFLAGS",
    "GOEXPERIMENT",
    "GOWORK",
    "GOTOOLCHAIN",
)


class StepLike(Protocol):
    label: str
    command: list[str]
    test_environment: bool
    environment: dict[str, str] | None


@dataclass(frozen=True)
class RepositorySnapshot:
    head_sha: str
    branch: str
    dirty: bool


@dataclass(frozen=True)
class ReuseDecision:
    source: pathlib.Path | None
    reusable: dict[str, dict[str, Any]]
    invalidated: list[dict[str, Any]]


def repository_snapshot(repo_root: pathlib.Path) -> RepositorySnapshot:
    head_sha = _git_output(repo_root, ["git", "rev-parse", "HEAD"], "git rev-parse HEAD failed")
    status = _git_output(
        repo_root,
        ["git", "status", "--porcelain", "--untracked-files=all"],
        "git status failed",
    )
    branch = _git_output(repo_root, ["git", "branch", "--show-current"], "git branch failed")
    return RepositorySnapshot(head_sha=head_sha, branch=branch, dirty=bool(status))


def _git_output(repo_root: pathlib.Path, command: list[str], error: str) -> str:
    completed = subprocess.run(
        command,
        cwd=repo_root,
        check=False,
        capture_output=True,
        text=True,
    )
    if completed.returncode:
        raise ValueError(error)
    return completed.stdout.strip()


def default_report_dir(report_root: pathlib.Path, snapshot: RepositorySnapshot, run_id: str) -> pathlib.Path:
    validate_run_id(run_id)
    if not re.fullmatch(r"[0-9a-fA-F]{40,64}", snapshot.head_sha):
        raise ValueError(f"invalid full commit SHA: {snapshot.head_sha}")
    return report_root / REPORT_KIND / snapshot.head_sha.lower() / run_id


def validate_run_id(run_id: str) -> None:
    if not re.fullmatch(r"[A-Za-z0-9_-][A-Za-z0-9._-]*", run_id):
        raise ValueError(f"run_id must be a safe basename: {run_id}")


def stable_fingerprint(value: Any) -> str:
    encoded = json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
    return "sha256:" + hashlib.sha256(encoded).hexdigest()


def plan_fingerprint(steps: Iterable[StepLike]) -> str:
    projection = [
        {
            "label": step.label,
            "command": [str(part) for part in step.command],
            "test_environment": bool(step.test_environment),
            "environment": dict(sorted((step.environment or {}).items())),
        }
        for step in steps
    ]
    return stable_fingerprint(projection)


def environment_fingerprint(*, web: bool) -> str:
    projection = {
        "platform": sys.platform,
        "platform_release": platform.release(),
        "machine": platform.machine(),
        "python": platform.python_version(),
        "go_version": _version_output(["go", "version"]),
        "go_environment": _version_output(["go", "env", *GO_ENV_FINGERPRINT_KEYS]),
        "make": _version_output(["make", "--version"], first_line=True),
    }
    if web:
        projection["node"] = _version_output(["node", "--version"])
        projection["pnpm"] = _version_output(["pnpm", "--version"])
    return stable_fingerprint(projection)


def artifact_fingerprints(repo_root: pathlib.Path) -> dict[str, dict[str, Any]]:
    relative = pathlib.Path("dist") / "juex"
    path = repo_root / relative
    if not path.is_file():
        return {}
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return {
        relative.as_posix(): {
            "sha256": "sha256:" + digest.hexdigest(),
            "size": path.stat().st_size,
        }
    }


def _version_output(command: list[str], *, first_line: bool = False) -> str:
    try:
        completed = subprocess.run(command, check=False, capture_output=True, text=True)
    except OSError as exc:
        return f"unavailable:{exc.__class__.__name__}"
    output = (completed.stdout or completed.stderr).strip()
    if first_line:
        output = output.splitlines()[0] if output else ""
    return f"exit={completed.returncode}:{output}"


def planned_step_record(step: StepLike) -> dict[str, Any]:
    return {
        "label": step.label,
        "command": [str(part) for part in step.command],
        "started_at": None,
        "duration": None,
        "exit_status": None,
        "log": None,
        "outcome": "not_run",
    }


def build_record(
    *,
    tier: str,
    run_id: str,
    snapshot: RepositorySnapshot,
    plan_fingerprint: str,
    environment_fingerprint: str,
    steps: list[dict[str, Any]],
    status: str,
    reused: list[str],
    executed: list[str],
    invalidated: list[dict[str, Any]],
    provider_summary: dict[str, Any] | None = None,
    artifacts: dict[str, dict[str, Any]] | None = None,
    started_at: str | None = None,
    completed_at: str | None = None,
) -> dict[str, Any]:
    validate_run_id(run_id)
    provider_summary = provider_summary or {}
    provider_refs = provider_summary.get("selected_provider_models") or []
    provider_ref = provider_summary.get("selected_provider_model")
    if provider_ref and not provider_refs:
        provider_refs = [provider_ref]
    return {
        "schema_version": SCHEMA_VERSION,
        "tier": tier,
        "run_id": run_id,
        "head_sha": snapshot.head_sha,
        "branch": snapshot.branch,
        "dirty": snapshot.dirty,
        "status": status,
        "started_at": started_at or utc_now(),
        "completed_at": completed_at or utc_now(),
        "plan_fingerprint": plan_fingerprint,
        "environment_fingerprint": environment_fingerprint,
        "provider_model_ref": provider_ref,
        "provider_model_refs": list(provider_refs),
        "redacted_config_hash": provider_summary.get("redacted_config_hash"),
        "artifacts": artifacts or {},
        "steps": steps,
        "reused": reused,
        "executed": executed,
        "invalidated": invalidated,
    }


def utc_now() -> str:
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())


def write_record(report_dir: pathlib.Path, record: dict[str, Any]) -> None:
    report_dir.mkdir(parents=True, exist_ok=True)
    record_json = report_dir / "record.json"
    record_md = report_dir / "record.md"
    record_json.write_text(json.dumps(record, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    record_md.write_text(render_markdown(record), encoding="utf-8")


def preserve_candidate_record(report_dir: pathlib.Path) -> pathlib.Path | None:
    record_json = report_dir / "record.json"
    if not record_json.is_file():
        return None
    try:
        record = json.loads(record_json.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise ValueError(f"cannot preserve candidate record: {record_json}") from exc
    if record.get("tier") != "candidate":
        return None
    candidate_json = report_dir / CANDIDATE_RECORD_NAME
    candidate_md = report_dir / "candidate-record.md"
    _preserve_file(record_json, candidate_json)
    record_md = report_dir / "record.md"
    if record_md.is_file():
        _preserve_file(record_md, candidate_md)
    return candidate_json


def _preserve_file(source: pathlib.Path, destination: pathlib.Path) -> None:
    if destination.exists():
        if destination.read_bytes() != source.read_bytes():
            raise ValueError(f"preserved candidate record already differs: {destination}")
        source.unlink()
        return
    source.replace(destination)


def render_markdown(record: dict[str, Any]) -> str:
    lines = [
        "# Commit Verification Record",
        "",
        f"- Schema version: `{record['schema_version']}`",
        f"- Tier: `{record['tier']}`",
        f"- Run ID: `{record['run_id']}`",
        f"- Head SHA: `{record['head_sha']}`",
        f"- Branch: `{record['branch']}`",
        f"- Dirty at start: {str(record['dirty']).lower()}",
        f"- Status: `{record['status']}`",
        f"- Plan fingerprint: `{record['plan_fingerprint']}`",
        f"- Environment fingerprint: `{record['environment_fingerprint']}`",
        f"- Provider/model: `{record.get('provider_model_ref') or 'not run'}`",
        f"- Redacted config hash: `{record.get('redacted_config_hash') or 'not run'}`",
        f"- Build artifacts: `{_artifact_summary(record.get('artifacts') or {}) or 'not available'}`",
        "",
        "## Steps",
        "",
        "| Label | Outcome | Exit | Started | Duration | Command | Log |",
        "| --- | --- | ---: | --- | ---: | --- | --- |",
    ]
    for step in record["steps"]:
        command = shlex.join(str(part) for part in step["command"])
        lines.append(
            "| `{label}` | {outcome} | {exit_status} | {started_at} | {duration} | `{command}` | `{log}` |".format(
                label=_markdown_cell(step["label"]),
                outcome=_markdown_cell(step.get("outcome") or ""),
                exit_status="" if step.get("exit_status") is None else step["exit_status"],
                started_at=_markdown_cell(step.get("started_at") or ""),
                duration="" if step.get("duration") is None else step["duration"],
                command=_markdown_cell(command),
                log=_markdown_cell(step.get("log") or ""),
            )
        )
    lines.extend(["", "## Reuse", ""])
    lines.append(f"- Reused: {', '.join(record['reused']) or 'none'}")
    lines.append(f"- Executed: {', '.join(record['executed']) or 'none'}")
    if record["invalidated"]:
        for item in record["invalidated"]:
            affected = ", ".join(item.get("steps") or []) or "candidate plan"
            lines.append(f"- Invalidated {affected}: {item['reason']} ({item.get('record') or 'no record'})")
    else:
        lines.append("- Invalidated: none")
    return "\n".join(lines) + "\n"


def _markdown_cell(value: object) -> str:
    return str(value).replace("|", "\\|").replace("`", "\\`")


def _artifact_summary(artifacts: dict[str, dict[str, Any]]) -> str:
    return ", ".join(
        f"{name}={value.get('sha256', 'unknown')}"
        for name, value in sorted(artifacts.items())
    )


def find_reusable_candidate(
    report_root: pathlib.Path,
    snapshot: RepositorySnapshot,
    expected_plan_fingerprint: str,
    expected_environment_fingerprint: str,
    candidate_steps: Iterable[StepLike],
    expected_artifacts: dict[str, dict[str, Any]] | None = None,
) -> ReuseDecision:
    steps = list(candidate_steps)
    labels = [step.label for step in steps]
    report_kind_root = report_root / REPORT_KIND
    records = sorted(
        [
            *report_kind_root.glob("*/*/record.json"),
            *report_kind_root.glob(f"*/*/{CANDIDATE_RECORD_NAME}"),
        ],
        key=lambda path: path.stat().st_mtime,
        reverse=True,
    )
    invalidated: list[dict[str, Any]] = []
    for path in records:
        try:
            record = json.loads(path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError):
            invalidated.append(_invalidation(path, "unreadable record", labels))
            continue
        if record.get("tier") != "candidate":
            continue
        reason = _candidate_invalidation_reason(
            record,
            snapshot,
            expected_plan_fingerprint,
            expected_environment_fingerprint,
            steps,
            expected_artifacts,
        )
        if reason:
            invalidated.append(_invalidation(path, reason, labels))
            continue
        by_label = {str(row["label"]): row for row in record["steps"]}
        return ReuseDecision(path, {label: by_label[label] for label in labels}, [])
    if not invalidated:
        invalidated.append(_invalidation(None, "no candidate record", labels))
    return ReuseDecision(None, {}, invalidated)


def _candidate_invalidation_reason(
    record: dict[str, Any],
    snapshot: RepositorySnapshot,
    expected_plan_fingerprint: str,
    expected_environment_fingerprint: str,
    steps: list[StepLike],
    expected_artifacts: dict[str, dict[str, Any]] | None,
) -> str:
    if record.get("schema_version") != SCHEMA_VERSION:
        return "schema_version mismatch"
    if record.get("head_sha") != snapshot.head_sha:
        return "head_sha mismatch"
    if record.get("plan_fingerprint") != expected_plan_fingerprint:
        return "plan_fingerprint mismatch"
    if record.get("environment_fingerprint") != expected_environment_fingerprint:
        return "environment_fingerprint mismatch"
    if record.get("dirty") is not False:
        return "candidate record is dirty"
    if record.get("status") != "pass":
        return "candidate status is not pass"
    if expected_artifacts is not None and record.get("artifacts") != expected_artifacts:
        return "build artifact mismatch"
    rows = record.get("steps")
    if not isinstance(rows, list):
        return "candidate steps are missing"
    by_label = {str(row.get("label")): row for row in rows if isinstance(row, dict)}
    for step in steps:
        row = by_label.get(step.label)
        if row is None:
            return f"candidate step missing: {step.label}"
        if row.get("command") != [str(part) for part in step.command]:
            return f"candidate step command mismatch: {step.label}"
        if row.get("exit_status") != 0 or row.get("outcome") != "executed":
            return f"candidate step did not pass: {step.label}"
    return ""


def _invalidation(path: pathlib.Path | None, reason: str, labels: list[str]) -> dict[str, Any]:
    return {
        "record": str(path) if path else None,
        "reason": reason,
        "steps": labels,
    }
