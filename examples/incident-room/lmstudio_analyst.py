#!/usr/bin/env python3
"""LM Studio-backed incident analyst for the incident-room example."""

from __future__ import annotations

import asyncio
import json
import os
import sys
import time

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "../../sdk/python/src"))

from tailbus import AsyncAgent, Manifest, RoomEvent

from incident_common import (
    BOLD,
    GREEN,
    LLM_BASE_URL,
    RESET,
    YELLOW,
    incident_from_events,
    llm_call,
    parse_json,
    render_llm_transcript,
    replay_room_with_retry,
    say,
)

SYSTEM_PROMPT = """You are an incident analysis specialist in a Tailbus room.

Read the room transcript and synthesize the specialist findings into:
- one concise root-cause hypothesis
- one confidence statement
- one next action

Return plain text only. Be concrete and operational."""

agent = AsyncAgent(
    "lmstudio-analyst",
    manifest=Manifest(
        description="Synthesizes incident-room findings into a root-cause hypothesis using LM Studio",
        commands=[],
        tags=["incident", "analysis", "lmstudio", "llm"],
        version="1.0.0",
        capabilities=["incident.analyze"],
        domains=["operations"],
        input_types=["application/json"],
        output_types=["application/json"],
    ),
    socket=os.environ.get("TAILBUS_SOCKET", "/tmp/tailbusd.sock"),
)

seen_turns: set[str] = set()


async def analyze_turn(room_id: str, payload: dict[str, object]) -> dict[str, object]:
    events = await replay_room_with_retry(agent, room_id)
    incident = incident_from_events(events)
    transcript = render_llm_transcript(events)
    user_prompt = (
        f"Incident title: {incident.get('title', '')}\n"
        f"Customer impact: {incident.get('customer_impact', '')}\n\n"
        f"Transcript:\n{transcript}\n\n"
        f"Instruction:\n{payload.get('instruction', '')}\n"
    )
    started = time.monotonic()
    loop = asyncio.get_running_loop()
    result = (await loop.run_in_executor(None, llm_call, SYSTEM_PROMPT, user_prompt)).strip()
    elapsed = round(time.monotonic() - started, 1)
    if result.startswith("[LLM error]"):
        return {
            "kind": "solver_reply",
            "turn_id": payload.get("turn_id", ""),
            "author": agent.handle,
            "round": payload.get("round", 0),
            "response_type": payload.get("response_type", "incident-analysis"),
            "status": "error",
            "capability": "incident.analyze",
            "error": result,
            "content": "",
            "elapsed_sec": elapsed,
        }
    return {
        "kind": "solver_reply",
        "turn_id": payload.get("turn_id", ""),
        "author": agent.handle,
        "round": payload.get("round", 0),
        "response_type": payload.get("response_type", "incident-analysis"),
        "status": "ok",
        "capability": "incident.analyze",
        "summary": result,
        "content": result,
        "elapsed_sec": elapsed,
    }


@agent.on_message
async def handle(msg: RoomEvent) -> None:
    if not isinstance(msg, RoomEvent):
        return
    if msg.event_type != "message_posted" or msg.content_type != "application/json":
        return
    payload = parse_json(msg.payload)
    if not payload or payload.get("kind") != "turn_request":
        return
    if payload.get("target_handle") not in ("", agent.handle):
        return
    if payload.get("target_capability") not in ("", "incident.analyze"):
        return
    turn_id = str(payload.get("turn_id", ""))
    if not turn_id or turn_id in seen_turns:
        return
    seen_turns.add(turn_id)
    say(agent.handle, f"LM Studio analysis {BOLD}{payload.get('response_type', 'turn')}{RESET} via {LLM_BASE_URL}")
    try:
        reply = await analyze_turn(msg.room_id, payload)
    except Exception as exc:
        reply = {
            "kind": "solver_reply",
            "turn_id": turn_id,
            "author": agent.handle,
            "round": payload.get("round", 0),
            "response_type": payload.get("response_type", "incident-analysis"),
            "status": "error",
            "capability": "incident.analyze",
            "error": str(exc),
            "content": "",
            "elapsed_sec": 0.0,
        }
    if reply["status"] == "ok":
        say(agent.handle, f"{GREEN}posted{RESET} analysis in {reply['elapsed_sec']:.1f}s")
    else:
        say(agent.handle, f"{YELLOW}error{RESET}: {reply.get('error', 'unknown error')}")
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
        say(agent.handle, f"{GREEN}ready{RESET} — LM Studio incident analysis online")
        await agent.run_forever()


if __name__ == "__main__":
    asyncio.run(main())
