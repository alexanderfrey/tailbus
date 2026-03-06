#!/usr/bin/env python3
"""Local workspace agent for the dev-task-room example."""

from __future__ import annotations

import asyncio
import json
import os
import shutil
import subprocess
import sys
import time
from pathlib import Path
from typing import Any

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "../../sdk/python/src"))

from tailbus import AsyncAgent, Manifest, RoomEvent

from dev_task_common import (
    BOLD,
    GREEN,
    RED,
    RESET,
    WORKSPACE_ROOT,
    ensure_workspace_exists,
    iter_workspace_files,
    parse_json,
    read_workspace_snapshot,
    reset_workspace,
    say,
)

WORKSPACE_TIMEOUT = int(os.environ.get("WORKSPACE_TIMEOUT", "60"))

agent = AsyncAgent(
    "workspace-agent",
    manifest=Manifest(
        description="Owns the local fixture workspace and is the only agent allowed to apply file writes there",
        commands=[],
        tags=["workspace", "local", "filesystem"],
        version="1.0.0",
        capabilities=["dev.workspace.apply"],
        domains=["engineering"],
        input_types=["application/json"],
        output_types=["application/json"],
    ),
    socket=os.environ.get("TAILBUS_SOCKET", "/tmp/tailbusd.sock"),
)

seen_turns: set[str] = set()


def validate_relative_path(path: str) -> Path:
    if not path or path in (".", "./"):
        raise ValueError(f"invalid workspace path: {path!r}")
    candidate = Path(path)
    if candidate.is_absolute():
        raise ValueError(f"absolute path not allowed: {path}")
    resolved = (WORKSPACE_ROOT / candidate).resolve()
    workspace_root = WORKSPACE_ROOT.resolve()
    if resolved != workspace_root and workspace_root not in resolved.parents:
        raise ValueError(f"path escapes workspace root: {path}")
    return resolved


def apply_change_set(change_set: dict[str, Any]) -> list[str]:
    changed_paths: list[str] = []
    for path in change_set.get("deleted_paths", []):
        resolved = validate_relative_path(str(path))
        if resolved.exists():
            if resolved.is_dir():
                shutil.rmtree(resolved)
            else:
                resolved.unlink()
            changed_paths.append(str(path))
    for file_item in change_set.get("files", []):
        rel_path = str(file_item.get("path", ""))
        content = str(file_item.get("content", ""))
        if not rel_path:
            raise ValueError("missing path in change set file entry")
        resolved = validate_relative_path(rel_path)
        resolved.parent.mkdir(parents=True, exist_ok=True)
        resolved.write_text(content, encoding="utf-8")
        changed_paths.append(rel_path)
    return sorted(set(changed_paths))


def run_fixture_tests() -> dict[str, Any]:
    ensure_workspace_exists()
    proc = subprocess.run(
        [sys.executable, "-m", "unittest", "discover", "-s", "tests", "-p", "test_*.py"],
        cwd=WORKSPACE_ROOT,
        capture_output=True,
        text=True,
        timeout=WORKSPACE_TIMEOUT,
    )
    output = (proc.stdout + ("\n" + proc.stderr if proc.stderr else "")).strip()
    summary = "All fixture tests passed." if proc.returncode == 0 else "Fixture tests failed."
    return {
        "kind": "test_result",
        "status": "ok" if proc.returncode == 0 else "failed",
        "summary": summary,
        "output": output[-4000:],
    }


def prepare_workspace_reply(turn_id: str) -> dict[str, Any]:
    files = reset_workspace()
    snapshot = read_workspace_snapshot()
    return {
        "kind": "workspace_prepare_reply",
        "turn_id": turn_id,
        "author": agent.handle,
        "status": "ok",
        "capability": "dev.workspace.apply",
        "summary": "Workspace reset from template.",
        "workspace_root": str(WORKSPACE_ROOT),
        "workspace_files": files,
        "workspace_snapshot": snapshot,
    }


def apply_workspace_reply(turn_id: str, change_set: dict[str, Any]) -> dict[str, Any]:
    ensure_workspace_exists()
    changed_paths = apply_change_set(change_set)
    test_result = run_fixture_tests()
    return {
        "kind": "apply_result",
        "turn_id": turn_id,
        "author": agent.handle,
        "status": "ok",
        "capability": "dev.workspace.apply",
        "summary": "Applied change set in local workspace.",
        "workspace_root": str(WORKSPACE_ROOT),
        "changed_paths": changed_paths,
        "test_result": test_result,
    }


@agent.on_message
async def handle(msg: RoomEvent) -> None:
    if not isinstance(msg, RoomEvent):
        return
    if msg.event_type != "message_posted" or msg.content_type != "application/json":
        return
    payload = parse_json(msg.payload)
    if not payload:
        return
    kind = payload.get("kind")
    if kind not in ("workspace_prepare_request", "apply_request"):
        return
    if payload.get("target_handle") not in ("", agent.handle):
        return
    if payload.get("target_capability") not in ("", "dev.workspace.apply"):
        return
    turn_id = str(payload.get("turn_id", ""))
    if not turn_id or turn_id in seen_turns:
        return
    seen_turns.add(turn_id)
    say(agent.handle, f"{BOLD}{kind}{RESET}")
    started = time.monotonic()
    try:
        if kind == "workspace_prepare_request":
            reply = prepare_workspace_reply(turn_id)
        else:
            reply = apply_workspace_reply(turn_id, dict(payload.get("change_set", {})))
        reply["elapsed_sec"] = round(time.monotonic() - started, 1)
    except Exception as exc:
        reply = {
            "kind": "apply_result" if kind == "apply_request" else "workspace_prepare_reply",
            "turn_id": turn_id,
            "author": agent.handle,
            "status": "error",
            "capability": "dev.workspace.apply",
            "summary": "",
            "error": str(exc),
            "elapsed_sec": round(time.monotonic() - started, 1),
        }
    if reply["status"] == "ok":
        say(agent.handle, f"{GREEN}posted{RESET} result in {reply['elapsed_sec']:.1f}s")
    else:
        say(agent.handle, f"{RED}error{RESET}: {reply.get('error', 'unknown error')}")
    await agent.post_room_message(
        msg.room_id,
        json.dumps(reply),
        content_type="application/json",
        trace_id=turn_id,
    )


async def main() -> None:
    say(agent.handle, "connecting...")
    async with agent:
        await agent.register()
        say(agent.handle, f"{GREEN}ready{RESET} — local workspace at {WORKSPACE_ROOT}")
        await agent.run_forever()


if __name__ == "__main__":
    asyncio.run(main())
