#!/bin/bash
set -u -o pipefail

usage() {
  cat <<'USAGE'
Usage: provider-model-smoke.sh [options]

Runs a live multi-turn smoke test for every provider/model in ~/.juex/juex.yaml.
The script creates one temporary Juex workdir per provider/model and copies only
the selected provider/model config into that workdir. Credentials are not
printed, and the temp root is deleted after success unless --keep is passed.

Options:
  --juex PATH       Juex binary to run. Default: ./dist/juex, then PATH juex.
  --config PATH     Provider matrix config. Default: ~/.juex/juex.yaml.
  --work-root DIR   Directory for smoke workdirs. Default: a temp directory.
  --only REF        Limit to one provider or provider/model ref.
  --timeout SEC     Per-turn timeout in seconds. Default: 240.
  --keep            Keep temp workdirs after success. They contain temp configs
                    with provider credentials.
  -h, --help        Show this help.

Environment:
  JUEX_BIN, JUEX_PROVIDER_CONFIG, JUEX_PROVIDER_SMOKE_ROOT,
  JUEX_PROVIDER_SMOKE_ONLY, JUEX_PROVIDER_SMOKE_TIMEOUT,
  JUEX_PROVIDER_SMOKE_KEEP=1
USAGE
}

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/../../../.." && pwd)"
real_home="$HOME"

juex_bin="${JUEX_BIN:-}"
config_path="${JUEX_PROVIDER_CONFIG:-$HOME/.juex/juex.yaml}"
work_root="${JUEX_PROVIDER_SMOKE_ROOT:-}"
only_ref="${JUEX_PROVIDER_SMOKE_ONLY:-}"
timeout_seconds="${JUEX_PROVIDER_SMOKE_TIMEOUT:-240}"
keep_workdirs="${JUEX_PROVIDER_SMOKE_KEEP:-0}"
codex_home="${CODEX_HOME:-$real_home/.codex}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --juex)
      juex_bin="${2:-}"
      shift 2
      ;;
    --config)
      config_path="${2:-}"
      shift 2
      ;;
    --work-root)
      work_root="${2:-}"
      shift 2
      ;;
    --only)
      only_ref="${2:-}"
      shift 2
      ;;
    --timeout)
      timeout_seconds="${2:-}"
      shift 2
      ;;
    --keep)
      keep_workdirs=1
      shift
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

if [[ -z "$juex_bin" ]]; then
  if [[ -x "$repo_root/dist/juex" ]]; then
    juex_bin="$repo_root/dist/juex"
  elif command -v juex >/dev/null 2>&1; then
    juex_bin="$(command -v juex)"
  else
    echo "juex binary not found; run 'mise exec -- make build' or pass --juex" >&2
    exit 2
  fi
fi

if [[ ! -x "$juex_bin" ]]; then
  echo "juex binary is not executable: $juex_bin" >&2
  exit 2
fi

if ! command -v ruby >/dev/null 2>&1; then
  echo "ruby is required to run this script" >&2
  exit 2
fi

if [[ ! -f "$config_path" ]]; then
  echo "provider config not found: $config_path" >&2
  exit 2
fi

if ! [[ "$timeout_seconds" =~ ^[0-9]+$ ]] || [[ "$timeout_seconds" -le 0 ]]; then
  echo "--timeout must be a positive integer, got: $timeout_seconds" >&2
  exit 2
fi

created_work_root=0
if [[ -z "$work_root" ]]; then
  work_root="$(mktemp -d "${TMPDIR:-/tmp}/juex-provider-smoke.XXXXXX")"
  created_work_root=1
else
  mkdir -p "$work_root"
fi

cleanup() {
  if [[ "$created_work_root" == 1 && "$keep_workdirs" != 1 ]]; then
    rm -rf "$work_root"
  fi
}
trap cleanup EXIT

matrix_file="$work_root/provider-models.tsv"
ruby -ryaml -e '
path = ARGV.fetch(0)
cfg = YAML.load_file(path) || {}
providers = cfg["providers"] || []
providers = providers.values if providers.is_a?(Hash)
providers.each do |provider|
  next unless provider.is_a?(Hash)
  provider_id = provider["id"].to_s.strip
  next if provider_id.empty?
  protocol = provider["protocol"].to_s.strip
  models = provider["models"] || []
  models.each do |model|
    model_id = model.is_a?(Hash) ? model["id"].to_s.strip : model.to_s.strip
    next if model_id.empty?
    puts [provider_id, model_id, protocol].join("\t")
  end
end
' "$config_path" > "$matrix_file"

if [[ ! -s "$matrix_file" ]]; then
  echo "no providers/models found in $config_path" >&2
  exit 2
fi

json_value() {
  ruby -rjson -e '
data = JSON.parse(File.read(ARGV.fetch(0)))
value = data
ARGV.fetch(1).split(".").each { |part| value = value.fetch(part) }
puts value
' "$1" "$2"
}

write_case_config() {
  ruby -ryaml -e '
source_path, provider_id, model_id, output_path = ARGV
cfg = YAML.load_file(source_path) || {}
providers = cfg["providers"] || []
providers = providers.values if providers.is_a?(Hash)
provider = providers.find { |entry| entry.is_a?(Hash) && entry["id"].to_s.strip == provider_id }
abort("provider not found: #{provider_id}") unless provider
provider = Marshal.load(Marshal.dump(provider))
models = provider["models"] || []
selected_model = models.find do |model|
  current = model.is_a?(Hash) ? model["id"].to_s.strip : model.to_s.strip
  current == model_id
end
abort("model not found: #{provider_id}/#{model_id}") unless selected_model
provider["models"] = [selected_model]
File.write(output_path, YAML.dump({
  "model" => "#{provider_id}/#{model_id}",
  "providers" => [provider],
}))
' "$1" "$2" "$3" "$4"
}

run_with_timeout() {
  local seconds="$1"
  shift
  ruby -e '
require "timeout"
seconds = Integer(ARGV.shift)
pid = nil
begin
  Timeout.timeout(seconds) do
    pid = Process.spawn(*ARGV)
    _pid, status = Process.wait2(pid)
    exit(status.exitstatus || 1)
  end
rescue Timeout::Error
  if pid
    begin
      Process.kill("TERM", pid)
    rescue Errno::ESRCH
    end
    begin
      Timeout.timeout(2) { Process.wait(pid) }
    rescue StandardError
      begin
        Process.kill("KILL", pid)
      rescue Errno::ESRCH
      end
    end
  end
  exit 124
end
' "$seconds" "$@"
}

run_turn() {
  local case_dir="$1"
  local case_config="$2"
  local label="$3"
  shift 3
  local stdout_file="$case_dir/$label.stdout.json"
  local stderr_file="$case_dir/$label.stderr.log"
  local case_home="$case_dir/home"
  mkdir -p "$case_home/.agents" "$case_home/.juex"

  run_with_timeout "$timeout_seconds" env "HOME=$case_home" "CODEX_HOME=$codex_home" "$juex_bin" -C "$case_dir" --config "$case_config" run --json "$@" >"$stdout_file" 2>"$stderr_file"
}

print_failure_tail() {
  local case_dir="$1"
  for file in "$case_dir"/*.stderr.log "$case_dir"/*.stdout.json; do
    [[ -f "$file" ]] || continue
    echo "--- ${file#$case_dir/} ---" >&2
    tail -n 20 "$file" >&2
  done
}

total=0
failed=0

echo "juex: $juex_bin"
echo "config: $config_path"
echo "work root: $work_root"
if [[ -n "$only_ref" ]]; then
  echo "filter: $only_ref"
fi

while IFS=$'\t' read -r provider_id model_id protocol; do
  ref="$provider_id/$model_id"
  if [[ -n "$only_ref" && "$only_ref" != "$provider_id" && "$only_ref" != "$ref" ]]; then
    continue
  fi

  total=$((total + 1))
  safe_ref="$(printf '%s' "$ref" | tr -c 'A-Za-z0-9._-' '_')"
  case_dir="$work_root/$safe_ref"
  rm -rf "$case_dir"
  mkdir -p "$case_dir/.juex"

  token="juex-smoke-${safe_ref}-$(date +%s)-$RANDOM"
  case_config="$case_dir/provider.juex.yaml"
  smoke_file="$case_dir/smoke.txt"
  write_case_config "$config_path" "$provider_id" "$model_id" "$case_config"
  printf 'provider_model_smoke_token=%s\n' "$token" > "$smoke_file"

  echo "==> $ref${protocol:+ [$protocol]}"

  if ! run_turn "$case_dir" "$case_config" "turn1" --new "This is provider/model smoke turn 1. Reply with exactly: READY $token"; then
    echo "FAIL $ref: turn1 failed" >&2
    print_failure_tail "$case_dir"
    failed=$((failed + 1))
    continue
  fi

  session_id="$(json_value "$case_dir/turn1.stdout.json" session_id 2>/dev/null || true)"
  if [[ -z "$session_id" ]]; then
    echo "FAIL $ref: turn1 did not return session_id" >&2
    print_failure_tail "$case_dir"
    failed=$((failed + 1))
    continue
  fi

  if ! run_turn "$case_dir" "$case_config" "turn2" --session "$session_id" "Use the read tool to read this exact file path: $smoke_file. Then answer with exactly the token value from the file, with no extra prose."; then
    echo "FAIL $ref: turn2 tool-call prompt failed" >&2
    print_failure_tail "$case_dir"
    failed=$((failed + 1))
    continue
  fi

  if ! grep -q "$token" "$case_dir/turn2.stdout.json"; then
    echo "FAIL $ref: turn2 response did not include the smoke token" >&2
    print_failure_tail "$case_dir"
    failed=$((failed + 1))
    continue
  fi

  if ! run_turn "$case_dir" "$case_config" "turn3" --session "$session_id" "Reason briefly if your API exposes thinking. What is 17 + 25? Include only '42' and the smoke token $token in the final answer."; then
    echo "FAIL $ref: turn3 reasoning prompt failed" >&2
    print_failure_tail "$case_dir"
    failed=$((failed + 1))
    continue
  fi

  conversation="$case_dir/.juex/sessions/$session_id/conversation.jsonl"
  if [[ ! -f "$conversation" ]]; then
    echo "FAIL $ref: missing conversation log for session $session_id" >&2
    print_failure_tail "$case_dir"
    failed=$((failed + 1))
    continue
  fi

  if grep -q '"type":"tool_use"' "$conversation"; then
    tool_status="toolcall=yes"
  else
    echo "FAIL $ref: conversation did not record a tool_use block" >&2
    print_failure_tail "$case_dir"
    failed=$((failed + 1))
    continue
  fi

  if grep -q '"type":"reasoning"' "$conversation"; then
    thinking_status="thinking=observed"
  else
    thinking_status="thinking=not_exposed"
  fi

  echo "ok  $ref session=$session_id $tool_status $thinking_status logs=$case_dir"
done < "$matrix_file"

if [[ "$total" -eq 0 ]]; then
  echo "no providers/models matched filter: $only_ref" >&2
  exit 2
fi

echo "summary: total=$total failed=$failed"
if [[ "$failed" -ne 0 ]]; then
  exit 1
fi
