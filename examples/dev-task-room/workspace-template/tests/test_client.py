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

    def test_get_json_partial_success_retries_once(self) -> None:
        call_count = 0
        delays: list[float] = []

        def fake_sleep(delay: float) -> None:
            delays.append(delay)

        def fake_fetch(url: str) -> FakeResponse:
            nonlocal call_count
            call_count += 1
            if call_count == 1:
                raise OSError("transient failure")
            return FakeResponse('{"ok": true, "retried": "once"}')

        result = get_json(
            "https://example.test/data",
            fetcher=fake_fetch,
            max_retries=2,
            backoff_factor=0.75,
            sleep_func=fake_sleep,
        )

        self.assertEqual(call_count, 2)
        self.assertEqual(delays, [0.75])
        self.assertEqual(result, {"ok": True, "retried": "once"})

    def test_get_json_single_attempt_skips_sleep(self) -> None:
        call_count = 0
        delays: list[float] = []

        def fake_sleep(delay: float) -> None:
            delays.append(delay)

        def fake_fetch(url: str) -> FakeResponse:
            nonlocal call_count
            call_count += 1
            raise OSError("permanent failure")

        with self.assertRaises(OSError):
            get_json(
                "https://example.test/data",
                fetcher=fake_fetch,
                max_retries=1,
                backoff_factor=1.25,
                sleep_func=fake_sleep,
            )

        self.assertEqual(call_count, 1)
        self.assertEqual(delays, [])

    def test_get_json_rejects_invalid_retry_parameters(self) -> None:
        def fake_fetch(url: str) -> FakeResponse:
            raise OSError("should not reach the network")

        with self.assertRaises(ValueError):
            get_json(
                "https://example.test/data",
                fetcher=fake_fetch,
                max_retries=0,
            )

        with self.assertRaises(ValueError):
            get_json(
                "https://example.test/data",
                fetcher=fake_fetch,
                backoff_factor=-0.5,
            )

        with self.assertRaises(ValueError):
            get_json(
                "https://example.test/data",
                fetcher=fake_fetch,
                max_backoff=-1.0,
            )

    def test_get_json_applies_exponential_backoff(self) -> None:
        call_count = 0
        delays: list[float] = []

        def fake_sleep(delay: float) -> None:
            delays.append(delay)

        def fake_fetch(url: str) -> FakeResponse:
            nonlocal call_count
            call_count += 1
            if call_count < 3:
                raise OSError("transient failure")
            return FakeResponse('{"ok": true, "retried": "twice"}')

        result = get_json(
            "https://example.test/data",
            fetcher=fake_fetch,
            max_retries=3,
            backoff_factor=0.5,
            sleep_func=fake_sleep,
        )

        self.assertEqual(call_count, 3)
        self.assertEqual(delays, [0.5, 1.0])
        self.assertEqual(result, {"ok": True, "retried": "twice"})

    def test_get_json_respects_max_backoff(self) -> None:
        call_count = 0
        delays: list[float] = []

        def fake_sleep(delay: float) -> None:
            delays.append(delay)

        def fake_fetch(url: str) -> FakeResponse:
            nonlocal call_count
            call_count += 1
            if call_count < 3:
                raise OSError("transient failure")
            return FakeResponse('{"ok": true, "capped": true}')

        result = get_json(
            "https://example.test/data",
            fetcher=fake_fetch,
            max_retries=3,
            backoff_factor=1.0,
            max_backoff=1.25,
            sleep_func=fake_sleep,
        )

        self.assertEqual(call_count, 3)
        self.assertEqual(delays, [1.0, 1.25])
        self.assertEqual(result, {"ok": True, "capped": True})


if __name__ == "__main__":
    unittest.main()
