#!/usr/bin/env bash
# Run a small live compaction quality smoke against one or more configured
# Juex model refs. The script intentionally uses a temporary work directory per
# model and copies only non-secret runtime artifacts into docs/reports.

set -euo pipefail

cd "$(dirname "$0")/.."

JUEX_BIN=${JUEX_BIN:-"./dist/juex"}
if [ ! -x "$JUEX_BIN" ]; then
  echo "Missing $JUEX_BIN. Run: mise exec -- make build" >&2
  exit 1
fi
PROVIDER_CONTEXT_WINDOW=${PROVIDER_CONTEXT_WINDOW:-32000}
KEEP_WORKDIR=${KEEP_WORKDIR:-0}
TMP_WORKDIRS=()

cleanup() {
  if [ "$KEEP_WORKDIR" = "1" ]; then
    return
  fi
  for dir in "${TMP_WORKDIRS[@]}"; do
    rm -rf "$dir"
  done
}
trap cleanup EXIT

if [ "$#" -gt 0 ]; then
  MODELS=("$@")
else
  MODELS=(
    "openai-codex/gpt-5.5"
    "ark/deepseek-v4-pro"
    "clip-local/gpt-5.5"
  )
fi

RUN_ID=${RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)}
OUT_ROOT=${OUT_ROOT:-"docs/reports/compaction-eval/${RUN_ID}"}
mkdir -p "$OUT_ROOT"

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

for model in "${MODELS[@]}"; do
  safe_model=${model//\//__}
  provider_id=${model%%/*}
  model_id=${model#*/}
  work=$(mktemp -d "${TMPDIR:-/tmp}/juex-compaction-eval.${safe_model}.XXXXXX")
  TMP_WORKDIRS+=("$work")
  out_dir="${OUT_ROOT}/${safe_model}"
  mkdir -p "$work/.juex" "$out_dir"

  cat > "$work/.juex/juex.yaml" <<EOF
model: ${model}
providers:
  - id: ${provider_id}
    capabilities:
      tools: false
    models:
      - id: ${model_id}
compaction:
  enabled: true
  reserve_tokens: 8000
  keep_recent_tokens: 6000
  tail_turns: 1
  summary_max_tokens: 2048
  tool_result_max_chars: 1200
EOF

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
    noise "turn1" 900
  } > "$turn1_prompt"

  {
    cat <<'EOF'
Continue the same evaluation. Do not use tools. This turn intentionally adds
irrelevant context pressure. Preserve the six GF facts from the previous turn
in conversation context only. Answer only: TURN2 STORED.

Irrelevant context begins below.
EOF
    noise "turn2" 700
  } > "$turn2_prompt"

  cat > "$turn3_prompt" <<'EOF'
No tools. Answer the evaluation questions using only this session's context.

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
  PROVIDER_CONTEXT_WINDOW="$PROVIDER_CONTEXT_WINDOW" "$JUEX_BIN" -C "$work" run "$(cat "$turn1_prompt")" | tee "$out_dir/turn1.txt"
  PROVIDER_CONTEXT_WINDOW="$PROVIDER_CONTEXT_WINDOW" "$JUEX_BIN" -C "$work" run "$(cat "$turn2_prompt")" | tee "$out_dir/turn2.txt"
  PROVIDER_CONTEXT_WINDOW="$PROVIDER_CONTEXT_WINDOW" "$JUEX_BIN" -C "$work" run "$(cat "$turn3_prompt")" | tee "$out_dir/turn3.txt"

  score=$(score_answer "$out_dir/turn3.txt")
  compacted="no"
  while IFS= read -r conversation; do
    if grep -q '"kind":"compact"' "$conversation"; then
      compacted="yes"
      break
    fi
  done < <(find "$work/.juex/sessions" -type f -name conversation.jsonl -print 2>/dev/null)

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
    echo "- Score: ${score}/52"
    echo "- Compacted: ${compacted}"
    echo "- Cache ratio: not captured"
  } > "$out_dir/scorecard.md"

  while IFS= read -r conversation; do
    cp "$conversation" "$out_dir/conversation.jsonl"
  done < <(find "$work/.juex/sessions" -type f -name conversation.jsonl -print 2>/dev/null)
  while IFS= read -r events; do
    cp "$events" "$out_dir/events.jsonl"
  done < <(find "$work/.juex/sessions" -type f -name events.jsonl -print 2>/dev/null)

  echo "==> $model score ${score}/52, compacted=${compacted}"
done

echo "Reports written to ${OUT_ROOT}"
