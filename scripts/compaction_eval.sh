#!/usr/bin/env bash
# Run a small live compaction quality smoke against one or more configured
# Juex model refs. The script intentionally uses a temporary work directory per
# model and copies only non-secret runtime artifacts into docs/reports.

set -euo pipefail

usage() {
  cat <<'USAGE'
Usage: compaction_eval.sh [options] [provider/model ...]

Runs the live compaction quality smoke. By default it rotates one model from
tests/e2e/live-models.yaml and advances the local rotation state only after a
successful run.

Options:
  --all-models             Run every ref in compaction_eval_models.
  --model-list PATH        YAML model list. Default: tests/e2e/live-models.yaml.
  -h, --help               Show this help.
USAGE
}

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/.." && pwd)"
cd "$repo_root"

if ! command -v uv >/dev/null 2>&1; then
  echo "Missing uv. Install uv or run through the project toolchain before using compaction_eval.sh." >&2
  exit 2
fi

run_py() {
  uv run --quiet --project "$repo_root" python "$@"
}

JUEX_BIN=${JUEX_BIN:-"./dist/juex"}
if [ ! -x "$JUEX_BIN" ]; then
  echo "Missing $JUEX_BIN. Run: mise exec -- make build" >&2
  exit 1
fi
CONFIG_PATH=${JUEX_PROVIDER_CONFIG:-"$HOME/.juex/juex.yaml"}
if [ ! -f "$CONFIG_PATH" ]; then
  echo "Missing provider config: $CONFIG_PATH" >&2
  exit 1
fi
MODEL_LIST_PATH=${JUEX_LIVE_MODEL_LIST:-"tests/e2e/live-models.yaml"}
PROVIDER_CONTEXT_WINDOW=${PROVIDER_CONTEXT_WINDOW:-32000}
EVAL_TURN_TIMEOUT=${JUEX_EVAL_TURN_TIMEOUT:-600}
KEEP_WORKDIR=${KEEP_WORKDIR:-0}
TMP_WORKDIRS=()
all_models=0
explicit_models=()
rotated_model=""

cleanup() {
  if [ "$KEEP_WORKDIR" = "1" ]; then
    return
  fi
  for dir in "${TMP_WORKDIRS[@]}"; do
    rm -rf "$dir"
  done
}
trap cleanup EXIT

while [[ $# -gt 0 ]]; do
  case "$1" in
    --all-models)
      all_models=1
      shift
      ;;
    --model-list)
      MODEL_LIST_PATH="${2:-}"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    --)
      shift
      explicit_models+=("$@")
      break
      ;;
    --*)
      echo "unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
    *)
      explicit_models+=("$1")
      shift
      ;;
  esac
done

if [ "$all_models" = "1" ] && [ "${#explicit_models[@]}" -gt 0 ]; then
  echo "--all-models cannot be combined with explicit provider/model refs" >&2
  exit 2
fi

if [ "${#explicit_models[@]}" -gt 0 ]; then
  MODELS=("${explicit_models[@]}")
elif [ "$all_models" = "1" ]; then
  MODELS=()
  model_list_tmp=$(mktemp "${TMPDIR:-/tmp}/juex-compaction-models.XXXXXX")
  if ! run_py scripts/evalhelper.py list-models --model-list "$MODEL_LIST_PATH" --section compaction_eval_models > "$model_list_tmp"; then
    rm -f "$model_list_tmp"
    exit 1
  fi
  while IFS= read -r model; do
    MODELS+=("$model")
  done < "$model_list_tmp"
  rm -f "$model_list_tmp"
  if [ "${#MODELS[@]}" -eq 0 ]; then
    echo "No compaction eval models found in $MODEL_LIST_PATH" >&2
    exit 1
  fi
else
  rotated_model="$(run_py scripts/live_model_rotation.py --model-list "$MODEL_LIST_PATH" select --section compaction_eval_models)"
  MODELS=("$rotated_model")
  echo "rotated compaction eval model: ${rotated_model}"
fi

RUN_ID=${RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)}
OUT_ROOT=${OUT_ROOT:-"docs/reports/compaction-eval/${RUN_ID}"}
mkdir -p "$OUT_ROOT"
failed=0

noise() {
  local label=$1
  local count=$2
  awk -v label="$label" -v count="$count" 'BEGIN {
    for (i = 0; i < count; i++) {
      printf "%s noise block %05d: alpha beta gamma delta epsilon zeta eta theta iota kappa lambda mu nu xi omicron pi rho sigma tau.\n", label, i
    }
  }'
}

score_answer() {
  local answer=$1
  local score=0
  if grep -q "CMP-2417" "$answer"; then score=$((score + 6)); fi
  if grep -q "high/context-projection" "$answer"; then score=$((score + 6)); fi
  if grep -q "/workspace/project/.juex/sessions/20260525T043307-7f5f9f85/session.lock" "$answer"; then score=$((score + 6)); fi
  if grep -q "compact context: openai codex responses: codex SSE read: context deadline exceeded" "$answer"; then score=$((score + 6)); fi
  if grep -qi "sidecar externalization" "$answer"; then
    if grep -qi "frozen" "$answer"; then score=$((score + 6)); fi
  fi
  if grep -q "mise exec -- go test ./internal/runtime -run TestTurn_AutoCompactionBoundsOversizedSummaryRequest -count=1" "$answer"; then score=$((score + 6)); fi
  if grep -Eqi "no tools|tools:[[:space:]]*none|tools:[[:space:]]*no" "$answer"; then score=$((score + 4)); fi
  if ! grep -Eqi "merged|pull request #[0-9]+|PR #[0-9]+.*merged" "$answer"; then
    score=$((score + 6))
  fi
  if grep -Eqi "compact|summary|compaction" "$answer"; then score=$((score + 6)); fi
  printf "%s" "$score"
}

cache_ratio_from_events() {
  local events=$1
  local line
  line=$(grep '"context_usage"' "$events" | grep '"cached_input_tokens"' | tail -1 || true)
  if [ -z "$line" ]; then
    printf "not captured"
    return
  fi
  local cached input
  cached=$(printf "%s" "$line" | sed -E 's/.*"cached_input_tokens":([0-9]+).*/\1/')
  input=$(printf "%s" "$line" | sed -E 's/.*"input_tokens":([0-9]+).*/\1/')
  if [ -z "$cached" ] || [ -z "$input" ] || [ "$input" = "0" ]; then
    printf "not captured"
    return
  fi
  awk -v cached="$cached" -v input="$input" 'BEGIN {
    printf "%s/%s (%.1f%%)", cached, input, (cached / input) * 100
  }'
}

run_with_timeout() {
  local seconds=$1
  shift
  run_py scripts/evalhelper.py run-timeout --seconds "$seconds" -- "$@"
}

run_eval_turn() {
  local work=$1
  local prompt_file=$2
  local output_file=$3
  run_with_timeout "$EVAL_TURN_TIMEOUT" \
    env "PROVIDER_CONTEXT_WINDOW=$PROVIDER_CONTEXT_WINDOW" \
      "$JUEX_BIN" -C "$work" --enable-user-global-resources=false run "$(cat "$prompt_file")" \
      >"$output_file" 2>&1
  local status=$?
  cat "$output_file"
  return "$status"
}

copy_runtime_artifacts() {
  local work=$1
  local out_dir=$2
  while IFS= read -r conversation; do
    cp "$conversation" "$out_dir/conversation.jsonl"
  done < <(find "$work/.juex/sessions" -type f -name conversation.jsonl -print 2>/dev/null)
  while IFS= read -r events; do
    cp "$events" "$out_dir/events.jsonl"
  done < <(find "$work/.juex/sessions" -type f -name events.jsonl -print 2>/dev/null)
}

first_events_file() {
  local work=$1
  local file
  while IFS= read -r file; do
    printf "%s" "$file"
    return
  done < <(find "$work/.juex/sessions" -type f -name events.jsonl -print 2>/dev/null)
}

write_failure_scorecard() {
  local model=$1
  local work=$2
  local out_dir=$3
  local stage=$4
  local compacted=$5
  local cache_ratio=$6
  local output_file=$7
  local error_tail
  error_tail=$(tail -n 20 "$output_file" 2>/dev/null || true)
  {
    echo "# Compaction Eval Scorecard"
    echo
    echo "- Model: \`${model}\`"
    if [ "$KEEP_WORKDIR" = "1" ]; then
      echo "- Work dir: \`${work}\`"
    else
      echo "- Work dir: cleaned after artifact copy; set \`KEEP_WORKDIR=1\` to keep it"
    fi
    echo "- Context window: ${PROVIDER_CONTEXT_WINDOW}"
    echo "- Turn timeout: ${EVAL_TURN_TIMEOUT}s"
    echo "- Score: n/a"
    echo "- Compacted: ${compacted}"
    echo "- Cache ratio: ${cache_ratio}"
    echo "- Error stage: ${stage}"
    echo
    echo "## Error Tail"
    echo
    echo '```text'
    printf "%s\n" "$error_tail"
    echo '```'
  } > "$out_dir/scorecard.md"
}

write_model_config() {
  local provider_id=$1
  local model_id=$2
  local output_path=$3
  run_py scripts/evalhelper.py write-model-config \
    --source "$CONFIG_PATH" \
    --provider "$provider_id" \
    --model "$model_id" \
    --output "$output_path" \
    --disable-tools \
    --compaction-eval
}

for model in "${MODELS[@]}"; do
  safe_model=${model//\//__}
  provider_id=${model%%/*}
  model_id=${model#*/}
  work=$(mktemp -d "${TMPDIR:-/tmp}/juex-compaction-eval.${safe_model}.XXXXXX")
  TMP_WORKDIRS+=("$work")
  out_dir="${OUT_ROOT}/${safe_model}"
  mkdir -p "$work/.juex" "$out_dir"
  if ! write_model_config "$provider_id" "$model_id" "$work/.juex/juex.yaml" 2>"$out_dir/config-error.txt"; then
    write_failure_scorecard "$model" "$work" "$out_dir" "config" "no" "not captured" "$out_dir/config-error.txt"
    echo "FAIL ${model}: provider/model not found in ${CONFIG_PATH}" >&2
    failed=$((failed + 1))
    continue
  fi

  turn1_prompt="$work/turn1.prompt.txt"
  turn2_prompt="$work/turn2.prompt.txt"
  turn3_prompt="$work/turn3.prompt.txt"

  {
    cat <<'EOF'
You are participating in a Juex context-compaction evaluation. Do not use
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
EOF
    noise "turn1" 1400
  } > "$turn1_prompt"

  {
    cat <<'EOF'
Continue the same evaluation. Do not use tools. This turn intentionally adds
irrelevant context pressure. Preserve the six GF facts from the previous turn
in conversation context only. Answer only: TURN2 STORED.

Irrelevant context begins below.
EOF
    noise "turn2" 1100
  } > "$turn2_prompt"

  cat > "$turn3_prompt" <<'EOF'
No tools. Answer the evaluation questions using only this session's context.
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
EOF

  echo "==> Running $model in $work"
  if ! run_eval_turn "$work" "$turn1_prompt" "$out_dir/turn1.txt"; then
    compacted="no"
    cache_ratio="not captured"
    copy_runtime_artifacts "$work" "$out_dir"
    write_failure_scorecard "$model" "$work" "$out_dir" "turn1" "$compacted" "$cache_ratio" "$out_dir/turn1.txt"
    echo "FAIL ${model}: turn1 failed" >&2
    failed=$((failed + 1))
    continue
  fi
  if ! run_eval_turn "$work" "$turn2_prompt" "$out_dir/turn2.txt"; then
    compacted="no"
    cache_ratio="not captured"
    copy_runtime_artifacts "$work" "$out_dir"
    write_failure_scorecard "$model" "$work" "$out_dir" "turn2" "$compacted" "$cache_ratio" "$out_dir/turn2.txt"
    echo "FAIL ${model}: turn2 failed" >&2
    failed=$((failed + 1))
    continue
  fi
  if ! run_eval_turn "$work" "$turn3_prompt" "$out_dir/turn3.txt"; then
    compacted="no"
    while IFS= read -r conversation; do
      if grep -q '"kind":"compact"' "$conversation"; then
        compacted="yes"
        break
      fi
    done < <(find "$work/.juex/sessions" -type f -name conversation.jsonl -print 2>/dev/null)
    cache_ratio="not captured"
    events_for_scorecard=$(first_events_file "$work")
    if [ -n "$events_for_scorecard" ]; then
      cache_ratio=$(cache_ratio_from_events "$events_for_scorecard")
    fi
    copy_runtime_artifacts "$work" "$out_dir"
    write_failure_scorecard "$model" "$work" "$out_dir" "turn3" "$compacted" "$cache_ratio" "$out_dir/turn3.txt"
    echo "FAIL ${model}: turn3 failed" >&2
    failed=$((failed + 1))
    continue
  fi

  score=$(score_answer "$out_dir/turn3.txt")
  compacted="no"
  while IFS= read -r conversation; do
    if grep -q '"kind":"compact"' "$conversation"; then
      compacted="yes"
      break
    fi
  done < <(find "$work/.juex/sessions" -type f -name conversation.jsonl -print 2>/dev/null)
  cache_ratio="not captured"
  events_for_scorecard=$(first_events_file "$work")
  if [ -n "$events_for_scorecard" ]; then
    cache_ratio=$(cache_ratio_from_events "$events_for_scorecard")
  fi

  {
    echo "# Compaction Eval Scorecard"
    echo
    echo "- Model: \`${model}\`"
    if [ "$KEEP_WORKDIR" = "1" ]; then
      echo "- Work dir: \`${work}\`"
    else
      echo "- Work dir: cleaned after artifact copy; set \`KEEP_WORKDIR=1\` to keep it"
    fi
    echo "- Context window: ${PROVIDER_CONTEXT_WINDOW}"
    echo "- Turn timeout: ${EVAL_TURN_TIMEOUT}s"
    echo "- Score: ${score}/52"
    echo "- Compacted: ${compacted}"
    echo "- Cache ratio: ${cache_ratio}"
  } > "$out_dir/scorecard.md"

  if [ "$compacted" != "yes" ]; then
    echo "FAIL ${model}: compaction did not run" >&2
    failed=$((failed + 1))
  fi
  if [ "$score" -lt 36 ]; then
    echo "FAIL ${model}: score ${score}/52 is below the regression threshold" >&2
    failed=$((failed + 1))
  fi

  copy_runtime_artifacts "$work" "$out_dir"

  echo "==> $model score ${score}/52, compacted=${compacted}"
done

echo "Reports written to ${OUT_ROOT}"
if [ "$failed" -ne 0 ]; then
  exit 1
fi
if [ -n "$rotated_model" ]; then
  run_py scripts/live_model_rotation.py --model-list "$MODEL_LIST_PATH" mark-success --section compaction_eval_models --model "$rotated_model"
fi
