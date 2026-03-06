#!/usr/bin/env python3
"""Support-facing intake agent for the incident-room demo."""

from __future__ import annotations

import asyncio
import json
import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "../../sdk/python/src"))

from tailbus import AsyncAgent, CommandSpec, Manifest, Message

from incident_common import GREEN, RED, RESET, parse_command_payload, say

agent = AsyncAgent(
    "support-triage",
    manifest=Manifest(
        description="Accepts customer incident reports and routes them to the incident orchestrator",
        commands=[
            CommandSpec(
                name="report_incident",
                description="Open a new incident from a customer-facing symptom report",
                parameters_schema=json.dumps(
                    {
                        "type": "object",
                        "properties": {
                            "incident": {"type": "string"},
                            "severity": {"type": "string"},
                            "region": {"type": "string"},
                            "customer_impact": {"type": "string"},
                            "account_id": {"type": "string"},
                        },
                        "required": ["incident"],
                    }
                ),
            )
        ],
        tags=["support", "incident", "intake"],
        version="1.0.0",
        capabilities=["incident.intake"],
        domains=["support"],
        input_types=["application/json", "text/plain"],
        output_types=["application/json"],
    ),
    socket=os.environ.get("TAILBUS_SOCKET", "/tmp/tailbusd.sock"),
)

pending: dict[str, asyncio.Future[str]] = {}


@agent.on_message
async def handle(msg: Message) -> None:
    if msg.session in pending:
        future = pending.pop(msg.session)
        if not future.done():
            future.set_result(msg.payload)
        return

    if msg.message_type != "session_open":
        return

    args = parse_command_payload(msg.payload)
    incident_text = str(args.get("incident") or args.get("title") or msg.payload).strip()
    if not incident_text:
        await agent.resolve(
            msg.session,
            json.dumps({"error": "incident text is required"}),
            content_type="application/json",
        )
        return

    say(agent.handle, f"intake: {incident_text}")
    matches = await agent.find_handles(capabilities=["incident.orchestrate"], limit=1)
    if not matches:
        await agent.resolve(
            msg.session,
            json.dumps({"error": "no incident orchestrator discovered on the mesh"}),
            content_type="application/json",
        )
        return
    orchestrator = matches[0]
    say(agent.handle, f"discovered @{orchestrator.handle} via incident.orchestrate")

    opened = await agent.open_session(
        orchestrator.handle,
        json.dumps(
            {
                "command": "open_incident",
                "arguments": {
                    "title": args.get("title") or incident_text,
                    "severity": args.get("severity", "sev2"),
                    "region": args.get("region", "eu-west"),
                    "customer_impact": args.get("customer_impact") or incident_text,
                    "account_id": args.get("account_id", "acct-demo-4812"),
                    "reported_by": agent.handle,
                },
            }
        ),
        content_type="application/json",
    )
    future: asyncio.Future[str] = asyncio.get_running_loop().create_future()
    pending[opened.session] = future
    try:
        result = await asyncio.wait_for(future, timeout=180)
    except asyncio.TimeoutError:
        pending.pop(opened.session, None)
        say(agent.handle, f"{RED}timeout{RESET} waiting for @{orchestrator.handle}")
        await agent.resolve(
            msg.session,
            json.dumps({"error": "incident orchestrator timed out"}),
            content_type="application/json",
        )
        return

    say(agent.handle, f"{GREEN}resolved{RESET} incident via @{orchestrator.handle}")
    await agent.resolve(msg.session, result, content_type="application/json")


async def main() -> None:
    say(agent.handle, "connecting...")
    async with agent:
        await agent.register()
        say(agent.handle, f"{GREEN}ready{RESET} — accepting incident reports")
        await agent.run_forever()


if __name__ == "__main__":
    asyncio.run(main())
