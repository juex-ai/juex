from __future__ import annotations

import argparse
import json
import os
import pathlib
import re
import shutil
import sys
import tempfile
import time

import yaml

from . import helper, outcomes, selection


DEFAULT_COMPACTION = {
    "enabled": True,
    "reserve_tokens": 8000,
    "keep_recent_tokens": 6000,
    "summary_max_tokens": 2048,
    "tool_result_max_chars": 1200,
    "user_input_inline_max_bytes": 524288,
}

AUTHORITATIVE_GOAL = {
    "version": 1,
    "description": "Ship authoritative compaction state fidelity for task CMP-2417.",
    "acceptance": "The compact Goal matches goal_state and Notes survive unchanged.",
    "status": "success",
    "updated_at": "2026-09-01T00:00:00Z",
}
AUTHORITATIVE_COMPLETED_NOTE = "Map the compaction runtime."
AUTHORITATIVE_OPEN_NOTE = "Run the live compaction evaluation and inspect its scorecard."
AUTHORITATIVE_NOTES = (
    f"- [x] {AUTHORITATIVE_COMPLETED_NOTE}\n"
    f"- [ ] {AUTHORITATIVE_OPEN_NOTE}\n"
)


def add_args(parser: argparse.ArgumentParser) -> None:
    parser.description = "Run the live compaction quality smoke."
    parser.add_argument(
        "--only",
        action="append",
        default=[],
        help="Run one explicit provider:model ref. May be repeated.",
    )
    parser.add_argument("--all-models", action="store_true", help="Run every eligible provider:model in the provider config.")
    parser.add_argument(
        "--selection-seed",
        default=os.environ.get("JUEX_EVAL_SELECTION_SEED") or selection.generated_seed(),
        help="Reproducible seed for provider-config candidate selection.",
    )
    parser.add_argument("--juex", default=os.environ.get("JUEX_BIN") or "./dist/juex")
    parser.add_argument(
        "--config",
        default=os.environ.get("JUEX_PROVIDER_CONFIG") or str(pathlib.Path.home() / ".juex" / "juex.yaml"),
    )
    parser.add_argument(
        "--report-dir",
        "--out-root",
        dest="out_root",
        metavar="REPORT_DIR",
        default=os.environ.get("JUEX_COMPACTION_REPORT_DIR") or os.environ.get("OUT_ROOT") or "",
        help="Write compaction eval reports under this directory.",
    )
    parser.add_argument(
        "--run-id",
        default=os.environ.get("JUEX_COMPACTION_RUN_ID") or os.environ.get("RUN_ID") or time.strftime("%Y%m%dT%H%M%SZ", time.gmtime()),
    )
    parser.add_argument("--context-window", type=int, default=int(os.environ.get("PROVIDER_CONTEXT_WINDOW") or "32000"))
    parser.add_argument("--turn-timeout", type=int, default=int(os.environ.get("JUEX_EVAL_TURN_TIMEOUT") or "600"))
    parser.add_argument("--keep-workdir", action="store_true", default=(os.environ.get("KEEP_WORKDIR") == "1"))


def run(args: argparse.Namespace) -> int:
    explicit_models = [*(args.only or [])]
    if args.all_models and explicit_models:
        raise ValueError("--all-models cannot be combined with --only")
    if args.context_window <= 0:
        raise ValueError("--context-window must be a positive integer")
    juex = pathlib.Path(args.juex)
    if not os.access(juex, os.X_OK):
        raise ValueError(f"Missing executable {args.juex}. Run: make build")
    config = selection.resolved_path(args.config)
    out_root = pathlib.Path(args.out_root or helper.default_report_dir("compaction-eval", args.run_id))
    out_root.mkdir(parents=True, exist_ok=True)
    summary_json = out_root / "summary.json"
    summary_md = out_root / "summary.md"
    command_prefix = [
        sys.executable,
        "-m",
        "tests.eval.juex_eval",
        "compaction",
        "--juex",
        args.juex,
        "--run-id",
        args.run_id,
        "--context-window",
        str(args.context_window),
        "--turn-timeout",
        str(args.turn_timeout),
    ]
    try:
        if not config.is_file():
            raise FileNotFoundError(f"Missing provider config: {config}")
        cfg, source_layers = helper.load_source_config_with_layers(config)
        helper.validate_source_layers(args.juex, source_layers)
        candidates, evidence = selection.select(
            cfg,
            kind="compaction",
            config_path=config,
            seed=args.selection_seed,
            only=explicit_models,
            all_models=args.all_models,
            required_context_window=args.context_window,
            command_prefix=command_prefix,
        )
    except selection.ProviderUnavailable as exc:
        unavailable = provider_unavailable_outcome(str(exc))
        write_compaction_summary(
            summary_json,
            summary_md,
            args,
            exc.evidence,
            [],
            exc.failure_category,
            str(exc),
            unavailable,
        )
        print(f"{selection.PROVIDER_UNAVAILABLE}: {exc}", file=sys.stderr)
        helper.print_selection_evidence(exc.evidence)
        print(outcomes.marker(unavailable))
        return 1
    except (OSError, ValueError, yaml.YAMLError) as exc:
        error = helper.safe_config_error(exc)
        evidence = selection.unavailable_evidence(
            config_path=config,
            seed=args.selection_seed,
            command_prefix=command_prefix,
            only=explicit_models,
            all_models=args.all_models,
        )
        invalid_config = outcomes.invalid_config_failure(error)
        write_compaction_summary(
            summary_json,
            summary_md,
            args,
            evidence,
            [],
            outcomes.ENVIRONMENT_FAILURE,
            error,
            invalid_config,
        )
        print(f"{outcomes.ENVIRONMENT_FAILURE}: {error}", file=sys.stderr)
        helper.print_selection_evidence(evidence)
        print(outcomes.marker(invalid_config))
        return 1

    helper.print_selection_evidence(evidence)
    temp_dirs: list[pathlib.Path] = []
    failed = 0
    results: list[dict[str, object]] = []
    try:
        for candidate in candidates:
            status = run_model(args, cfg, candidate.ref, out_root, temp_dirs)
            failed += status
            result = load_model_outcome(out_root / helper.safe_ref(candidate.ref), status)
            results.append(
                {
                    "provider_model": candidate.ref,
                    "status": "fail" if status else "pass",
                    **result.as_dict(),
                }
            )
    finally:
        if not args.keep_workdir:
            for temp_dir in temp_dirs:
                shutil.rmtree(temp_dir, ignore_errors=True)

    validation_outcome = aggregate_compaction_outcome(results)
    write_compaction_summary(summary_json, summary_md, args, evidence, results, "", "", validation_outcome)
    print(f"Reports written to {out_root}")
    print(outcomes.marker(validation_outcome))
    return 1 if failed else 0


def write_compaction_summary(
    summary_json: pathlib.Path,
    summary_md: pathlib.Path,
    args: argparse.Namespace,
    evidence: selection.SelectionEvidence,
    results: list[dict[str, object]],
    failure_category: str,
    error: str,
    validation_outcome: outcomes.ValidationOutcome | None = None,
) -> None:
    validation_outcome = validation_outcome or aggregate_compaction_outcome(results)
    summary: dict[str, object] = {
        "run_id": args.run_id,
        "failure_category": failure_category or None,
        "error": error or None,
        "context_window": args.context_window,
        "turn_timeout_seconds": args.turn_timeout,
        "total": len(results),
        "passed": sum(1 for result in results if result["status"] == "pass"),
        "failed": sum(1 for result in results if result["status"] != "pass"),
        "results": results,
        **validation_outcome.as_dict(),
    }
    summary.update(evidence.as_dict())
    summary_json.write_text(json.dumps(summary, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    lines = [
        "# Compaction Eval Summary",
        "",
        f"- Run ID: `{args.run_id}`",
        f"- Selection source: `{selection.SELECTION_SOURCE}`",
        f"- Selected provider/model: `{summary['selected_provider_model'] or ''}`",
        f"- Selected provider/models: `{', '.join(evidence.selected_refs)}`",
        f"- Selection seed: `{evidence.seed}`",
        f"- Eligible candidate count: {len(evidence.eligible_refs)}",
        f"- Eligible candidate refs: `{', '.join(evidence.eligible_refs)}`",
        f"- Resolved config path: `{evidence.resolved_config_path}`",
        f"- Redacted config hash: `{evidence.redacted_config_hash}`",
        f"- Reproduction command: `{evidence.reproduction_command}`",
        f"- Context window: {args.context_window}",
        f"- Failure category: `{failure_category}`",
        f"- Error: {error}",
        f"- Outcome: `{validation_outcome.outcome}`",
        f"- Reason: {validation_outcome.reason}",
        f"- Matched rule: `{validation_outcome.matched_rule}`",
        f"- Blocks merge: {str(validation_outcome.blocks_merge).lower()}",
        f"- Recommended action: `{validation_outcome.recommended_action}`",
        f"- Total: {len(results)}",
        f"- Passed: {summary['passed']}",
        f"- Failed: {summary['failed']}",
    ]
    if results:
        lines.extend(["", "| Provider/model | Status |", "| --- | --- |"])
        for result in results:
            lines.append(f"| `{result['provider_model']}` | {result['status']} |")
    summary_md.write_text("\n".join(lines) + "\n", encoding="utf-8")


def provider_unavailable_outcome(reason: str) -> outcomes.ValidationOutcome:
    return outcomes.ValidationOutcome(
        outcomes.PROVIDER_UNAVAILABLE,
        reason or "no eligible provider/model is available",
        "provider-selection-unavailable",
        True,
        "stop",
    )


def aggregate_compaction_outcome(results: list[dict[str, object]]) -> outcomes.ValidationOutcome:
    failed = [result for result in results if result.get("status") != "pass"]
    if not failed:
        return outcomes.success(attempt_count=1)
    priority = {
        outcomes.PRODUCT_FAILURE: 0,
        outcomes.ENVIRONMENT_FAILURE: 1,
        outcomes.PROVIDER_UNAVAILABLE: 2,
        outcomes.TRANSIENT_FAILURE: 3,
    }
    selected = min(failed, key=lambda result: priority.get(str(result.get("outcome")), 99))
    return outcomes.ValidationOutcome(
        str(selected.get("outcome") or outcomes.PRODUCT_FAILURE),
        str(selected.get("reason") or "compaction evaluation failed"),
        str(selected.get("matched_rule") or "compaction-product-failure"),
        True,
        str(selected.get("recommended_action") or "fix_code"),
        bool(selected.get("retryable")),
    )


def load_model_outcome(out_dir: pathlib.Path, status: int) -> outcomes.ValidationOutcome:
    path = out_dir / "outcome.json"
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
        outcome = str(value.get("outcome"))
        if outcome not in outcomes.OUTCOME_VALUES:
            raise ValueError("invalid outcome")
        return outcomes.ValidationOutcome(
            outcome,
            str(value.get("reason") or "compaction evaluation result"),
            str(value.get("matched_rule") or "compaction-result"),
            bool(value.get("blocks_merge")),
            str(value.get("recommended_action") or ("continue" if status == 0 else "fix_code")),
            bool(value.get("retryable")),
        )
    except (OSError, ValueError, json.JSONDecodeError):
        if status == 0:
            return outcomes.success(attempt_count=1)
        return outcomes.ValidationOutcome(
            outcomes.PRODUCT_FAILURE,
            "compaction evaluation failed without a structured model outcome",
            "compaction-unstructured-failure",
            True,
            "fix_code",
        )


def write_model_outcome(out_dir: pathlib.Path, result: outcomes.ValidationOutcome) -> None:
    (out_dir / "outcome.json").write_text(
        json.dumps(result.as_dict(), ensure_ascii=False, indent=2) + "\n",
        encoding="utf-8",
    )


def parse_model_ref(model: str) -> tuple[str, str, str]:
    model = model.strip()
    if ":" not in model:
        raise ValueError("invalid model ref format (expected provider:model)")
    provider_id, model_id = (part.strip() for part in model.split(":", 1))
    if not provider_id or not model_id:
        raise ValueError("invalid model ref format (expected provider:model)")
    return f"{provider_id}:{model_id}", provider_id, model_id


def run_model(args: argparse.Namespace, cfg: dict, model: str, out_root: pathlib.Path, temp_dirs: list[pathlib.Path]) -> int:
    try:
        model, provider_id, model_id = parse_model_ref(model)
    except ValueError:
        print(f"FAIL {model}: invalid model ref format (expected provider:model)", file=sys.stderr)
        return 1
    safe = helper.safe_ref(model)
    work = pathlib.Path(tempfile.mkdtemp(prefix=f"juex-compaction-eval.{safe}."))
    temp_dirs.append(work)
    out_dir = out_root / safe
    (work / ".juex").mkdir(parents=True, exist_ok=True)
    out_dir.mkdir(parents=True, exist_ok=True)

    try:
        selected_config = work / ".juex" / "juex.yaml"
        helper.write_selected_config(
            cfg,
            provider_id,
            model_id,
            selected_config,
            disable_tools=True,
            compaction=DEFAULT_COMPACTION,
        )
        helper.validate_source_config(args.juex, selected_config)
    except Exception as exc:  # noqa: BLE001
        err = out_dir / "config-error.txt"
        err.write_text(str(exc), encoding="utf-8")
        write_failure_scorecard(model, work, out_dir, "config", "no", "not captured", err, args.keep_workdir, args.context_window, args.turn_timeout)
        write_model_outcome(
            out_dir,
            outcomes.ValidationOutcome(
                outcomes.ENVIRONMENT_FAILURE,
                "compaction provider configuration could not be prepared",
                "environment-compaction-config",
                True,
                "fix_environment",
            ),
        )
        print(f"FAIL {model}: provider:model not found in {args.config}", file=sys.stderr)
        return 1

    prompts = write_prompts(work)
    print(f"==> Running {model} in {work}")
    for turn in ["turn1", "turn2", "turn3"]:
        output = out_dir / f"{turn}.txt"
        status = run_eval_turn(args, work, prompts[turn], output)
        if status != 0:
            compacted = "yes" if has_compaction(work) else "no"
            cache_ratio = cache_ratio_from_work(work)
            copy_runtime_artifacts(work, out_dir)
            write_failure_scorecard(model, work, out_dir, turn, compacted, cache_ratio, output, args.keep_workdir, args.context_window, args.turn_timeout)
            failure = outcomes.classify_failure(
                output.read_text(encoding="utf-8", errors="replace"),
                deterministic=False,
                exit_status=status,
            )
            write_model_outcome(out_dir, failure)
            print(f"FAIL {model}: {turn} failed", file=sys.stderr)
            return 1

        if turn == "turn1":
            try:
                seed_authoritative_state(work)
            except Exception as exc:  # noqa: BLE001
                error = out_dir / "state-seed-error.txt"
                error.write_text(str(exc), encoding="utf-8")
                compacted = "yes" if has_compaction(work) else "no"
                cache_ratio = cache_ratio_from_work(work)
                copy_runtime_artifacts(work, out_dir)
                write_failure_scorecard(model, work, out_dir, "state-seed", compacted, cache_ratio, error, args.keep_workdir, args.context_window, args.turn_timeout)
                write_model_outcome(
                    out_dir,
                    outcomes.ValidationOutcome(
                        outcomes.ENVIRONMENT_FAILURE,
                        "compaction authoritative state could not be prepared",
                        "environment-compaction-state",
                        True,
                        "fix_environment",
                    ),
                )
                print(f"FAIL {model}: unable to seed authoritative state", file=sys.stderr)
                return 1

    answer = (out_dir / "turn3.txt").read_text(encoding="utf-8", errors="replace")
    fact_score = score_answer(answer)
    authoritative = score_authoritative_state(work, answer)
    score = fact_score + authoritative["score"]
    compacted = "yes" if has_compaction(work) else "no"
    cache_ratio = cache_ratio_from_work(work)
    write_scorecard(model, work, out_dir, fact_score, authoritative, compacted, cache_ratio, args.keep_workdir, args.context_window, args.turn_timeout)
    copy_runtime_artifacts(work, out_dir)
    failed = 0
    if compacted != "yes":
        print(f"FAIL {model}: compaction did not run", file=sys.stderr)
        failed = 1
    if fact_score < 36:
        print(f"FAIL {model}: fact score {fact_score}/52 is below the regression threshold", file=sys.stderr)
        failed = 1
    failed_state_checks = [name for name, passed in authoritative["checks"].items() if not passed]
    if failed_state_checks:
        print(f"FAIL {model}: authoritative state checks failed: {', '.join(failed_state_checks)}", file=sys.stderr)
        failed = 1
    if failed:
        write_model_outcome(
            out_dir,
            outcomes.ValidationOutcome(
                outcomes.PRODUCT_FAILURE,
                "compaction quality or authoritative-state contract failed",
                "compaction-quality-contract",
                True,
                "fix_code",
            ),
        )
    else:
        write_model_outcome(out_dir, outcomes.success(attempt_count=1))
    print(f"==> {model} score {score}/82, compacted={compacted}")
    return failed


def write_prompts(work: pathlib.Path) -> dict[str, pathlib.Path]:
    turn1 = work / "turn1.prompt.txt"
    turn2 = work / "turn2.prompt.txt"
    turn3 = work / "turn3.prompt.txt"
    turn1.write_text(
        """You are participating in a Juex context-compaction evaluation. Do not use
tools in any turn of this evaluation.

Store these facts for later recall in conversation context only, then answer
only: TURN1 STORED.

GF1: Task ID is CMP-2417.
GF2: Branch is high/context-projection.
GF3: Do not modify /workspace/project/.juex/threads/0/journal.jsonl unless the user explicitly approves.
GF4: The failing error string is compact context: openai codex responses: codex SSE read: context deadline exceeded.
GF5: The selected design is sidecar externalization plus frozen provider-visible replacement.
GF6: The next command is go test ./internal/runtime -run TestTurn_AutoCompactionBoundsOversizedSummaryRequest -count=1.

Ignore the following noise for later recall.
"""
        + noise("turn1", 900),
        encoding="utf-8",
    )
    turn2.write_text(
        """Continue the same evaluation. Do not use tools. This turn intentionally adds
irrelevant context pressure. Preserve the six GF facts from the previous turn
in conversation context only. Answer only: TURN2 STORED.

Irrelevant context begins below.
"""
        + noise("turn2", 40),
        encoding="utf-8",
    )
    turn3.write_text(
        """No tools. Answer the evaluation questions using only this Thread's context.
The Thread may have been compacted; compacted summaries and replacement
context are valid Thread context for this evaluation.

Return exactly these labels:
GF1:
GF2:
GF3:
GF4:
GF5:
GF6:
Tools:
CompactionSource:
NoInventedMerge:
GoalDescription:
GoalAcceptance:
GoalStatus:
NotesCompleted:
NotesOpen:
""",
        encoding="utf-8",
    )
    return {"turn1": turn1, "turn2": turn2, "turn3": turn3}


def noise(label: str, count: int) -> str:
    return "".join(
        f"{label} noise block {idx:05d}: alpha beta gamma delta epsilon zeta eta theta iota kappa lambda mu nu xi omicron pi rho sigma tau.\n"
        for idx in range(count)
    )


def run_eval_turn(args: argparse.Namespace, work: pathlib.Path, prompt_file: pathlib.Path, output_file: pathlib.Path) -> int:
    env = os.environ.copy()
    case_home = work / "home"
    case_home.mkdir(parents=True, exist_ok=True)
    codex_home = env.get("CODEX_HOME") or str(pathlib.Path.home() / ".codex")
    env["HOME"] = str(case_home)
    env["USERPROFILE"] = str(case_home)
    env["JUEX_HOME"] = str(case_home / ".juex")
    env["GIT_CONFIG_GLOBAL"] = str(case_home / "gitconfig")
    env["GIT_CONFIG_NOSYSTEM"] = "1"
    env["CODEX_HOME"] = codex_home
    for name in helper.ISOLATED_PROVIDER_ENVIRONMENT_KEYS:
        env.pop(name, None)
    env["PROVIDER_CONTEXT_WINDOW"] = str(args.context_window)
    command = [
        args.juex,
        "-C",
        str(work),
        "--enable-user-agents-resources=false",
        "send",
        "--wait",
        prompt_file.read_text(encoding="utf-8"),
    ]
    with output_file.open("wb") as output:
        status = helper.run_subprocess_with_timeout(command, args.turn_timeout, env=env, stdout=output, stderr=output)
    helper.stop_agent_runtime(args.juex, work, env)
    print(output_file.read_text(encoding="utf-8", errors="replace"), end="")
    return status


def score_answer(answer: str) -> int:
    score = 0
    checks = [
        ("CMP-2417", 6),
        ("high/context-projection", 6),
        ("/workspace/project/.juex/threads/0/journal.jsonl", 6),
        ("compact context: openai codex responses: codex SSE read: context deadline exceeded", 6),
        ("go test ./internal/runtime -run TestTurn_AutoCompactionBoundsOversizedSummaryRequest -count=1", 6),
    ]
    for needle, value in checks:
        if needle in answer:
            score += value
    if re.search("sidecar externalization", answer, re.I) and re.search("frozen", answer, re.I):
        score += 6
    if re.search(r"no tools|tools:\s*none|tools:\s*no", answer, re.I):
        score += 4
    positive_merge_claim = re.compile(
        r"(pull request|pr)\s*#?[0-9]+([^A-Za-z0-9]+[A-Za-z0-9]+){0,8}[^A-Za-z0-9]merged|"
        r"merged\s+(pull request|pr)\s*#?[0-9]+|merged\s+into\s+main|merge\s+commit\s+[0-9a-f]{7,}",
        re.I,
    )
    if not positive_merge_claim.search(answer):
        score += 6
    if re.search(r"compact|summary|compaction", answer, re.I):
        score += 6
    return score


def seed_authoritative_state(work: pathlib.Path) -> None:
    journals = thread_files(work, "journal.jsonl")
    if len(journals) != 1:
        raise ValueError(f"expected one active Thread after turn1, found {len(journals)}")
    thread_dir = journals[0].parent
    write_json_atomic(thread_dir / "goal_state.json", AUTHORITATIVE_GOAL)
    write_text_atomic(thread_dir / "notes.md", AUTHORITATIVE_NOTES)


def write_json_atomic(path: pathlib.Path, value: object) -> None:
    payload = (json.dumps(value, ensure_ascii=False, indent=2) + "\n").encode("utf-8")
    temp_path: pathlib.Path | None = None
    try:
        with tempfile.NamedTemporaryFile("wb", dir=path.parent, prefix=f".{path.name}.", delete=False) as output:
            temp_path = pathlib.Path(output.name)
            output.write(payload)
            output.flush()
            os.fsync(output.fileno())
        os.replace(temp_path, path)
        temp_path = None
    finally:
        if temp_path is not None:
            temp_path.unlink(missing_ok=True)


def write_text_atomic(path: pathlib.Path, value: str) -> None:
    temp_path: pathlib.Path | None = None
    try:
        with tempfile.NamedTemporaryFile("w", encoding="utf-8", dir=path.parent, prefix=f".{path.name}.", delete=False) as output:
            temp_path = pathlib.Path(output.name)
            output.write(value)
            output.flush()
            os.fsync(output.fileno())
        os.replace(temp_path, path)
        temp_path = None
    finally:
        if temp_path is not None:
            temp_path.unlink(missing_ok=True)


def score_authoritative_state(work: pathlib.Path, answer: str) -> dict:
    goal = read_latest_json(work, "goal_state.json")
    notes = read_latest_text(work, "notes.md")
    summary = latest_compact_summary(work)
    goal_section = summary_section(summary, "Goal", "Critical Context")
    next_steps = summary_section(summary, "Next Steps", "Relevant Files")
    checks = {
        "goal_description": bool(goal.get("description")) and goal["description"] in goal_section,
        "goal_acceptance": bool(goal.get("acceptance")) and goal["acceptance"] in goal_section,
        "goal_status": bool(goal.get("status")) and goal["status"] in goal_section,
        "notes_unchanged": notes == AUTHORITATIVE_NOTES,
        "notes_in_next_steps": contains_note_text(next_steps, AUTHORITATIVE_OPEN_NOTE),
        "notes_recited_after_compaction": contains_note_text(answer, AUTHORITATIVE_COMPLETED_NOTE) and contains_note_text(answer, AUTHORITATIVE_OPEN_NOTE),
    }
    goal_score = sum(6 for name in ["goal_description", "goal_acceptance", "goal_status"] if checks[name])
    notes_score = sum(4 for name in ["notes_unchanged", "notes_in_next_steps", "notes_recited_after_compaction"] if checks[name])
    return {"score": goal_score + notes_score, "checks": checks}


def contains_note_text(haystack: str, note: str) -> bool:
    normalized_haystack = " ".join(haystack.casefold().split())
    normalized_note = " ".join(note.casefold().split()).rstrip(".!?")
    return bool(normalized_note) and normalized_note in normalized_haystack


def read_latest_json(work: pathlib.Path, name: str) -> dict:
    paths = thread_files(work, name)
    if not paths:
        return {}
    try:
        value = json.loads(paths[-1].read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return {}
    return value if isinstance(value, dict) else {}


def read_latest_text(work: pathlib.Path, name: str) -> str:
    paths = thread_files(work, name)
    if not paths:
        return ""
    return paths[-1].read_text(encoding="utf-8", errors="replace")


def latest_compact_summary(work: pathlib.Path) -> str:
    latest = ""
    for path in thread_files(work, "journal.jsonl"):
        for line in path.read_text(encoding="utf-8", errors="replace").splitlines():
            try:
                commit = json.loads(line)
            except json.JSONDecodeError:
                continue
            for fact in commit.get("facts") or []:
                if fact.get("type") != "context.compacted":
                    continue
                summary = fact.get("summary") or {}
                for block in summary.get("blocks") or []:
                    if block.get("type") == "text" and block.get("text"):
                        latest = str(block["text"])
                        break
    return latest


def summary_section(summary: str, heading: str, next_heading: str) -> str:
    heading_pattern = rf"^\s*(?:#{{1,6}}\s*)?{re.escape(heading)}\s*:?\s*$"
    next_pattern = rf"^\s*(?:#{{1,6}}\s*)?{re.escape(next_heading)}\s*:?\s*$"
    match = re.search(heading_pattern + rf"\n(.*?)(?={next_pattern})", summary, re.I | re.M | re.S)
    return match.group(1).strip() if match else ""


def thread_files(work: pathlib.Path, name: str) -> list[pathlib.Path]:
    try:
        threads = thread_root(work)
    except (OSError, ValueError, json.JSONDecodeError):
        return []
    return sorted(threads.rglob(name)) if threads.is_dir() else []


def thread_root(work: pathlib.Path) -> pathlib.Path:
    return helper.agent_threads_dir(work, work / "home" / ".juex")


def has_compaction(work: pathlib.Path) -> bool:
    return any('"type":"context.compacted"' in path.read_text(encoding="utf-8", errors="replace") for path in thread_files(work, "journal.jsonl"))


def cache_ratio_from_work(work: pathlib.Path) -> str:
    for path in thread_files(work, "journal.jsonl"):
        ratio = cache_ratio_from_events(path)
        if ratio != "not captured":
            return ratio
    return "not captured"


def cache_ratio_from_events(path: pathlib.Path) -> str:
    lines = [line for line in path.read_text(encoding="utf-8", errors="replace").splitlines() if '"context_usage"' in line and '"cached_input_tokens"' in line]
    if not lines:
        return "not captured"
    line = lines[-1]
    cached = match_int(line, r'"cached_input_tokens":([0-9]+)')
    input_tokens = match_int(line, r'"input_tokens":([0-9]+)')
    if not cached or not input_tokens:
        return "not captured"
    return f"{cached}/{input_tokens} ({(cached / input_tokens) * 100:.1f}%)"


def match_int(text: str, pattern: str) -> int:
    match = re.search(pattern, text)
    return int(match.group(1)) if match else 0


def copy_runtime_artifacts(work: pathlib.Path, out_dir: pathlib.Path) -> None:
    for name in ["journal.jsonl", "thread.json", "goal_state.json", "notes.md"]:
        for path in thread_files(work, name):
            shutil.copy2(path, out_dir / name)


def write_scorecard(
    model: str,
    work: pathlib.Path,
    out_dir: pathlib.Path,
    fact_score: int,
    authoritative: dict,
    compacted: str,
    cache_ratio: str,
    keep_workdir: bool,
    context_window: int,
    timeout: int,
) -> None:
    total = fact_score + authoritative["score"]
    write_scorecard_common(model, work, out_dir, f"{total}/82", compacted, cache_ratio, "", "", keep_workdir, context_window, timeout, fact_score, authoritative)


def write_failure_scorecard(
    model: str,
    work: pathlib.Path,
    out_dir: pathlib.Path,
    stage: str,
    compacted: str,
    cache_ratio: str,
    output_file: pathlib.Path,
    keep_workdir: bool,
    context_window: int,
    timeout: int,
) -> None:
    error_tail = "\n".join(output_file.read_text(encoding="utf-8", errors="replace").splitlines()[-20:]) if output_file.is_file() else ""
    write_scorecard_common(model, work, out_dir, "n/a", compacted, cache_ratio, stage, error_tail, keep_workdir, context_window, timeout, None, None)


def write_scorecard_common(
    model: str,
    work: pathlib.Path,
    out_dir: pathlib.Path,
    score: str,
    compacted: str,
    cache_ratio: str,
    stage: str,
    error_tail: str,
    keep_workdir: bool,
    context_window: int,
    timeout: int,
    fact_score: int | None,
    authoritative: dict | None,
) -> None:
    lines = [
        "# Compaction Eval Scorecard",
        "",
        f"- Model: `{model}`",
        f"- Work dir: `{work}`" if keep_workdir else "- Work dir: cleaned after artifact copy; set `--keep-workdir` to keep it",
        f"- Context window: {context_window}",
        f"- Turn timeout: {timeout}s",
        f"- Score: {score}",
        f"- Compacted: {compacted}",
        f"- Cache ratio: {cache_ratio}",
    ]
    if fact_score is not None and authoritative is not None:
        lines += [
            f"- Fact score: {fact_score}/52",
            f"- Authoritative state score: {authoritative['score']}/30",
            "",
            "## Authoritative State Checks",
            "",
        ]
        labels = {
            "goal_description": "Goal description in compact Goal",
            "goal_acceptance": "Goal acceptance in compact Goal",
            "goal_status": "Goal status in compact Goal",
            "notes_unchanged": "Notes unchanged on disk",
            "notes_in_next_steps": "Unfinished Notes item in compact Next Steps",
            "notes_recited_after_compaction": "Notes recited after compaction",
        }
        for name, label in labels.items():
            status = "pass" if authoritative["checks"].get(name) else "fail"
            lines.append(f"- {label}: {status}")
    if stage:
        lines += ["- Error stage: " + stage, "", "## Error Tail", "", "```text", error_tail, "```"]
    (out_dir / "scorecard.md").write_text("\n".join(lines) + "\n", encoding="utf-8")
