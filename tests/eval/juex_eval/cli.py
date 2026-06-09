from __future__ import annotations

import argparse
import json
import os
import pathlib
import shlex
import shutil
import subprocess
import sys
import time

from . import compaction, helper, rotation


REPO_ROOT = pathlib.Path(__file__).resolve().parents[3]


def main(argv: list[str] | None = None) -> int:
    argv = list(sys.argv[1:] if argv is None else argv)
    if argv and argv[0] in {
        "list-models",
        "write-model-config",
        "run-timeout",
        "append-command",
        "write-development-record",
    }:
        return helper.main_with_args(argv)
    if argv and argv[0] == "rotation":
        return rotation.main_with_args(argv[1:])

    parser = argparse.ArgumentParser(prog="juex-eval", description="JueX local evaluation commands.")
    sub = parser.add_subparsers(dest="command", required=True)

    development_parser = sub.add_parser(
        "development",
        help="Run the standard post-development validation stack.",
        description="Run deterministic tests, build, provider smoke, optional compaction eval, and write a record.",
    )
    add_development_args(development_parser)

    provider_parser = sub.add_parser("provider-smoke", help="Run live provider/model smoke tests.")
    add_provider_args(provider_parser)

    compaction_parser = sub.add_parser("compaction", help="Run live compaction quality evaluation.")
    compaction.add_args(compaction_parser)

    rotation_parser = sub.add_parser("rotation", help="Select or update rotating live-model targets.")
    rotation_parser.add_argument("rotation_args", nargs=argparse.REMAINDER)

    parsed = parser.parse_args(argv)
    try:
        if parsed.command == "development":
            return run_development(parsed)
        if parsed.command == "provider-smoke":
            return run_provider_smoke(parsed)
        if parsed.command == "compaction":
            return compaction.run(parsed)
        if parsed.command == "rotation":
            return rotation.main_with_args(parsed.rotation_args)
    except Exception as exc:  # noqa: BLE001 - command-line entry should report succinctly.
        print(str(exc), file=sys.stderr)
        return 1
    return 2


def add_development_args(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--run-id", default=os.environ.get("JUEX_DEVELOPMENT_EVAL_RUN_ID") or time.strftime("%Y%m%dT%H%M%SZ", time.gmtime()))
    parser.add_argument("--report-dir", default=os.environ.get("JUEX_DEVELOPMENT_EVAL_REPORT_DIR") or "")
    parser.add_argument("--provider-only", default=os.environ.get("JUEX_PROVIDER_SMOKE_ONLY") or "")
    parser.add_argument("--provider-timeout", type=int, default=int(os.environ.get("JUEX_PROVIDER_SMOKE_TIMEOUT") or "240"))
    parser.add_argument("--provider-all-models", action="store_true")
    parser.add_argument("--provider-all-config-models", action="store_true")
    parser.add_argument("--no-provider-smoke", action="store_true")
    parser.add_argument("--skip-tests", action="store_true")
    parser.add_argument("--compaction-eval", action="store_true")
    parser.add_argument("--compaction-all-models", action="store_true")
    parser.add_argument("--compaction-model", action="append", default=[])


def add_provider_args(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--juex", default=os.environ.get("JUEX_BIN") or helper.default_juex_bin())
    parser.add_argument("--config", default=os.environ.get("JUEX_PROVIDER_CONFIG") or str(pathlib.Path.home() / ".juex" / "juex.yaml"))
    parser.add_argument("--model-list", default=os.environ.get("JUEX_LIVE_MODEL_LIST") or str(REPO_ROOT / "tests" / "eval" / "live-models.yaml"))
    parser.add_argument("--rotation-state", default=os.environ.get("JUEX_LIVE_MODEL_ROTATION_STATE") or str(REPO_ROOT / ".juex" / "live-model-rotation.json"))
    parser.add_argument("--all-models", action="store_true", default=truthy(os.environ.get("JUEX_PROVIDER_SMOKE_ALL_MODELS")))
    parser.add_argument("--all-config-models", action="store_true", default=truthy(os.environ.get("JUEX_PROVIDER_SMOKE_ALL_CONFIG_MODELS")))
    parser.add_argument("--work-root", default=os.environ.get("JUEX_PROVIDER_SMOKE_ROOT") or "")
    parser.add_argument("--report-dir", default=os.environ.get("JUEX_PROVIDER_SMOKE_REPORT_DIR") or "")
    parser.add_argument("--run-id", default=os.environ.get("JUEX_PROVIDER_SMOKE_RUN_ID") or time.strftime("%Y%m%dT%H%M%SZ", time.gmtime()))
    parser.add_argument("--only", default=os.environ.get("JUEX_PROVIDER_SMOKE_ONLY") or "")
    parser.add_argument("--timeout", type=int, default=int(os.environ.get("JUEX_PROVIDER_SMOKE_TIMEOUT") or "240"))
    parser.add_argument("--retries", type=int, default=int(os.environ.get("JUEX_PROVIDER_SMOKE_RETRIES") or "1"))
    parser.add_argument("--keep", action="store_true", default=truthy(os.environ.get("JUEX_PROVIDER_SMOKE_KEEP")))


def run_development(args: argparse.Namespace) -> int:
    if args.provider_only and (args.provider_all_models or args.provider_all_config_models):
        raise ValueError("--provider-only cannot be combined with provider all-model options")
    if args.provider_all_models and args.provider_all_config_models:
        raise ValueError("--provider-all-models and --provider-all-config-models are mutually exclusive")
    if args.compaction_all_models and args.compaction_model:
        raise ValueError("--compaction-all-models cannot be combined with --compaction-model")

    report_dir = pathlib.Path(args.report_dir or REPO_ROOT / "docs" / "reports" / "development-validation" / args.run_id)
    command_logs = report_dir / "command-logs"
    command_logs.mkdir(parents=True, exist_ok=True)
    commands_file = report_dir / "commands.jsonl"
    commands_file.write_text("", encoding="utf-8")

    tool_prefix = ["mise", "exec", "--"] if shutil.which("mise") else []
    overall = 0

    if not args.skip_tests:
        overall |= run_logged("go-test-e2e", [*tool_prefix, "go", "test", "./tests/e2e", "-count=1"], command_logs, commands_file)
        overall |= run_logged("go-test-all", [*tool_prefix, "go", "test", "./...", "-count=1"], command_logs, commands_file)

    overall |= run_logged("make-build", [*tool_prefix, "make", "build"], command_logs, commands_file)

    provider_report_dir = report_dir / "provider-model-smoke"
    if not args.no_provider_smoke:
        provider_cmd = [
            sys.executable,
            "-m",
            "tests.eval.juex_eval",
            "provider-smoke",
            "--juex",
            "./dist/juex",
            "--report-dir",
            str(provider_report_dir),
            "--run-id",
            args.run_id,
            "--timeout",
            str(args.provider_timeout),
        ]
        if args.provider_only:
            provider_cmd += ["--only", args.provider_only]
        if args.provider_all_models:
            provider_cmd.append("--all-models")
        if args.provider_all_config_models:
            provider_cmd.append("--all-config-models")
        overall |= run_logged("provider-model-smoke", provider_cmd, command_logs, commands_file)

    compaction_report_dir = ""
    if args.compaction_eval:
        compaction_report_dir = str(report_dir / "compaction-eval")
        compaction_cmd = [
            sys.executable,
            "-m",
            "tests.eval.juex_eval",
            "compaction",
            "--juex",
            "./dist/juex",
            "--out-root",
            compaction_report_dir,
            "--run-id",
            args.run_id,
        ]
        if args.compaction_all_models:
            compaction_cmd.append("--all-models")
        for model in args.compaction_model:
            compaction_cmd.append(model)
        overall |= run_logged("compaction-eval", compaction_cmd, command_logs, commands_file)

    helper.write_development_record(
        report_dir,
        args.run_id,
        commands_file,
        provider_report_dir / "summary.json",
        compaction_report_dir,
        overall,
        report_dir / "record.json",
        report_dir / "record.md",
    )
    print(f"record: {report_dir / 'record.md'}")
    return 1 if overall else 0


def run_logged(label: str, command: list[str], log_dir: pathlib.Path, commands_file: pathlib.Path) -> int:
    log_path = log_dir / f"{label}.log"
    rendered = " ".join(shlex.quote(part) for part in command)
    print(f"==> {label}: {rendered}")
    with log_path.open("wb") as log:
        proc = subprocess.run(command, cwd=REPO_ROOT, stdout=log, stderr=subprocess.STDOUT, check=False)
    helper.append_jsonl(
        commands_file,
        {
            "label": label,
            "command": rendered,
            "exit_status": proc.returncode,
            "log": str(log_path),
        },
    )
    if proc.returncode:
        print(f"FAIL {label} (exit {proc.returncode}), log: {log_path}", file=sys.stderr)
        print_tail(log_path, 40)
        return 1
    print(f"ok  {label}")
    return 0


def print_tail(path: pathlib.Path, lines: int) -> None:
    try:
        content = path.read_text(encoding="utf-8", errors="replace").splitlines()
    except OSError:
        return
    for line in content[-lines:]:
        print(line, file=sys.stderr)


def run_provider_smoke(args: argparse.Namespace) -> int:
    helper_args = provider_helper_args(args)
    if explicit_provider_scope(args):
        return helper.provider_smoke(helper_args)

    refs = rotation.load_model_refs(pathlib.Path(args.model_list).expanduser(), "provider_smoke_models")
    rotation_state = pathlib.Path(args.rotation_state).expanduser()
    state = rotation.load_state(rotation_state)
    selected = rotation.select_next(refs, state, "provider_smoke_models")
    print(f"rotated provider smoke model: {selected}")

    status = helper.provider_smoke(["--only", selected, *helper_args])
    if status == 0:
        rotation.mark_success(state, "provider_smoke_models", selected)
        rotation.write_state(rotation_state, state)
    return status


def explicit_provider_scope(args: argparse.Namespace) -> bool:
    return bool(args.only or args.all_models or args.all_config_models)


def provider_helper_args(args: argparse.Namespace) -> list[str]:
    out = [
        "--juex",
        args.juex,
        "--config",
        args.config,
        "--model-list",
        args.model_list,
        "--run-id",
        args.run_id,
        "--timeout",
        str(args.timeout),
        "--retries",
        str(args.retries),
    ]
    if args.work_root:
        out += ["--work-root", args.work_root]
    if args.report_dir:
        out += ["--report-dir", args.report_dir]
    if args.only:
        out += ["--only", args.only]
    if args.all_models:
        out.append("--all-models")
    if args.all_config_models:
        out.append("--all-config-models")
    if args.keep:
        out.append("--keep")
    return out


def truthy(value: str | None) -> bool:
    return (value or "").strip().lower() in {"1", "true", "yes", "on"}
