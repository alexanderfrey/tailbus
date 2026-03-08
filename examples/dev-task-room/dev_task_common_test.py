#!/usr/bin/env python3
"""Focused regression tests for dev-task-room helpers."""

from __future__ import annotations

import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from dev_task_common import (
    select_workspace_snapshot_files,
    strip_code_fences,
    truncate_preserving_ends,
)


class DevTaskCommonTests(unittest.TestCase):
    def test_strip_code_fences_handles_single_line_fence(self) -> None:
        self.assertEqual(strip_code_fences("```json"), "")

    def test_strip_code_fences_unwraps_fenced_json(self) -> None:
        self.assertEqual(strip_code_fences("```json\n{\"ok\": true}\n```"), '{"ok": true}')

    def test_truncate_preserving_ends_keeps_head_and_tail(self) -> None:
        text = "head-" + ("x" * 40) + "-tail"
        truncated = truncate_preserving_ends(text, 20, marker="...")
        self.assertTrue(truncated.startswith("head-"))
        self.assertTrue(truncated.endswith("-tail"))
        self.assertIn("...", truncated)
        self.assertLessEqual(len(truncated), 20)

    def test_select_workspace_snapshot_files_includes_new_sources_and_skips_cache(self) -> None:
        files = (
            "README.md",
            "snake_game.py",
            "tests/test_snake_game.py",
            "__pycache__/snake_game.cpython-312.pyc",
            ".pytest_cache/README.md",
        )
        selected = select_workspace_snapshot_files(files)
        self.assertIn("snake_game.py", selected)
        self.assertIn("tests/test_snake_game.py", selected)
        self.assertNotIn("__pycache__/snake_game.cpython-312.pyc", selected)
        self.assertNotIn(".pytest_cache/README.md", selected)


if __name__ == "__main__":
    unittest.main()
