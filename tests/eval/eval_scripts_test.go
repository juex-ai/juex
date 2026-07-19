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

func TestEvalHelpersResolveAgentHomeSessions(t *testing.T) {
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
		"from tests.eval.juex_eval import compaction, helper",
		"with tempfile.TemporaryDirectory() as tmp:",
		"    work = Path(tmp) / 'work'",
		"    juex_home = work / 'home' / '.juex'",
		"    marker = work / '.juex' / 'juex.local.json'",
		"    marker.parent.mkdir(parents=True)",
		"    marker.write_text(json.dumps({'agent_id': 'abcdefgh2345672a'}), encoding='utf-8')",
		"    expected = juex_home / 'agents' / 'abcdefgh2345672a' / 'sessions'",
		"    assert helper.agent_sessions_dir(work, juex_home) == expected",
		"    assert compaction.session_root(work) == expected",
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
		"    marker = work / '.juex' / 'juex.local.json'",
		"    marker.parent.mkdir(parents=True)",
		"    marker.write_text(json.dumps({'agent_id': 'abcdefgh2345672a'}), encoding='utf-8')",
		"    session = work / 'home' / '.juex' / 'agents' / 'abcdefgh2345672a' / 'sessions' / 'session-1'",
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

func TestScheduleRoutingEvalContract(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not installed; install via `brew install uv` to enable this smoke")
	}
	root, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}

	program := strings.Join([]string{
		"import copy",
		"import json",
		"import tempfile",
		"from pathlib import Path",
		"from tests.eval.juex_eval import schedule_routing",
		"expect = schedule_routing.ScheduleRoutingExpectation(",
		"    schedule_id='schedule-routing-eval',",
		"    every_seconds=21600,",
		"    content='schedule routing token 6h',",
		"    completion_token='SCHEDULE_ROUTING_PASS token-6h',",
		")",
		"prompt = schedule_routing.build_prompt(expect)",
		"assert 'schedule_create' not in prompt, prompt",
		"assert 'observable_create' not in prompt, prompt",
		"assert 'juex-observables' not in prompt, prompt",
		"assert 'Do not run commands' not in prompt, prompt",
		"assert 'shell polling' in prompt, prompt",
		"assert 'schedule-routing-eval' in prompt and 'schedule routing token 6h' in prompt, prompt",
		"def rows():",
		"    return [",
		"        {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'list-1', 'tool_name': 'observable_list', 'input': {}}]},",
		"        {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'list-1', 'tool_name': 'observable_list', 'content': '{\"observables\": []}'}]},",
		"        {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'create-1', 'tool_name': 'schedule_create', 'input': {'id': expect.schedule_id, 'interval': {'every_seconds': expect.every_seconds}, 'observation': {'content': expect.content}}}]},",
		"        {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'create-1', 'tool_name': 'schedule_create', 'content': '{\"id\": \"schedule-routing-eval\"}'}]},",
		"        {'role': 'assistant', 'blocks': [{'type': 'text', 'text': expect.completion_token}]},",
		"    ]",
		"def config():",
		"    return {'observables': [{'id': expect.schedule_id, 'type': 'schedule', 'schedule_config': {'interval': {'every_seconds': expect.every_seconds}, 'observation': {'content': expect.content}}}]}",
		"def validate(work, conv_rows=None, cfg=None, raw_conversation=None, raw_config=None):",
		"    conversation = work / 'conversation.jsonl'",
		"    observables = work / 'observables.json'",
		"    if raw_conversation is None:",
		"        conversation.write_text('\\n'.join(json.dumps(row) for row in (rows() if conv_rows is None else conv_rows)) + '\\n', encoding='utf-8')",
		"    else:",
		"        conversation.write_text(raw_conversation, encoding='utf-8')",
		"    if raw_config is None:",
		"        observables.write_text(json.dumps(config() if cfg is None else cfg), encoding='utf-8')",
		"    else:",
		"        observables.write_text(raw_config, encoding='utf-8')",
		"    return schedule_routing.validate_contract(conversation, observables, expect)",
		"def reject(report, needle):",
		"    assert not report.passed, report",
		"    assert needle in report.message(), report.message()",
		"with tempfile.TemporaryDirectory() as tmp:",
		"    work = Path(tmp)",
		"    report = validate(work)",
		"    assert report.passed, report.message()",
		"    broken = rows()",
		"    broken[0], broken[2] = broken[2], broken[0]",
		"    reject(validate(work, broken), 'before')",
		"    broken = rows()",
		"    broken[0] = {'role': 'assistant', 'blocks': [broken[0]['blocks'][0], broken[2]['blocks'][0]]}",
		"    del broken[2]",
		"    reject(validate(work, broken), 'same assistant message')",
		"    broken = rows()",
		"    broken[0]['blocks'].insert(0, {'type': 'text', 'text': expect.completion_token})",
		"    del broken[-1]",
		"    reject(validate(work, broken), 'after successful schedule_create')",
		"    for index, label in [(1, 'observable_list'), (3, 'schedule_create')]:",
		"        broken = rows()",
		"        broken[index]['blocks'][0]['is_error'] = True",
		"        reject(validate(work, broken), label)",
		"        broken = rows()",
		"        del broken[index]",
		"        reject(validate(work, broken), label)",
		"    parallel = rows()",
		"    parallel[0]['blocks'].insert(0, {'type': 'tool_use', 'tool_use_id': 'skill-1', 'tool_name': 'skill_load', 'input': {'name': 'juex-observables'}})",
		"    parallel[1]['blocks'].append({'type': 'tool_result', 'tool_use_id': 'skill-1', 'tool_name': 'skill_load', 'content': 'guide unavailable', 'is_error': True})",
		"    report = validate(work, parallel)",
		"    assert report.passed, report.message()",
		"    late = rows()",
		"    late.insert(4, {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'skill-2', 'tool_name': 'skill_load', 'input': {'name': 'juex-observables'}}]})",
		"    late.insert(5, {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'skill-2', 'tool_name': 'skill_load', 'content': '# JueX Observables'}]})",
		"    report = validate(work, late)",
		"    assert report.passed, report.message()",
		"    inspected = rows()",
		"    inspected.insert(2, {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'inspect-1', 'tool_name': 'exec_command', 'input': {'cmd': 'ls .juex'}}]})",
		"    inspected.insert(3, {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'inspect-1', 'tool_name': 'exec_command', 'content': 'observables.json'}]})",
		"    report = validate(work, inspected)",
		"    assert report.passed, report.message()",
		"    interactive_inspection = rows()",
		"    interactive_inspection.insert(2, {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'inspect-stdin-1', 'tool_name': 'write_stdin', 'input': {'session_id': 42, 'chars': 'pwd\\n'}}]})",
		"    interactive_inspection.insert(3, {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'inspect-stdin-1', 'tool_name': 'write_stdin', 'content': '/tmp'}]})",
		"    report = validate(work, interactive_inspection)",
		"    assert report.passed, report.message()",
		"    polling = rows()",
		"    polling.insert(2, {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'poll-1', 'tool_name': 'exec_command', 'input': {'cmd': 'while true; do echo tick; sleep 21600; done &'}}]})",
		"    polling.insert(3, {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'poll-1', 'tool_name': 'exec_command', 'content': 'started'}]})",
		"    reject(validate(work, polling), 'shell scheduling command')",
		"    for command in ('watch echo tick', 'watch -n21600 echo tick', 'watch --interval 21600 echo tick', 'watch --interval=21600 echo tick'):",
		"        watch_polling = rows()",
		"        watch_polling.insert(2, {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'watch-1', 'tool_name': 'exec_command', 'input': {'cmd': command}}]})",
		"        watch_polling.insert(3, {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'watch-1', 'tool_name': 'exec_command', 'content': 'started'}]})",
		"        reject(validate(work, watch_polling), 'shell scheduling command')",
		"    mentioned_watch = rows()",
		"    mentioned_watch.insert(2, {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'mention-watch-1', 'tool_name': 'exec_command', 'input': {'cmd': \"printf '%s\\\\n' 'watch --interval 21600 is forbidden'\"}}]})",
		"    mentioned_watch.insert(3, {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'mention-watch-1', 'tool_name': 'exec_command', 'content': 'watch --interval 21600 is forbidden'}]})",
		"    report = validate(work, mentioned_watch)",
		"    assert report.passed, report.message()",
		"    interactive_polling = rows()",
		"    interactive_polling.insert(2, {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'shell-1', 'tool_name': 'exec_command', 'input': {'cmd': 'bash', 'tty': True}}]})",
		"    interactive_polling.insert(3, {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'shell-1', 'tool_name': 'exec_command', 'content': 'running', 'is_error': False}]})",
		"    interactive_polling.insert(4, {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'poll-stdin-1', 'tool_name': 'write_stdin', 'input': {'session_id': 42, 'chars': 'while true; do echo tick; sleep 21600; done &\\n'}}]})",
		"    interactive_polling.insert(5, {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'poll-stdin-1', 'tool_name': 'write_stdin', 'content': 'started'}]})",
		"    reject(validate(work, interactive_polling), 'shell scheduling command')",
		"    verified = rows()",
		"    verified.insert(4, {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'list-2', 'tool_name': 'observable_list', 'input': {}}]})",
		"    verified.insert(5, {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'list-2', 'tool_name': 'observable_list', 'content': '{\"observables\": [{\"id\": \"schedule-routing-eval\"}]}'}]})",
		"    report = validate(work, verified)",
		"    assert report.passed, report.message()",
		"    retried = rows()",
		"    retried.insert(2, {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'create-bad', 'tool_name': 'schedule_create', 'input': {'id': expect.schedule_id, 'interval': {'every_seconds': expect.every_seconds}, 'catch_up': {'mode': 'skip'}, 'observation': {'content': expect.content}}}]})",
		"    retried.insert(3, {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'create-bad', 'tool_name': 'schedule_create', 'content': 'catch_up.mode is invalid', 'is_error': True}]})",
		"    report = validate(work, retried)",
		"    assert report.passed, report.message()",
		"    for forbidden in sorted(schedule_routing.FORBIDDEN_TOOLS):",
		"        broken = rows()",
		"        broken.insert(2, {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'bad-1', 'tool_name': forbidden, 'input': {}}]})",
		"        reject(validate(work, broken), forbidden)",
		"    broken = rows()",
		"    broken.insert(4, copy.deepcopy(broken[2]))",
		"    broken[4]['blocks'][0]['tool_use_id'] = 'create-2'",
		"    broken.insert(5, {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'create-2', 'tool_name': 'schedule_create', 'content': '{\"id\": \"schedule-routing-eval\"}'}]})",
		"    reject(validate(work, broken), 'exactly one successful schedule_create')",
		"    bad_config = config()",
		"    bad_config['observables'][0]['schedule_config']['interval']['every_seconds'] = 60",
		"    reject(validate(work, cfg=bad_config), 'every_seconds')",
		"    bad_config = config()",
		"    bad_config['observables'][0]['id'] = 'wrong-id'",
		"    reject(validate(work, cfg=bad_config), 'persisted id')",
		"    bad_config = config()",
		"    bad_config['observables'][0]['type'] = 'command'",
		"    reject(validate(work, cfg=bad_config), 'persisted type')",
		"    bad_config = config()",
		"    bad_config['observables'][0]['schedule_config']['observation']['content'] = 'wrong content'",
		"    reject(validate(work, cfg=bad_config), 'observation.content')",
		"    bad_config = config()",
		"    del bad_config['observables'][0]['schedule_config']",
		"    reject(validate(work, cfg=bad_config), 'schedule_config')",
		"    bad_config = config()",
		"    bad_config['observables'][0]['source'] = {'type': 'schedule'}",
		"    reject(validate(work, cfg=bad_config), 'legacy or unknown fields')",
		"    bad_config = config()",
		"    bad_config['observables'][0]['command_config'] = {'command': 'sleep'}",
		"    reject(validate(work, cfg=bad_config), 'legacy or unknown fields')",
		"    bad_config = config()",
		"    bad_config['observables'][0].update({'command': 'sleep', 'args': ['1'], 'observation': {'content': 'old'}})",
		"    reject(validate(work, cfg=bad_config), 'command, observation')",
		"    bad_config = config()",
		"    bad_config['observables'].append(copy.deepcopy(bad_config['observables'][0]))",
		"    reject(validate(work, cfg=bad_config), 'exactly one')",
		"    reject(validate(work, raw_conversation='{bad json\\n'), 'invalid JSON')",
		"    reject(validate(work, raw_config='{bad json\\n'), 'invalid JSON')",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestScheduleRoutingEvalReportingIsAdditive(t *testing.T) {
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
		"from tests.eval.juex_eval import helper",
		"with tempfile.TemporaryDirectory() as tmp:",
		"    work = Path(tmp)",
		"    result = helper.SmokeResult(",
		"        run_id='unit', ref='provider:model', provider_id='provider', model_id='model',",
		"        protocol='openai', reasoning_effort_capability='default', tools_capability='default', thinking_effort='unset',",
		"        status='pass', schedule_routing_status='yes',",
		"    )",
		"    summary = {",
		"        'run_id': 'unit', 'juex': './dist/juex', 'config': 'config.yaml', 'model_list': 'unit', 'work_root': 'cleaned',",
		"        'total': 1, 'passed': 1, 'failed': 0, 'tool_use_recorded': 1, 'exec_command_tool_use_recorded': 1,",
		"        'tty_recorded': 1, 'stdin_recorded': 1, 'filesystem_verified': 1, 'event_delta_recorded': 1,",
		"        'thinking_observed': 0, 'schedule_routing_verified': 1, 'results_jsonl_path': 'results.jsonl',",
		"    }",
		"    summary_json = work / 'summary.json'",
		"    summary_md = work / 'summary.md'",
		"    helper.write_smoke_summary(summary_json, summary_md, summary, [result])",
		"    parsed = json.loads(summary_json.read_text(encoding='utf-8'))",
		"    assert parsed['total'] == 1 and parsed['schedule_routing_verified'] == 1, parsed",
		"    markdown = summary_md.read_text(encoding='utf-8')",
		"    assert 'Schedule routing verified: 1' in markdown, markdown",
		"    assert '| not_observed | yes |  |' in markdown, markdown",
		"    commands = work / 'commands.jsonl'",
		"    commands.write_text(json.dumps({'label': 'provider-model-smoke', 'exit_status': 0, 'log': 'provider.log'}) + '\\n', encoding='utf-8')",
		"    record_json = work / 'record.json'",
		"    record_md = work / 'record.md'",
		"    helper.write_development_record(work, 'unit', commands, summary_json, '', 0, record_json, record_md)",
		"    record = record_md.read_text(encoding='utf-8')",
		"    assert 'Schedule routing verified: 1' in record, record",
	}, "\n")
	runUV(t, root, "python", "-c", program)
}

func TestScheduleRoutingEvalRetriesUseFreshAttempts(t *testing.T) {
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
		"from tests.eval.juex_eval import helper, schedule_routing",
		"expect = schedule_routing.ScheduleRoutingExpectation('schedule-routing-eval', 21600, 'retry token', 'SCHEDULE_ROUTING_PASS retry')",
		"def valid_rows():",
		"    return [",
		"        {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'skill-1', 'tool_name': 'skill_load', 'input': {'name': 'juex-observables'}}]},",
		"        {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'skill-1', 'content': 'loaded'}]},",
		"        {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'list-1', 'tool_name': 'observable_list', 'input': {}}]},",
		"        {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'list-1', 'content': '{}'}]},",
		"        {'role': 'assistant', 'blocks': [{'type': 'tool_use', 'tool_use_id': 'create-1', 'tool_name': 'schedule_create', 'input': {'id': expect.schedule_id, 'interval': {'every_seconds': expect.every_seconds}, 'observation': {'content': expect.content}}}]},",
		"        {'role': 'user', 'blocks': [{'type': 'tool_result', 'tool_use_id': 'create-1', 'content': '{}'}]},",
		"        {'role': 'assistant', 'blocks': [{'type': 'text', 'text': expect.completion_token}]},",
		"    ]",
		"with tempfile.TemporaryDirectory() as tmp:",
		"    work = Path(tmp)",
		"    row = helper.MatrixRow('provider', 'model', 'openai', 'default', 'default', 'unset', 'provider:model')",
		"    ctx = helper.ProviderSmokeContext(row, '/fake/juex', {'providers': []}, work / 'work', work / 'report', 'unit', 5, 1, str(work / 'codex'))",
		"    attempts = []",
		"    def fake_write_config(cfg, provider_id, model_id, output_path):",
		"        output_path.parent.mkdir(parents=True, exist_ok=True)",
		"        output_path.write_text('model: provider:model\\n', encoding='utf-8')",
		"    def fake_run_turn(ctx, case_dir, case_config, label, args):",
		"        attempts.append(case_dir)",
		"        case_dir.mkdir(parents=True, exist_ok=True)",
		"        (case_dir / 'turn1.stdout.json').write_text('{}\\n', encoding='utf-8')",
		"        (case_dir / 'turn1.stderr.log').write_text('timeout\\n', encoding='utf-8')",
		"        if len(attempts) == 1:",
		"            (case_dir / '.juex' / 'observables.json').write_text(json.dumps({'observables': [{'id': 'dirty-attempt-1'}]}), encoding='utf-8')",
		"            return 124",
		"        session_id = 'session-attempt-2'",
		"        (case_dir / 'turn1.stdout.json').write_text(json.dumps({'session_id': session_id, 'blocks': [{'type': 'text', 'text': expect.completion_token}]}) + '\\n', encoding='utf-8')",
		"        agent_id = 'abcdefgh2345672a'",
		"        (case_dir / '.juex' / 'juex.local.json').write_text(json.dumps({'agent_id': agent_id}), encoding='utf-8')",
		"        session = case_dir / 'home' / '.juex' / 'agents' / agent_id / 'sessions' / session_id",
		"        session.mkdir(parents=True)",
		"        (session / 'conversation.jsonl').write_text('\\n'.join(json.dumps(row) for row in valid_rows()) + '\\n', encoding='utf-8')",
		"        (session / 'events.jsonl').write_text(json.dumps({'type': 'session.completed'}) + '\\n', encoding='utf-8')",
		"        observables = {'observables': [{'id': expect.schedule_id, 'type': 'schedule', 'schedule_config': {'interval': {'every_seconds': expect.every_seconds}, 'observation': {'content': expect.content}}}]}",
		"        (case_dir / '.juex' / 'observables.json').write_text(json.dumps(observables), encoding='utf-8')",
		"        return 0",
		"    original_write_config = helper.write_selected_config",
		"    original_run_turn = helper.run_turn",
		"    helper.write_selected_config = fake_write_config",
		"    helper.run_turn = fake_run_turn",
		"    try:",
		"        report, session_id = helper.run_schedule_routing_case(ctx, work / 'report' / 'cases' / 'provider_model', expect)",
		"    finally:",
		"        helper.write_selected_config = original_write_config",
		"        helper.run_turn = original_run_turn",
		"    assert report.passed, report.message()",
		"    assert session_id == 'session-attempt-2', session_id",
		"    assert len(attempts) == 2 and attempts[0] != attempts[1], attempts",
		"    assert attempts[0].name == 'attempt-1' and attempts[1].name == 'attempt-2', attempts",
		"    artifacts = work / 'report' / 'cases' / 'provider_model' / 'schedule-routing'",
		"    assert (artifacts / 'attempt-1' / 'turn1.stderr.log').is_file(), artifacts",
		"    dirty = json.loads((artifacts / 'attempt-1' / 'observables.json').read_text(encoding='utf-8'))",
		"    assert dirty['observables'][0]['id'] == 'dirty-attempt-1', dirty",
		"    assert (artifacts / 'attempt-2' / 'conversation.jsonl').is_file(), artifacts",
		"    assert (artifacts / 'attempt-2' / 'events.jsonl').is_file(), artifacts",
		"    assert (artifacts / 'attempt-2' / 'observables.json').is_file(), artifacts",
		"    assert (artifacts / 'attempt-2' / 'prompt.txt').is_file(), artifacts",
		"    contract = json.loads((artifacts / 'attempt-2' / 'contract.json').read_text(encoding='utf-8'))",
		"    assert contract == {'passed': True, 'issues': []}, contract",
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
