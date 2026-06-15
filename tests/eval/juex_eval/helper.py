#!/usr/bin/env python3
"""Local evaluation helper for JueX development scripts.

This is intentionally a repository-local script, not production runtime code.
It runs through uv-managed dependencies so validation scripts behave
consistently across developer machines.
"""

from __future__ import annotations

import argparse
import copy
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
from dataclasses import dataclass
from typing import Any

import yaml

try:
    from . import contract_oracle
except ImportError:  # pragma: no cover - direct script fallback.
    import contract_oracle  # type: ignore[no-redef]


REPO_ROOT = pathlib.Path(__file__).resolve().parents[3]
REPORT_ROOT = REPO_ROOT / ".tmp" / "reports"


def main() -> int:
    return main_with_args(sys.argv[1:])


def main_with_args(argv: list[str]) -> int:
    if len(argv) < 1:
        print(
            "usage: evalhelper.py "
            "<provider-smoke|list-models|write-model-config|run-timeout|append-command|write-development-record> ...",
            file=sys.stderr,
        )
        return 2

    command = argv[0]
    args = argv[1:]
    try:
        if command == "provider-smoke":
            return provider_smoke(args)
        if command == "list-models":
            return list_models(args)
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
        description="Run live multi-turn smoke tests for selected provider/model refs.",
    )
    parser.add_argument("--juex", default=env_default("JUEX_BIN", default_juex_bin()))
    parser.add_argument("--config", default=env_default("JUEX_PROVIDER_CONFIG", str(pathlib.Path.home() / ".juex" / "juex.yaml")))
    parser.add_argument("--model-list", default=env_default("JUEX_LIVE_MODEL_LIST", str(REPO_ROOT / "tests" / "eval" / "live-models.yaml")))
    parser.add_argument(
        "--all-models",
        action="store_true",
        default=env_bool("JUEX_PROVIDER_SMOKE_ALL_MODELS"),
        help="Run every model ref listed in --model-list provider_smoke_models.",
    )
    parser.add_argument(
        "--all-config-models",
        action="store_true",
        default=env_bool("JUEX_PROVIDER_SMOKE_ALL_CONFIG_MODELS"),
        help="Run every provider/model found in the provider config.",
    )
    parser.add_argument("--work-root", default=env_default("JUEX_PROVIDER_SMOKE_ROOT", ""))
    parser.add_argument("--report-dir", default=env_default("JUEX_PROVIDER_SMOKE_REPORT_DIR", ""))
    parser.add_argument("--run-id", default=env_default("JUEX_PROVIDER_SMOKE_RUN_ID", time.strftime("%Y%m%dT%H%M%SZ", time.gmtime())))
    parser.add_argument("--only", default=env_default("JUEX_PROVIDER_SMOKE_ONLY", ""))
    parser.add_argument("--timeout", type=int, default=env_int("JUEX_PROVIDER_SMOKE_TIMEOUT", 240))
    parser.add_argument("--retries", type=int, default=env_int("JUEX_PROVIDER_SMOKE_RETRIES", 1))
    parser.add_argument("--keep", action="store_true", default=env_bool("JUEX_PROVIDER_SMOKE_KEEP"))
    parsed = parser.parse_args(argv)

    if parsed.timeout <= 0:
        raise ValueError("--timeout must be a positive integer")
    if parsed.retries < 0:
        raise ValueError("--retries must be a non-negative integer")
    if parsed.only and (parsed.all_models or parsed.all_config_models):
        raise ValueError("--only is mutually exclusive with --all-models and --all-config-models")
    if parsed.all_models and parsed.all_config_models:
        raise ValueError("--all-models and --all-config-models are mutually exclusive")
    if not parsed.juex:
        raise ValueError("juex binary not found; run 'make build' or pass --juex")
    if not os.access(parsed.juex, os.X_OK):
        raise ValueError(f"juex binary is not executable: {parsed.juex}")
    config_path = pathlib.Path(parsed.config).expanduser()
    model_list_path = pathlib.Path(parsed.model_list).expanduser()
    if not config_path.is_file():
        raise ValueError(f"provider config not found: {parsed.config}")

    report_dir = pathlib.Path(parsed.report_dir or default_report_dir("provider-model-smoke", parsed.run_id))
    (report_dir / "cases").mkdir(parents=True, exist_ok=True)
    work_root_created = False
    if parsed.work_root:
        work_root = pathlib.Path(parsed.work_root)
        work_root.mkdir(parents=True, exist_ok=True)
    else:
        work_root = pathlib.Path(tempfile.mkdtemp(prefix="juex-provider-smoke."))
        work_root_created = True

    try:
        cfg = load_yaml_file(config_path)
        rows = enumerate_provider_matrix(cfg)
        if not rows:
            raise ValueError(f"no providers/models found in {parsed.config}")

        selected_refs: set[str] | None = None
        model_list_label = "all provider config models"
        if parsed.all_config_models:
            pass
        elif parsed.only:
            model_list_label = f"filter {parsed.only}"
        else:
            selected = load_model_refs(model_list_path, "provider_smoke_models")
            selected_refs = set(selected)
            missing = sorted(ref for ref in selected if ref not in {row.ref for row in rows})
            if missing:
                raise ValueError(
                    "model list refs are not present in provider config: "
                    + ", ".join(missing)
                    + f" (model list: {parsed.model_list})"
                )
            model_list_label = f"{parsed.model_list} ({'all listed models' if parsed.all_models else 'listed model scope'})"

        matrix_file = report_dir / "matrix.tsv"
        results_file = report_dir / "results.jsonl"
        summary_json = report_dir / "summary.json"
        summary_md = report_dir / "summary.md"
        results_file.write_text("", encoding="utf-8")
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
        print(f"config: {config_path}")
        print(f"model list: {model_list_label}")
        print(f"work root: {work_root}")
        print(f"report dir: {report_dir}")

        total = 0
        failed = 0
        results: list[SmokeResult] = []
        for row in rows:
            if parsed.only and parsed.only not in {row.provider_id, row.ref}:
                continue
            if selected_refs is not None and row.ref not in selected_refs:
                continue
            total += 1
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
                )
            )
            results.append(result)
            append_jsonl(results_file, result.as_dict())
            if result.status != "pass":
                failed += 1

        if total == 0:
            raise ValueError("no providers/models matched the requested scope")

        write_smoke_summary(
            summary_json,
            summary_md,
            {
                "run_id": parsed.run_id,
                "juex": parsed.juex,
                "config": str(config_path),
                "model_list": model_list_label,
                "report_dir": str(report_dir),
                "work_root": str(work_root) if parsed.keep else "cleaned",
                "total": total,
                "passed": total - failed,
                "failed": failed,
                "tool_use_recorded": sum(1 for result in results if result.tool_status == "yes"),
                "exec_command_tool_use_recorded": sum(1 for result in results if result.exec_command_status == "yes"),
                "tty_recorded": sum(1 for result in results if result.tty_status == "yes"),
                "stdin_recorded": sum(1 for result in results if result.stdin_status == "yes"),
                "filesystem_verified": sum(1 for result in results if result.filesystem_status == "yes"),
                "event_delta_recorded": sum(1 for result in results if result.event_delta_status == "yes"),
                "thinking_observed": sum(1 for result in results if result.thinking_status == "observed"),
                "results_jsonl_path": str(results_file),
            },
            results,
        )
        print(f"summary: total={total} failed={failed} report={summary_md}")
        return 1 if failed else 0
    finally:
        if work_root_created and not parsed.keep:
            shutil.rmtree(work_root, ignore_errors=True)


@dataclass(frozen=True)
class MatrixRow:
    provider_id: str
    model_id: str
    protocol: str
    reasoning_effort_capability: str
    tools_capability: str
    thinking_effort: str
    ref: str


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
    event_delta_status: str = "no"
    thinking_status: str = "not_observed"
    error_stage: str = ""
    error: str = ""
    artifacts: str = ""

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


def enumerate_provider_matrix(cfg: dict[str, Any]) -> list[MatrixRow]:
    providers = cfg.get("providers") or []
    if isinstance(providers, dict):
        providers = list(providers.values())
    rows: list[MatrixRow] = []
    for provider in providers:
        if not isinstance(provider, dict):
            continue
        provider_id = str(provider.get("id") or "").strip()
        if not provider_id:
            continue
        protocol = str(provider.get("protocol") or "").strip() or "preset"
        capabilities = provider.get("capabilities")
        if not isinstance(capabilities, dict):
            capabilities = {}
        reasoning_effort = jsonish(capabilities["reasoning_effort"]) if "reasoning_effort" in capabilities else "default"
        tools = jsonish(capabilities["tools"]) if "tools" in capabilities else "default"
        models = provider.get("models") or []
        for model in models:
            model_id = model_id_from(model)
            if not model_id:
                continue
            thinking_effort = "unset"
            if isinstance(model, dict) and "thinking_effort" in model:
                thinking_effort = jsonish(model["thinking_effort"])
            rows.append(
                MatrixRow(
                    provider_id=provider_id,
                    model_id=model_id,
                    protocol=protocol,
                    reasoning_effort_capability=reasoning_effort,
                    tools_capability=tools,
                    thinking_effort=thinking_effort,
                    ref=f"{provider_id}/{model_id}",
                )
            )
    return rows


def model_id_from(model: Any) -> str:
    if isinstance(model, dict):
        return str(model.get("id") or "").strip()
    return str(model or "").strip()


def jsonish(value: Any) -> str:
    try:
        return json.dumps(value, ensure_ascii=False, separators=(",", ":"))
    except TypeError:
        return str(value)


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
        artifacts=str(artifact_dir),
    )

    print(f"==> {row.ref} [{row.protocol}]")
    token = f"juex-smoke-{safe}-{int(time.time())}-{random.randrange(0x1000000):06x}"
    case_config = case_dir / "provider.juex.yaml"
    manifest_file = case_dir / "release-manifest.txt"
    notes_file = case_dir / "agent-notes.txt"
    try:
        write_selected_config(ctx.config, row.provider_id, row.model_id, case_config)
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
    if run_turn_with_retries(ctx, case_dir, case_config, "turn1", ["--new", prompt]) != 0:
        return fail_smoke_case(result, case_dir, artifact_dir, "turn1", "turn1 failed")
    session_id = json_file_value(case_dir / "turn1.stdout.json", "session_id")
    if not session_id:
        return fail_smoke_case(result, case_dir, artifact_dir, "turn1", "missing session_id")
    result.session_id = session_id
    if not file_contains(case_dir / "turn1.stdout.json", f"EVAL_PASS {token}"):
        return fail_smoke_case(result, case_dir, artifact_dir, "turn1", "missing final EVAL_PASS token")

    conversation = case_dir / ".juex" / "sessions" / session_id / "conversation.jsonl"
    if not conversation.is_file():
        return fail_smoke_case(result, case_dir, artifact_dir, "session", "missing conversation log")
    ok, detail = validate_agent_smoke_files(notes_file, case_dir / "tty-result.txt", token)
    if not ok:
        return fail_smoke_case(result, case_dir, artifact_dir, "filesystem", detail)
    result.filesystem_status = "yes"
    events = case_dir / ".juex" / "sessions" / session_id / "events.jsonl"
    contract_report = contract_oracle.validate_agent_smoke_contract(conversation, events, token)
    if not contract_report.passed:
        return fail_smoke_case(result, case_dir, artifact_dir, "session", contract_report.message())
    result.tool_status = "yes"
    result.exec_command_status = "yes"
    result.tty_status = "yes"
    result.stdin_status = "yes"
    result.event_delta_status = "yes"
    result.thinking_status = "observed" if file_contains(conversation, '"type":"reasoning"') else "not_exposed"
    copy_case_artifacts(case_dir, artifact_dir)
    result.status = "pass"
    print(
        f"ok  {row.ref} session={session_id} toolcall={result.tool_status} "
        f"exec_command={result.exec_command_status} tty={result.tty_status} "
        f"stdin={result.stdin_status} events={result.event_delta_status} "
        f"thinking={result.thinking_status} artifacts={artifact_dir}"
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


def events_have_agent_smoke_deltas(path: pathlib.Path, token: str) -> tuple[bool, str]:
    return contract_oracle.events_have_agent_smoke_deltas(path, token)


def run_turn_with_retries(ctx: ProviderSmokeContext, case_dir: pathlib.Path, case_config: pathlib.Path, label: str, args: list[str]) -> int:
    status = 1
    for attempt in range(ctx.retries + 1):
        status = run_turn(ctx, case_dir, case_config, label, args)
        if status == 0:
            return 0
        if attempt >= ctx.retries or not turn_failure_retryable(case_dir, label, status):
            return status
        print(f"retry {ctx.row.ref} {label} after retryable failure (attempt {attempt + 1}/{ctx.retries + 1})", file=sys.stderr)
        time.sleep(attempt + 1)
    return status


def run_turn(ctx: ProviderSmokeContext, case_dir: pathlib.Path, case_config: pathlib.Path, label: str, args: list[str]) -> int:
    stdout_file = case_dir / f"{label}.stdout.json"
    stderr_file = case_dir / f"{label}.stderr.log"
    case_home = case_dir / "home"
    (case_home / ".agents").mkdir(parents=True, exist_ok=True)
    (case_home / ".juex").mkdir(parents=True, exist_ok=True)
    env = os.environ.copy()
    env.update(
        {
            "HOME": str(case_home),
            "USERPROFILE": str(case_home),
            "CODEX_HOME": ctx.codex_home,
            "PROVIDER_API_ID": "",
            "PROVIDER_API_PROTOCOL": "",
            "PROVIDER_API_BASE": "",
            "PROVIDER_API_KEY": "",
            "PROVIDER_API_MODEL": "",
            "PROVIDER_THINKING_EFFORT": "",
            "PROVIDER_CONTEXT_WINDOW": "",
        }
    )
    command = [
        ctx.juex_bin,
        "-C",
        str(case_dir),
        "--config",
        str(case_config),
        "--enable-user-global-resources=false",
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
    if status == 124:
        return True
    stderr = case_dir / f"{label}.stderr.log"
    text = stderr.read_text(encoding="utf-8", errors="replace") if stderr.is_file() else ""
    return re.search(r'"retryable"\s*:\s*true|TLS handshake timeout|context deadline exceeded|connection reset|temporary failure|timeout', text, re.I) is not None


def fail_smoke_case(result: SmokeResult, case_dir: pathlib.Path, artifact_dir: pathlib.Path, stage: str, message: str) -> SmokeResult:
    copy_case_artifacts(case_dir, artifact_dir)
    write_error_tail(case_dir, artifact_dir)
    provider_message = provider_error_message(artifact_dir)
    result.error_stage = stage
    result.error = combine_error(message, provider_message)
    print(f"FAIL {result.ref}: {message}", file=sys.stderr)
    return result


def copy_case_artifacts(case_dir: pathlib.Path, artifact_dir: pathlib.Path) -> None:
    artifact_dir.mkdir(parents=True, exist_ok=True)
    for path in sorted(case_dir.glob("*.stdout.json")) + sorted(case_dir.glob("*.stderr.log")):
        shutil.copy2(path, artifact_dir / path.name)
    sessions = case_dir / ".juex" / "sessions"
    if sessions.is_dir():
        for path in sorted(sessions.rglob("*")):
            if path.is_file() and path.name in {"conversation.jsonl", "events.jsonl"}:
                shutil.copy2(path, artifact_dir / path.name)


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
        f"- Model list: `{summary['model_list']}`",
        f"- Work root: `{summary['work_root']}`",
        f"- Total: {summary['total']}",
        f"- Passed: {summary['passed']}",
        f"- Failed: {summary['failed']}",
        f"- Tool use recorded: {summary['tool_use_recorded']}",
        f"- Exec command tool use recorded: {summary['exec_command_tool_use_recorded']}",
        f"- TTY recorded: {summary['tty_recorded']}",
        f"- Stdin recorded: {summary['stdin_recorded']}",
        f"- Filesystem verified: {summary['filesystem_verified']}",
        f"- Event delta recorded: {summary['event_delta_recorded']}",
        f"- Thinking observed: {summary['thinking_observed']}",
        "",
        "| Provider/model | Protocol | Thinking effort | Status | Tool use | Exec command | TTY | Stdin | Filesystem | Deltas | Thinking | Error stage |",
        "| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |",
    ]
    for result in results:
        lines.append(
            f"| `{result.ref}` | `{result.protocol}` | `{result.thinking_effort}` | "
            f"{result.status} | {result.tool_status} | {result.exec_command_status} | "
            f"{result.tty_status} | {result.stdin_status} | {result.filesystem_status} | "
            f"{result.event_delta_status} | {result.thinking_status} | {result.error_stage} |"
        )
    summary_md.write_text("\n".join(lines) + "\n", encoding="utf-8")


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
        capabilities = provider.get("capabilities")
        if not isinstance(capabilities, dict):
            capabilities = {}
        capabilities["tools"] = False
        provider["capabilities"] = capabilities
    out: dict[str, Any] = {
        "model": f"{provider_id}/{model_id}",
        "enable_user_global_resources": False,
        "providers": [provider],
    }
    if compaction:
        out["compaction"] = compaction
    output_path.parent.mkdir(parents=True, exist_ok=True)
    output_path.write_text(dump_yaml(out), encoding="utf-8")
    output_path.chmod(0o600)


def selected_provider_model(cfg: dict[str, Any], provider_id: str, model_id: str) -> tuple[dict[str, Any], Any]:
    providers = cfg.get("providers") or []
    if isinstance(providers, dict):
        providers = list(providers.values())
    for provider in providers:
        if not isinstance(provider, dict) or str(provider.get("id") or "").strip() != provider_id:
            continue
        for model in provider.get("models") or []:
            if model_id_from(model) == model_id:
                return provider, model
        raise ValueError(f"model not found: {provider_id}/{model_id}")
    raise ValueError(f"provider not found: {provider_id}")


def list_models(argv: list[str]) -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--model-list", default=env_default("JUEX_LIVE_MODEL_LIST", str(REPO_ROOT / "tests" / "eval" / "live-models.yaml")))
    parser.add_argument("--section", default="provider_smoke_models")
    parsed = parser.parse_args(argv)
    for ref in load_model_refs(pathlib.Path(parsed.model_list), parsed.section):
        print(ref)
    return 0


def write_model_config_command(argv: list[str]) -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--source", required=True)
    parser.add_argument("--provider", required=True)
    parser.add_argument("--model", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--disable-tools", action="store_true")
    parser.add_argument("--compaction-eval", action="store_true")
    parsed = parser.parse_args(argv)
    compaction = None
    if parsed.compaction_eval:
        compaction = {
            "enabled": True,
            "reserve_tokens": 8000,
            "keep_recent_tokens": 6000,
            "tail_turns": 1,
            "summary_max_tokens": 2048,
            "tool_result_max_chars": 1200,
            "user_input_inline_max_bytes": 524288,
        }
    cfg = load_yaml_file(pathlib.Path(parsed.source).expanduser())
    write_selected_config(
        cfg,
        parsed.provider,
        parsed.model,
        pathlib.Path(parsed.output),
        disable_tools=parsed.disable_tools,
        compaction=compaction,
    )
    return 0


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
    branch = command_output(["git", "branch", "--show-current"]).strip()
    commit = command_output(["git", "rev-parse", "HEAD"]).strip()
    dirty = bool(command_output(["git", "status", "--short"]).strip())
    record_status = "pass" if status == 0 else "fail"
    record = {
        "run_id": run_id,
        "branch": branch,
        "commit": commit,
        "dirty": dirty,
        "status": record_status,
        "commands": commands,
        "provider_model_smoke": provider,
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
        "",
        "## Commands",
        "",
        "| Label | Exit | Log |",
        "| --- | ---: | --- |",
    ]
    for command in commands:
        lines.append(f"| `{command['label']}` | {command['exit_status']} | `{command['log']}` |")
    lines.extend(["", "## Provider Model Smoke", ""])
    if provider:
        lines.append(f"- Summary: `{provider_summary_path}`")
        for key, label in [
            ("total", "Total"),
            ("passed", "Passed"),
            ("failed", "Failed"),
            ("tool_use_recorded", "Tool use recorded"),
            ("exec_command_tool_use_recorded", "Exec command tool use recorded"),
            ("tty_recorded", "TTY recorded"),
            ("stdin_recorded", "Stdin recorded"),
            ("filesystem_verified", "Filesystem verified"),
            ("event_delta_recorded", "Event delta recorded"),
            ("thinking_observed", "Thinking observed"),
        ]:
            if key in provider:
                lines.append(f"- {label}: {provider[key]}")
    else:
        lines.append("- Not run.")
    lines.extend(["", "## Quality Evaluation", ""])
    if compaction_dir:
        lines.append(f"- Compaction evaluation: `{compaction_dir}`")
        for scorecard in sorted(pathlib.Path(compaction_dir).glob("*/scorecard.md")):
            model, score = scorecard_model_and_score(scorecard)
            lines.append(f"- {model}: {score}")
    else:
        lines.append("- Not run. Run with `--compaction-eval` when touching compaction, context projection, provider replay, or long-session behavior.")
    record_md.write_text("\n".join(lines) + "\n", encoding="utf-8")


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


def load_model_refs(path: pathlib.Path, section: str) -> list[str]:
    data = load_yaml_file(path)
    values = data.get(section)
    if not isinstance(values, list):
        raise ValueError(f"model list {path} section {section} is missing or not a list")
    refs = [str(value).strip() for value in values if str(value).strip()]
    if not refs:
        raise ValueError(f"model list {path} section {section} is empty")
    return refs


def load_yaml_file(path: pathlib.Path) -> dict[str, Any]:
    value = yaml.safe_load(path.read_text(encoding="utf-8")) or {}
    if not isinstance(value, dict):
        raise ValueError(f"{path} must contain a YAML mapping")
    return value


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
    return re.sub(r"[^A-Za-z0-9._-]", "_", ref)


if __name__ == "__main__":
    sys.exit(main())
