#!/usr/bin/env python3
"""Codex-backed status update writer for the incident-room example."""

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
    CODEX_MODEL,
    GREEN,
    RESET,
    YELLOW,
    incident_from_events,
    parse_json,
    render_llm_transcript,
    replay_room_with_retry,
    run_codex_prompt,
    say,
)

agent = AsyncAgent(
    "codex-status-agent",
    manifest=Manifest(
        description="Drafts customer-facing incident updates with Codex using a small OpenAI model",
        commands=[],
        tags=["incident", "status", "codex", "llm"],
        version="1.0.0",
        capabilities=["statuspage.compose"],
        domains=["communications"],
        input_types=["application/json"],
        output_types=["application/json"],
    ),
    socket=os.environ.get("TAILBUS_SOCKET", "/tmp/tailbusd.sock"),
)

seen_turns: set[str] = set()


async def write_update(room_id: str, payload: dict[str, object]) -> dict[str, object]:
    events = await replay_room_with_retry(agent, room_id)
    incident = incident_from_events(events)
    transcript = render_llm_transcript(events)
    prompt = (
        "You are drafting a customer-facing status update for an active production incident.\n"
        "Use only the evidence in the transcript. Be calm, factual, and avoid internal jargon.\n"
        "Write 2-3 sentences max. Do not invent timelines beyond the transcript.\n\n"
        f"Incident title: {incident.get('title', '')}\n"
        f"Customer impact: {incident.get('customer_impact', '')}\n\n"
        f"Transcript:\n{transcript}\n\n"
        f"Instruction:\n{payload.get('instruction', '')}\n"
    )
    started = time.monotonic()
    result = (await run_codex_prompt(prompt, prefix="incident_room_status")).strip()
    elapsed = round(time.monotonic() - started, 1)
    if result.startswith("[codex error]"):
        return {
            "kind": "solver_reply",
            "turn_id": payload.get("turn_id", ""),
            "author": agent.handle,
            "round": payload.get("round", 0),
            "response_type": payload.get("response_type", "customer-update"),
            "status": "error",
            "capability": "statuspage.compose",
            "error": result,
            "content": "",
            "elapsed_sec": elapsed,
        }
    if result.startswith("```"):
        result = result.split("\n", 1)[1].rsplit("```", 1)[0].strip()
    return {
        "kind": "solver_reply",
        "turn_id": payload.get("turn_id", ""),
        "author": agent.handle,
        "round": payload.get("round", 0),
        "response_type": payload.get("response_type", "customer-update"),
        "status": "ok",
        "capability": "statuspage.compose",
        "customer_update": result,
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
    if payload.get("target_capability") not in ("", "statuspage.compose"):
        return
    turn_id = str(payload.get("turn_id", ""))
    if not turn_id or turn_id in seen_turns:
        return
    seen_turns.add(turn_id)
    say(agent.handle, f"Codex status draft {BOLD}{payload.get('response_type', 'turn')}{RESET} via {CODEX_MODEL}")
    try:
        reply = await write_update(msg.room_id, payload)
    except Exception as exc:
        reply = {
            "kind": "solver_reply",
            "turn_id": turn_id,
            "author": agent.handle,
            "round": payload.get("round", 0),
            "response_type": payload.get("response_type", "customer-update"),
            "status": "error",
            "capability": "statuspage.compose",
            "error": str(exc),
            "content": "",
            "elapsed_sec": 0.0,
        }
    if reply["status"] == "ok":
        say(agent.handle, f"{GREEN}posted{RESET} update in {reply['elapsed_sec']:.1f}s")
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
        say(agent.handle, f"{GREEN}ready{RESET} — Codex status drafting online")
        await agent.run_forever()


if __name__ == "__main__":
    asyncio.run(main())
