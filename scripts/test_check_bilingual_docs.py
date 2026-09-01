from __future__ import annotations

import subprocess
import tempfile
import unittest
from pathlib import Path

from scripts import check_bilingual_docs


class RepositoryFixture:
    def __init__(self) -> None:
        self._temp = tempfile.TemporaryDirectory()
        self.root = Path(self._temp.name)
        subprocess.run(["git", "init", "--quiet"], cwd=self.root, check=True)

    def close(self) -> None:
        self._temp.cleanup()

    def write(self, relative_path: str, content: str) -> None:
        destination = self.root / relative_path
        destination.parent.mkdir(parents=True, exist_ok=True)
        destination.write_text(content, encoding="utf-8")

    def track_all(self) -> None:
        subprocess.run(["git", "add", "."], cwd=self.root, check=True)


class CheckBilingualDocsTest(unittest.TestCase):
    def setUp(self) -> None:
        self.repo = RepositoryFixture()

    def tearDown(self) -> None:
        self.repo.close()

    def write_whitelist(self, content: str = "") -> None:
        self.repo.write("docs/bilingual-whitelist.txt", content)

    def check(self) -> list[str]:
        self.repo.track_all()
        return check_bilingual_docs.check_repository(self.repo.root)

    def test_accepts_reciprocal_pair_and_exact_whitelist_entry(self) -> None:
        self.write_whitelist("# Machine-owned guidance\nAGENTS.md\n")
        self.repo.write("AGENTS.md", "# Agents\n")
        self.repo.write("README.md", "# Project\n\n> English | [中文](README.zh.md)\n")
        self.repo.write("README.zh.md", "# 项目\n\n> [English](README.md) | 中文\n")

        self.assertEqual(self.check(), [])

    def test_reports_missing_language_peer(self) -> None:
        self.write_whitelist()
        self.repo.write("README.md", "# Project\n")

        errors = self.check()

        self.assertIn("missing Chinese peer: README.zh.md (for README.md)", errors)

    def test_reports_missing_reciprocal_top_link(self) -> None:
        self.write_whitelist()
        self.repo.write("README.md", "# Project\n\n> English | [中文](README.zh.md)\n")
        self.repo.write("README.zh.md", "# 项目\n\nNo language link here.\n")

        errors = self.check()

        self.assertIn("README.zh.md: missing top link to README.md", errors)

    def test_checks_links_after_yaml_frontmatter(self) -> None:
        self.write_whitelist()
        self.repo.write(
            "guide/SKILL.md",
            "---\nname: example\n---\n\n# Guide\n\n> English | [中文](SKILL.zh.md)\n",
        )
        self.repo.write(
            "guide/SKILL.zh.md",
            "---\nname: example\n---\n\n# 指南\n\n> [English](SKILL.md) | 中文\n",
        )

        self.assertEqual(self.check(), [])

    def test_ignores_non_navigation_peer_syntax(self) -> None:
        invalid_sources = {
            "comment": "<!-- [English](comment.md) -->",
            "fence": "```markdown\n[English](fence.md)\n```",
            "list_fence": (
                "- ```markdown\n"
                "  [English](list_fence.md)\n"
                "  ```"
            ),
            "image": "![English](image.md)",
        }
        for name, invalid_source in invalid_sources.items():
            self.repo.write(
                f"{name}.md",
                f"# {name}\n\n> English | [中文]({name}.zh.md)\n",
            )
            self.repo.write(
                f"{name}.zh.md",
                f"# {name}\n\n{invalid_source}\n",
            )
        self.write_whitelist()

        errors = self.check()

        for name in invalid_sources:
            self.assertIn(
                f"{name}.zh.md: missing top link to {name}.md",
                errors,
            )

    def test_reports_whitelist_entries_that_are_not_tracked_markdown(self) -> None:
        self.write_whitelist("missing.md\n")

        errors = self.check()

        self.assertIn(
            "whitelist entry is not a tracked Markdown file: missing.md",
            errors,
        )

    def test_rejects_whitelist_entry_that_has_a_language_peer(self) -> None:
        self.write_whitelist("README.md\n")
        self.repo.write("README.md", "# Project\n")
        self.repo.write("README.zh.md", "# 项目\n")

        errors = self.check()

        self.assertIn(
            "whitelist entry has a tracked language peer: README.md "
            "(peer: README.zh.md)",
            errors,
        )

    def test_reports_chinese_document_without_english_peer(self) -> None:
        self.write_whitelist()
        self.repo.write("orphan.zh.md", "# 孤立文档\n")

        errors = self.check()

        self.assertIn("missing English peer: orphan.md (for orphan.zh.md)", errors)


if __name__ == "__main__":
    unittest.main()
