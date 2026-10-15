import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

from scripts.ci_changes import docs_only


class CIChangesTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.git("init", "-q")
        self.git("config", "user.name", "CI Test")
        self.git("config", "user.email", "ci@example.invalid")
        self.git("config", "commit.gpgsign", "false")
        self.write("README.md", "initial\n")
        self.write("main.go", "package main\n")
        self.base = self.commit()

    def git(self, *args):
        return subprocess.check_output(["git", *args], cwd=self.root).decode().strip()

    def write(self, path, content):
        target = self.root / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(content)

    def commit(self):
        self.git("add", "--all")
        self.git("commit", "-qm", "test change")
        return self.git("rev-parse", "HEAD")

    def event(self, head, base=None, kind="pull_request"):
        base = self.base if base is None else base
        if kind == "pull_request":
            return {"pull_request": {"base": {"sha": base}, "head": {"sha": head}}}
        return {"before": base, "after": head}

    def test_docs_add_modify_delete_and_unusual_names(self):
        for path in ["README.md", "docs/中文 guide.md", "docs/line\nbreak.md"]:
            self.write(path, "updated\n")
        head = self.commit()
        self.assertTrue(docs_only("pull_request", self.event(head), self.root))
        (self.root / "README.md").unlink()
        head = self.commit()
        self.assertTrue(docs_only("push", self.event(head, kind="push"), self.root))

    def test_non_document_inputs(self):
        for path in [
            "main.go", "frontend/src/app.tsx", "go.mod", "Makefile",
            ".github/workflows/ci.yml", "scripts/check.py",
            "docs/bilingual-whitelist.txt",
            "internal/features/skills/builtin/example/SKILL.md",
            "internal/features/skills/builtin/example/SKILL.zh.md",
            "internal/entrypoints/webassets/dist/example.md",
        ]:
            with self.subTest(path=path):
                before = self.git("rev-parse", "HEAD")
                self.write(path, "changed\n")
                head = self.commit()
                self.assertFalse(docs_only("push", self.event(head, before, "push"), self.root))

    def test_whole_pr_and_multi_commit_push_include_earlier_code_change(self):
        self.write("main.go", "package example\n")
        self.commit()
        self.write("README.md", "docs only in latest commit\n")
        head = self.commit()
        for kind in ["push", "pull_request"]:
            with self.subTest(kind=kind):
                self.assertFalse(docs_only(kind, self.event(head, kind=kind), self.root))

    def test_pr_ignores_unrelated_changes_on_base_branch(self):
        self.write("README.md", "branch docs\n")
        head = self.commit()
        self.git("checkout", "-q", "--detach", self.base)
        self.write("main.go", "package base_update\n")
        updated_base = self.commit()
        self.assertTrue(docs_only("pull_request", self.event(head, updated_base), self.root))

    def test_code_deletion_and_rename_to_markdown_run_full_ci(self):
        # A rename must check both the deleted source and the added destination.
        (self.root / "main.go").rename(self.root / "code.md")
        head = self.commit()
        self.assertFalse(docs_only("pull_request", self.event(head), self.root))
        (self.root / "code.md").unlink()
        head = self.commit()
        self.assertFalse(docs_only("pull_request", self.event(head), self.root))

    def test_document_rename_stays_docs_only(self):
        (self.root / "README.md").rename(self.root / "guide.md")
        self.assertTrue(docs_only("pull_request", self.event(self.commit()), self.root))

    def test_uncertain_or_empty_diff_runs_full_ci(self):
        cases = [
            ("pull_request", self.event(self.base)),
            ("push", self.event(self.base, "0" * 40, "push")),
            ("push", self.event(self.base, "1" * 40, "push")),
            ("push", self.event(self.base, "--help", "push")),
            ("pull_request", {}),
            ("workflow_dispatch", {}),
        ]
        for kind, event in cases:
            with self.subTest(kind=kind, event=event):
                self.assertFalse(docs_only(kind, event, self.root))

    def test_cli_writes_github_output_and_handles_invalid_event(self):
        self.write("README.md", "changed\n")
        event = self.event(self.commit())
        event_path = self.root / "event.json"
        output = self.root / "output"
        env = dict(os.environ, GITHUB_EVENT_NAME="pull_request",
                   GITHUB_EVENT_PATH=str(event_path), GITHUB_OUTPUT=str(output))
        script = Path(__file__).with_name("ci_changes.py")
        for content, expected in [(json.dumps(event), "true"), ("invalid json", "false")]:
            with self.subTest(content=content):
                event_path.write_text(content)
                output.write_text("")
                subprocess.run([sys.executable, str(script)], cwd=self.root,
                               env=env, check=True, capture_output=True)
                self.assertEqual(output.read_text(), f"docs_only={expected}\n")


if __name__ == "__main__":
    unittest.main()
