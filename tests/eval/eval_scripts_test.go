package eval

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLiveModelRotationScript(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	work := t.TempDir()
	modelList := filepath.Join(work, "live-models.yaml")
	state := filepath.Join(work, "rotation.json")
	if err := os.WriteFile(modelList, []byte(strings.Join([]string{
		"provider_smoke_models:",
		"  - provider:a",
		"  - provider:b",
		"  - provider:c",
		"compaction_eval_models:",
		"  - compaction:a",
		"  - compaction:b",
		"",
	}, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := runRotation(t, root, modelList, state, "select", "--section", "provider_smoke_models"); got != "provider:a" {
		t.Fatalf("initial provider selection = %q, want provider:a", got)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatalf("select should not create state file, stat err=%v", err)
	}

	runRotation(t, root, modelList, state, "mark-success", "--section", "provider_smoke_models", "--model", "provider:a")
	if got := runRotation(t, root, modelList, state, "select", "--section", "provider_smoke_models"); got != "provider:b" {
		t.Fatalf("rotated provider selection = %q, want provider:b", got)
	}

	runRotation(t, root, modelList, state, "mark-success", "--section", "compaction_eval_models", "--model", "compaction:a")
	if got := runRotation(t, root, modelList, state, "select", "--section", "compaction_eval_models"); got != "compaction:b" {
		t.Fatalf("rotated compaction selection = %q, want compaction:b", got)
	}

	raw, err := os.ReadFile(state)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Sections map[string]struct {
			LastSuccessful string `json:"last_successful"`
		} `json:"sections"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	if got := parsed.Sections["provider_smoke_models"].LastSuccessful; got != "provider:a" {
		t.Fatalf("provider last_successful = %q, want provider:a", got)
	}
	if got := parsed.Sections["compaction_eval_models"].LastSuccessful; got != "compaction:a" {
		t.Fatalf("compaction last_successful = %q, want compaction:a", got)
	}
}

func TestEvalPythonModuleAndShellWrappersExposeHelp(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	moduleHelp := runUV(t, root, "python", "-m", "tests.eval.juex_eval", "--help")
	for _, want := range []string{"development", "provider-smoke", "compaction", "rotation"} {
		assertHelpContains(t, moduleHelp, want)
	}

	providerHelp := runUV(t, root, "python", "-m", "tests.eval.juex_eval", "provider-smoke", "--help")
	assertHelpContains(t, providerHelp, "--only", "--report-dir")

	compactionHelp := runUV(t, root, "python", "-m", "tests.eval.juex_eval", "compaction", "--help")
	assertHelpContains(t, compactionHelp, "--only", "--report-dir")

	developmentHelp := runUV(t, root, "python", "-m", "tests.eval.juex_eval", "development", "--help")
	assertHelpContains(t, developmentHelp, "--only", "--compaction-only", "--report-dir")

	for _, script := range []string{
		"tests/eval/development_eval.sh",
		"tests/eval/provider_model_smoke.sh",
		"tests/eval/compaction_eval.sh",
	} {
		t.Run(script, func(t *testing.T) {
			if _, err := exec.LookPath("bash"); err != nil {
				t.Skip("bash not found; skipping shell wrapper test")
			}
			cmd := exec.Command("bash", filepath.Join(root, script), "--help")
			cmd.Dir = t.TempDir()
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%s --help failed: %v\n%s", script, err, out)
			}
			if !strings.Contains(strings.ToLower(string(out)), "usage:") {
				t.Fatalf("%s --help missing Usage:\n%s", script, out)
			}
		})
	}
}

func TestEvalDevelopmentStepBuilderUsesConsistentFlags(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"import json",
		"from argparse import Namespace",
		"from pathlib import Path",
		"from tests.eval.juex_eval import cli",
		"args = Namespace(",
		"    skip_tests=True,",
		"    no_provider_smoke=False,",
		"    compaction_eval=True,",
		"    run_id='unit',",
		"    provider_timeout=7,",
		"    provider_only='ark:model',",
		"    provider_all_models=False,",
		"    provider_all_config_models=False,",
		"    compaction_all_models=False,",
		"    compaction_only=['openai:model', 'ark:other'],",
		")",
		"steps, _, _ = cli.development_steps(args, Path('reports'))",
		"print(json.dumps([{'label': label, 'command': command} for label, command in steps]))",
	}, "\n")
	out := runUV(t, root, "python", "-c", program)

	var steps []struct {
		Label   string   `json:"label"`
		Command []string `json:"command"`
	}
	if err := json.Unmarshal([]byte(out), &steps); err != nil {
		t.Fatalf("decode steps: %v\n%s", err, out)
	}

	providerCmd := findEvalCommand(t, steps, "provider-model-smoke")
	assertCommandFlagValue(t, providerCmd, "--only", "ark:model")
	assertCommandHasFlag(t, providerCmd, "--report-dir")
	assertCommandLacks(t, providerCmd, "--provider-only")

	compactionCmd := findEvalCommand(t, steps, "compaction-eval")
	assertCommandFlagValue(t, compactionCmd, "--only", "openai:model")
	assertCommandFlagValue(t, compactionCmd, "--only", "ark:other")
	assertCommandHasFlag(t, compactionCmd, "--report-dir")
	assertCommandLacks(t, compactionCmd, "--out-root")
}

func TestEvalHelpersTolerateProgrammaticNone(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"from argparse import Namespace",
		"from tests.eval.juex_eval import cli, compaction",
		"command = []",
		"cli.append_repeated(command, '--only', None)",
		"assert command == []",
		"args = Namespace(",
		"    only=None,",
		"    models=None,",
		"    all_models=False,",
		"    juex='/no/such/juex',",
		"    config='/no/such/config',",
		"    model_list='/no/such/models.yaml',",
		"    rotation_state='/no/such/rotation.json',",
		"    out_root='',",
		"    keep_workdir=False,",
		")",
		"try:",
		"    compaction.run(args)",
		"except ValueError as exc:",
		"    assert 'Missing executable' in str(exc)",
		"else:",
		"    raise AssertionError('expected missing executable error')",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestEvalDefaultReportDirsUseTmpRoot(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"from tests.eval.juex_eval import helper",
		"for kind in ['provider-model-smoke', 'development-validation', 'compaction-eval']:",
		"    path = helper.default_report_dir(kind, 'run-id').as_posix()",
		"    assert path.endswith(f'/.tmp/reports/{kind}/run-id'), path",
		"    assert '/docs/reports/' not in path, path",
		"for bad_run_id in ['', ' ', '../run', 'nested/run', r'nested\\run']:",
		"    try:",
		"        helper.default_report_dir('provider-model-smoke', bad_run_id)",
		"    except ValueError:",
		"        pass",
		"    else:",
		"        raise AssertionError(f'expected invalid run_id: {bad_run_id!r}')",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestCompactionEvalScoresAuthoritativeGoalAndNotes(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"import json",
		"import tempfile",
		"from pathlib import Path",
		"from tests.eval.juex_eval import compaction",
		"with tempfile.TemporaryDirectory() as tmp:",
		"    work = Path(tmp)",
		"    session = work / '.juex' / 'sessions' / 'session-1'",
		"    session.mkdir(parents=True)",
		"    (session / 'goal_state.json').write_text(json.dumps(compaction.AUTHORITATIVE_GOAL), encoding='utf-8')",
		"    (session / 'notes.md').write_text(compaction.AUTHORITATIVE_NOTES, encoding='utf-8')",
		"    goal = compaction.AUTHORITATIVE_GOAL",
		"    summary = '\\n'.join([",
		"        'Goal',",
		"        f\"description: {goal['description']}\",",
		"        f\"acceptance: {goal['acceptance']}\",",
		"        f\"status: {goal['status']}\",",
		"        'Critical Context', 'facts', 'Constraints & Preferences', 'none',",
		"        'Progress', 'mapped', 'Key Decisions', 'preserve state', 'Next Steps',",
		"        compaction.AUTHORITATIVE_OPEN_NOTE.rstrip('.'), 'Relevant Files', 'notes.md', 'Tool Failures', 'none',",
		"    ])",
		"    message = {'kind': 'compact', 'blocks': [{'type': 'text', 'text': summary}]}",
		"    (session / 'conversation.jsonl').write_text(json.dumps(message) + '\\n', encoding='utf-8')",
		"    answer = compaction.AUTHORITATIVE_COMPLETED_NOTE.rstrip('.') + '\\n' + compaction.AUTHORITATIVE_OPEN_NOTE.rstrip('.')",
		"    result = compaction.score_authoritative_state(work, answer)",
		"    assert result['score'] == 30, result",
		"    assert all(result['checks'].values()), result",
		"    bad = compaction.score_authoritative_state(work, answer.replace('scorecard', 'report'))",
		"    assert not bad['checks']['notes_recited_after_compaction'], bad",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestEvalWriteSelectedConfigUsesColonModelRef(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"import tempfile",
		"from pathlib import Path",
		"from tests.eval.juex_eval import helper",
		"cfg = {'providers': [{'id': 'openrouter', 'models': [{'id': 'meta-llama/llama-3'}]}]}",
		"with tempfile.TemporaryDirectory() as tmp:",
		"    out = Path(tmp) / 'juex.yaml'",
		"    helper.write_selected_config(cfg, 'openrouter', 'meta-llama/llama-3', out)",
		"    text = out.read_text(encoding='utf-8')",
		"    assert 'model: openrouter:meta-llama/llama-3' in text, text",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestEvalCompactionModelRefParserTrimsWhitespace(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"from tests.eval.juex_eval import compaction",
		"model, provider, model_id = compaction.parse_model_ref('  openrouter : meta-llama/llama-3  ')",
		"assert model == 'openrouter:meta-llama/llama-3', model",
		"assert provider == 'openrouter', provider",
		"assert model_id == 'meta-llama/llama-3', model_id",
		"for bad in ['openrouter/meta', ' : model', 'provider: ']:",
		"    try:",
		"        compaction.parse_model_ref(bad)",
		"    except ValueError:",
		"        pass",
		"    else:",
		"        raise AssertionError(f'expected invalid model ref: {bad!r}')",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestEvalAgentSmokeToolEventContract(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"import json",
		"import tempfile",
		"from pathlib import Path",
		"from tests.eval.juex_eval import contract_oracle, helper",
		"token = 'contract-token'",
		"with tempfile.TemporaryDirectory() as tmp:",
		"    conversation = Path(tmp) / 'conversation.jsonl'",
		"    events = Path(tmp) / 'events.jsonl'",
		"    conv_rows = [",
		"        {'role': 'assistant', 'blocks': [",
		"            {'type': 'tool_use', 'tool_use_id': 'read_1', 'tool_name': 'read'},",
		"            {'type': 'tool_use', 'tool_use_id': 'write_1', 'tool_name': 'write'},",
		"            {'type': 'tool_use', 'tool_use_id': 'edit_1', 'tool_name': 'edit'},",
		"            {'type': 'tool_use', 'tool_use_id': 'grep_1', 'tool_name': 'grep'},",
		"            {'type': 'tool_use', 'tool_use_id': 'call_1', 'tool_name': 'exec_command', 'input': {'tty': True}},",
		"            {'type': 'tool_use', 'tool_use_id': 'call_2', 'tool_name': 'write_stdin'},",
		"        ]},",
		"        {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'call_1', 'content': f'TTY-DONE {token}\\nProcess exited with code 0'}]},",
		"    ]",
		"    conversation.write_text('\\n'.join(json.dumps(row) for row in conv_rows) + '\\n', encoding='utf-8')",
		"    rows = [",
		"        {'type': 'tool.output_delta', 'payload': {'name': 'exec_command', 'tool_use_id': 'call_1', 'session_id': '1', 'chunk_id': 1, 'stream': 'combined', 'text': 'INSTALL 10%\\r', 'truncated': True}},",
		"        {'type': 'tool.output_delta', 'payload': {'name': 'exec_command', 'tool_use_id': 'call_1', 'session_id': '1', 'chunk_id': 2, 'stream': 'combined', 'text': 'PROMPT approve install?'}},",
		"        {'type': 'tool.output_delta', 'payload': {'name': 'exec_command', 'tool_use_id': 'call_1', 'session_id': '1', 'chunk_id': 3, 'stream': 'combined', 'text': f'TTY-DONE {token}'}},",
		"        {'type': 'tool.completed', 'payload': {'name': 'exec_command', 'tool_use_id': 'call_1', 'timeout_seconds': 5, 'len': 10, 'preview': 'prompt', 'result': {'session_id': 3, 'running': True, 'chunk_id': 2, 'output': 'PROMPT approve install?', 'original_bytes': 23, 'original_token_count': 6}}},",
		"        {'type': 'tool.completed', 'payload': {'name': 'write_stdin', 'tool_use_id': 'call_2', 'timeout_seconds': 5, 'len': 2, 'preview': 'ok', 'result': {'running': False, 'exit_code': 0, 'chunk_id': 5, 'output': f'TTY-DONE {token}', 'original_bytes': 18, 'original_token_count': 5}}},",
		"    ]",
		"    events.write_text('\\n'.join(json.dumps(row) for row in rows) + '\\n', encoding='utf-8')",
		"    report = contract_oracle.validate_agent_smoke_contract(conversation, events, token)",
		"    assert report.passed, report.message()",
		"    ok, msg = contract_oracle.events_have_agent_smoke_deltas(events, token)",
		"    assert ok, msg",
		"    broken = Path(tmp) / 'broken-events.jsonl'",
		"    broken_rows = [dict(row) for row in rows]",
		"    broken_rows[2] = {'type': 'tool.output_delta', 'payload': {'name': 'exec_command', 'tool_use_id': 'call_1', 'session_id': '1', 'chunk_id': 3, 'stream': 'combined', 'chunk_text': f'TTY-DONE {token}'}}",
		"    broken.write_text('\\n'.join(json.dumps(row) for row in broken_rows) + '\\n', encoding='utf-8')",
		"    ok, msg = helper.events_have_agent_smoke_deltas(broken, token)",
		"    assert not ok and 'TTY-DONE token' in msg, msg",
		"    broken_rows = [dict(row) for row in rows]",
		"    broken_rows[-1] = {'type': 'tool.completed', 'payload': {'name': 'write_stdin', 'tool_use_id': 'call_2'}}",
		"    broken.write_text('\\n'.join(json.dumps(row) for row in broken_rows) + '\\n', encoding='utf-8')",
		"    ok, msg = helper.events_have_agent_smoke_deltas(broken, token)",
		"    assert not ok and 'structured write_stdin result' in msg, msg",
		"    broken_rows = [dict(row) for row in rows]",
		"    broken_rows[-2] = {'type': 'tool.completed', 'payload': {'name': 'exec_command', 'tool_use_id': 'call_1', 'result': {'running': True, 'session_id': True}}}",
		"    broken.write_text('\\n'.join(json.dumps(row) for row in broken_rows) + '\\n', encoding='utf-8')",
		"    ok, msg = contract_oracle.events_have_agent_smoke_deltas(broken, token)",
		"    assert not ok and 'structured exec_command running result' in msg, msg",
		"    broken_rows = [dict(row) for row in rows]",
		"    broken_rows[-1] = {'type': 'tool.completed', 'payload': {'name': 'write_stdin', 'tool_use_id': 'call_2', 'result': {'running': False, 'exit_code': False, 'output': f'TTY-DONE {token}'}}}",
		"    broken.write_text('\\n'.join(json.dumps(row) for row in broken_rows) + '\\n', encoding='utf-8')",
		"    ok, msg = contract_oracle.events_have_agent_smoke_deltas(broken, token)",
		"    assert not ok and 'structured write_stdin result' in msg, msg",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func runRotation(t *testing.T, root, modelList, state string, args ...string) string {
	t.Helper()
	baseArgs := []string{
		"run",
		"--quiet",
		"--project",
		root,
		"python",
		"-m",
		"tests.eval.juex_eval",
		"rotation",
		"--model-list",
		modelList,
		"--state",
		state,
	}
	cmd := exec.Command("uv", append(baseArgs, args...)...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("rotation command failed: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func runUV(t *testing.T, root string, args ...string) string {
	t.Helper()
	baseArgs := []string{"run", "--quiet", "--project", root}
	cmd := exec.Command("uv", append(baseArgs, args...)...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("uv command failed: %v\n%s", err, out)
	}
	return string(out)
}

func assertHelpContains(t *testing.T, help string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(help, want) {
			t.Fatalf("help missing %q:\n%s", want, help)
		}
	}
}

func findEvalCommand(t *testing.T, steps []struct {
	Label   string   `json:"label"`
	Command []string `json:"command"`
}, label string) []string {
	t.Helper()
	for _, step := range steps {
		if step.Label == label {
			return step.Command
		}
	}
	t.Fatalf("missing step %q: %#v", label, steps)
	return nil
}

func assertCommandFlagValue(t *testing.T, command []string, flag, value string) {
	t.Helper()
	for index, part := range command {
		if part == flag && index+1 < len(command) && command[index+1] == value {
			return
		}
	}
	t.Fatalf("command missing %s %s: %#v", flag, value, command)
}

func assertCommandHasFlag(t *testing.T, command []string, flag string) {
	t.Helper()
	for _, part := range command {
		if part == flag {
			return
		}
	}
	t.Fatalf("command missing %s: %#v", flag, command)
}

func assertCommandLacks(t *testing.T, command []string, forbidden string) {
	t.Helper()
	for _, part := range command {
		if part == forbidden {
			t.Fatalf("command should not contain %s: %#v", forbidden, command)
		}
	}
}
