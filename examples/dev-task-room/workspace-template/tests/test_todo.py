from __future__ import annotations

import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(__file__)))

from todo import filter_by_status


ITEMS = [
    {"title": "write docs", "status": "open"},
    {"title": "ship build", "status": "done"},
    {"title": "fix flaky test", "status": "open"},
]


class TodoTests(unittest.TestCase):
    def test_filter_by_status_returns_all_for_all(self) -> None:
        self.assertEqual(filter_by_status(ITEMS, "all"), ITEMS)

    def test_filter_by_status_matches_exact_value(self) -> None:
        self.assertEqual(
            filter_by_status(ITEMS, "open"),
            [
                {"title": "write docs", "status": "open"},
                {"title": "fix flaky test", "status": "open"},
            ],
        )


if __name__ == "__main__":
    unittest.main()
