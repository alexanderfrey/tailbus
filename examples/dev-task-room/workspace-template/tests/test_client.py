from __future__ import annotations

import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(__file__)))

from client import get_json


class FakeResponse:
    def __init__(self, body: str) -> None:
        self._body = body

    def read(self) -> bytes:
        return self._body.encode("utf-8")

    def __enter__(self) -> "FakeResponse":
        return self

    def __exit__(self, exc_type, exc, tb) -> None:
        return None


class ClientTests(unittest.TestCase):
    def test_get_json_parses_payload(self) -> None:
        def fake_fetch(url: str) -> FakeResponse:
            self.assertEqual(url, "https://example.test/data")
            return FakeResponse('{"ok": true, "count": 2}')

        result = get_json("https://example.test/data", fetcher=fake_fetch)
        self.assertEqual(result, {"ok": True, "count": 2})


if __name__ == "__main__":
    unittest.main()
