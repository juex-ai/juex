from __future__ import annotations

import argparse
import os
import pathlib
import re
import shutil
import sys
import tempfile
import time

from . import helper, rotation


REPO_ROOT = pathlib.Path(__file__).resolve().parents[3]
DEFAULT_COMPACTION = {
    "enabled": True,
    "reserve_tokens": 8000,
    "keep_recent_tokens": 6000,
    "tail_turns": 1,
    "summary_max_tokens": 2048,
    "tool_result_max_chars": 1200,
    "user_input_inline_max_bytes": 524288,
}


def add_args(parser: argparse.ArgumentParser) -> None:
    parser.description = "Run the live compaction quality smoke."
    parser.add_argument(
        "models",
        nargs="*",
        help="Explicit provider/model refs. Prefer --only for new usage. Defaults to one rotated ref.",
    )
    parser.add_argument(
        "--only",
        action="append",
        default=[],
        help="Run one explicit provider/model ref. May be repeated.",
    )
    parser.add_argument("--all-models", action="store_true", help="Run every ref in compaction_eval_models.")
    parser.add_argument(
        "--model-list",
        default=os.environ.get("JUEX_LIVE_MODEL_LIST") or str(REPO_ROOT / "tests" / "eval" / "live-models.yaml"),
    )
    parser.add_argument(
        "--rotation-state",
        default=os.environ.get("JUEX_LIVE_MODEL_ROTATION_STATE") or str(REPO_ROOT / ".juex" / "live-model-rotation.json"),
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
    explicit_models = [*(args.only or []), *(args.models or [])]
    if args.all_models and explicit_models:
        raise ValueError("--all-models cannot be combined with --only or positional provider/model refs")
    juex = pathlib.Path(args.juex)
    if not os.access(juex, os.X_OK):
        raise ValueError(f"Missing executable {args.juex}. Run: mise exec -- make build")
    config = pathlib.Path(args.config).expanduser()
    if not config.is_file():
        raise ValueError(f"Missing provider config: {config}")

    model_list = pathlib.Path(args.model_list).expanduser()
    rotation_state = pathlib.Path(args.rotation_state).expanduser()
    rotated_model = ""
    if explicit_models:
        models = explicit_models
    elif args.all_models:
        models = rotation.load_model_refs(model_list, "compaction_eval_models")
    else:
        refs = rotation.load_model_refs(model_list, "compaction_eval_models")
        state = rotation.load_state(rotation_state)
        rotated_model = rotation.select_next(refs, state, "compaction_eval_models")
        models = [rotated_model]
        print(f"rotated compaction eval model: {rotated_model}")

    out_root = pathlib.Path(args.out_root or REPO_ROOT / "docs" / "reports" / "compaction-eval" / args.run_id)
    out_root.mkdir(parents=True, exist_ok=True)
    cfg = helper.load_yaml_file(config)
    temp_dirs: list[pathlib.Path] = []
    failed = 0
    try:
        for model in models:
            failed += run_model(args, cfg, model, out_root, temp_dirs)
    finally:
        if not args.keep_workdir:
            for temp_dir in temp_dirs:
                shutil.rmtree(temp_dir, ignore_errors=True)

    print(f"Reports written to {out_root}")
    if failed:
        return 1
    if rotated_model:
        state = rotation.load_state(rotation_state)
        rotation.mark_success(state, "compaction_eval_models", rotated_model)
        rotation.write_state(rotation_state, state)
    return 0


def run_model(args: argparse.Namespace, cfg: dict, model: str, out_root: pathlib.Path, temp_dirs: list[pathlib.Path]) -> int:
    safe = model.replace("/", "__")
    if "/" not in model:
        print(f"FAIL {model}: invalid model ref format (expected provider/model)", file=sys.stderr)
        return 1
    provider_id, model_id = model.split("/", 1)
    if not provider_id or not model_id:
        print(f"FAIL {model}: invalid model ref format (expected provider/model)", file=sys.stderr)
        return 1
    work = pathlib.Path(tempfile.mkdtemp(prefix=f"juex-compaction-eval.{safe}."))
    temp_dirs.append(work)
    out_dir = out_root / safe
    (work / ".juex").mkdir(parents=True, exist_ok=True)
    out_dir.mkdir(parents=True, exist_ok=True)

    try:
        helper.write_selected_config(
            cfg,
            provider_id,
            model_id,
            work / ".juex" / "juex.yaml",
            disable_tools=True,
            compaction=DEFAULT_COMPACTION,
        )
    except Exception as exc:  # noqa: BLE001
        err = out_dir / "config-error.txt"
        err.write_text(str(exc), encoding="utf-8")
        write_failure_scorecard(model, work, out_dir, "config", "no", "not captured", err, args.keep_workdir, args.context_window, args.turn_timeout)
        print(f"FAIL {model}: provider/model not found in {args.config}", file=sys.stderr)
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
            print(f"FAIL {model}: {turn} failed", file=sys.stderr)
            return 1

    score = score_answer((out_dir / "turn3.txt").read_text(encoding="utf-8", errors="replace"))
    compacted = "yes" if has_compaction(work) else "no"
    cache_ratio = cache_ratio_from_work(work)
    write_scorecard(model, work, out_dir, score, compacted, cache_ratio, args.keep_workdir, args.context_window, args.turn_timeout)
    copy_runtime_artifacts(work, out_dir)
    failed = 0
    if compacted != "yes":
        print(f"FAIL {model}: compaction did not run", file=sys.stderr)
        failed = 1
    if score < 36:
        print(f"FAIL {model}: score {score}/52 is below the regression threshold", file=sys.stderr)
        failed = 1
    print(f"==> {model} score {score}/52, compacted={compacted}")
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
GF3: Do not modify /workspace/project/.juex/sessions/20260525T043307-7f5f9f85/session.lock unless the user explicitly approves.
GF4: The failing error string is compact context: openai codex responses: codex SSE read: context deadline exceeded.
GF5: The selected design is sidecar externalization plus frozen provider-visible replacement.
GF6: The next command is mise exec -- go test ./internal/runtime -run TestTurn_AutoCompactionBoundsOversizedSummaryRequest -count=1.

Ignore the following noise for later recall.
"""
        + noise("turn1", 1400),
        encoding="utf-8",
    )
    turn2.write_text(
        """Continue the same evaluation. Do not use tools. This turn intentionally adds
irrelevant context pressure. Preserve the six GF facts from the previous turn
in conversation context only. Answer only: TURN2 STORED.

Irrelevant context begins below.
"""
        + noise("turn2", 1100),
        encoding="utf-8",
    )
    turn3.write_text(
        """No tools. Answer the evaluation questions using only this session's context.
The session may have been compacted; compacted summaries and replacement
context are valid session context for this evaluation.

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
    env["PROVIDER_CONTEXT_WINDOW"] = str(args.context_window)
    command = [
        args.juex,
        "-C",
        str(work),
        "--enable-user-global-resources=false",
        "run",
        prompt_file.read_text(encoding="utf-8"),
    ]
    with output_file.open("wb") as output:
        status = helper.run_subprocess_with_timeout(command, args.turn_timeout, env=env, stdout=output, stderr=output)
    print(output_file.read_text(encoding="utf-8", errors="replace"), end="")
    return status


def score_answer(answer: str) -> int:
    score = 0
    checks = [
        ("CMP-2417", 6),
        ("high/context-projection", 6),
        ("/workspace/project/.juex/sessions/20260525T043307-7f5f9f85/session.lock", 6),
        ("compact context: openai codex responses: codex SSE read: context deadline exceeded", 6),
        ("mise exec -- go test ./internal/runtime -run TestTurn_AutoCompactionBoundsOversizedSummaryRequest -count=1", 6),
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


def session_files(work: pathlib.Path, name: str) -> list[pathlib.Path]:
    sessions = work / ".juex" / "sessions"
    return sorted(sessions.rglob(name)) if sessions.is_dir() else []


def has_compaction(work: pathlib.Path) -> bool:
    return any('"kind":"compact"' in path.read_text(encoding="utf-8", errors="replace") for path in session_files(work, "conversation.jsonl"))


def cache_ratio_from_work(work: pathlib.Path) -> str:
    for path in session_files(work, "events.jsonl"):
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
    for name in ["conversation.jsonl", "events.jsonl"]:
        for path in session_files(work, name):
            shutil.copy2(path, out_dir / name)


def write_scorecard(
    model: str,
    work: pathlib.Path,
    out_dir: pathlib.Path,
    score: int,
    compacted: str,
    cache_ratio: str,
    keep_workdir: bool,
    context_window: int,
    timeout: int,
) -> None:
    write_scorecard_common(model, work, out_dir, f"{score}/52", compacted, cache_ratio, "", "", keep_workdir, context_window, timeout)


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
    write_scorecard_common(model, work, out_dir, "n/a", compacted, cache_ratio, stage, error_tail, keep_workdir, context_window, timeout)


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
    if stage:
        lines += ["- Error stage: " + stage, "", "## Error Tail", "", "```text", error_tail, "```"]
    (out_dir / "scorecard.md").write_text("\n".join(lines) + "\n", encoding="utf-8")
