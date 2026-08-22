#!/usr/bin/env python3
"""Local evaluation helper for JueX development scripts.

This is intentionally a repository-local script, not production runtime code.
It runs through uv-managed dependencies so validation scripts behave
consistently across developer machines.
"""

from __future__ import annotations

import argparse
import contextlib
import copy
import datetime
import hashlib
import json
import os
import pathlib
import random
import re
import shlex
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
from collections.abc import Iterator
from dataclasses import dataclass
from typing import Any

import yaml

try:
    from . import contract_oracle, outcomes, schedule_routing, selection
except ImportError:  # pragma: no cover - direct script fallback.
    import contract_oracle  # type: ignore[no-redef]
    import outcomes  # type: ignore[no-redef]
    import schedule_routing  # type: ignore[no-redef]
    import selection  # type: ignore[no-redef]


REPO_ROOT = pathlib.Path(__file__).resolve().parents[3]
REPORT_ROOT = REPO_ROOT / ".tmp" / "reports"
SCENARIO_PASSED = "passed"
SCENARIO_CAPABILITY_FAILED = "capability_failed"
SCENARIO_HARD_FAILED = "hard_failed"
SELECTED_PROVIDER_ENVIRONMENT_KEYS = (
    "PROVIDER_API_BASE",
    "PROVIDER_API_KEY",
    "PROVIDER_THINKING_EFFORT",
    "PROVIDER_CONTEXT_WINDOW",
)
ISOLATED_PROVIDER_ENVIRONMENT_KEYS = (
    "PROVIDER_API_ID",
    "PROVIDER_API_PROTOCOL",
    "PROVIDER_API_BASE",
    "PROVIDER_API_KEY",
    "PROVIDER_API_MODEL",
    "PROVIDER_THINKING_EFFORT",
    "PROVIDER_CONTEXT_WINDOW",
)
CONFIG_IMPORT_TIMEOUT_SECONDS = 5
CONFIG_IMPORT_MAX_BYTES = 1 << 20
CONFIG_IMPORT_MAX_CACHE_AGE = datetime.timedelta(days=7)


def main() -> int:
    return main_with_args(sys.argv[1:])


def main_with_args(argv: list[str]) -> int:
    if len(argv) < 1:
        print(
            "usage: evalhelper.py "
            "<provider-smoke|write-model-config|run-timeout|append-command|write-development-record> ...",
            file=sys.stderr,
        )
        return 2

    command = argv[0]
    args = argv[1:]
    try:
        if command == "provider-smoke":
            return provider_smoke(args)
        if command == "write-model-config":
            return write_model_config_command(args)
        if command == "run-timeout":
            return run_timeout_command(args)
        if command == "append-command":
            return append_command(args)
        if command == "write-development-record":
            return write_development_record_command(args)
        print(f"unknown subcommand: {command}", file=sys.stderr)
        return 2
    except Exception as exc:  # noqa: BLE001 - command-line helper should report succinctly.
        print(str(exc), file=sys.stderr)
        return 1


def env_default(name: str, default: str) -> str:
    return os.environ.get(name) or default


def env_bool(name: str) -> bool:
    return (os.environ.get(name) or "").strip().lower() in {"1", "true", "yes", "on"}


def env_int(name: str, default: int) -> int:
    raw = os.environ.get(name)
    if raw is None or raw == "":
        return default
    try:
        return int(raw)
    except ValueError:
        return default


def default_juex_bin() -> str:
    local = REPO_ROOT / "dist" / "juex"
    if os.access(local, os.X_OK):
        return str(local)
    found = shutil.which("juex")
    return found or ""


def default_report_dir(kind: str, run_id: str) -> pathlib.Path:
    if not run_id.strip():
        raise ValueError("run_id cannot be empty")
    if "/" in run_id or "\\" in run_id:
        raise ValueError(f"run_id cannot contain path separators: {run_id}")
    return REPORT_ROOT / kind / run_id


def provider_smoke(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(
        prog="provider_model_smoke.sh",
        description="Run live multi-turn smoke tests for selected provider:model refs.",
    )
    parser.add_argument("--juex", default=env_default("JUEX_BIN", default_juex_bin()))
    parser.add_argument("--config", default=env_default("JUEX_PROVIDER_CONFIG", str(pathlib.Path.home() / ".juex" / "juex.yaml")))
    parser.add_argument(
        "--selection-seed",
        default=env_default("JUEX_EVAL_SELECTION_SEED", selection.generated_seed()),
        help="Reproducible seed for provider-config candidate selection.",
    )
    parser.add_argument(
        "--all-models",
        action="store_true",
        default=env_bool("JUEX_PROVIDER_SMOKE_ALL_MODELS"),
        help="Run every eligible provider:model found in the provider config.",
    )
    parser.add_argument("--work-root", default=env_default("JUEX_PROVIDER_SMOKE_ROOT", ""))
    parser.add_argument("--report-dir", default=env_default("JUEX_PROVIDER_SMOKE_REPORT_DIR", ""))
    parser.add_argument("--run-id", default=env_default("JUEX_PROVIDER_SMOKE_RUN_ID", time.strftime("%Y%m%dT%H%M%SZ", time.gmtime())))
    parser.add_argument("--only", default=env_default("JUEX_PROVIDER_SMOKE_ONLY", ""))
    parser.add_argument("--timeout", type=int, default=env_int("JUEX_PROVIDER_SMOKE_TIMEOUT", 240))
    parser.add_argument("--retries", type=int, choices=(0, 1), default=env_int("JUEX_PROVIDER_SMOKE_RETRIES", 1))
    parser.add_argument("--keep", action="store_true", default=env_bool("JUEX_PROVIDER_SMOKE_KEEP"))
    parsed = parser.parse_args(argv)

    if parsed.timeout <= 0:
        raise ValueError("--timeout must be a positive integer")
    if parsed.only and parsed.all_models:
        raise ValueError("--only is mutually exclusive with --all-models")
    if not parsed.juex:
        raise ValueError("juex binary not found; run 'make build' or pass --juex")
    if not os.access(parsed.juex, os.X_OK):
        raise ValueError(f"juex binary is not executable: {parsed.juex}")
    report_dir = pathlib.Path(parsed.report_dir or default_report_dir("provider-model-smoke", parsed.run_id))
    (report_dir / "cases").mkdir(parents=True, exist_ok=True)
    config_path = selection.resolved_path(parsed.config)
    work_root_created = False
    if parsed.work_root:
        work_root = pathlib.Path(parsed.work_root)
        work_root.mkdir(parents=True, exist_ok=True)
    else:
        work_root = pathlib.Path(tempfile.mkdtemp(prefix="juex-provider-smoke."))
        work_root_created = True

    matrix_file = report_dir / "matrix.tsv"
    results_file = report_dir / "results.jsonl"
    summary_json = report_dir / "summary.json"
    summary_md = report_dir / "summary.md"
    results_file.write_text("", encoding="utf-8")
    command_prefix = [
        sys.executable,
        "-m",
        "tests.eval.juex_eval",
        "provider-smoke",
        "--juex",
        parsed.juex,
        "--run-id",
        parsed.run_id,
        "--timeout",
        str(parsed.timeout),
        "--retries",
        str(parsed.retries),
    ]

    try:
        try:
            if not config_path.is_file():
                raise FileNotFoundError(f"provider config not found: {config_path}")
            cfg, source_layers = load_source_config_with_layers(config_path)
            validate_source_layers(parsed.juex, source_layers)
            rows, evidence = selection.select(
                cfg,
                kind="provider-smoke",
                config_path=config_path,
                seed=parsed.selection_seed,
                only=[parsed.only] if parsed.only else [],
                all_models=parsed.all_models,
                command_prefix=command_prefix,
            )
            materialized_configs = materialize_and_validate_selected_configs(
                parsed.juex,
                cfg,
                rows,
                work_root / ".validated-provider-configs",
            )
        except selection.ProviderUnavailable as exc:
            summary = provider_summary(parsed, report_dir, work_root, exc.evidence, [], exc.failure_category, str(exc))
            write_smoke_summary(
                summary_json,
                summary_md,
                summary,
                [],
            )
            print(f"{selection.PROVIDER_UNAVAILABLE}: {exc}", file=sys.stderr)
            print_selection_evidence(exc.evidence)
            print_summary_outcome(summary)
            return 1
        except (OSError, ValueError, yaml.YAMLError) as exc:
            error = safe_config_error(exc)
            evidence = selection.unavailable_evidence(
                config_path=config_path,
                seed=parsed.selection_seed,
                command_prefix=command_prefix,
                only=[parsed.only] if parsed.only else [],
                all_models=parsed.all_models,
            )
            summary = provider_summary(
                parsed,
                report_dir,
                work_root,
                evidence,
                [],
                outcomes.ENVIRONMENT_FAILURE,
                error,
            )
            write_smoke_summary(
                summary_json,
                summary_md,
                summary,
                [],
            )
            print(f"{outcomes.ENVIRONMENT_FAILURE}: {error}", file=sys.stderr)
            print_selection_evidence(evidence)
            print_summary_outcome(summary)
            return 1

        matrix_file.write_text(
            "".join(
                "\t".join(
                    [
                        row.provider_id,
                        row.model_id,
                        row.protocol,
                        row.reasoning_effort_capability,
                        row.tools_capability,
                        row.thinking_effort,
                    ]
                )
                + "\n"
                for row in rows
            ),
            encoding="utf-8",
        )

        print(f"juex: {parsed.juex}")
        print_selection_evidence(evidence)
        print(f"work root: {work_root}")
        print(f"report dir: {report_dir}")
        schedule_variant = schedule_routing.variant_for_run_id(parsed.run_id)
        print(f"schedule routing variant: {schedule_variant}")

        failed = 0
        results: list[SmokeResult] = []
        for row in rows:
            result = run_provider_smoke_case(
                ProviderSmokeContext(
                    row=row,
                    juex_bin=parsed.juex,
                    config=cfg,
                    work_root=work_root,
                    report_dir=report_dir,
                    run_id=parsed.run_id,
                    timeout_seconds=parsed.timeout,
                    retries=parsed.retries,
                    codex_home=env_default("CODEX_HOME", str(pathlib.Path.home() / ".codex")),
                    materialized_config=materialized_configs[row.ref],
                )
            )
            results.append(result)
            append_jsonl(results_file, result.as_dict())
            if result.status != "pass":
                failed += 1

        summary = provider_summary(parsed, report_dir, work_root, evidence, results, "", "")
        write_smoke_summary(
            summary_json,
            summary_md,
            summary,
            results,
        )
        print(f"summary: total={len(results)} failed={failed} report={summary_md}")
        print_summary_outcome(summary)
        return 1 if failed else 0
    finally:
        if work_root_created and not parsed.keep:
            shutil.rmtree(work_root, ignore_errors=True)


def provider_summary(
    args: argparse.Namespace,
    report_dir: pathlib.Path,
    work_root: pathlib.Path,
    evidence: selection.SelectionEvidence,
    results: list[SmokeResult],
    failure_category: str,
    error: str,
) -> dict[str, Any]:
    total = len(results)
    failed = sum(1 for result in results if result.status != "pass")
    validation_outcome = aggregate_smoke_outcome(results, failure_category, error)
    summary = {
        "run_id": args.run_id,
        "juex": args.juex,
        "config": evidence.resolved_config_path,
        "report_dir": str(report_dir),
        "work_root": str(work_root) if args.keep else "cleaned",
        "failure_category": failure_category or None,
        "error": error or None,
        "total": total,
        "passed": total - failed,
        "failed": failed,
        "tool_use_recorded": sum(1 for result in results if result.tool_status == "yes"),
        "exec_command_tool_use_recorded": sum(1 for result in results if result.exec_command_status == "yes"),
        "tty_recorded": sum(1 for result in results if result.tty_status == "yes"),
        "stdin_recorded": sum(1 for result in results if result.stdin_status == "yes"),
        "filesystem_verified": sum(1 for result in results if result.filesystem_status == "yes"),
        "terminal_event_verified": sum(1 for result in results if result.event_contract_status == "yes"),
        "thinking_observed": sum(1 for result in results if result.thinking_status == "observed"),
        "schedule_routing_verified": sum(1 for result in results if result.schedule_routing_status == "passed"),
        "schedule_routing_failures": sum(1 for result in results if result.schedule_routing_status in {"failed", "hard_failed"}),
        "schedule_routing_variant": schedule_routing.variant_for_run_id(args.run_id),
        "results_jsonl_path": str(report_dir / "results.jsonl"),
        **validation_outcome.as_dict(),
    }
    summary.update(evidence.as_dict())
    return summary


def aggregate_smoke_outcome(
    results: list[SmokeResult],
    failure_category: str,
    error: str,
) -> outcomes.ValidationOutcome:
    if failure_category == outcomes.ENVIRONMENT_FAILURE:
        return outcomes.invalid_config_failure(error)
    if failure_category == selection.PROVIDER_UNAVAILABLE:
        return outcomes.ValidationOutcome(
            outcomes.PROVIDER_UNAVAILABLE,
            error or "no eligible provider/model is available",
            "provider-selection-unavailable",
            True,
            "stop",
        )
    failed = [result for result in results if result.status != "pass"]
    if failed:
        priority = {
            outcomes.PRODUCT_FAILURE: 0,
            outcomes.ENVIRONMENT_FAILURE: 1,
            outcomes.PROVIDER_UNAVAILABLE: 2,
            outcomes.TRANSIENT_FAILURE: 3,
        }
        selected = min(failed, key=lambda result: priority.get(result.outcome, 99))
        return outcomes.ValidationOutcome(
            selected.outcome,
            selected.reason,
            selected.matched_rule,
            True,
            selected.recommended_action,
            selected.retryable,
        )
    attempt_count = 2 if any(result.outcome == outcomes.FLAKY_PASS for result in results) else 1
    return outcomes.success(attempt_count=attempt_count)


def print_summary_outcome(summary: dict[str, Any]) -> None:
    value = {key: summary.get(key) for key in outcomes.ValidationOutcome.__dataclass_fields__}
    print(outcomes.STRUCTURED_PREFIX + json.dumps(value, ensure_ascii=False, separators=(",", ":")))


def print_selection_evidence(evidence: selection.SelectionEvidence) -> None:
    print(f"selection_source={selection.SELECTION_SOURCE}")
    print(f"selected_provider_model={evidence.selected_refs[0] if len(evidence.selected_refs) == 1 else ''}")
    print(f"selected_provider_models={','.join(evidence.selected_refs)}")
    print(f"selection_seed={evidence.seed}")
    print(f"eligible_candidate_count={len(evidence.eligible_refs)}")
    print(f"eligible_candidate_refs={','.join(evidence.eligible_refs)}")
    print(f"resolved_config_path={evidence.resolved_config_path}")
    print(f"redacted_config_hash={evidence.redacted_config_hash}")
    print(f"reproduction_command={evidence.reproduction_command}")


MatrixRow = selection.Candidate


@dataclass(frozen=True)
class SourceConfigLayer:
    scope: str
    imports: tuple[dict[str, Any], ...]
    declaring: dict[str, Any]


@dataclass
class SmokeResult:
    run_id: str
    ref: str
    provider_id: str
    model_id: str
    protocol: str
    reasoning_effort_capability: str
    tools_capability: str
    thinking_effort: str
    status: str = "fail"
    session_id: str = ""
    tool_status: str = "no"
    exec_command_status: str = "no"
    tty_status: str = "no"
    stdin_status: str = "no"
    filesystem_status: str = "no"
    event_contract_status: str = "no"
    thinking_status: str = "not_observed"
    schedule_routing_status: str = "not_run"
    schedule_routing_variant: str = ""
    schedule_routing_existing_id: str = ""
    error_stage: str = ""
    error: str = ""
    artifacts: str = ""
    outcome: str = outcomes.PRODUCT_FAILURE
    reason: str = "provider smoke has not completed"
    matched_rule: str = "provider-smoke-not-complete"
    blocks_merge: bool = True
    recommended_action: str = "fix_code"
    retryable: bool = False

    def as_dict(self) -> dict[str, Any]:
        return self.__dict__.copy()


@dataclass(frozen=True)
class ProviderSmokeContext:
    row: MatrixRow
    juex_bin: str
    config: dict[str, Any]
    work_root: pathlib.Path
    report_dir: pathlib.Path
    run_id: str
    timeout_seconds: int
    retries: int
    codex_home: str
    materialized_config: pathlib.Path | None = None


@dataclass(frozen=True)
class ScenarioRunOutcome:
    kind: str
    report: contract_oracle.ContractReport
    session_id: str = ""
    validation_outcome: outcomes.ValidationOutcome | None = None
    attempt_count: int = 1


def enumerate_provider_matrix(cfg: dict[str, Any]) -> list[MatrixRow]:
    return selection.enumerate_candidates(cfg)


def model_id_from(model: Any) -> str:
    if isinstance(model, dict):
        return str(model.get("id") or "").strip()
    return str(model or "").strip()


def run_provider_smoke_case(ctx: ProviderSmokeContext) -> SmokeResult:
    row = ctx.row
    safe = safe_ref(row.ref)
    case_dir = ctx.work_root / safe
    artifact_dir = ctx.report_dir / "cases" / safe
    shutil.rmtree(case_dir, ignore_errors=True)
    shutil.rmtree(artifact_dir, ignore_errors=True)
    (case_dir / ".juex").mkdir(parents=True, exist_ok=True)
    artifact_dir.mkdir(parents=True, exist_ok=True)
    result = SmokeResult(
        run_id=ctx.run_id,
        ref=row.ref,
        provider_id=row.provider_id,
        model_id=row.model_id,
        protocol=row.protocol,
        reasoning_effort_capability=row.reasoning_effort_capability,
        tools_capability=row.tools_capability,
        thinking_effort=row.thinking_effort,
        schedule_routing_variant=schedule_routing.variant_for_run_id(ctx.run_id),
        artifacts=str(artifact_dir),
    )

    print(f"==> {row.ref} [{row.protocol}]")
    token = f"juex-smoke-{safe}-{int(time.time())}-{random.randrange(0x1000000):06x}"
    case_config = case_dir / "provider.juex.yaml"
    manifest_file = case_dir / "release-manifest.txt"
    notes_file = case_dir / "agent-notes.txt"
    try:
        if ctx.materialized_config is None:
            write_selected_config(ctx.config, row.provider_id, row.model_id, case_config)
        else:
            shutil.copyfile(ctx.materialized_config, case_config)
            case_config.chmod(0o600)
        manifest_file.write_text(
            "\n".join(
                [
                    "release=interactive-agent-smoke",
                    f"token={token}",
                    "required_tools=read,write,edit,grep,exec_command,write_stdin",
                    "required_tty=true",
                    "",
                ]
            ),
            encoding="utf-8",
        )
    except Exception as exc:  # noqa: BLE001
        return fail_smoke_case(result, case_dir, artifact_dir, "config", str(exc))

    installer_cmd = tty_installer_command(token)
    prompt = provider_smoke_agent_prompt(manifest_file, notes_file, token, installer_cmd)
    turn_status, turn_outcome, turn_attempt_count = run_turn_with_retries(
        ctx,
        case_dir,
        case_config,
        "turn1",
        ["--new", prompt],
    )
    if turn_status != 0:
        return fail_smoke_case(result, case_dir, artifact_dir, "turn1", "turn1 failed", turn_outcome)
    session_id = json_file_value(case_dir / "turn1.stdout.json", "session_id")
    if not session_id:
        return fail_smoke_case(result, case_dir, artifact_dir, "turn1", "missing session_id")
    result.session_id = session_id
    if not file_contains(case_dir / "turn1.stdout.json", f"EVAL_PASS {token}"):
        return fail_smoke_case(result, case_dir, artifact_dir, "turn1", "missing final EVAL_PASS token")

    sessions = agent_sessions_dir(case_dir, case_dir / "home" / ".juex")
    conversation = sessions / session_id / "conversation.jsonl"
    if not conversation.is_file():
        return fail_smoke_case(result, case_dir, artifact_dir, "session", "missing conversation log")
    ok, detail = validate_agent_smoke_files(notes_file, case_dir / "tty-result.txt", token)
    if not ok:
        return fail_smoke_case(result, case_dir, artifact_dir, "filesystem", detail)
    result.filesystem_status = "yes"
    events = sessions / session_id / "events.jsonl"
    contract_report = contract_oracle.validate_agent_smoke_contract(conversation, events, token)
    if not contract_report.passed:
        return fail_smoke_case(result, case_dir, artifact_dir, "session", contract_report.message())
    result.tool_status = "yes"
    result.exec_command_status = "yes"
    result.tty_status = "yes"
    result.stdin_status = "yes"
    result.event_contract_status = "yes"
    result.thinking_status = "observed" if file_contains(conversation, '"type":"reasoning"') else "not_exposed"
    copy_case_artifacts(case_dir, artifact_dir)
    schedule_key = f"{int(time.time())}-{random.randrange(0x1000000):06x}"
    existing_schedule_id = (
        f"schedule-routing-existing-{schedule_key}"
        if result.schedule_routing_variant == schedule_routing.SEEDED_EQUIVALENT_VARIANT
        else None
    )
    expectation = schedule_routing.ScheduleRoutingExpectation(
        schedule_id=f"schedule-routing-eval-{schedule_key}",
        every_seconds=21600,
        content=f"schedule routing evaluation {row.ref} {schedule_key}",
        completion_token=f"SCHEDULE_ROUTING_PASS {schedule_key}",
        existing_schedule_id=existing_schedule_id,
    )
    schedule_outcome = run_schedule_routing_case(ctx, artifact_dir, expectation)
    result.schedule_routing_status = "passed" if schedule_outcome.kind == SCENARIO_PASSED else (
        "hard_failed" if schedule_outcome.kind == SCENARIO_HARD_FAILED else "failed"
    )
    result.schedule_routing_existing_id = existing_schedule_id or ""
    if schedule_outcome.kind != SCENARIO_PASSED:
        result.error_stage = "schedule-routing"
        result.error = schedule_outcome.report.message()
        apply_smoke_outcome(
            result,
            schedule_outcome.validation_outcome
            or product_outcome("schedule routing contract failed", "schedule-routing-contract"),
        )
        print(f"FAIL {result.ref}: {result.error}", file=sys.stderr)
        return result
    result.status = "pass"
    apply_smoke_outcome(
        result,
        outcomes.success(attempt_count=2 if turn_attempt_count > 1 or schedule_outcome.attempt_count > 1 else 1),
    )
    print(
        f"ok  {row.ref} session={session_id} toolcall={result.tool_status} "
        f"exec_command={result.exec_command_status} tty={result.tty_status} "
        f"stdin={result.stdin_status} events={result.event_contract_status} "
        f"thinking={result.thinking_status} schedule_routing={result.schedule_routing_status} "
        f"schedule_variant={result.schedule_routing_variant} "
        f"schedule_session={schedule_outcome.session_id} artifacts={artifact_dir}"
    )
    return result


def provider_smoke_agent_prompt(manifest_file: pathlib.Path, notes_file: pathlib.Path, token: str, installer_cmd: str) -> str:
    return "\n".join(
        [
            "You are running a live JueX agent smoke evaluation. This is not a Q&A task.",
            "You must complete the workflow by using the requested tools. Do not skip tool calls.",
            "Use exactly one tool call per assistant response, in this order:",
            f"1. read: read this manifest file: {manifest_file}",
            f"2. write: create this notes file: {notes_file}",
            "   The notes file content must be exactly:",
            f"   token={token}",
            "   status=pending",
            "   manifest=read",
            f"3. edit: in {notes_file}, replace exactly one occurrence of status=pending with status=edited.",
            f"4. grep: search for the exact token {token} in {manifest_file}.",
            "5. exec_command: run the exact command below with tty:true, yield_time_ms:1600, max_output_tokens:20000.",
            "   This command prints changing progress with carriage returns, then waits for confirmation.",
            installer_cmd,
            "6. write_stdin: use the numeric session_id from the exec_command result, chars exactly \"yes\\n\", yield_time_ms:2500, max_output_tokens:20000.",
            "7. exec_command: run this exact verification command with yield_time_ms:1000 and max_output_tokens:20000:",
            f"   cat {shlex.quote(str(notes_file))} && cat {shlex.quote('tty-result.txt')} && printf 'POST_CHECK={token}\\n'",
            f"Only after all seven tool steps have succeeded, answer exactly: EVAL_PASS {token}",
        ]
    )


def tty_installer_command(token: str) -> str:
    code = "\n".join(
        [
            "import pathlib, sys, time",
            f"token = {token!r}",
            "print('TTY-BOOT ' + token, flush=True)",
            "for pct in (0, 20, 40, 60, 80):",
            "    sys.stdout.write('\\rINSTALL %03d%%' % pct)",
            "    sys.stdout.flush()",
            "    time.sleep(0.25)",
            "print('\\nPROMPT approve install? [yes/no]: ', end='', flush=True)",
            "answer = sys.stdin.readline().strip()",
            "print('INPUT=' + answer, flush=True)",
            "for step in ('unpack', 'configure', 'verify'):",
            "    print('STEP ' + step, flush=True)",
            "    time.sleep(0.2)",
            "pathlib.Path('tty-result.txt').write_text('token=' + token + '\\napproved=' + answer + '\\n', encoding='utf-8')",
            "print('TTY-DONE ' + token, flush=True)",
        ]
    )
    return "python3 -u -c " + shlex.quote(code)


def validate_agent_smoke_files(notes_file: pathlib.Path, tty_result_file: pathlib.Path, token: str) -> tuple[bool, str]:
    if not notes_file.is_file():
        return False, f"missing notes file: {notes_file}"
    notes = notes_file.read_text(encoding="utf-8", errors="replace")
    for want in (f"token={token}", "status=edited", "manifest=read"):
        if want not in notes:
            return False, f"notes file missing {want!r}: {notes!r}"
    if "status=pending" in notes:
        return False, f"notes file was not edited: {notes!r}"
    if not tty_result_file.is_file():
        return False, f"missing tty result file: {tty_result_file}"
    tty_result = tty_result_file.read_text(encoding="utf-8", errors="replace")
    for want in (f"token={token}", "approved=yes"):
        if want not in tty_result:
            return False, f"tty result missing {want!r}: {tty_result!r}"
    return True, ""


def conversation_has_agent_smoke_tools(path: pathlib.Path, token: str) -> tuple[bool, str]:
    return contract_oracle.conversation_has_agent_smoke_tools(path, token)


def events_have_agent_smoke_terminal_results(path: pathlib.Path, token: str) -> tuple[bool, str]:
    return contract_oracle.events_have_agent_smoke_terminal_results(path, token)


def run_turn_with_retries(
    ctx: ProviderSmokeContext,
    case_dir: pathlib.Path,
    case_config: pathlib.Path,
    label: str,
    args: list[str],
) -> tuple[int, outcomes.ValidationOutcome, int]:
    if ctx.retries not in {0, 1}:
        raise ValueError("provider smoke retries must be 0 or 1")
    status = 1
    result = product_outcome("provider turn did not run", "provider-turn-not-run")
    for attempt in range(1, ctx.retries + 2):
        status = run_turn(ctx, case_dir, case_config, label, args)
        archive_turn_attempt(case_dir, label, attempt)
        if status == 0:
            return 0, outcomes.success(attempt_count=attempt), attempt
        result = turn_failure_outcome(case_dir, label, status)
        if attempt > ctx.retries or not result.retryable:
            return status, result, attempt
        print(
            f"retry {ctx.row.ref} {label} after {result.outcome} "
            f"(rule={result.matched_rule}, attempt {attempt}/{ctx.retries + 1})",
            file=sys.stderr,
        )
        time.sleep(attempt)
    return status, result, ctx.retries + 1


def run_schedule_routing_case(
    ctx: ProviderSmokeContext,
    artifact_dir: pathlib.Path,
    expectation: schedule_routing.ScheduleRoutingExpectation,
) -> ScenarioRunOutcome:
    if ctx.retries not in {0, 1}:
        raise ValueError("provider smoke retries must be 0 or 1")
    work_root = ctx.work_root / safe_ref(ctx.row.ref) / "schedule-routing"
    report_root = artifact_dir / "schedule-routing"
    for attempt in range(1, ctx.retries + 2):
        case_dir = work_root / f"attempt-{attempt}"
        attempt_artifacts = report_root / f"attempt-{attempt}"
        shutil.rmtree(case_dir, ignore_errors=True)
        shutil.rmtree(attempt_artifacts, ignore_errors=True)
        (case_dir / ".juex").mkdir(parents=True, exist_ok=True)
        attempt_artifacts.mkdir(parents=True, exist_ok=True)
        case_config = case_dir / "provider.juex.yaml"
        prompt = schedule_routing.build_prompt(expectation)
        try:
            write_selected_config(ctx.config, ctx.row.provider_id, ctx.row.model_id, case_config)
            (case_dir / "prompt.txt").write_text(prompt + "\n", encoding="utf-8")
            if expectation.variant == schedule_routing.SEEDED_EQUIVALENT_VARIANT:
                seed = schedule_routing.seeded_observables_config(expectation)
                seed_text = json.dumps(seed, ensure_ascii=False, indent=2) + "\n"
                (case_dir / ".juex" / "observables.json").write_text(seed_text, encoding="utf-8")
                (attempt_artifacts / "seed-observables.json").write_text(seed_text, encoding="utf-8")
        except Exception as exc:  # noqa: BLE001
            copy_schedule_routing_artifacts(case_dir, attempt_artifacts)
            report = contract_oracle.ContractReport(False, [f"schedule routing config: {exc}"])
            write_contract_report(attempt_artifacts, SCENARIO_HARD_FAILED, report)
            return ScenarioRunOutcome(
                SCENARIO_HARD_FAILED,
                report,
                validation_outcome=outcomes.ValidationOutcome(
                    outcomes.ENVIRONMENT_FAILURE,
                    "schedule routing fixture could not be prepared",
                    "environment-schedule-fixture",
                    True,
                    "fix_environment",
                ),
                attempt_count=attempt,
            )

        status = run_turn(ctx, case_dir, case_config, "turn1", ["--new", prompt])
        copy_schedule_routing_artifacts(case_dir, attempt_artifacts)
        if status != 0:
            write_error_tail(case_dir, attempt_artifacts)
            failure = turn_failure_outcome(case_dir, "turn1", status)
            if attempt <= ctx.retries and failure.retryable:
                print(
                    f"retry {ctx.row.ref} schedule-routing after retryable failure "
                    f"(attempt {attempt}/{ctx.retries + 1})",
                    file=sys.stderr,
                )
                time.sleep(attempt)
                continue
            provider_message = provider_error_message(attempt_artifacts)
            detail = combine_error(f"schedule routing turn failed with status {status}", provider_message)
            report = contract_oracle.ContractReport(False, [detail])
            write_contract_report(attempt_artifacts, SCENARIO_HARD_FAILED, report)
            return ScenarioRunOutcome(
                SCENARIO_HARD_FAILED,
                report,
                validation_outcome=failure,
                attempt_count=attempt,
            )

        session_id = json_file_value(case_dir / "turn1.stdout.json", "session_id")
        if not session_id:
            report = contract_oracle.ContractReport(False, ["schedule routing turn missing session_id"])
            write_contract_report(attempt_artifacts, SCENARIO_HARD_FAILED, report)
            return ScenarioRunOutcome(
                SCENARIO_HARD_FAILED,
                report,
                validation_outcome=product_outcome("schedule routing turn omitted its session ID", "schedule-routing-session"),
                attempt_count=attempt,
            )
        try:
            sessions = agent_sessions_dir(case_dir, case_dir / "home" / ".juex")
        except (OSError, ValueError, json.JSONDecodeError) as exc:
            report = contract_oracle.ContractReport(False, [f"schedule routing session artifacts: {exc}"])
            write_contract_report(attempt_artifacts, SCENARIO_HARD_FAILED, report)
            return ScenarioRunOutcome(
                SCENARIO_HARD_FAILED,
                report,
                session_id,
                outcomes.ValidationOutcome(
                    outcomes.ENVIRONMENT_FAILURE,
                    "schedule routing artifacts could not be read",
                    "environment-schedule-artifacts",
                    True,
                    "fix_environment",
                ),
                attempt,
            )
        conversation = sessions / session_id / "conversation.jsonl"
        observables = case_dir / ".juex" / "observables.json"
        validation = schedule_routing.validate_outcome(conversation, observables, expectation)
        copy_schedule_routing_artifacts(case_dir, attempt_artifacts)
        write_contract_report(attempt_artifacts, validation.kind, validation.report)
        validation_outcome = (
            outcomes.success(attempt_count=attempt)
            if validation.report.passed
            else product_outcome(validation.report.message(), "schedule-routing-contract")
        )
        return ScenarioRunOutcome(validation.kind, validation.report, session_id, validation_outcome, attempt)
    report = contract_oracle.ContractReport(False, ["schedule routing retries exhausted"])
    return ScenarioRunOutcome(
        SCENARIO_HARD_FAILED,
        report,
        validation_outcome=product_outcome("schedule routing did not produce a result", "schedule-routing-no-result"),
        attempt_count=ctx.retries + 1,
    )


def run_turn(ctx: ProviderSmokeContext, case_dir: pathlib.Path, case_config: pathlib.Path, label: str, args: list[str]) -> int:
    stdout_file = case_dir / f"{label}.stdout.json"
    stderr_file = case_dir / f"{label}.stderr.log"
    case_home = case_dir / "home"
    (case_home / ".agents").mkdir(parents=True, exist_ok=True)
    (case_home / ".juex").mkdir(parents=True, exist_ok=True)
    env = os.environ.copy()
    for name in ISOLATED_PROVIDER_ENVIRONMENT_KEYS:
        env.pop(name, None)
    env.update(
        {
            "HOME": str(case_home),
            "USERPROFILE": str(case_home),
            "JUEX_HOME": str(case_home / ".juex"),
            "GIT_CONFIG_GLOBAL": str(case_home / "gitconfig"),
            "GIT_CONFIG_NOSYSTEM": "1",
            "CODEX_HOME": ctx.codex_home,
        }
    )
    command = [
        ctx.juex_bin,
        "-C",
        str(case_dir),
        "--config",
        str(case_config),
        "--enable-user-agents-resources=false",
        "run",
        "--json",
        *args,
    ]
    with stdout_file.open("wb") as stdout, stderr_file.open("wb") as stderr:
        return run_subprocess_with_timeout(command, ctx.timeout_seconds, env=env, stdout=stdout, stderr=stderr)


def run_subprocess_with_timeout(
    command: list[str],
    timeout_seconds: int,
    *,
    env: dict[str, str] | None = None,
    stdout: Any | None = None,
    stderr: Any | None = None,
    stdin: Any | None = None,
) -> int:
    proc = subprocess.Popen(
        command,
        env=env,
        stdout=stdout,
        stderr=stderr,
        stdin=stdin,
        start_new_session=True,
    )
    try:
        return proc.wait(timeout=timeout_seconds)
    except subprocess.TimeoutExpired:
        try:
            os.killpg(proc.pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
        try:
            proc.wait(timeout=2)
        except subprocess.TimeoutExpired:
            try:
                os.killpg(proc.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            proc.wait()
        return 124


def turn_failure_retryable(case_dir: pathlib.Path, label: str, status: int) -> bool:
    return turn_failure_outcome(case_dir, label, status).retryable


def turn_failure_outcome(
    case_dir: pathlib.Path,
    label: str,
    status: int,
) -> outcomes.ValidationOutcome:
    paths = (case_dir / f"{label}.stderr.log", case_dir / f"{label}.stdout.json")
    text = "\n".join(
        path.read_text(encoding="utf-8", errors="replace")
        for path in paths
        if path.is_file()
    )
    return outcomes.classify_failure(text, deterministic=False, exit_status=status)


def archive_turn_attempt(case_dir: pathlib.Path, label: str, attempt: int) -> None:
    for suffix in ("stdout.json", "stderr.log"):
        source = case_dir / f"{label}.{suffix}"
        if source.is_file():
            shutil.copy2(source, case_dir / f"{label}.attempt-{attempt}.{suffix}")


def product_outcome(reason: str, rule: str) -> outcomes.ValidationOutcome:
    return outcomes.ValidationOutcome(
        outcomes.PRODUCT_FAILURE,
        reason,
        rule,
        True,
        "fix_code",
    )


def apply_smoke_outcome(result: SmokeResult, outcome: outcomes.ValidationOutcome) -> None:
    result.outcome = outcome.outcome
    result.reason = outcome.reason
    result.matched_rule = outcome.matched_rule
    result.blocks_merge = outcome.blocks_merge
    result.recommended_action = outcome.recommended_action
    result.retryable = outcome.retryable


def fail_smoke_case(
    result: SmokeResult,
    case_dir: pathlib.Path,
    artifact_dir: pathlib.Path,
    stage: str,
    message: str,
    outcome: outcomes.ValidationOutcome | None = None,
) -> SmokeResult:
    copy_case_artifacts(case_dir, artifact_dir)
    write_error_tail(case_dir, artifact_dir)
    provider_message = provider_error_message(artifact_dir)
    result.error_stage = stage
    result.error = combine_error(message, provider_message)
    apply_smoke_outcome(
        result,
        outcome
        or (
            outcomes.ValidationOutcome(
                outcomes.ENVIRONMENT_FAILURE,
                result.error,
                "environment-provider-smoke-config",
                True,
                "fix_environment",
            )
            if stage == "config"
            else product_outcome(result.error, f"provider-smoke-{stage}-contract")
        ),
    )
    print(f"FAIL {result.ref}: {message}", file=sys.stderr)
    return result


def copy_case_artifacts(case_dir: pathlib.Path, artifact_dir: pathlib.Path) -> None:
    artifact_dir.mkdir(parents=True, exist_ok=True)
    for path in sorted(case_dir.glob("*.stdout.json")) + sorted(case_dir.glob("*.stderr.log")):
        shutil.copy2(path, artifact_dir / path.name)
    try:
        sessions = agent_sessions_dir(case_dir, case_dir / "home" / ".juex")
    except (OSError, ValueError, json.JSONDecodeError):
        return
    if sessions.is_dir():
        for path in sorted(sessions.rglob("*")):
            if path.is_file() and path.name in {"conversation.jsonl", "events.jsonl"}:
                shutil.copy2(path, artifact_dir / path.name)


def copy_schedule_routing_artifacts(case_dir: pathlib.Path, artifact_dir: pathlib.Path) -> None:
    copy_case_artifacts(case_dir, artifact_dir)
    for relative in (pathlib.Path("prompt.txt"), pathlib.Path(".juex") / "observables.json"):
        source = case_dir / relative
        if source.is_file():
            shutil.copy2(source, artifact_dir / source.name)


def write_contract_report(
    artifact_dir: pathlib.Path,
    outcome: str,
    report: contract_oracle.ContractReport,
) -> None:
    (artifact_dir / "contract.json").write_text(
        json.dumps(
            {"outcome": outcome, "passed": report.passed, "issues": report.issues},
            ensure_ascii=False,
            indent=2,
        )
        + "\n",
        encoding="utf-8",
    )


def agent_sessions_dir(work_dir: pathlib.Path, juex_home: pathlib.Path) -> pathlib.Path:
    marker_path = work_dir / ".juex" / "juex.local.json"
    marker = json.loads(marker_path.read_text(encoding="utf-8"))
    agent_id = marker.get("agent_id") if isinstance(marker, dict) else None
    if not isinstance(agent_id, str) or re.fullmatch(r"[a-z2-7]{6}", agent_id) is None:
        raise ValueError(f"invalid or missing agent_id in {marker_path}")
    return juex_home / "agents" / agent_id / "sessions"


def write_error_tail(case_dir: pathlib.Path, artifact_dir: pathlib.Path) -> None:
    chunks: list[str] = []
    for path in sorted(case_dir.glob("*.stderr.log")) + sorted(case_dir.glob("*.stdout.json")):
        chunks.append(f"--- {path.name} ---\n{tail_file(path, 30)}")
    (artifact_dir / "error-tail.txt").write_text("\n".join(chunks), encoding="utf-8")


def provider_error_message(artifact_dir: pathlib.Path) -> str:
    for path in sorted(artifact_dir.glob("*.stderr.log")) + sorted(artifact_dir.glob("*.stdout.json")):
        message = provider_error_from_file(path)
        if message:
            return message
    return ""


def provider_error_from_file(path: pathlib.Path) -> str:
    text = path.read_text(encoding="utf-8", errors="replace") if path.is_file() else ""
    message = provider_error_from_json(text)
    if message:
        return message
    for line in text.splitlines():
        message = provider_error_from_json(line)
        if message:
            return message
    return ""


def provider_error_from_json(text: str) -> str:
    try:
        data = json.loads(text)
    except json.JSONDecodeError:
        return ""
    if not isinstance(data, dict):
        return ""
    for key in ("message", "error"):
        if key in data:
            return stringify_message(data[key])
    return ""


def stringify_message(value: Any) -> str:
    if value is None:
        return ""
    if isinstance(value, str):
        return value
    try:
        return json.dumps(value, ensure_ascii=False, sort_keys=True)
    except TypeError:
        return str(value)


def combine_error(message: str, provider_message: str) -> str:
    if provider_message and provider_message not in message:
        return ": ".join(part for part in [message, provider_message] if part)
    return message


def write_smoke_summary(summary_json: pathlib.Path, summary_md: pathlib.Path, summary: dict[str, Any], results: list[SmokeResult]) -> None:
    summary_json.write_text(json.dumps(summary, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    lines = [
        "# Provider Model Smoke Summary",
        "",
        f"- Run ID: `{summary['run_id']}`",
        f"- Juex: `{summary['juex']}`",
        f"- Config: `{summary['config']}`",
        f"- Selection source: `{summary['selection_source']}`",
        f"- Selected provider/model: `{summary['selected_provider_model'] or ''}`",
        f"- Selected provider/models: `{', '.join(summary['selected_provider_models'])}`",
        f"- Selection seed: `{summary['selection_seed']}`",
        f"- Eligible candidate count: {summary['eligible_candidate_count']}",
        f"- Eligible candidate refs: `{', '.join(summary['eligible_candidate_refs'])}`",
        f"- Resolved config path: `{summary['resolved_config_path']}`",
        f"- Redacted config hash: `{summary['redacted_config_hash']}`",
        f"- Reproduction command: `{summary['reproduction_command']}`",
        f"- Failure category: `{summary['failure_category'] or ''}`",
        f"- Error: {summary['error'] or ''}",
        f"- Outcome: `{summary.get('outcome') or ''}`",
        f"- Reason: {summary.get('reason') or ''}",
        f"- Matched rule: `{summary.get('matched_rule') or ''}`",
        f"- Blocks merge: {str(bool(summary.get('blocks_merge'))).lower()}",
        f"- Recommended action: `{summary.get('recommended_action') or ''}`",
        f"- Work root: `{summary['work_root']}`",
        f"- Total: {summary['total']}",
        f"- Passed: {summary['passed']}",
        f"- Failed: {summary['failed']}",
        f"- Tool use recorded: {summary['tool_use_recorded']}",
        f"- Exec command tool use recorded: {summary['exec_command_tool_use_recorded']}",
        f"- TTY recorded: {summary['tty_recorded']}",
        f"- Stdin recorded: {summary['stdin_recorded']}",
        f"- Filesystem verified: {summary['filesystem_verified']}",
        f"- Terminal event verified: {summary['terminal_event_verified']}",
        f"- Thinking observed: {summary['thinking_observed']}",
        f"- Schedule routing verified: {summary['schedule_routing_verified']}",
        f"- Schedule routing failures: {summary['schedule_routing_failures']}",
        f"- Schedule routing variant: `{summary['schedule_routing_variant']}`",
        "",
        "| Provider/model | Protocol | Thinking effort | Status | Tool use | Exec command | TTY | Stdin | Filesystem | Terminal events | Thinking | Schedule routing | Variant | Error stage |",
        "| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |",
    ]
    for result in results:
        lines.append(
            f"| `{result.ref}` | `{result.protocol}` | `{result.thinking_effort}` | "
            f"{result.status} | {result.tool_status} | {result.exec_command_status} | "
            f"{result.tty_status} | {result.stdin_status} | {result.filesystem_status} | "
            f"{result.event_contract_status} | {result.thinking_status} | "
            f"{schedule_routing_status_label(result.schedule_routing_status)} | "
            f"{result.schedule_routing_variant} | {result.error_stage} |"
        )
    summary_md.write_text("\n".join(lines) + "\n", encoding="utf-8")


def schedule_routing_status_label(status: str) -> str:
    return {
        "passed": "passed",
        "failed": "failed",
        "hard_failed": "failed (hard failure)",
        "not_run": "not run",
    }.get(status, status)


def write_selected_config(
    cfg: dict[str, Any],
    provider_id: str,
    model_id: str,
    output_path: pathlib.Path,
    *,
    disable_tools: bool = False,
    compaction: dict[str, Any] | None = None,
) -> None:
    provider, selected_model = selected_provider_model(cfg, provider_id, model_id)
    provider = copy.deepcopy(provider)
    provider["models"] = [copy.deepcopy(selected_model)]
    if disable_tools:
        for target in (provider, provider["models"][0]):
            capabilities = target.get("capabilities")
            if not isinstance(capabilities, dict):
                capabilities = {}
            capabilities["tools"] = False
            target["capabilities"] = capabilities
    out: dict[str, Any] = {
        "models": [f"{provider_id}:{model_id}"],
        "enable_user_agents_resources": False,
        "providers": [provider],
    }
    environment = cfg.get("environment")
    variables = environment.get("variables") if isinstance(environment, dict) else None
    if isinstance(variables, dict):
        selected_variables = {
            key: copy.deepcopy(variables[key])
            for key in SELECTED_PROVIDER_ENVIRONMENT_KEYS
            if key in variables
        }
        if selected_variables:
            out["environment"] = {"variables": selected_variables}
    if compaction:
        out["compaction"] = compaction
    output_path.parent.mkdir(parents=True, exist_ok=True)
    output_path.write_text(dump_yaml(out), encoding="utf-8")
    output_path.chmod(0o600)


def materialize_and_validate_selected_configs(
    juex_bin: str,
    cfg: dict[str, Any],
    rows: list[MatrixRow],
    output_dir: pathlib.Path,
) -> dict[str, pathlib.Path]:
    shutil.rmtree(output_dir, ignore_errors=True)
    output_dir.mkdir(parents=True, mode=0o700)
    materialized: dict[str, pathlib.Path] = {}
    for row in rows:
        output = output_dir / f"{safe_ref(row.ref)}.yaml"
        write_selected_config(cfg, row.provider_id, row.model_id, output)
        validate_source_config(juex_bin, output)
        materialized[row.ref] = output
    return materialized


def selected_provider_model(cfg: dict[str, Any], provider_id: str, model_id: str) -> tuple[dict[str, Any], Any]:
    for provider in selection.merged_providers(cfg):
        if str(provider.get("id") or "").strip() != provider_id:
            continue
        for model in provider.get("models") or []:
            if model_id_from(model) == model_id:
                return provider, model
        raise ValueError(f"model not found: {provider_id}:{model_id}")
    raise ValueError(f"provider not found: {provider_id}")


def validate_source_config(juex_bin: str, config_path: pathlib.Path) -> None:
    """Ask Juex to parse the complete source config without exposing diagnostics."""
    config_path = selection.resolved_path(config_path)
    with tempfile.TemporaryDirectory(prefix="juex-eval-config-check.") as work:
        command = [juex_bin, "-C", work]
        env = os.environ.copy()
        for name in ISOLATED_PROVIDER_ENVIRONMENT_KEYS:
            env.pop(name, None)
        if not env.get("CODEX_HOME", "").strip():
            env["CODEX_HOME"] = str(pathlib.Path.home() / ".codex")
        default_home, effective_home = provider_home_dirs()
        home_config_paths = {home / "juex.yaml" for home in (default_home, effective_home)}
        isolated_user_home = pathlib.Path(work) / "home"
        env["HOME"] = str(isolated_user_home)
        env["USERPROFILE"] = str(isolated_user_home)
        if config_path in home_config_paths:
            env["JUEX_HOME"] = str(config_path.parent)
        else:
            env["JUEX_HOME"] = str(pathlib.Path(work) / "juex-home")
            command.extend(["--config", str(config_path)])
        command.extend(["doctor", "--offline", "--format", "json"])
        result = _run_source_config_validation(command, env)
    _require_valid_source_config(result)


def validate_source_layers(juex_bin: str, layers: list[SourceConfigLayer]) -> None:
    """Ask Juex to validate materialized source documents at their original scopes."""
    by_scope: dict[str, SourceConfigLayer] = {}
    for layer in layers:
        if layer.scope not in {"default-home", "effective-home", "explicit"}:
            raise ValueError("provider config has an unsupported source scope")
        if layer.scope in by_scope:
            raise ValueError("provider config has duplicate source scopes")
        by_scope[layer.scope] = layer

    with tempfile.TemporaryDirectory(prefix="juex-eval-source-check.") as work:
        root = pathlib.Path(work)
        default_home = root / "home" / ".juex"
        effective_home = root / "juex-home"
        explicit_dir = root / "explicit"
        workspace = root / "work"
        for directory in (default_home, effective_home, explicit_dir, workspace):
            directory.mkdir(parents=True, mode=0o700)

        paths = {
            "default-home": default_home / "juex.yaml",
            "effective-home": effective_home / "juex.yaml",
            "explicit": explicit_dir / "juex.yaml",
        }
        for scope, layer in by_scope.items():
            _materialize_source_layer(layer, paths[scope])

        command = [juex_bin, "-C", str(workspace)]
        env = os.environ.copy()
        for name in ISOLATED_PROVIDER_ENVIRONMENT_KEYS:
            env.pop(name, None)
        if not env.get("CODEX_HOME", "").strip():
            env["CODEX_HOME"] = str(pathlib.Path.home() / ".codex")
        env["HOME"] = str(root / "home")
        env["USERPROFILE"] = str(root / "home")
        env["JUEX_HOME"] = str(effective_home if "effective-home" in by_scope else default_home)
        if "explicit" in by_scope:
            command.extend(["--config", str(paths["explicit"])])
        command.extend(["doctor", "--offline", "--format", "json"])
        result = _run_source_config_validation(command, env)
    _require_valid_source_config(result)


def _materialize_source_layer(layer: SourceConfigLayer, config_path: pathlib.Path) -> None:
    imports: list[dict[str, str]] = []
    for index, imported in enumerate(layer.imports):
        imported_path = config_path.parent / "imports" / f"import-{index}.yaml"
        imported_path.parent.mkdir(parents=True, mode=0o700)
        imported_path.write_text(dump_yaml(imported), encoding="utf-8")
        imported_path.chmod(0o600)
        imports.append({"source": str(imported_path.relative_to(config_path.parent))})
    declaring = copy.deepcopy(layer.declaring)
    if imports:
        declaring = {"imports": imports, **declaring}
    config_path.write_text(dump_yaml(declaring), encoding="utf-8")
    config_path.chmod(0o600)


def _run_source_config_validation(command: list[str], env: dict[str, str]) -> Any:
    try:
        completed = subprocess.run(
            command,
            check=False,
            capture_output=True,
            text=True,
            timeout=30,
            env=env,
        )
        return json.loads(completed.stdout)
    except (OSError, subprocess.SubprocessError, json.JSONDecodeError) as exc:
        raise ValueError("provider config validation through Juex failed") from exc


def _require_valid_source_config(result: Any) -> None:
    checks = result.get("checks") if isinstance(result, dict) else None
    config_check = next(
        (check for check in checks or [] if isinstance(check, dict) and check.get("name") == "config"),
        None,
    )
    if not config_check or config_check.get("status") != "ok":
        raise ValueError("provider config is not loadable by Juex")


def write_model_config_command(argv: list[str]) -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--source", required=True)
    parser.add_argument("--ref", default="")
    parser.add_argument("--provider", default="")
    parser.add_argument("--model", default="")
    parser.add_argument("--output", required=True)
    parser.add_argument("--disable-tools", action="store_true")
    parser.add_argument("--compaction-eval", action="store_true")
    parsed = parser.parse_args(argv)
    if parsed.ref and (parsed.provider or parsed.model):
        raise ValueError("--ref is mutually exclusive with --provider/--model")
    if bool(parsed.provider) != bool(parsed.model):
        raise ValueError("--provider and --model must be provided together")
    compaction = None
    if parsed.compaction_eval:
        compaction = {
            "enabled": True,
            "reserve_tokens": 8000,
            "keep_recent_tokens": 6000,
            "summary_max_tokens": 2048,
            "tool_result_max_chars": 1200,
            "user_input_inline_max_bytes": 524288,
        }
    cfg = load_source_config(pathlib.Path(parsed.source).expanduser())
    if parsed.ref:
        provider_id, model_id = split_provider_model_ref(parsed.ref)
    elif parsed.provider:
        provider_id = parsed.provider
        model_id = parsed.model
    else:
        models = cfg.get("models")
        if not isinstance(models, list) or not models:
            raise ValueError("source config must define a non-empty models list")
        provider_id, model_id = split_provider_model_ref(str(models[0] or ""))
    write_selected_config(
        cfg,
        provider_id,
        model_id,
        pathlib.Path(parsed.output),
        disable_tools=parsed.disable_tools,
        compaction=compaction,
    )
    return 0


def split_provider_model_ref(raw: str) -> tuple[str, str]:
    provider_id, separator, model_id = raw.strip().partition(":")
    if not separator or not provider_id.strip() or not model_id.strip():
        raise ValueError(f"model ref must be provider:model, got {raw!r}")
    return provider_id.strip(), model_id.strip()


def run_timeout_command(argv: list[str]) -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--seconds", type=int, default=60)
    parser.add_argument("command", nargs=argparse.REMAINDER)
    parsed = parser.parse_args(argv)
    command = parsed.command
    if command and command[0] == "--":
        command = command[1:]
    if not command:
        raise ValueError("run-timeout requires a command")
    return run_subprocess_with_timeout(command, parsed.seconds, stdin=sys.stdin, stdout=sys.stdout, stderr=sys.stderr)


def append_command(argv: list[str]) -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--file", required=True)
    parser.add_argument("--label", required=True)
    parser.add_argument("--command", required=True)
    parser.add_argument("--status", type=int, required=True)
    parser.add_argument("--log", required=True)
    parsed = parser.parse_args(argv)
    append_jsonl(
        pathlib.Path(parsed.file),
        {
            "label": parsed.label,
            "command": parsed.command.strip(),
            "exit_status": parsed.status,
            "log": parsed.log,
        },
    )
    return 0


def write_development_record_command(argv: list[str]) -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--report-dir", required=True)
    parser.add_argument("--run-id", required=True)
    parser.add_argument("--commands-file", required=True)
    parser.add_argument("--provider-summary", default="")
    parser.add_argument("--compaction-dir", default="")
    parser.add_argument("--status", type=int, required=True)
    parser.add_argument("--record-json", required=True)
    parser.add_argument("--record-md", required=True)
    parsed = parser.parse_args(argv)
    write_development_record(
        pathlib.Path(parsed.report_dir),
        parsed.run_id,
        pathlib.Path(parsed.commands_file),
        pathlib.Path(parsed.provider_summary) if parsed.provider_summary else None,
        parsed.compaction_dir,
        parsed.status,
        pathlib.Path(parsed.record_json),
        pathlib.Path(parsed.record_md),
    )
    return 0


def write_development_record(
    report_dir: pathlib.Path,
    run_id: str,
    commands_file: pathlib.Path,
    provider_summary_path: pathlib.Path | None,
    compaction_dir: str,
    status: int,
    record_json: pathlib.Path,
    record_md: pathlib.Path,
) -> None:
    commands = [json.loads(line) for line in commands_file.read_text(encoding="utf-8").splitlines() if line.strip()]
    provider = None
    if provider_summary_path and provider_summary_path.is_file():
        provider = json.loads(provider_summary_path.read_text(encoding="utf-8"))
    compaction = None
    compaction_summary_path = pathlib.Path(compaction_dir) / "summary.json" if compaction_dir else None
    if compaction_summary_path and compaction_summary_path.is_file():
        compaction = json.loads(compaction_summary_path.read_text(encoding="utf-8"))
    branch = command_output(["git", "branch", "--show-current"]).strip()
    commit = command_output(["git", "rev-parse", "HEAD"]).strip()
    dirty = bool(command_output(["git", "status", "--short"]).strip())
    record_status = "pass" if status == 0 else "fail"
    outcome_summary = development_outcome_summary(commands, status)
    record = {
        "run_id": run_id,
        "branch": branch,
        "commit": commit,
        "dirty": dirty,
        "status": record_status,
        **outcome_summary,
        "commands": commands,
        "provider_model_smoke": provider,
        "compaction_eval": compaction,
        "compaction_eval_dir": compaction_dir or None,
    }
    record_json.write_text(json.dumps(record, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")

    lines = [
        "# Development Evaluation Record",
        "",
        f"- Run ID: `{run_id}`",
        f"- Branch: `{branch}`",
        f"- Commit: `{commit}`",
        f"- Dirty worktree at record time: {str(dirty).lower()}",
        f"- Status: {record_status}",
        f"- Blocks merge: {str(outcome_summary['blocks_merge']).lower()}",
        f"- Failure type: {outcome_summary['failure_type'] or 'none'}",
        f"- Recommended action: {outcome_summary['recommended_action']}",
        "",
        "## Commands",
        "",
        "| Label | Outcome | Reason | Rule | Attempts | Exit | Log |",
        "| --- | --- | --- | --- | ---: | ---: | --- |",
    ]
    for command in commands:
        lines.append(
            f"| `{command['label']}` | {command.get('outcome') or ''} | "
            f"{command.get('reason') or ''} | {command.get('matched_rule') or ''} | "
            f"{len(command.get('attempts') or [])} | {command['exit_status']} | `{command['log']}` |"
        )
    lines.extend(["", "## Provider Model Smoke", ""])
    if provider:
        lines.append(f"- Summary: `{provider_summary_path}`")
        append_selection_record(lines, provider)
        for key, label in [
            ("total", "Total"),
            ("passed", "Passed"),
            ("failed", "Failed"),
            ("tool_use_recorded", "Tool use recorded"),
            ("exec_command_tool_use_recorded", "Exec command tool use recorded"),
            ("tty_recorded", "TTY recorded"),
            ("stdin_recorded", "Stdin recorded"),
            ("filesystem_verified", "Filesystem verified"),
            ("terminal_event_verified", "Terminal event verified"),
            ("thinking_observed", "Thinking observed"),
            ("schedule_routing_verified", "Schedule routing verified"),
            ("schedule_routing_failures", "Schedule routing failures"),
            ("schedule_routing_variant", "Schedule routing variant"),
        ]:
            if key in provider:
                lines.append(f"- {label}: {provider[key]}")
    else:
        lines.append("- Not run.")
    lines.extend(["", "## Quality Evaluation", ""])
    if compaction_dir:
        lines.append(f"- Compaction evaluation: `{compaction_dir}`")
        if compaction:
            append_selection_record(lines, compaction)
        for scorecard in sorted(pathlib.Path(compaction_dir).glob("*/scorecard.md")):
            model, score = scorecard_model_and_score(scorecard)
            lines.append(f"- {model}: {score}")
    else:
        lines.append("- Not run. Run with `--compaction-eval` when touching compaction, context projection, provider replay, or long-session behavior.")
    record_md.write_text("\n".join(lines) + "\n", encoding="utf-8")


def development_outcome_summary(commands: list[dict[str, Any]], status: int) -> dict[str, Any]:
    blocking = [row for row in commands if row.get("blocks_merge") is True]
    kinds = {str(row.get("outcome")) for row in blocking}
    if outcomes.PRODUCT_FAILURE in kinds:
        failure_type = "code_failure"
        action = "fix_code"
    elif outcomes.ENVIRONMENT_FAILURE in kinds:
        failure_type = "validation_incomplete"
        action = "fix_environment"
    elif blocking or status:
        failure_type = "validation_incomplete"
        action = "stop"
    else:
        failure_type = None
        action = "continue"
    return {
        "blocks_merge": bool(blocking or status),
        "failure_type": failure_type,
        "recommended_action": action,
        "blocking_steps": [str(row.get("label")) for row in blocking],
    }


def append_selection_record(lines: list[str], summary: dict[str, Any]) -> None:
    for key, label in [
        ("selection_source", "Selection source"),
        ("selected_provider_model", "Selected provider/model"),
        ("selected_provider_models", "Selected provider/models"),
        ("selection_seed", "Selection seed"),
        ("eligible_candidate_count", "Eligible candidate count"),
        ("eligible_candidate_refs", "Eligible candidate refs"),
        ("resolved_config_path", "Resolved config path"),
        ("redacted_config_hash", "Redacted config hash"),
        ("reproduction_command", "Reproduction command"),
        ("failure_category", "Failure category"),
    ]:
        if key not in summary:
            continue
        value = summary[key]
        if isinstance(value, list):
            value = ", ".join(str(item) for item in value)
        lines.append(f"- {label}: {value or ''}")


def scorecard_model_and_score(path: pathlib.Path) -> tuple[str, str]:
    model = str(path)
    score = "score not found"
    for line in path.read_text(encoding="utf-8", errors="replace").splitlines():
        if line.startswith("- Model:"):
            model = line.removeprefix("- Model:").strip()
        if line.startswith("- Score:"):
            score = line.removeprefix("- Score:").strip()
    return model, score


def command_output(command: list[str]) -> str:
    try:
        return subprocess.check_output(command, text=True, stderr=subprocess.DEVNULL)
    except subprocess.CalledProcessError:
        return ""


def append_jsonl(path: pathlib.Path, value: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("a", encoding="utf-8") as handle:
        handle.write(json.dumps(value, ensure_ascii=False, separators=(",", ":")) + "\n")


def load_yaml_file(path: pathlib.Path) -> dict[str, Any]:
    return _load_yaml_text(path.read_text(encoding="utf-8"), str(path))


def load_source_config(path: pathlib.Path) -> dict[str, Any]:
    """Load the same Home layers plus explicit overlay used by Juex validation."""
    config, _ = load_source_config_with_layers(path)
    return config


def load_source_config_with_layers(path: pathlib.Path) -> tuple[dict[str, Any], list[SourceConfigLayer]]:
    """Load selection data and retain the exact source-scope layers for Juex validation."""
    config_path = selection.resolved_path(path)
    default_home, effective_home = provider_home_dirs()
    default_config = default_home / "juex.yaml"
    effective_config = effective_home / "juex.yaml"
    if config_path == default_config:
        sources = [default_config]
    elif config_path == effective_config:
        sources = [default_config, effective_config]
    else:
        sources = [default_config, effective_config, config_path]
    merged: dict[str, Any] = {}
    seen: set[pathlib.Path] = set()
    remote_memo: dict[str, tuple[str, str]] = {}
    layers: list[SourceConfigLayer] = []
    explicit_paths = [] if config_path in {default_config, effective_config} else [config_path]
    cache_context_digest = _config_import_context_digest(pathlib.Path.cwd(), *explicit_paths)
    with _config_import_cache_lock(effective_home):
        for source in sources:
            source = selection.resolved_path(source)
            if source in seen:
                continue
            seen.add(source)
            if source != config_path and not source.is_file():
                continue
            document, imported, declaring = _load_config_document_parts(source, remote_memo, cache_context_digest)
            merged = _merge_source_config(merged, document)
            if source == default_config:
                scope = "default-home"
            elif source == effective_config:
                scope = "effective-home"
            else:
                scope = "explicit"
            layers.append(SourceConfigLayer(scope=scope, imports=tuple(imported), declaring=declaring))
    return merged, layers


def _load_config_document(
    path: pathlib.Path,
    remote_memo: dict[str, tuple[str, str]] | None = None,
) -> dict[str, Any]:
    document, _, _ = _load_config_document_parts(path, remote_memo, _config_import_standalone_digest())
    return document


def _load_config_document_parts(
    path: pathlib.Path,
    remote_memo: dict[str, tuple[str, str]] | None = None,
    cache_context_digest: str | None = None,
) -> tuple[dict[str, Any], list[dict[str, Any]], dict[str, Any]]:
    if remote_memo is None:
        remote_memo = {}
    main = load_yaml_file(path)
    imported = _config_imports(main, str(path))
    merged: dict[str, Any] = {}
    imported_values: list[dict[str, Any]] = []
    cache_context_digest = cache_context_digest or _config_import_standalone_digest()
    for index, raw_source in enumerate(imported):
        source, label = _read_config_import(path, raw_source, remote_memo, cache_context_digest)
        value = _load_yaml_text(source, label)
        if "imports" in value:
            raise ValueError(f"{path} imports[{index}] {label}: nested imports are not supported")
        imported_values.append(value)
        merged = _merge_source_config(merged, value)
    declaring = copy.deepcopy(main)
    declaring.pop("imports", None)
    return _merge_source_config(merged, declaring), imported_values, declaring


def _config_imports(value: dict[str, Any], label: str) -> list[str]:
    imports = value.get("imports")
    if imports is None:
        return []
    if not isinstance(imports, list):
        raise ValueError(f"{label} imports must be a YAML sequence")
    sources: list[str] = []
    for index, item in enumerate(imports):
        if not isinstance(item, dict):
            raise ValueError(f"{label} imports[{index}] must be a YAML mapping")
        if set(item) != {"source"}:
            raise ValueError(f"{label} imports[{index}] must contain only source")
        source = item.get("source")
        if not isinstance(source, str) or not source.strip():
            raise ValueError(f"{label} imports[{index}].source must be a non-empty YAML string")
        sources.append(source.strip())
    return sources


def _read_config_import(
    declaring: pathlib.Path,
    raw_source: str,
    remote_memo: dict[str, tuple[str, str]],
    cache_context_digest: str,
) -> tuple[str, str]:
    local_candidate = pathlib.Path(raw_source)
    if local_candidate.is_absolute():
        source = local_candidate.resolve()
        return source.read_text(encoding="utf-8"), str(source)
    if "://" not in raw_source:
        source = (declaring.parent / raw_source).resolve()
        return source.read_text(encoding="utf-8"), str(source)
    parsed = urllib.parse.urlsplit(raw_source)
    if parsed.scheme.lower() not in {"http", "https"}:
        raise ValueError(f"unsupported config import URL scheme {parsed.scheme!r}")
    if not parsed.netloc or parsed.username is not None or parsed.password is not None or parsed.fragment:
        raise ValueError("invalid remote config import source")
    if any(ord(character) < 0x20 for character in raw_source):
        raise ValueError("invalid remote config import source")
    if raw_source in remote_memo:
        return remote_memo[raw_source]
    content, fresh = _read_remote_config_import(raw_source, parsed, declaring, cache_context_digest)
    result = content, _safe_remote_import_label(parsed)
    if fresh:
        remote_memo[raw_source] = result
    return result


class _ConfigImportRedirectHandler(urllib.request.HTTPRedirectHandler):
    max_redirections = 3
    max_repeats = 3

    def redirect_request(self, req, fp, code, msg, headers, newurl):  # noqa: ANN001, ANN201 - urllib hook signature.
        old_scheme = urllib.parse.urlsplit(req.full_url).scheme.lower()
        parsed = urllib.parse.urlsplit(newurl)
        if parsed.scheme.lower() not in {"http", "https"}:
            raise urllib.error.URLError("config import redirect uses an unsupported scheme")
        if old_scheme == "https" and parsed.scheme.lower() == "http":
            raise urllib.error.URLError("config import redirect from https to http is not allowed")
        if parsed.username is not None or parsed.password is not None or parsed.fragment:
            raise urllib.error.URLError("config import redirect contains forbidden URL metadata")
        return super().redirect_request(req, fp, code, msg, headers, newurl)


def _read_remote_config_import(
    identity: str,
    parsed: urllib.parse.SplitResult,
    declaring: pathlib.Path,
    cache_context_digest: str,
) -> tuple[str, bool]:
    cache = _read_config_import_cache(identity, parsed, declaring, cache_context_digest)
    headers: dict[str, str] = {}
    if cache is not None:
        if cache.get("etag"):
            headers["If-None-Match"] = str(cache["etag"])
        if cache.get("last_modified"):
            headers["If-Modified-Since"] = str(cache["last_modified"])
    request = urllib.request.Request(identity, headers=headers, method="GET")
    opener = urllib.request.build_opener(_ConfigImportRedirectHandler())
    try:
        with opener.open(request, timeout=CONFIG_IMPORT_TIMEOUT_SECONDS) as response:
            if response.status != 200:
                raise ValueError(f"remote config import returned HTTP {response.status}")
            data = response.read(CONFIG_IMPORT_MAX_BYTES + 1)
            if len(data) > CONFIG_IMPORT_MAX_BYTES:
                raise ValueError("remote config import exceeds the one-MiB response limit")
            return data.decode("utf-8"), True
    except urllib.error.HTTPError as exc:
        if exc.code == 304 and cache is not None:
            return str(cache["content"]), True
        if exc.code not in {408, 429} and exc.code < 500:
            raise ValueError(f"remote config import returned HTTP {exc.code}") from None
        if cache is not None and _config_import_cache_is_current(cache):
            return str(cache["content"]), False
        raise ValueError("remote config import is unavailable and has no current Last-Known-Good cache") from None
    except (OSError, urllib.error.URLError):
        if cache is not None and _config_import_cache_is_current(cache):
            return str(cache["content"]), False
        raise ValueError("remote config import is unavailable and has no current Last-Known-Good cache") from None


def _read_config_import_cache(
    identity: str,
    parsed: urllib.parse.SplitResult,
    declaring: pathlib.Path,
    cache_context_digest: str,
) -> dict[str, Any] | None:
    _, effective_home = provider_home_dirs()
    source_digest = hashlib.sha256(identity.encode("utf-8")).hexdigest()
    declaring_identity = _config_import_path_identity(declaring)
    declaring_digest = hashlib.sha256(str(declaring_identity).encode("utf-8")).hexdigest()
    path = effective_home / "cache" / "config-imports" / f"{source_digest}-{declaring_digest}-{cache_context_digest}.json"
    try:
        if os.name != "nt" and (path.stat().st_mode & 0o777) != 0o600:
            return None
        record = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, ValueError, json.JSONDecodeError):
        return None
    if (
        not isinstance(record, dict)
        or record.get("version") != 3
        or record.get("source_sha256") != source_digest
        or record.get("declaring_sha256") != declaring_digest
        or record.get("context_sha256") != cache_context_digest
    ):
        return None
    safe_source = _safe_remote_import_label(parsed)
    content = record.get("content")
    content_digest = record.get("content_sha256")
    if record.get("source") != safe_source or not isinstance(content, str) or len(content.encode("utf-8")) > CONFIG_IMPORT_MAX_BYTES:
        return None
    if content_digest != "sha256:" + hashlib.sha256(content.encode("utf-8")).hexdigest():
        return None
    for name in ("etag", "last_modified"):
        value = record.get(name)
        if value is not None and (not isinstance(value, str) or len(value) > 8192 or "\r" in value or "\n" in value):
            return None
    return record


def _config_import_standalone_digest() -> str:
    return hashlib.sha256(b"standalone").hexdigest()


def _config_import_context_digest(work_dir: pathlib.Path, *explicit_paths: pathlib.Path) -> str:
    identities = [f"work_dir={_config_import_path_identity(work_dir)}"]
    identities.extend(f"explicit={_config_import_path_identity(path)}" for path in explicit_paths)
    return hashlib.sha256("\n".join(identities).encode("utf-8")).hexdigest()


def _config_import_path_identity(path: pathlib.Path) -> pathlib.Path:
    absolute = pathlib.Path(os.path.abspath(os.path.normpath(os.fspath(path))))
    try:
        parent = absolute.parent.resolve(strict=True)
    except OSError:
        return absolute
    return parent / absolute.name


@contextlib.contextmanager
def _config_import_cache_lock(effective_home: pathlib.Path) -> Iterator[None]:
    lock_path = effective_home / ".locks" / "config-imports-cache.lock"
    lock_path.parent.mkdir(parents=True, mode=0o700, exist_ok=True)
    descriptor = os.open(lock_path, os.O_CREAT | os.O_RDWR, 0o600)
    locked = False
    try:
        if os.name == "nt":
            import msvcrt

            if os.fstat(descriptor).st_size == 0:
                os.write(descriptor, b"\0")
            os.lseek(descriptor, 0, os.SEEK_SET)
            msvcrt.locking(descriptor, msvcrt.LK_LOCK, 1)
        else:
            import fcntl

            fcntl.flock(descriptor, fcntl.LOCK_EX)
        locked = True
    except OSError as exc:
        os.close(descriptor)
        raise ValueError(f"cannot lock config import cache: {exc}") from None
    try:
        yield
    finally:
        try:
            if locked:
                if os.name == "nt":
                    import msvcrt

                    os.lseek(descriptor, 0, os.SEEK_SET)
                    msvcrt.locking(descriptor, msvcrt.LK_UNLCK, 1)
                else:
                    import fcntl

                    fcntl.flock(descriptor, fcntl.LOCK_UN)
        finally:
            os.close(descriptor)


def _config_import_cache_is_current(record: dict[str, Any]) -> bool:
    fetched_at = record.get("fetched_at")
    if not isinstance(fetched_at, str):
        return False
    try:
        instant = datetime.datetime.fromisoformat(fetched_at.replace("Z", "+00:00"))
    except ValueError:
        return False
    if instant.tzinfo is None:
        return False
    age = datetime.datetime.now(datetime.timezone.utc) - instant.astimezone(datetime.timezone.utc)
    return datetime.timedelta(0) <= age <= CONFIG_IMPORT_MAX_CACHE_AGE


def _safe_remote_import_label(parsed: urllib.parse.SplitResult) -> str:
    return urllib.parse.urlunsplit((parsed.scheme, parsed.netloc, parsed.path, "", ""))


def _load_yaml_text(text: str, label: str) -> dict[str, Any]:
    node = yaml.compose(text)
    if node is not None:
        _reject_duplicate_yaml_keys(node, label, set())
    value = yaml.safe_load(text)
    if value is None:
        value = {}
    if not isinstance(value, dict):
        raise ValueError(f"{label} must contain a YAML mapping")
    if isinstance(node, yaml.nodes.MappingNode):
        _restore_runtime_string_values(value, node)
    return value


def _reject_duplicate_yaml_keys(node: yaml.nodes.Node, label: str, visited: set[int]) -> None:
    identity = id(node)
    if identity in visited:
        return
    visited.add(identity)
    if isinstance(node, yaml.nodes.MappingNode):
        seen: set[tuple[str, str]] = set()
        for key, value in node.value:
            if isinstance(key, yaml.nodes.ScalarNode):
                marker = (key.tag, key.value)
                if marker in seen:
                    raise ValueError(f"{label} contains duplicate YAML key {key.value!r}")
                seen.add(marker)
            _reject_duplicate_yaml_keys(key, label, visited)
            _reject_duplicate_yaml_keys(value, label, visited)
    elif isinstance(node, yaml.nodes.SequenceNode):
        for item in node.value:
            _reject_duplicate_yaml_keys(item, label, visited)


def provider_home_dirs() -> tuple[pathlib.Path, pathlib.Path]:
    default_home = selection.resolved_path(pathlib.Path.home() / ".juex")
    configured_home = os.environ.get("JUEX_HOME", "").strip()
    effective_home = selection.resolved_path(configured_home) if configured_home else default_home
    return default_home, effective_home


def _merge_source_config(base: dict[str, Any], override: dict[str, Any]) -> dict[str, Any]:
    merged = copy.deepcopy(base)
    for name, value in override.items():
        if name == "models":
            if value is not None:
                merged[name] = copy.deepcopy(value)
        elif name == "providers":
            if value is None:
                continue
            if isinstance(value, list):
                existing = merged.get(name)
                merged[name] = [*(existing if isinstance(existing, list) else []), *copy.deepcopy(value)]
            else:
                merged[name] = copy.deepcopy(value)
        elif name == "environment":
            if value is None:
                continue
            if not isinstance(value, dict):
                merged[name] = copy.deepcopy(value)
                continue
            environment = copy.deepcopy(merged.get(name)) if isinstance(merged.get(name), dict) else {}
            for environment_name, environment_value in value.items():
                if environment_name == "variables":
                    if environment_value is None:
                        continue
                    if not isinstance(environment_value, dict):
                        environment[environment_name] = copy.deepcopy(environment_value)
                        continue
                    variables = environment.get(environment_name)
                    variables = copy.deepcopy(variables) if isinstance(variables, dict) else {}
                    variables.update(copy.deepcopy(environment_value))
                    environment[environment_name] = variables
                else:
                    environment[environment_name] = copy.deepcopy(environment_value)
            merged[name] = environment
        elif name == "runtime":
            merged[name] = _merge_config_mapping(merged.get(name), value)
        elif name == "compaction":
            merged[name] = _merge_positive_config_mapping(
                merged.get(name),
                value,
                positive_fields={
                    "reserve_tokens",
                    "keep_recent_tokens",
                    "summary_max_tokens",
                    "tool_result_max_chars",
                    "user_input_inline_max_bytes",
                    "user_input_preview_head_bytes",
                    "user_input_preview_tail_bytes",
                    "max_auto_failures",
                },
                string_fields={"summary_model"},
                nullable_fields={"enabled", "instructions"},
            )
        elif name == "tool_output":
            merged[name] = _merge_positive_config_mapping(
                merged.get(name),
                value,
                positive_fields={"inline_max_bytes", "preview_head_bytes", "preview_tail_bytes"},
            )
        elif name == "skills":
            merged[name] = _merge_positive_config_mapping(
                merged.get(name),
                value,
                positive_fields={"prompt_budget_chars"},
                nullable_fields={"include", "exclude"},
            )
        elif name == "extensions":
            merged[name] = _merge_nullable_config_mapping(merged.get(name), value, {"allow"})
        elif name == "modules":
            merged[name] = _merge_modules_config(merged.get(name), value)
        elif name == "fleet":
            merged[name] = _merge_fleet_config(merged.get(name), value)
        elif name == "hooks":
            merged[name] = _merge_hooks_config(merged.get(name), value)
        elif name == "sandbox":
            merged[name] = _merge_sandbox_config(merged.get(name), value)
        else:
            merged[name] = copy.deepcopy(value)
    return merged


def _merge_config_mapping(base: Any, override: Any) -> Any:
    if override is None:
        return copy.deepcopy(base)
    if not isinstance(override, dict):
        return copy.deepcopy(override)
    merged = copy.deepcopy(base) if isinstance(base, dict) else {}
    merged.update(copy.deepcopy(override))
    return merged


def _merge_positive_config_mapping(
    base: Any,
    override: Any,
    *,
    positive_fields: set[str],
    string_fields: set[str] | None = None,
    nullable_fields: set[str] | None = None,
) -> Any:
    if override is None:
        return copy.deepcopy(base)
    if not isinstance(override, dict):
        return copy.deepcopy(override)
    merged = copy.deepcopy(base) if isinstance(base, dict) else {}
    string_fields = string_fields or set()
    nullable_fields = nullable_fields or set()
    for name, value in override.items():
        if name in nullable_fields:
            if value is not None:
                merged[name] = copy.deepcopy(value)
        elif name in string_fields:
            if isinstance(value, str) and value.strip():
                merged[name] = copy.deepcopy(value)
            elif not isinstance(value, (str, type(None))):
                merged[name] = copy.deepcopy(value)
        elif name in positive_fields:
            if isinstance(value, int) and not isinstance(value, bool):
                if value > 0:
                    merged[name] = value
            elif value is not None:
                merged[name] = copy.deepcopy(value)
        else:
            merged[name] = copy.deepcopy(value)
    return merged


def _merge_nullable_config_mapping(base: Any, override: Any, nullable_fields: set[str]) -> Any:
    if override is None:
        return copy.deepcopy(base)
    if not isinstance(override, dict):
        return copy.deepcopy(override)
    merged = copy.deepcopy(base) if isinstance(base, dict) else {}
    for name, value in override.items():
        if name not in nullable_fields or value is not None:
            merged[name] = copy.deepcopy(value)
    return merged


def _merge_modules_config(base: Any, override: Any) -> Any:
    if override is None:
        return copy.deepcopy(base)
    if not isinstance(override, dict):
        return copy.deepcopy(override)
    merged = copy.deepcopy(base) if isinstance(base, dict) else {}
    for module_id, settings in override.items():
        if not isinstance(settings, dict):
            merged[module_id] = copy.deepcopy(settings)
            continue
        existing = merged.get(module_id)
        module = copy.deepcopy(existing) if isinstance(existing, dict) else {}
        if settings.get("enabled") is not None:
            module["enabled"] = copy.deepcopy(settings["enabled"])
        for field, value in settings.items():
            if field != "enabled":
                module[field] = copy.deepcopy(value)
        merged[module_id] = module
    return merged


def _merge_fleet_config(base: Any, override: Any) -> Any:
    if override is None:
        return copy.deepcopy(base)
    if not isinstance(override, dict):
        return copy.deepcopy(override)
    merged = copy.deepcopy(base) if isinstance(base, dict) else {}
    for name, value in override.items():
        if name == "addr" and isinstance(value, str) and not value.strip():
            continue
        if name == "unsafe_bind_any" and value is None:
            continue
        merged[name] = copy.deepcopy(value)
    return merged


def _merge_hooks_config(base: Any, override: Any) -> Any:
    if override is None:
        return copy.deepcopy(base)
    if not isinstance(override, dict):
        return copy.deepcopy(override)
    merged = copy.deepcopy(base) if isinstance(base, dict) else {}
    for name, value in override.items():
        if name == "commands" and isinstance(value, list):
            existing = merged.get(name)
            merged[name] = [*(existing if isinstance(existing, list) else []), *copy.deepcopy(value)]
        else:
            merged[name] = copy.deepcopy(value)
    return merged


def _merge_sandbox_config(base: Any, override: Any) -> Any:
    if not isinstance(override, dict):
        return copy.deepcopy(override)
    merged = copy.deepcopy(base) if isinstance(base, dict) else {}
    for name, value in override.items():
        if name == "file_system" and isinstance(value, dict):
            file_system = copy.deepcopy(merged.get(name)) if isinstance(merged.get(name), dict) else {}
            for field, field_value in value.items():
                if field == "blocked_paths" and isinstance(field_value, list):
                    existing = file_system.get(field)
                    if field_value:
                        file_system[field] = [
                            *(existing if isinstance(existing, list) else []),
                            *copy.deepcopy(field_value),
                        ]
                elif field == "outside_workspace" and isinstance(field_value, str) and not field_value.strip():
                    continue
                else:
                    file_system[field] = copy.deepcopy(field_value)
            merged[name] = file_system
        elif name == "network" and isinstance(value, dict):
            merged[name] = _merge_nullable_config_mapping(merged.get(name), value, {"enabled"})
        elif name == "enabled" and value is None:
            continue
        else:
            merged[name] = copy.deepcopy(value)
    return merged


def _restore_runtime_string_values(value: dict[str, Any], root: yaml.nodes.MappingNode) -> None:
    root_nodes = _mapping_nodes(root)
    environment = value.get("environment")
    environment_node = root_nodes.get("environment")
    if isinstance(environment, dict) and isinstance(environment_node, yaml.nodes.MappingNode):
        _restore_string_map_field(environment, environment_node, "variables")
    providers = value.get("providers")
    providers_node = root_nodes.get("providers")
    if not isinstance(providers, list) or not isinstance(providers_node, yaml.nodes.SequenceNode):
        return
    for provider, provider_node in zip(providers, providers_node.value, strict=False):
        if not isinstance(provider, dict) or not isinstance(provider_node, yaml.nodes.MappingNode):
            continue
        for name in ("id", "protocol", "base_url", "api_key"):
            _restore_string_field(provider, provider_node, name)
        _restore_compat_string_fields(provider, provider_node)
        for name in ("headers", "query"):
            _restore_string_map_field(provider, provider_node, name)
        models = provider.get("models")
        models_node = _mapping_nodes(provider_node).get("models")
        if not isinstance(models, list) or not isinstance(models_node, yaml.nodes.SequenceNode):
            continue
        for model, model_node in zip(models, models_node.value, strict=False):
            if not isinstance(model, dict) or not isinstance(model_node, yaml.nodes.MappingNode):
                continue
            for name in ("id", "thinking_effort"):
                _restore_string_field(model, model_node, name)
            _restore_compat_string_fields(model, model_node)
            for name in ("headers", "query"):
                _restore_string_map_field(model, model_node, name)


def _restore_string_field(container: dict[str, Any], node: yaml.nodes.MappingNode, name: str) -> None:
    field_node = _mapping_nodes(node).get(name)
    if name in container and isinstance(field_node, yaml.nodes.ScalarNode):
        container[name] = _runtime_string_node_value(field_node)


def _restore_compat_string_fields(container: dict[str, Any], node: yaml.nodes.MappingNode) -> None:
    compat = container.get("compat")
    compat_node = _mapping_nodes(node).get("compat")
    if not isinstance(compat, dict) or not isinstance(compat_node, yaml.nodes.MappingNode):
        return
    _restore_string_field(compat, compat_node, "codex_transport")
    fields_node = _mapping_nodes(compat_node).get("reasoning_replay_fields")
    fields = compat.get("reasoning_replay_fields")
    if isinstance(fields, list) and isinstance(fields_node, yaml.nodes.SequenceNode):
        compat["reasoning_replay_fields"] = [
            _runtime_string_node_value(item) if isinstance(item, yaml.nodes.ScalarNode) else value
            for value, item in zip(fields, fields_node.value, strict=False)
        ]


def _restore_string_map_field(container: dict[str, Any], node: yaml.nodes.MappingNode, name: str) -> None:
    current = container.get(name)
    field_node = _mapping_nodes(node).get(name)
    if not isinstance(current, dict) or not isinstance(field_node, yaml.nodes.MappingNode):
        return
    container[name] = _runtime_string_map_node(field_node)


def _runtime_string_map_node(node: yaml.nodes.MappingNode) -> dict[str, str]:
    restored: dict[str, str] = {}
    for key_node, value_node in node.value:
        if isinstance(key_node, yaml.nodes.ScalarNode) and key_node.value == "<<":
            restored.update(_runtime_merged_string_maps(value_node))
    for key_node, value_node in node.value:
        if (
            isinstance(key_node, yaml.nodes.ScalarNode)
            and key_node.value != "<<"
            and isinstance(value_node, yaml.nodes.ScalarNode)
        ):
            restored[_runtime_string_node_value(key_node)] = _runtime_string_node_value(value_node)
    return restored


def _runtime_merged_string_maps(node: yaml.nodes.Node) -> dict[str, str]:
    if isinstance(node, yaml.nodes.MappingNode):
        return _runtime_string_map_node(node)
    if isinstance(node, yaml.nodes.SequenceNode):
        restored: dict[str, str] = {}
        for item in reversed(node.value):
            if isinstance(item, yaml.nodes.MappingNode):
                restored.update(_runtime_string_map_node(item))
        return restored
    return {}


def _runtime_string_node_value(node: yaml.nodes.ScalarNode) -> str:
    return "" if node.tag == "tag:yaml.org,2002:null" else node.value


def _mapping_nodes(node: yaml.nodes.MappingNode) -> dict[str, yaml.nodes.Node]:
    restored: dict[str, yaml.nodes.Node] = {}
    for key, value in node.value:
        if isinstance(key, yaml.nodes.ScalarNode) and key.value == "<<":
            restored.update(_merged_mapping_nodes(value))
    for key, value in node.value:
        if isinstance(key, yaml.nodes.ScalarNode) and key.value != "<<":
            restored[key.value] = value
    return restored


def _merged_mapping_nodes(node: yaml.nodes.Node) -> dict[str, yaml.nodes.Node]:
    if isinstance(node, yaml.nodes.MappingNode):
        return _mapping_nodes(node)
    if isinstance(node, yaml.nodes.SequenceNode):
        restored: dict[str, yaml.nodes.Node] = {}
        for item in reversed(node.value):
            if isinstance(item, yaml.nodes.MappingNode):
                restored.update(_mapping_nodes(item))
        return restored
    return {}


def safe_config_error(exc: Exception) -> str:
    if isinstance(exc, yaml.YAMLError):
        return "provider config YAML is invalid"
    return str(exc)


def dump_yaml(value: Any) -> str:
    return yaml.safe_dump(value, sort_keys=False, allow_unicode=True)


def json_file_value(path: pathlib.Path, key: str) -> str:
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except Exception:  # noqa: BLE001
        return ""
    value = data.get(key) if isinstance(data, dict) else ""
    return "" if value is None else str(value)


def file_contains(path: pathlib.Path, needle: str) -> bool:
    try:
        return needle in path.read_text(encoding="utf-8", errors="replace")
    except OSError:
        return False


def tail_file(path: pathlib.Path, lines: int) -> str:
    try:
        content = path.read_text(encoding="utf-8", errors="replace").splitlines()
    except OSError:
        return ""
    return "\n".join(content[-lines:]) + ("\n" if content else "")


def safe_ref(ref: str) -> str:
    slug = re.sub(r"[^A-Za-z0-9._-]", "_", ref)[:80] or "ref"
    digest = hashlib.sha256(ref.encode("utf-8")).hexdigest()[:12]
    return f"{slug}-{digest}"


if __name__ == "__main__":
    sys.exit(main())
