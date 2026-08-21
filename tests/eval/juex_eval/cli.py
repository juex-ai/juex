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
from dataclasses import dataclass
from typing import Callable

import yaml

from . import compaction, helper, outcomes, selection, validation_plan, verification


REPO_ROOT = pathlib.Path(__file__).resolve().parents[3]
TEST_HOME_RUNNER = (REPO_ROOT / "scripts" / "with-test-juex-home.sh").as_posix()
ENSURE_RIPGREP = (REPO_ROOT / "scripts" / "ensure-ripgrep.sh").as_posix()


def windows_bash_from_git(git_executable: str | None) -> str | None:
    if not git_executable:
        return None
    git_path = pathlib.Path(git_executable).resolve()
    for ancestor in git_path.parents:
        for relative in (pathlib.Path("bin") / "bash.exe", pathlib.Path("usr") / "bin" / "bash.exe"):
            candidate = ancestor / relative
            if candidate.is_file():
                return str(candidate)
    return None


if os.name == "nt":
    BASH_EXECUTABLE = windows_bash_from_git(shutil.which("git")) or "bash"
else:
    BASH_EXECUTABLE = "bash"


@dataclass(frozen=True)
class VerificationStep:
    label: str
    command: list[str]
    test_environment: bool = False
    environment: dict[str, str] | None = None
    retry_transient: bool = False


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

    plan_parser = sub.add_parser("plan", help="Generate a deterministic Git-diff validation plan.")
    add_plan_args(plan_parser)

    verify_parser = sub.add_parser("verify", help="Run a stable local verification tier.")
    add_verify_args(verify_parser)

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
        if parsed.command == "plan":
            return run_plan(parsed)
        if parsed.command == "verify":
            return run_verify(parsed)
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


def add_verify_args(parser: argparse.ArgumentParser) -> None:
    tiers = parser.add_subparsers(dest="tier", required=True)
    focused = tiers.add_parser("focused", help="Run explicitly scoped deterministic Go tests.")
    focused.add_argument(
        "packages",
        nargs="*",
        help="Explicit Go package patterns; omit only together with --planned.",
    )
    focused.add_argument(
        "--planned",
        action="store_true",
        help="Explicitly opt in to package and gate selection from the dirty diff plan.",
    )
    add_validation_plan_args(focused)

    candidate = tiers.add_parser("candidate", help="Verify a deterministic PR candidate.")
    candidate.add_argument("--race", action="store_true")
    candidate.add_argument("--web", action="store_true")
    add_validation_plan_args(candidate)
    add_commit_verification_record_args(candidate, "candidate")

    final = tiers.add_parser("final", help="Verify a final candidate with live gates.")
    final.add_argument("--race", action="store_true")
    final.add_argument("--web", action="store_true")
    final.add_argument("--compaction", action="store_true")
    add_validation_plan_args(final)
    add_commit_verification_record_args(final, "final")
    final.add_argument(
        "--config",
        default=os.environ.get("JUEX_PROVIDER_CONFIG") or str(pathlib.Path.home() / ".juex" / "juex.yaml"),
    )
    final.add_argument(
        "--selection-seed",
        default=os.environ.get("JUEX_EVAL_SELECTION_SEED") or selection.generated_seed(),
    )
    final.add_argument("--provider-timeout", type=int, default=int(os.environ.get("JUEX_PROVIDER_SMOKE_TIMEOUT") or "240"))


def add_plan_args(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--tier", choices=("focused", "candidate", "final"), default="focused")
    parser.add_argument("--base", default="", help="Use this exact commit instead of the default diff base.")
    parser.add_argument("--output-dir", default="", help="Write plan.json and plan.md in this directory.")
    parser.add_argument("--explain", action="store_true", help="Print the human-readable gate explanation.")


def add_validation_plan_args(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--base", default="", help="Use this exact commit instead of the default diff base.")
    parser.add_argument("--explain", action="store_true", help="Print the selected gates and their causes.")


def add_commit_verification_record_args(parser: argparse.ArgumentParser, tier: str) -> None:
    parser.add_argument(
        "--run-id",
        default=os.environ.get("JUEX_VERIFY_RUN_ID")
        or f"{tier}-{time.strftime('%Y%m%dT%H%M%SZ', time.gmtime())}",
    )
    parser.add_argument(
        "--report-dir",
        default=os.environ.get("JUEX_VERIFY_REPORT_DIR") or "",
        help="Store the commit-bound development-validation hierarchy below this report root.",
    )


def run_plan(args: argparse.Namespace) -> int:
    plan = validation_plan.collect_plan(REPO_ROOT, args.tier, base=args.base or None)
    output_dir = (
        pathlib.Path(args.output_dir)
        if args.output_dir
        else helper.REPORT_ROOT / "validation-plan" / plan.fingerprint.removeprefix("sha256:")
    )
    json_path, markdown_path = validation_plan.write_plan(output_dir, plan)
    print(f"plan: {json_path}")
    print(f"explanation: {markdown_path}")
    if args.explain:
        print(markdown_path.read_text(encoding="utf-8"), end="")
    return 0


def apply_validation_plan(args: argparse.Namespace, plan: validation_plan.ValidationPlan) -> None:
    explicit_packages = [str(package).strip() for package in getattr(args, "packages", []) if str(package).strip()]
    if args.tier == "focused":
        args.packages = explicit_packages or list(plan.focused_packages)
        args.race = False if explicit_packages else "race" in plan.candidate_flags
        args.web = False if explicit_packages else "web" in plan.candidate_flags
        return
    args.race = bool(getattr(args, "race", False) or "race" in plan.candidate_flags)
    args.web = bool(getattr(args, "web", False) or "web" in plan.candidate_flags)
    args.live_integration = "integration" in plan.final_flags
    args.provider_smoke = "provider-smoke" in plan.final_flags
    explicit_compaction = bool(getattr(args, "compaction", False))
    args.compaction = explicit_compaction or "compaction" in plan.final_flags
    if args.tier == "final" and explicit_compaction:
        args.live_integration = True
        args.provider_smoke = True


def plan_with_cli_overrides(
    args: argparse.Namespace,
    plan: validation_plan.ValidationPlan,
) -> validation_plan.ValidationPlan:
    return validation_plan.with_cli_overrides(
        plan,
        focused_packages=getattr(args, "packages", ()),
        race=bool(getattr(args, "race", False)),
        web=bool(getattr(args, "web", False)),
        compaction=bool(getattr(args, "compaction", False)),
    )


def verification_steps(args: argparse.Namespace) -> list[VerificationStep]:
    if args.tier == "focused":
        packages = [str(package).strip() for package in args.packages if str(package).strip()]
        steps: list[VerificationStep] = []
        if packages:
            if "./..." in packages:
                packages = ["./..."]
            command = bash_script_command(TEST_HOME_RUNNER, "go", "test", *packages)
            if getattr(args, "race", False):
                command.append("-race")
            command.append("-count=1")
            steps.extend(
                [
                    VerificationStep("web-stub", ["make", "web-stub"]),
                    VerificationStep("go-test-focused", command, test_environment=True),
                ]
            )
        if getattr(args, "web", False):
            steps.extend(
                [
                    VerificationStep("web-check", ["make", "web-check"]),
                    VerificationStep("make-build-go", ["make", "build-go"]),
                ]
            )
        return steps
    if args.tier == "candidate":
        return candidate_verification_steps(race=args.race, web=args.web)
    if args.tier == "final":
        steps = candidate_verification_steps(race=args.race, web=args.web)
        if getattr(args, "live_integration", True):
            steps.append(
                VerificationStep(
                    "integration-contracts",
                    ["make", "integration-contracts"],
                    test_environment=True,
                )
            )
            steps.append(
                VerificationStep(
                    "live-integration",
                    ["make", "integration-live"],
                    environment={"JUEX_PROVIDER_CONFIG": args.config},
                    retry_transient=True,
                )
            )
        if getattr(args, "provider_smoke", True):
            steps.append(
                VerificationStep(
                    "provider-model-smoke",
                    final_provider_smoke_command(args),
                    test_environment=True,
                    retry_transient=True,
                )
            )
        if args.compaction:
            steps.append(VerificationStep("compaction-eval", final_compaction_command(args), retry_transient=True))
        return steps
    raise ValueError(f"unsupported verification tier: {args.tier}")


def candidate_verification_steps(*, race: bool, web: bool) -> list[VerificationStep]:
    test_command = bash_script_command(TEST_HOME_RUNNER, "go", "test", "./...")
    if race:
        test_command.append("-race")
    test_command.append("-count=1")
    steps = [
        VerificationStep("web-stub", ["make", "web-stub"]),
        VerificationStep("go-test-all-race" if race else "go-test-all", test_command, test_environment=True),
    ]
    if web:
        steps.extend(
            [
                VerificationStep("web-check", ["make", "web-check"]),
                VerificationStep("make-build-go", ["make", "build-go"]),
            ]
        )
    else:
        steps.append(VerificationStep("make-build", ["make", "build"]))
    return steps


def final_provider_smoke_command(args: argparse.Namespace) -> list[str]:
    command = module_command("provider-smoke")
    append_value(command, "--juex", "./dist/juex")
    append_value(command, "--config", args.config)
    append_value(command, "--selection-seed", args.selection_seed)
    append_value(command, "--run-id", args.run_id)
    append_value(command, "--timeout", args.provider_timeout)
    append_value(command, "--retries", 0)
    report_dir = getattr(args, "verification_report_dir", "")
    append_value(command, "--report-dir", pathlib.Path(report_dir) / "provider-model-smoke" if report_dir else "")
    return command


def final_compaction_command(args: argparse.Namespace) -> list[str]:
    command = module_command("compaction")
    append_value(command, "--juex", "./dist/juex")
    append_value(command, "--config", args.config)
    append_value(command, "--selection-seed", args.selection_seed)
    append_value(command, "--run-id", args.run_id)
    report_dir = getattr(args, "verification_report_dir", "")
    append_value(command, "--report-dir", pathlib.Path(report_dir) / "compaction-eval" if report_dir else "")
    return command


def execute_verification_steps(steps: list[VerificationStep], run_step: Callable[[VerificationStep], int]) -> int:
    for step in steps:
        if run_step(step):
            return 1
    return 0


def execute_development_steps(steps: list[VerificationStep], run_step: Callable[[VerificationStep], int]) -> int:
    overall = 0
    for step in steps:
        if run_step(step):
            overall = 1
    return overall


def run_verify(args: argparse.Namespace) -> int:
    if args.tier == "focused":
        if not getattr(args, "packages", ()) and not getattr(args, "planned", False):
            raise ValueError("focused verification requires packages or --planned")
        plan = validation_plan.collect_plan(REPO_ROOT, "focused", base=getattr(args, "base", "") or None)
        plan = plan_with_cli_overrides(args, plan)
        apply_validation_plan(args, plan)
        plan_dir = helper.REPORT_ROOT / "validation-plan" / plan.fingerprint.removeprefix("sha256:")
        _, markdown_path = validation_plan.write_plan(plan_dir, plan)
        print(f"plan: {plan_dir / 'plan.json'}")
        if getattr(args, "explain", False):
            print(markdown_path.read_text(encoding="utf-8"), end="")
        steps = verification_steps(args)
        if not steps:
            print("no code gates selected")
            return 0
        test_env = isolated_test_environment() if any(step.test_environment for step in steps) else None
        return execute_verification_steps(steps, lambda step: run_visible(step, test_env))

    snapshot = require_clean_worktree()
    plan = validation_plan.collect_plan(REPO_ROOT, args.tier, base=getattr(args, "base", "") or None)
    if plan.head_sha != snapshot.head_sha:
        raise ValueError("HEAD changed during validation planning")
    plan = plan_with_cli_overrides(args, plan)
    apply_validation_plan(args, plan)
    if args.tier == "final":
        args.config = str(selection.resolved_path(args.config))
    candidate_steps = candidate_verification_steps(race=args.race, web=args.web)
    candidate_plan_fingerprint = verification.stable_fingerprint(
        {
            "validation_plan": validation_plan.candidate_fingerprint(plan),
            "candidate_steps": verification.plan_fingerprint(candidate_steps),
        }
    )
    candidate_test_env = (
        isolated_test_environment() if any(step.test_environment for step in candidate_steps) else None
    )
    environment_fingerprint = verification.environment_fingerprint(
        web=args.web,
        repo_root=REPO_ROOT,
        test_environment=candidate_test_env,
    )
    report_root = pathlib.Path(args.report_dir) if args.report_dir else helper.REPORT_ROOT
    report_dir = verification.default_report_dir(report_root, snapshot, args.run_id)
    args.verification_report_dir = str(report_dir)
    _, plan_markdown_path = validation_plan.write_plan(report_dir, plan)
    print(f"plan: {report_dir / 'plan.json'}")
    if getattr(args, "explain", False):
        print(plan_markdown_path.read_text(encoding="utf-8"), end="")
    steps = verification_steps(args)
    planned_provider_summary = (
        provider_record_summary(args)
        if args.tier == "final" and getattr(args, "provider_smoke", True)
        else None
    )
    decision = verification.ReuseDecision(None, {}, [])
    if args.tier == "final":
        decision = verification.find_reusable_candidate(
            report_root,
            snapshot,
            candidate_plan_fingerprint,
            environment_fingerprint,
            candidate_steps,
            verification.artifact_fingerprints(REPO_ROOT),
        )
        if decision.source == report_dir / "record.json":
            preserved = verification.preserve_candidate_record(report_dir)
            if preserved is not None:
                decision = verification.ReuseDecision(preserved, decision.reusable, decision.invalidated)
    report_dir.mkdir(parents=True, exist_ok=True)
    command_logs = report_dir / "command-logs"
    command_logs.mkdir(parents=True, exist_ok=True)
    rows = [verification.planned_step_record(step) for step in steps]
    row_by_label = {row["label"]: row for row in rows}
    steps_to_execute = [step for step in steps if step.label not in decision.reusable]
    test_env = candidate_test_env if any(step.test_environment for step in steps_to_execute) else None
    executed: list[str] = []
    reused: list[str] = []
    status = 0
    started_at = verification.utc_now()
    for step in steps:
        reusable = decision.reusable.get(step.label)
        if reusable is not None:
            row = row_by_label[step.label]
            for key in (
                "started_at",
                "duration",
                "exit_status",
                "log",
                "attempts",
                "initial_outcome",
                "outcome",
                "reason",
                "matched_rule",
                "blocks_merge",
                "recommended_action",
                "retryable",
            ):
                row[key] = reusable.get(key)
            row["execution_state"] = "reused"
            row["reused_from"] = str(decision.source) if decision.source else None
            reused.append(step.label)
            print(f"reused: {step.label} ({decision.source})")
            continue
        row_by_label[step.label].update(run_recorded_verification_step(step, command_logs, test_env))
        executed.append(step.label)
        print(f"executed: {step.label}")
        if row_by_label[step.label]["blocks_merge"]:
            status = 1
            break

    invalidated = list(decision.invalidated)
    if status == 0:
        post_snapshot = verification.repository_snapshot(REPO_ROOT)
        if post_snapshot.head_sha != snapshot.head_sha:
            status = 1
            invalidated.append(
                {
                    "record": None,
                    "reason": "head_sha changed during verification",
                    "steps": [step.label for step in steps],
                }
            )
        if post_snapshot.dirty:
            status = 1
            invalidated.append(
                {
                    "record": None,
                    "reason": "worktree became dirty during verification",
                    "steps": [step.label for step in steps],
                }
            )

    for item in invalidated:
        affected = ", ".join(item.get("steps") or []) or "candidate plan"
        print(f"invalidated: {affected}: {item['reason']} ({item.get('record') or 'no record'})")
    invalidated_labels = sorted({label for item in invalidated for label in item.get("steps") or []})
    print(f"reused steps: {', '.join(reused) or 'none'}")
    print(f"executed steps: {', '.join(executed) or 'none'}")
    print(f"invalidated steps: {', '.join(invalidated_labels) or 'none'}")

    provider_summary = planned_provider_summary
    if args.tier == "final" and getattr(args, "provider_smoke", True):
        provider_summary = (
            load_optional_json(report_dir / "provider-model-smoke" / "summary.json")
            or planned_provider_summary
        )
    record = verification.build_record(
        tier=args.tier,
        run_id=args.run_id,
        snapshot=snapshot,
        plan_fingerprint=candidate_plan_fingerprint,
        environment_fingerprint=environment_fingerprint,
        steps=rows,
        status="pass" if status == 0 else "fail",
        reused=reused,
        executed=executed,
        invalidated=invalidated,
        provider_summary=provider_summary,
        artifacts=verification.artifact_fingerprints(REPO_ROOT),
        started_at=started_at,
    )
    verification.write_record(report_dir, record)
    print(verification.render_terminal_summary(record))
    print(f"record: {report_dir / 'record.md'}")
    return status


def require_clean_worktree() -> verification.RepositorySnapshot:
    snapshot = verification.repository_snapshot(REPO_ROOT)
    if snapshot.dirty:
        raise ValueError("candidate and final verification require a clean worktree")
    return snapshot


def isolated_test_environment() -> dict[str, str]:
    existing_ripgrep = shutil.which("rg")
    if existing_ripgrep:
        ripgrep_dir = os.path.dirname(os.path.abspath(existing_ripgrep))
    else:
        ripgrep_dir = provisioned_ripgrep_directory()
    env = os.environ.copy()
    env["PATH"] = ripgrep_dir + os.pathsep + env.get("PATH", "")
    return env


def provisioned_ripgrep_directory() -> str:
    completed = subprocess.run(
        bash_script_command(ENSURE_RIPGREP),
        cwd=REPO_ROOT,
        check=False,
        capture_output=True,
        text=True,
    )
    ripgrep_dir = completed.stdout.strip()
    if completed.returncode != 0 or not ripgrep_dir:
        detail = completed.stderr.strip()
        suffix = f": {detail}" if detail else ""
        raise ValueError(f"failed to provision ripgrep for isolated Go tests{suffix}")
    if os.name == "nt":
        return str(REPO_ROOT / ".tmp" / "dev-ripgrep" / "juex-path")
    return ripgrep_dir


def bash_script_command(script: str, *args: str) -> list[str]:
    return [BASH_EXECUTABLE, script, *args]


def run_visible(step: VerificationStep, test_env: dict[str, str] | None) -> int:
    rendered = shlex.join(step.command)
    print(f"==> {step.label}: {rendered}")
    env = test_env.copy() if step.test_environment and test_env is not None else None
    if step.environment:
        if env is None:
            env = os.environ.copy()
        env.update(step.environment)
    try:
        completed = subprocess.run(
            step.command,
            cwd=REPO_ROOT,
            env=env,
            check=False,
        )
        status = completed.returncode
        process_error = None
    except OSError as exc:
        status = 1
        process_error = exc
    result = (
        outcomes.success(attempt_count=1)
        if status == 0 and process_error is None
        else outcomes.classify_failure(
            "",
            deterministic=True,
            exit_status=status,
            process_error=process_error,
        )
    )
    print(outcomes.marker(result))
    if result.blocks_merge:
        print(f"FAIL {step.label} (exit {status}) outcome={result.outcome}", file=sys.stderr)
        return 1
    print(f"ok  {step.label} outcome={result.outcome}")
    return 0


def run_recorded_verification_step(
    step: VerificationStep,
    log_dir: pathlib.Path,
    test_env: dict[str, str] | None,
) -> dict[str, object]:
    rendered = shlex.join(step.command)
    print(f"==> {step.label}: {rendered}")
    env = test_env.copy() if step.test_environment and test_env is not None else None
    if step.environment:
        if env is None:
            env = os.environ.copy()
        env.update(step.environment)
    attempts: list[dict[str, object]] = []
    for attempt_number in (1, 2):
        log_path = log_dir / f"{step.label}.attempt-{attempt_number}.log"
        started_at = verification.utc_now()
        started = time.monotonic()
        process_error: OSError | None = None
        exit_status = 1
        try:
            with log_path.open("wb") as log:
                completed = subprocess.run(
                    step.command,
                    cwd=REPO_ROOT,
                    env=env,
                    stdout=log,
                    stderr=subprocess.STDOUT,
                    check=False,
                )
            exit_status = completed.returncode
        except OSError as exc:
            process_error = exc
            log_path.write_text(f"{exc.__class__.__name__}: {exc}\n", encoding="utf-8")
        duration = round(time.monotonic() - started, 6)
        log_text = log_path.read_text(encoding="utf-8", errors="replace")
        result = (
            outcomes.success(attempt_count=1)
            if exit_status == 0 and process_error is None
            else outcomes.classify_failure(
                log_text,
                deterministic=not step.retry_transient,
                exit_status=exit_status,
                process_error=process_error,
            )
        )
        attempt = {
            "attempt": attempt_number,
            "started_at": started_at,
            "duration": duration,
            "exit_status": exit_status,
            "log": str(log_path),
            **result.as_dict(),
        }
        attempts.append(attempt)
        if exit_status == 0 and process_error is None:
            terminal = outcomes.success(attempt_count=attempt_number)
            break
        if attempt_number == 2 or not (step.retry_transient and result.retryable):
            terminal = result
            break
        try:
            archived_report = archive_step_report(step.command, attempt_number)
        except OSError as exc:
            terminal = outcomes.ValidationOutcome(
                outcomes.ENVIRONMENT_FAILURE,
                f"could not archive retry artifacts: {exc.__class__.__name__}",
                "environment-retry-archive",
                True,
                "fix_environment",
            )
            attempt["archive_error"] = str(exc)
            break
        if archived_report is not None:
            attempt["report"] = archived_report
        print(
            f"retry {step.label} after {result.outcome} "
            f"(rule={result.matched_rule}, attempt 1/2)",
            file=sys.stderr,
        )

    final_attempt = attempts[-1]
    if terminal.blocks_merge:
        print(
            f"FAIL {step.label} (exit {final_attempt['exit_status']}), "
            f"outcome={terminal.outcome}, log: {final_attempt['log']}",
            file=sys.stderr,
        )
        print_tail(pathlib.Path(str(final_attempt["log"])), 40)
    else:
        print(f"ok  {step.label} outcome={terminal.outcome}")
    return {
        "execution_state": "executed",
        "started_at": attempts[0]["started_at"],
        "duration": round(sum(float(row["duration"]) for row in attempts), 6),
        "exit_status": final_attempt["exit_status"],
        "log": final_attempt["log"],
        "attempts": attempts,
        "initial_outcome": attempts[0]["outcome"],
        **terminal.as_dict(),
    }


def archive_step_report(command: list[str], attempt_number: int) -> str | None:
    try:
        index = command.index("--report-dir")
        source = pathlib.Path(command[index + 1])
    except (ValueError, IndexError):
        return None
    if not source.is_dir():
        return None
    destination = source.with_name(f"{source.name}.attempt-{attempt_number}")
    collision = 1
    while destination.exists():
        destination = source.with_name(f"{source.name}.attempt-{attempt_number}-{collision}")
        collision += 1
    shutil.copytree(source, destination)
    return str(destination)


def load_optional_json(path: pathlib.Path) -> dict[str, object] | None:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return None
    return value if isinstance(value, dict) else None


def provider_record_summary(args: argparse.Namespace) -> dict[str, object]:
    config_path = pathlib.Path(args.config)
    try:
        config = helper.load_source_config(config_path)
        _, evidence = selection.select(
            config,
            kind="provider-smoke",
            config_path=config_path,
            seed=args.selection_seed,
            command_prefix=module_command("provider-smoke"),
        )
    except selection.ProviderUnavailable as exc:
        evidence = exc.evidence
    except (OSError, ValueError, yaml.YAMLError):
        evidence = selection.unavailable_evidence(
            config_path=config_path,
            seed=args.selection_seed,
            command_prefix=module_command("provider-smoke"),
        )
    return evidence.as_dict()


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
    parser.add_argument(
        "--retries",
        type=int,
        choices=(0, 1),
        default=int(os.environ.get("JUEX_PROVIDER_SMOKE_RETRIES") or "1"),
    )
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

    test_env = isolated_test_environment() if any(step.test_environment for step in steps) else None
    overall = execute_development_steps(
        steps,
        lambda step: run_logged(
            step,
            command_logs,
            commands_file,
            test_env,
        ),
    )

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
    commands = [
        json.loads(line)
        for line in commands_file.read_text(encoding="utf-8").splitlines()
        if line.strip()
    ]
    print(verification.render_terminal_summary(helper.development_outcome_summary(commands, overall)))
    print(f"record: {report_dir / 'record.md'}")
    return 1 if overall else 0


def validate_development_args(args: argparse.Namespace) -> None:
    if args.provider_only and args.provider_all_models:
        raise ValueError("--only cannot be combined with --provider-all-models")
    if args.compaction_all_models and args.compaction_only:
        raise ValueError("--compaction-all-models cannot be combined with --compaction-only")


def development_steps(args: argparse.Namespace, report_dir: pathlib.Path) -> tuple[list[VerificationStep], pathlib.Path, str]:
    provider_report_dir = report_dir / "provider-model-smoke"
    compaction_report_dir = report_dir / "compaction-eval" if args.compaction_eval else None

    candidate_steps = candidate_verification_steps(race=False, web=False)
    if args.skip_tests:
        candidate_steps = [step for step in candidate_steps if not step.test_environment]
    steps = candidate_steps

    if not args.no_provider_smoke:
        steps.append(
            VerificationStep(
                "provider-model-smoke",
                provider_smoke_development_command(args, provider_report_dir),
                test_environment=True,
                retry_transient=True,
            )
        )
    if compaction_report_dir is not None:
        steps.append(
            VerificationStep(
                "compaction-eval",
                compaction_development_command(args, compaction_report_dir),
                retry_transient=True,
            )
        )
    return steps, provider_report_dir, str(compaction_report_dir or "")


def provider_smoke_development_command(args: argparse.Namespace, report_dir: pathlib.Path) -> list[str]:
    command = module_command("provider-smoke")
    append_value(command, "--juex", "./dist/juex")
    append_value(command, "--config", args.config)
    append_value(command, "--selection-seed", args.selection_seed)
    append_value(command, "--report-dir", report_dir)
    append_value(command, "--run-id", args.run_id)
    append_value(command, "--timeout", args.provider_timeout)
    append_value(command, "--retries", 0)
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


def run_logged(
    step: VerificationStep,
    log_dir: pathlib.Path,
    commands_file: pathlib.Path,
    test_env: dict[str, str] | None,
) -> int:
    result = run_recorded_verification_step(step, log_dir, test_env)
    helper.append_jsonl(
        commands_file,
        {
            "label": step.label,
            "command": shlex.join(step.command),
            **result,
        },
    )
    return 1 if result["blocks_merge"] else 0


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
