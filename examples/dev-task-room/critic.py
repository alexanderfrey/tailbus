#!/usr/bin/env python3
"""LM Studio-backed critic for the dev-task-room example."""

from __future__ import annotations

import asyncio
import collections
import json
import os
import sys
import time

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "../../sdk/python/src"))

from tailbus import AsyncAgent, Manifest, RoomEvent

from dev_task_common import (
    BOLD,
    GREEN,
    LLM_BASE_URL,
    REVIEW_TIMEOUT,
    TURN_PROGRESS_INTERVAL,
    RESET,
    YELLOW,
    is_room_closed_error,
    llm_call,
    parse_json,
    parse_json_object,
    progress_pinger,
    say,
)

CHUNK_SYSTEM_PROMPT = """You are the critical reviewer in a Tailbus engineering room.

Review only the scoped chunk of the proposed change set against the task.

Return exactly one JSON object:
{
  "summary": "short review summary",
  "decision": "approve" or "revise",
  "findings": ["bug or risk"],
  "required_changes": ["concrete requested fix"],
  "test_gaps": ["missing test coverage"]
}

Rules:
- Be strict about correctness and regression risk.
- Review only the provided scope. Do not speculate about files or ranges outside it.
- Approve only if the provided scope is good enough at the chunk level.
- Return JSON only.
"""

SYNTHESIS_SYSTEM_PROMPT = """You are the final reviewer in a Tailbus engineering room.

You will receive the task, changed-path manifest, and completed chunk-review outputs.

Return exactly one JSON object:
{
  "summary": "short review summary",
  "decision": "approve" or "revise",
  "findings": ["bug or risk"],
  "required_changes": ["concrete requested fix"],
  "test_gaps": ["missing test coverage"]
}

Rules:
- Focus on cross-file issues, missing integration concerns, and deduping chunk findings.
- Do not re-review raw code that is not present.
- Approve only if the overall change set is good enough to apply in the local workspace.
- Return JSON only.
"""

agent = AsyncAgent(
    "critic",
    manifest=Manifest(
        description="Reviews development change sets for bugs, regressions, and missing tests using LM Studio",
        commands=[],
        tags=["llm", "lmstudio", "development", "review"],
        version="1.0.0",
        capabilities=["dev.review"],
        domains=["engineering"],
        input_types=["application/json"],
        output_types=["application/json"],
    ),
    socket=os.environ.get("TAILBUS_SOCKET", "/tmp/tailbusd.sock"),
)

seen_turns: set[str] = set()
seen_turns_order: collections.deque[str] = collections.deque()
MAX_SEEN_TURNS = 500


def build_chunk_prompt(payload: dict[str, object], task_text: str) -> str:
    review_chunk = dict(payload.get("review_chunk", {}))
    parts = [
        "Task:",
        task_text,
        "",
        f"Chunk: {payload.get('chunk_id', '')}",
        f"Scope paths: {json.dumps(payload.get('scope_paths', []))}",
        f"Scope ranges: {json.dumps(payload.get('scope_ranges', []))}",
        f"Changed paths: {json.dumps(payload.get('change_paths', []))}",
        "",
        "Scoped sections:",
        json.dumps(review_chunk.get("sections", []), indent=2),
    ]
    return "\n".join(parts)


def build_synthesis_prompt(payload: dict[str, object], task_text: str) -> str:
    chunk_reviews = [
        {
            "chunk_id": item.get("chunk_id", ""),
            "summary": item.get("summary", ""),
            "scope_paths": item.get("scope_paths", []),
            "scope_ranges": item.get("scope_ranges", []),
            "findings": item.get("findings", []),
            "required_changes": item.get("required_changes", []),
            "test_gaps": item.get("test_gaps", []),
        }
        for item in list(payload.get("chunk_reviews", []))
    ]
    parts = [
        "Task:",
        task_text,
        "",
        f"Implementer summary: {payload.get('implement_summary', '')}",
        f"Changed paths: {json.dumps(payload.get('change_paths', []))}",
        f"Chunk review count: {payload.get('chunk_total', 0)}",
        "",
        "Chunk reviews:",
        json.dumps(chunk_reviews, indent=2),
    ]
    return "\n".join(parts)


async def review_turn(payload: dict[str, object]) -> dict[str, object]:
    progress_state = payload.get("_progress_state")
    review_mode = str(payload.get("review_mode", "chunk"))
    task_text = str(payload.get("task", "")).strip()
    if review_mode == "synthesis":
        user_prompt = build_synthesis_prompt(payload, task_text)
        system_prompt = SYNTHESIS_SYSTEM_PROMPT
    else:
        user_prompt = build_chunk_prompt(payload, task_text)
        system_prompt = CHUNK_SYSTEM_PROMPT
    started = time.monotonic()
    if isinstance(progress_state, dict):
        progress_state["summary"] = "Critic waiting for LM Studio response."
    say(
        agent.handle,
        f"{review_mode} prompt ready ({len(user_prompt)} chars); calling {BOLD}{LLM_BASE_URL}{RESET}",
    )

    try:
        raw = (
            await asyncio.wait_for(
                asyncio.shield(asyncio.to_thread(llm_call, system_prompt, user_prompt)),
                timeout=REVIEW_TIMEOUT,
            )
        ).strip()
    except asyncio.TimeoutError:
        elapsed = round(time.monotonic() - started, 1)
        return {
            "kind": "review_reply",
            "turn_id": payload.get("turn_id", ""),
            "author": agent.handle,
            "status": "error",
            "capability": "dev.review",
            "summary": "",
            "error": f"LM Studio review timed out after {REVIEW_TIMEOUT:.0f}s",
            "elapsed_sec": elapsed,
        }
    elapsed = round(time.monotonic() - started, 1)
    if raw.startswith("[LLM error]"):
        return {
            "kind": "review_reply",
            "turn_id": payload.get("turn_id", ""),
            "author": agent.handle,
            "status": "error",
            "capability": "dev.review",
            "summary": "",
            "error": raw,
            "elapsed_sec": elapsed,
        }
    if not raw:
        return {
            "kind": "review_reply",
            "turn_id": payload.get("turn_id", ""),
            "author": agent.handle,
            "status": "error",
            "capability": "dev.review",
            "summary": "",
            "error": "LM Studio returned empty output",
            "elapsed_sec": elapsed,
        }
    result = parse_json_object(raw)
    if not result:
        return {
            "kind": "review_reply",
            "turn_id": payload.get("turn_id", ""),
            "author": agent.handle,
            "status": "error",
            "capability": "dev.review",
            "summary": "",
            "error": "LM Studio did not return valid JSON",
            "raw_output": raw[:2000],
            "elapsed_sec": elapsed,
        }
    return {
        "kind": "review_reply",
        "turn_id": payload.get("turn_id", ""),
        "author": agent.handle,
        "status": "ok",
        "capability": "dev.review",
        "review_mode": review_mode,
        "chunk_id": str(payload.get("chunk_id", "")),
        "summary": str(result.get("summary", "Review complete.")),
        "decision": str(result.get("decision", "revise")),
        "findings": list(result.get("findings", [])),
        "required_changes": list(result.get("required_changes", [])),
        "test_gaps": list(result.get("test_gaps", [])),
        "coverage_paths": list(payload.get("scope_paths", [])),
        "coverage_ranges": list(payload.get("scope_ranges", [])),
        "elapsed_sec": elapsed,
    }


@agent.on_message
async def handle(msg: RoomEvent) -> None:
    if not isinstance(msg, RoomEvent):
        return
    if msg.event_type != "message_posted" or msg.content_type != "application/json":
        return
    payload = parse_json(msg.payload)
    if not payload or payload.get("kind") != "review_request":
        return
    if payload.get("target_handle") not in ("", agent.handle):
        return
    if payload.get("target_capability") not in ("", "dev.review"):
        return
    turn_id = str(payload.get("turn_id", ""))
    if not turn_id or turn_id in seen_turns:
        return
    seen_turns.add(turn_id)
    seen_turns_order.append(turn_id)
    while len(seen_turns) > MAX_SEEN_TURNS:
        seen_turns.discard(seen_turns_order.popleft())
    say(agent.handle, f"reviewing via {BOLD}{LLM_BASE_URL}{RESET}")
    progress_state = {"summary": "Critic preparing review prompt."}
    progress_task = asyncio.create_task(
        progress_pinger(
            agent,
            room_id=msg.room_id,
            turn_id=turn_id,
            round_no=int(payload.get("round", 0)),
            target_handle=agent.handle,
            target_capability="dev.review",
            summary=lambda: str(progress_state["summary"]),
            interval=TURN_PROGRESS_INTERVAL,
        )
    )
    try:
        payload["_progress_state"] = progress_state
        reply = await review_turn(payload)
    except Exception as exc:
        reply = {
            "kind": "review_reply",
            "turn_id": turn_id,
            "author": agent.handle,
            "status": "error",
            "capability": "dev.review",
            "summary": "",
            "error": str(exc),
            "elapsed_sec": 0.0,
        }
    finally:
        progress_task.cancel()
        try:
            await progress_task
        except (asyncio.CancelledError, Exception):
            pass
    if reply["status"] == "ok":
        say(agent.handle, f"{GREEN}posted{RESET} review in {reply['elapsed_sec']:.1f}s")
    else:
        say(agent.handle, f"{YELLOW}error{RESET}: {reply.get('error', 'unknown error')}")
    try:
        await agent.post_room_message(
            msg.room_id,
            json.dumps(reply),
            content_type="application/json",
            trace_id=turn_id,
        )
    except Exception as exc:
        if is_room_closed_error(exc):
            say(agent.handle, f"{YELLOW}room closed{RESET} before review reply could be posted")
        else:
            raise


async def main() -> None:
    say(agent.handle, "connecting...")
    async with agent:
        await agent.register()
        say(agent.handle, f"{GREEN}ready{RESET} — capability dev.review")
        await agent.run_forever()


if __name__ == "__main__":
    asyncio.run(main())
