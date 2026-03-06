#!/usr/bin/env python3
"""Incident-room orchestrator."""

from __future__ import annotations

import asyncio
import json
import os
import sys
import time
import uuid
from pathlib import Path
from typing import Any

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "../../sdk/python/src"))

from tailbus import AsyncAgent, CommandSpec, Manifest, Message, RoomEvent

from incident_common import (
    BOLD,
    CYAN,
    DIM,
    GREEN,
    RED,
    RESET,
    YELLOW,
    build_internal_summary,
    parse_command_payload,
    parse_json,
    render_markdown_transcript,
    replay_room_with_retry,
    say,
    scenario_for_incident,
)

OUTPUT_DIR = Path(os.environ.get("OUTPUT_DIR", Path(__file__).resolve().parent / "output"))
TURN_TIMEOUT = float(os.environ.get("TURN_TIMEOUT", "60"))

SPECIALISTS: list[dict[str, str]] = [
    {
        "capability": "ops.logs.search",
        "instruction": "Check recent logs and identify the strongest failure signature for this incident.",
        "response_type": "logs-analysis",
    },
    {
        "capability": "ops.metrics.query",
        "instruction": "Summarize the key error-rate, latency, and saturation metrics tied to this incident.",
        "response_type": "metrics-analysis",
    },
    {
        "capability": "ops.release.history",
        "instruction": "Identify any recent release or config change that best correlates with the incident.",
        "response_type": "release-analysis",
    },
    {
        "capability": "billing.account.lookup",
        "instruction": "Check whether this incident is isolated to billing/account state or broader application flow.",
        "response_type": "billing-analysis",
    },
]

agent = AsyncAgent(
    "incident-orchestrator",
    manifest=Manifest(
        description="Coordinates a cross-department incident room using capability discovery and rooms",
        commands=[
            CommandSpec(
                name="open_incident",
                description="Open an incident room and coordinate specialist analysis",
                parameters_schema=json.dumps(
                    {
                        "type": "object",
                        "properties": {
                            "title": {"type": "string"},
                            "severity": {"type": "string"},
                            "region": {"type": "string"},
                            "customer_impact": {"type": "string"},
                            "account_id": {"type": "string"},
                            "reported_by": {"type": "string"},
                        },
                        "required": ["title"],
                    }
                ),
            )
        ],
        tags=["orchestration", "incident", "rooms", "discovery"],
        version="1.0.0",
        capabilities=["incident.orchestrate", "room.manage"],
        domains=["support", "operations"],
        input_types=["application/json"],
        output_types=["application/json"],
    ),
    socket=os.environ.get("TAILBUS_SOCKET", "/tmp/tailbusd.sock"),
)

pending_turns: dict[str, asyncio.Future[dict[str, Any]]] = {}


def short(text: str, limit: int = 96) -> str:
    return text if len(text) <= limit else text[: limit - 3] + "..."


async def post_room(room_id: str, payload: dict[str, Any]) -> None:
    await agent.post_room_message(
        room_id,
        json.dumps(payload),
        content_type="application/json",
        trace_id=str(payload.get("turn_id", payload.get("incident_id", ""))),
    )


async def handle_room_event(event: RoomEvent) -> None:
    if event.event_type != "message_posted" or event.content_type != "application/json":
        return
    payload = parse_json(event.payload)
    if not payload or payload.get("kind") != "solver_reply":
        return
    turn_id = str(payload.get("turn_id", ""))
    if not turn_id:
        return
    future = pending_turns.get(turn_id)
    if future is None or future.done():
        return
    future.set_result(payload)


async def discover_handle(capability: str) -> dict[str, Any]:
    matches = await agent.find_handles(capabilities=[capability], limit=1)
    if not matches:
        raise RuntimeError(f"no handle found for capability {capability}")
    match = matches[0]
    return {
        "capability": capability,
        "handle": match.handle,
        "score": match.score,
        "reasons": list(match.match_reasons),
    }


async def request_turn(
    *,
    room_id: str,
    incident_id: str,
    round_no: int,
    target_handle: str,
    target_capability: str,
    instruction: str,
    response_type: str,
) -> dict[str, Any]:
    turn_id = str(uuid.uuid4())
    payload = {
        "kind": "turn_request",
        "turn_id": turn_id,
        "incident_id": incident_id,
        "round": round_no,
        "target_handle": target_handle,
        "target_capability": target_capability,
        "instruction": instruction,
        "response_type": response_type,
        "requested_by": agent.handle,
        "requested_at": int(time.time()),
    }
    future: asyncio.Future[dict[str, Any]] = asyncio.get_running_loop().create_future()
    pending_turns[turn_id] = future
    await post_room(room_id, payload)
    say(agent.handle, f"{CYAN}→{RESET} @{target_handle} [{target_capability}]")
    say(agent.handle, f"   {DIM}{short(instruction)}{RESET}")
    try:
        reply = await asyncio.wait_for(future, timeout=TURN_TIMEOUT)
        if reply.get("status") == "ok":
            say(agent.handle, f"{GREEN}←{RESET} @{target_handle} ({reply.get('elapsed_sec', 0):.1f}s)")
        else:
            say(agent.handle, f"{YELLOW}!{RESET} @{target_handle} returned {reply.get('status', 'unknown')}")
        return reply
    except asyncio.TimeoutError:
        await post_room(
            room_id,
            {
                "kind": "turn_timeout",
                "turn_id": turn_id,
                "incident_id": incident_id,
                "round": round_no,
                "target_handle": target_handle,
                "target_capability": target_capability,
                "seconds": TURN_TIMEOUT,
                "recorded_at": int(time.time()),
            },
        )
        say(agent.handle, f"{RED}timeout{RESET} waiting for @{target_handle}")
        return {
            "kind": "solver_reply",
            "turn_id": turn_id,
            "author": target_handle,
            "status": "timeout",
            "capability": target_capability,
            "content": "",
            "error": f"Timed out after {TURN_TIMEOUT:.0f}s",
        }
    finally:
        pending_turns.pop(turn_id, None)


def build_hypothesis(incident: dict[str, Any], replies: list[dict[str, Any]]) -> str:
    scenario = scenario_for_incident(incident)
    root_cause = scenario.get("root_cause", "Specialists have not agreed on a root cause yet.")
    good_replies = [reply for reply in replies if reply.get("status") == "ok"]
    if len(good_replies) < 2:
        return f"Working hypothesis is still weak. Current best guess: {root_cause}"
    return root_cause


def build_output_markdown(
    *,
    incident: dict[str, Any],
    room_id: str,
    discovered: list[dict[str, Any]],
    internal_summary: str,
    customer_update: str,
    events: list[RoomEvent],
) -> str:
    lines = [
        f"# Incident Room — {incident.get('incident_id', '')}",
        "",
        f"- Room: `{room_id}`",
        f"- Title: {incident.get('title', '')}",
        f"- Severity: {incident.get('severity', '')}",
        f"- Region: {incident.get('region', '')}",
        "",
        "## Discovered Specialists",
    ]
    for item in discovered:
        lines.append(
            f"- `{item['capability']}` -> `{item['handle']}` (score={item['score']}, reasons={', '.join(item['reasons'])})"
        )
    lines.extend(
        [
            "",
            "## Internal Summary",
            "",
            internal_summary,
            "",
            "## Customer Update",
            "",
            customer_update,
            "",
            render_markdown_transcript(events),
            "",
        ]
    )
    return "\n".join(lines)


@agent.on_message
async def handle(msg: Message | RoomEvent) -> None:
    if isinstance(msg, RoomEvent):
        await handle_room_event(msg)
        return

    if msg.message_type != "session_open":
        return

    args = parse_command_payload(msg.payload)
    if not args.get("title"):
        await agent.resolve(
            msg.session,
            json.dumps({"error": "incident title is required"}),
            content_type="application/json",
        )
        return

    started = time.monotonic()
    incident_id = "INC-" + time.strftime("%Y%m%d-%H%M%S")
    incident = {
        "kind": "problem_opened",
        "incident_id": incident_id,
        "title": args.get("title", ""),
        "severity": args.get("severity", "sev2"),
        "region": args.get("region", "eu-west"),
        "customer_impact": args.get("customer_impact", args.get("title", "")),
        "account_id": args.get("account_id", "acct-demo-4812"),
        "reported_by": args.get("reported_by", msg.from_handle),
        "opened_at": int(time.time()),
    }

    say(agent.handle, f"{BOLD}{incident_id}{RESET} {incident['title']}")
    discovered: list[dict[str, Any]] = []
    try:
        for item in SPECIALISTS:
            result = await discover_handle(item["capability"])
            discovered.append(result)
            say(agent.handle, f"discovered @{result['handle']} for {result['capability']}")
        status_handle = await discover_handle("statuspage.compose")
        discovered.append(status_handle)
        say(agent.handle, f"discovered @{status_handle['handle']} for statuspage.compose")
    except Exception as exc:
        await agent.resolve(
            msg.session,
            json.dumps({"error": str(exc)}),
            content_type="application/json",
        )
        return

    room_members = sorted({item["handle"] for item in discovered})
    room_title = f"{incident_id} {incident['title']}"
    room_id = await agent.create_room(room_title, room_members)
    await post_room(room_id, incident)
    say(agent.handle, f"room {room_id} created with {len(room_members)} specialists")

    replies: list[dict[str, Any]] = []
    round_no = 1
    for item in SPECIALISTS:
        target = next(result for result in discovered if result["capability"] == item["capability"])
        replies.append(
            await request_turn(
                room_id=room_id,
                incident_id=incident_id,
                round_no=round_no,
                target_handle=target["handle"],
                target_capability=target["capability"],
                instruction=item["instruction"],
                response_type=item["response_type"],
            )
        )
        round_no += 1

    hypothesis = build_hypothesis(incident, replies)
    await post_room(
        room_id,
        {
            "kind": "hypothesis",
            "incident_id": incident_id,
            "author": agent.handle,
            "summary": hypothesis,
            "recorded_at": int(time.time()),
        },
    )

    public_reply = await request_turn(
        room_id=room_id,
        incident_id=incident_id,
        round_no=round_no,
        target_handle=status_handle["handle"],
        target_capability=status_handle["capability"],
        instruction="Write a concise customer-safe status update using the room evidence so far.",
        response_type="customer-update",
    )
    replies.append(public_reply)
    customer_update = public_reply.get("customer_update") or public_reply.get("content") or "We are investigating the issue."
    internal_summary = build_internal_summary(incident, replies, hypothesis, customer_update)

    await post_room(
        room_id,
        {
            "kind": "final_summary",
            "incident_id": incident_id,
            "author": agent.handle,
            "internal_summary": internal_summary,
            "customer_update": customer_update,
            "final_answer": customer_update,
            "recorded_at": int(time.time()),
        },
    )

    events = await replay_room_with_retry(agent, room_id)
    OUTPUT_DIR.mkdir(parents=True, exist_ok=True)
    output_path = OUTPUT_DIR / f"incident-{time.strftime('%Y%m%d-%H%M%S')}.md"
    output_path.write_text(
        build_output_markdown(
            incident=incident,
            room_id=room_id,
            discovered=discovered,
            internal_summary=internal_summary,
            customer_update=customer_update,
            events=events,
        ),
        encoding="utf-8",
    )

    await agent.resolve(
        msg.session,
        json.dumps(
            {
                "status": "complete",
                "incident_id": incident_id,
                "room_id": room_id,
                "internal_summary": internal_summary,
                "customer_update": customer_update,
                "output_file": str(output_path),
                "discovered_agents": discovered,
                "total_time": round(time.monotonic() - started, 1),
            }
        ),
        content_type="application/json",
    )


async def main() -> None:
    say(agent.handle, "connecting...")
    async with agent:
        await agent.register()
        say(agent.handle, f"{GREEN}ready{RESET} — incident orchestration online")
        await agent.run_forever()


if __name__ == "__main__":
    asyncio.run(main())
