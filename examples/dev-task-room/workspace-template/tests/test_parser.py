from __future__ import annotations

import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(__file__)))

from parser import parse_csv_line


class ParserTests(unittest.TestCase):
    def test_parse_csv_line_splits_simple_values(self) -> None:
        self.assertEqual(parse_csv_line("alice, engineer, remote"), ["alice", "engineer", "remote"])


if __name__ == "__main__":
    unittest.main()
