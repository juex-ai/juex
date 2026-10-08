"""Boundary tests for live evidence, secret handling, and candidate identity."""
import contextlib
import copy
import io
import json
import os
import pathlib
import subprocess
import tempfile
import unittest
from unittest.mock import patch

from tests.eval.juex_eval import helper, selection, validation_plan, verification


MODEL = dict(provider='test', name='model', protocol='openai/chat', endpoint='https://example.invalid/v1', api_key='private-test-key', context_window=32768, max_output=2048)


class SelectionTest(unittest.TestCase):
    def test_selection_is_reproducible_and_redacted(self):
        cfg = {'models': [MODEL, dict(MODEL, name='second')]}
        kwargs = dict(kind='provider-smoke', config_path=pathlib.Path('/tmp/models.json'), seed='seed')
        first, evidence = selection.select(cfg, **kwargs)
        second, _ = selection.select(cfg, **kwargs)
        self.assertEqual(first, second)
        self.assertNotIn(MODEL['api_key'], json.dumps(evidence.as_dict()))
        changed = copy.deepcopy(cfg)
        changed['models'][0]['api_key'] = 'rotated'
        self.assertEqual(selection.redacted_config_hash([], cfg), selection.redacted_config_hash([], changed))
        with self.assertRaises(selection.ProviderUnavailable):
            selection.select(cfg, only=['missing:model'], **kwargs)
        with self.assertRaises(selection.ProviderUnavailable):
            selection.select(cfg, required_context_window=65536, **kwargs)

    def test_invalid_model_config_is_rejected(self):
        for cfg in ({}, {'models': [dict(MODEL, context_window=True)]}, {'models': [MODEL, MODEL]}, {'models': [dict(MODEL, endpoint='')]}):
            with self.subTest(cfg=cfg):
                with self.assertRaises(ValueError):
                    selection.validate_config(cfg)

    def test_output_cap_and_reserve_are_independent(self):
        for model in (dict(MODEL, max_output=0, output_reserve=4096), dict(MODEL, output_reserve=8192)):
            self.assertEqual(selection.validate_config({'models': [model]}), {'models': [model]})
        for limits in (dict(max_output=0), dict(max_output=-1, output_reserve=4096), dict(output_reserve=0), dict(output_reserve=1024), dict(output_reserve=32768), dict(output_reserve=True)):
            with self.subTest(limits=limits), self.assertRaises(ValueError):
                selection.validate_config({'models': [dict(MODEL, **limits)]})
        with self.assertRaises(ValueError):
            selection.validate_config({'models': [dict(MODEL, protocol='anthropic/messages', max_output=0, output_reserve=512)]})
        selection.validate_config({'models': [dict(MODEL, protocol='anthropic/messages', max_output=0, output_reserve=4096)]})


class LiveEvidenceTest(unittest.TestCase):
    def run_case(self, stdout, returncode=0):
        with tempfile.TemporaryDirectory() as raw:
            root = pathlib.Path(raw)
            config = root / 'config.json'
            config.write_text(json.dumps({'models': [MODEL]}))
            report = root / 'report'
            args = helper.live_parser().parse_args(['--config', str(config), '--report-dir', str(report)])
            selected_files = []

            def execute(command, **kwargs):
                self.assertIn('-tags=postgres,integration', command)
                model_file = pathlib.Path(kwargs['env']['JUEX_LIVE_MODEL_FILE'])
                selected_files.append(model_file)
                self.assertEqual(model_file.stat().st_mode & 0o777, 0o600)
                self.assertEqual(json.loads(model_file.read_text()), MODEL)
                return subprocess.CompletedProcess(command, returncode, stdout)

            with patch.dict(os.environ, {'JUEX_TEST_POSTGRES_URL': 'postgres://private@test/db'}), patch.object(helper.subprocess, 'run', side_effect=execute), contextlib.redirect_stdout(io.StringIO()):
                code = helper.run_live(args, 'provider-smoke')
            self.assertTrue(selected_files)
            self.assertTrue(all(not path.exists() for path in selected_files))
            log = (report / '1.log').read_text()
            self.assertNotIn(MODEL['api_key'], log)
            summary = json.loads((report / 'summary.json').read_text())
            return code, summary

    def test_zero_exit_without_evidence_fails(self):
        code, summary = self.run_case('PASS no tests to run')
        self.assertEqual(code, 1)
        self.assertTrue(summary['blocks_merge'])

    def test_failure_cannot_reuse_success_marker(self):
        code, _ = self.run_case('MANAGED_LIVE_EVIDENCE tools\nFAIL', 1)
        self.assertEqual(code, 1)

    def test_evidence_and_success_required_secrets_removed(self):
        code, summary = self.run_case('MANAGED_LIVE_EVIDENCE tools ' + MODEL['api_key'])
        self.assertEqual(code, 0)
        self.assertFalse(summary['blocks_merge'])

    def test_missing_configuration_does_not_run(self):
        with tempfile.TemporaryDirectory() as report, patch.dict(os.environ, {}, clear=True), patch.object(helper.subprocess, 'run') as execute, contextlib.redirect_stdout(io.StringIO()):
            args = helper.live_parser().parse_args(['--report-dir', report])
            self.assertEqual(helper.run_live(args, 'integration'), 1)
            execute.assert_not_called()
            self.assertEqual(json.loads((pathlib.Path(report) / 'summary.json').read_text())['outcome'], 'environment_failure')


class CandidateIdentityTest(unittest.TestCase):
    def test_database_identity_is_hashed_and_changes_fingerprint(self):
        first = verification.inherited_test_inputs({'JUEX_TEST_POSTGRES_URL': 'postgres://secret@one/db'}, None)
        second = verification.inherited_test_inputs({'JUEX_TEST_POSTGRES_URL': 'postgres://secret@two/db'}, None)
        self.assertNotEqual(first, second)
        self.assertNotIn('secret', json.dumps(first))

    def test_service_artifact_mutation_changes_identity(self):
        with tempfile.TemporaryDirectory() as raw:
            root = pathlib.Path(raw)
            (root / 'dist').mkdir()
            binary = root / 'dist/juex-runtime'
            binary.write_bytes(b'first')
            first = verification.artifact_fingerprints(root)
            binary.write_bytes(b'second')
            second = verification.artifact_fingerprints(root)
            self.assertNotEqual(first, second)
            self.assertEqual(len(first), 9)
            self.assertEqual(first['dist/juex-management']['status'], 'missing')

    def test_runtime_requires_race_live_and_compaction(self):
        plan = validation_plan.plan_for_changes('final', [validation_plan.ChangedFile('M', 'internal/managedruntime/engine.go')], base_sha='a' * 40, head_sha='b' * 40, dirty=False)
        self.assertIn('race', plan.candidate_flags)
        self.assertTrue({'compaction', 'integration', 'provider-smoke'} <= set(plan.final_flags))

    def test_unsafe_run_id_is_rejected(self):
        with self.assertRaises(ValueError):
            verification.validate_run_id('../replace')


if __name__ == '__main__':
    unittest.main()
