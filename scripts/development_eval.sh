#!/usr/bin/env bash
set -u -o pipefail

usage() {
  cat <<'USAGE'
Usage: development_eval.sh [options]

Runs the standard post-development validation stack and writes a redacted run
record under docs/reports/development-validation/<run-id>.

Default checks:
  - go test ./tests/e2e -count=1
  - go test ./... -count=1
  - make build
  - provider_model_smoke.sh against the curated tests/e2e/live-models.yaml set

Options:
  --run-id ID              Stable run id. Default: UTC timestamp.
  --report-dir DIR         Output dir. Default:
                           docs/reports/development-validation/<run-id>.
  --provider-only REF      Pass --only to provider_model_smoke.sh.
  --provider-timeout SEC   Per-turn provider smoke timeout. Default: 240.
  --provider-all-models    Run every provider/model from ~/.juex/juex.yaml.
  --no-provider-smoke      Skip live provider/model smoke.
  --skip-tests             Skip deterministic Go tests.
  --compaction-eval        Also run scripts/compaction_eval.sh.
  --compaction-model REF   Add a compaction eval model. Repeatable. If omitted,
                           compaction_eval.sh uses its default model set.
  -h, --help               Show this help.
USAGE
}

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/.." && pwd)"
cd "$repo_root"

run_id="${JUEX_DEVELOPMENT_EVAL_RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)}"
report_dir="${JUEX_DEVELOPMENT_EVAL_REPORT_DIR:-}"
provider_only="${JUEX_PROVIDER_SMOKE_ONLY:-}"
provider_timeout="${JUEX_PROVIDER_SMOKE_TIMEOUT:-240}"
run_provider_smoke=1
provider_all_models=0
run_tests=1
run_compaction_eval=0
compaction_models=()

while [[ $# -gt 0 ]]; do
  case "$1" in
    --run-id)
      run_id="${2:-}"
      shift 2
      ;;
    --report-dir)
      report_dir="${2:-}"
      shift 2
      ;;
    --provider-only)
      provider_only="${2:-}"
      shift 2
      ;;
    --provider-timeout)
      provider_timeout="${2:-}"
      shift 2
      ;;
    --provider-all-models)
      provider_all_models=1
      shift
      ;;
    --no-provider-smoke)
      run_provider_smoke=0
      shift
      ;;
    --skip-tests)
      run_tests=0
      shift
      ;;
    --compaction-eval)
      run_compaction_eval=1
      shift
      ;;
    --compaction-model)
      compaction_models+=("${2:-}")
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [[ -z "$report_dir" ]]; then
  report_dir="$repo_root/docs/reports/development-validation/$run_id"
fi

mkdir -p "$report_dir/command-logs"
commands_file="$report_dir/commands.jsonl"
: > "$commands_file"

if command -v mise >/dev/null 2>&1; then
  tool_prefix=(mise exec --)
else
  tool_prefix=()
fi

command_string() {
  printf '%q ' "$@"
}

append_command_result() {
  python3 scripts/evalhelper.py append-command \
    --file "$commands_file" \
    --label "$1" \
    --command "$2" \
    --status "$3" \
    --log "$4"
}

run_cmd() {
  local label="$1"
  shift
  local log="$report_dir/command-logs/$label.log"
  local rendered
  rendered="$(command_string "$@")"
  echo "==> $label: $rendered"
  "$@" >"$log" 2>&1
  local status=$?
  append_command_result "$label" "$rendered" "$status" "$log"
  if [[ "$status" -ne 0 ]]; then
    echo "FAIL $label (exit $status), log: $log" >&2
    tail -n 40 "$log" >&2 || true
  else
    echo "ok  $label"
  fi
  return "$status"
}

overall=0

if [[ "$run_tests" == 1 ]]; then
  run_cmd go-test-e2e "${tool_prefix[@]}" go test ./tests/e2e -count=1 || overall=1
  run_cmd go-test-all "${tool_prefix[@]}" go test ./... -count=1 || overall=1
fi

run_cmd make-build "${tool_prefix[@]}" make build || overall=1

provider_report_dir="$report_dir/provider-model-smoke"
if [[ "$run_provider_smoke" == 1 ]]; then
  provider_args=(bash scripts/provider_model_smoke.sh --juex ./dist/juex --report-dir "$provider_report_dir" --run-id "$run_id" --timeout "$provider_timeout")
  if [[ -n "$provider_only" ]]; then
    provider_args+=(--only "$provider_only")
  fi
  if [[ "$provider_all_models" == 1 ]]; then
    provider_args+=(--all-models)
  fi
  run_cmd provider-model-smoke "${provider_args[@]}" || overall=1
fi

compaction_report_dir=""
if [[ "$run_compaction_eval" == 1 ]]; then
  compaction_report_dir="$report_dir/compaction-eval"
  compaction_args=(env "OUT_ROOT=$compaction_report_dir" "RUN_ID=$run_id" "JUEX_BIN=./dist/juex" bash scripts/compaction_eval.sh)
  if [[ "${#compaction_models[@]}" -gt 0 ]]; then
    compaction_args+=("${compaction_models[@]}")
  fi
  run_cmd compaction-eval "${compaction_args[@]}" || overall=1
fi

record_json="$report_dir/record.json"
record_md="$report_dir/record.md"

python3 scripts/evalhelper.py write-development-record \
  --report-dir "$report_dir" \
  --run-id "$run_id" \
  --commands-file "$commands_file" \
  --provider-summary "$provider_report_dir/summary.json" \
  --compaction-dir "$compaction_report_dir" \
  --status "$overall" \
  --record-json "$record_json" \
  --record-md "$record_md"

echo "record: $record_md"
exit "$overall"
