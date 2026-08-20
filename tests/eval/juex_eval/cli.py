from __future__ import annotations

import argparse
import json
import os
import pathlib
import shlex
import subprocess
import sys
import time

from . import compaction, helper, selection


REPO_ROOT = pathlib.Path(__file__).resolve().parents[3]
TEST_HOME_RUNNER = str(REPO_ROOT / "scripts" / "with-test-juex-home.sh")


def main(argv: list[str] | None = None) -> int:
    argv = list(sys.argv[1:] if argv is None else argv)
    if argv and argv[0] in {
        "write-model-config",
        "run-timeout",
        "append-command",
        "write-development-record",
    }:
        return helper.main_with_args(argv)
    parser = argparse.ArgumentParser(prog="juex-eval", description="JueX local evaluation commands.")
    sub = parser.add_subparsers(dest="command", required=True)

    development_parser = sub.add_parser(
        "development",
        help="Run the standard post-development validation stack.",
        description="Run deterministic tests, build, provider smoke, optional compaction eval, and write a record.",
    )
    add_development_args(development_parser)

    provider_parser = sub.add_parser("provider-smoke", help="Run live provider:model smoke tests.")
    add_provider_args(provider_parser)

    compaction_parser = sub.add_parser("compaction", help="Run live compaction quality evaluation.")
    compaction.add_args(compaction_parser)

    parsed = parser.parse_args(argv)
    try:
        if parsed.command == "development":
            return run_development(parsed)
        if parsed.command == "provider-smoke":
            return run_provider_smoke(parsed)
        if parsed.command == "compaction":
            return compaction.run(parsed)
    except Exception as exc:  # noqa: BLE001 - command-line entry should report succinctly.
        print(str(exc), file=sys.stderr)
        return 1
    return 2


def add_development_args(parser: argparse.ArgumentParser) -> None:
    parser.add_argument(
        "--run-id",
        default=os.environ.get("JUEX_DEVELOPMENT_EVAL_RUN_ID") or time.strftime("%Y%m%dT%H%M%SZ", time.gmtime()),
    )
    parser.add_argument(
        "--report-dir",
        default=os.environ.get("JUEX_DEVELOPMENT_EVAL_REPORT_DIR") or "",
        help="Write the development validation record under this directory.",
    )
    parser.add_argument(
        "--only",
        "--provider-only",
        dest="provider_only",
        default=os.environ.get("JUEX_PROVIDER_SMOKE_ONLY") or "",
        help="Only run this provider:model ref for provider smoke.",
    )
    parser.add_argument(
        "--config",
        default=os.environ.get("JUEX_PROVIDER_CONFIG") or str(pathlib.Path.home() / ".juex" / "juex.yaml"),
        help="Provider config used by live evaluation steps.",
    )
    parser.add_argument(
        "--selection-seed",
        default=os.environ.get("JUEX_EVAL_SELECTION_SEED") or selection.generated_seed(),
        help="Reproducible seed for provider-config candidate selection.",
    )
    parser.add_argument("--provider-timeout", type=int, default=int(os.environ.get("JUEX_PROVIDER_SMOKE_TIMEOUT") or "240"))
    parser.add_argument("--provider-all-models", action="store_true")
    parser.add_argument("--no-provider-smoke", action="store_true")
    parser.add_argument("--skip-tests", action="store_true")
    parser.add_argument("--compaction-eval", action="store_true")
    parser.add_argument("--compaction-all-models", action="store_true")
    parser.add_argument(
        "--compaction-only",
        "--compaction-model",
        dest="compaction_only",
        action="append",
        default=[],
        help="Only run this provider:model ref for compaction eval. May be repeated.",
    )


def add_provider_args(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--juex", default=os.environ.get("JUEX_BIN") or helper.default_juex_bin())
    parser.add_argument(
        "--config",
        default=os.environ.get("JUEX_PROVIDER_CONFIG") or str(pathlib.Path.home() / ".juex" / "juex.yaml"),
    )
    parser.add_argument(
        "--selection-seed",
        default=os.environ.get("JUEX_EVAL_SELECTION_SEED") or selection.generated_seed(),
        help="Reproducible seed for provider-config candidate selection.",
    )
    parser.add_argument(
        "--all-models",
        action="store_true",
        default=truthy(os.environ.get("JUEX_PROVIDER_SMOKE_ALL_MODELS")),
        help="Run every eligible provider:model found in the provider config.",
    )
    parser.add_argument("--work-root", default=os.environ.get("JUEX_PROVIDER_SMOKE_ROOT") or "")
    parser.add_argument(
        "--report-dir",
        default=os.environ.get("JUEX_PROVIDER_SMOKE_REPORT_DIR") or "",
        help="Write provider smoke reports under this directory.",
    )
    parser.add_argument(
        "--run-id",
        default=os.environ.get("JUEX_PROVIDER_SMOKE_RUN_ID") or time.strftime("%Y%m%dT%H%M%SZ", time.gmtime()),
    )
    parser.add_argument(
        "--only",
        default=os.environ.get("JUEX_PROVIDER_SMOKE_ONLY") or "",
        help="Run exactly one eligible provider:model ref.",
    )
    parser.add_argument("--timeout", type=int, default=int(os.environ.get("JUEX_PROVIDER_SMOKE_TIMEOUT") or "240"))
    parser.add_argument("--retries", type=int, default=int(os.environ.get("JUEX_PROVIDER_SMOKE_RETRIES") or "1"))
    parser.add_argument("--keep", action="store_true", default=truthy(os.environ.get("JUEX_PROVIDER_SMOKE_KEEP")))


def run_development(args: argparse.Namespace) -> int:
    validate_development_args(args)
    args.config = str(selection.resolved_path(args.config))

    report_dir = pathlib.Path(args.report_dir or helper.default_report_dir("development-validation", args.run_id))
    command_logs = report_dir / "command-logs"
    command_logs.mkdir(parents=True, exist_ok=True)
    commands_file = report_dir / "commands.jsonl"
    commands_file.write_text("", encoding="utf-8")

    steps, provider_report_dir, compaction_report_dir = development_steps(args, report_dir)

    overall = 0
    for label, command in steps:
        overall |= run_logged(label, command, command_logs, commands_file)

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


def validate_development_args(args: argparse.Namespace) -> None:
    if args.provider_only and args.provider_all_models:
        raise ValueError("--only cannot be combined with --provider-all-models")
    if args.compaction_all_models and args.compaction_only:
        raise ValueError("--compaction-all-models cannot be combined with --compaction-only")


def development_steps(args: argparse.Namespace, report_dir: pathlib.Path) -> tuple[list[tuple[str, list[str]]], pathlib.Path, str]:
    provider_report_dir = report_dir / "provider-model-smoke"
    compaction_report_dir = report_dir / "compaction-eval" if args.compaction_eval else None

    steps: list[tuple[str, list[str]]] = []
    if not args.skip_tests:
        steps.extend(
            [
                ("go-test-e2e", [TEST_HOME_RUNNER, "go", "test", "./tests/e2e", "-count=1"]),
                ("go-test-all", [TEST_HOME_RUNNER, "go", "test", "./...", "-count=1"]),
            ]
        )
    steps.append(("make-build", ["make", "build"]))

    if not args.no_provider_smoke:
        steps.append(("provider-model-smoke", provider_smoke_development_command(args, provider_report_dir)))
    if compaction_report_dir is not None:
        steps.append(("compaction-eval", compaction_development_command(args, compaction_report_dir)))
    return steps, provider_report_dir, str(compaction_report_dir or "")


def provider_smoke_development_command(args: argparse.Namespace, report_dir: pathlib.Path) -> list[str]:
    command = module_command("provider-smoke")
    append_value(command, "--juex", "./dist/juex")
    append_value(command, "--config", args.config)
    append_value(command, "--selection-seed", args.selection_seed)
    append_value(command, "--report-dir", report_dir)
    append_value(command, "--run-id", args.run_id)
    append_value(command, "--timeout", args.provider_timeout)
    append_value(command, "--only", args.provider_only)
    append_flag(command, "--all-models", args.provider_all_models)
    return command


def compaction_development_command(args: argparse.Namespace, report_dir: pathlib.Path) -> list[str]:
    command = module_command("compaction")
    append_value(command, "--juex", "./dist/juex")
    append_value(command, "--config", args.config)
    append_value(command, "--selection-seed", args.selection_seed)
    append_value(command, "--report-dir", report_dir)
    append_value(command, "--run-id", args.run_id)
    append_flag(command, "--all-models", args.compaction_all_models)
    append_repeated(command, "--only", args.compaction_only)
    return command


def module_command(command: str) -> list[str]:
    return [sys.executable, "-m", "tests.eval.juex_eval", command]


def append_value(command: list[str], flag: str, value: object) -> None:
    if value is not None and str(value) != "":
        command.extend([flag, str(value)])


def append_repeated(command: list[str], flag: str, values: list[str] | None) -> None:
    for value in values or []:
        append_value(command, flag, value)


def append_flag(command: list[str], flag: str, enabled: bool) -> None:
    if enabled:
        command.append(flag)


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
    return helper.provider_smoke(provider_helper_args(args))


def provider_helper_args(args: argparse.Namespace) -> list[str]:
    out = [
        "--juex",
        args.juex,
        "--config",
        str(selection.resolved_path(args.config)),
        "--selection-seed",
        args.selection_seed,
        "--run-id",
        args.run_id,
        "--timeout",
        str(args.timeout),
        "--retries",
        str(args.retries),
    ]
    append_value(out, "--work-root", args.work_root)
    append_value(out, "--report-dir", args.report_dir)
    append_value(out, "--only", args.only)
    append_flag(out, "--all-models", args.all_models)
    append_flag(out, "--keep", args.keep)
    return out


def truthy(value: str | None) -> bool:
    return (value or "").strip().lower() in {"1", "true", "yes", "on"}
