"""Tests for tailbus._sync — SyncAgent tests."""

import asyncio
import json
import unittest
from unittest.mock import AsyncMock, patch

from tailbus._protocol import Manifest
from tailbus._sync import SyncAgent


class FakeStdout:
    """Fake async stdout for testing."""

    def __init__(self) -> None:
        self._queue: asyncio.Queue[bytes] = asyncio.Queue()

    async def readline(self) -> bytes:
        return await self._queue.get()

    def feed(self, line: str) -> None:
        self._queue.put_nowait((line.rstrip("\n") + "\n").encode("utf-8"))

    def feed_eof(self) -> None:
        self._queue.put_nowait(b"")


class FakeStdin:
    """Fake async stdin that auto-feeds responses."""

    def __init__(self, stdout: FakeStdout) -> None:
        self.written: list[bytes] = []
        self._stdout = stdout
        self._auto_responses: list[dict] = []

    def write(self, data: bytes) -> None:
        self.written.append(data)
        if self._auto_responses:
            resp = self._auto_responses.pop(0)
            self._stdout.feed(json.dumps(resp))

    async def drain(self) -> None:
        pass

    def close(self) -> None:
        pass

    def queue_response(self, resp: dict) -> None:
        self._auto_responses.append(resp)


class FakeProcess:
    """Fake asyncio.subprocess.Process."""

    def __init__(self) -> None:
        self.stdout = FakeStdout()
        self.stdin = FakeStdin(self.stdout)
        self.returncode: int | None = None
        self._wait_event = asyncio.Event()

    async def wait(self) -> int:
        await self._wait_event.wait()
        return self.returncode or 0

    def terminate(self) -> None:
        self.returncode = -15
        self._wait_event.set()

    def kill(self) -> None:
        self.returncode = -9
        self._wait_event.set()


class TestSyncAgent(unittest.TestCase):
    def setUp(self) -> None:
        self._fake_process: FakeProcess | None = None

        async def fake_start_process(cmd: list[str]) -> FakeProcess:
            proc = FakeProcess()
            self._fake_process = proc
            return proc  # type: ignore[return-value]

        self.patcher_find = patch(
            "tailbus._agent.find_binary", return_value="/usr/bin/tailbus"
        )
        self.patcher_start = patch(
            "tailbus._agent.start_process", side_effect=fake_start_process
        )
        self.patcher_stop = patch(
            "tailbus._agent.stop_process",
            new=AsyncMock(),
        )
        self.patcher_find.start()
        self.patcher_start.start()
        self.patcher_stop.start()

    def tearDown(self) -> None:
        self.patcher_find.stop()
        self.patcher_start.stop()
        self.patcher_stop.stop()

    def _queue(self, data: dict) -> None:
        assert self._fake_process is not None
        self._fake_process.stdin.queue_response(data)

    def test_context_manager(self) -> None:
        with SyncAgent("test-agent") as agent:
            self.assertIsNotNone(agent._async_agent)
            assert agent._async_agent is not None
            self.assertTrue(agent._async_agent._is_started)
            self.assertIsNotNone(agent._thread)
            assert agent._thread is not None
            self.assertTrue(agent._thread.daemon)
        self.assertIsNone(agent._async_agent)

    def test_register(self) -> None:
        with SyncAgent("test-agent") as agent:
            self._queue({"type": "registered", "handle": "test-agent"})
            result = agent.register()
            self.assertEqual(result.handle, "test-agent")
            self.assertTrue(agent.is_registered)

    def test_open_send_resolve(self) -> None:
        with SyncAgent("test-agent") as agent:
            self._queue({"type": "registered", "handle": "test-agent"})
            agent.register()

            self._queue(
                {
                    "type": "opened",
                    "session": "s1",
                    "message_id": "m1",
                    "trace_id": "t1",
                }
            )
            opened = agent.open_session("other", "hello")
            self.assertEqual(opened.session, "s1")

            self._queue({"type": "sent", "message_id": "m2"})
            sent = agent.send("s1", "follow-up")
            self.assertEqual(sent.message_id, "m2")

            self._queue({"type": "resolved", "message_id": "m3"})
            resolved = agent.resolve("s1", "done")
            self.assertEqual(resolved.message_id, "m3")

    def test_thread_is_daemon(self) -> None:
        with SyncAgent("test-agent") as agent:
            assert agent._thread is not None
            self.assertTrue(agent._thread.daemon)

    def test_is_registered_before_start(self) -> None:
        agent = SyncAgent("test-agent")
        self.assertFalse(agent.is_registered)


if __name__ == "__main__":
    unittest.main()
