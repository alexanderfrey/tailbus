#!/usr/bin/env python3
"""Room-based pair solver orchestrator.

The orchestrator controls turn-taking, but the room is the shared transcript.
Each solver rebuilds context by replaying the room instead of relying on
manually forwarded conversation history.
"""

from __future__ import annotations

import asyncio
import json
import os
import sys
import time
import uuid
from dataclasses import dataclass
from pathlib import Path
from typing import Any

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "../../sdk/python/src"))

from tailbus import AsyncAgent, CommandSpec, Manifest, Message, RoomEvent

OUTPUT_DIR = Path(os.environ.get("OUTPUT_DIR", Path(__file__).resolve().parent / "output"))
MAX_ROUNDS = int(os.environ.get("MAX_ROUNDS", "2"))
TURN_TIMEOUT = float(os.environ.get("TURN_TIMEOUT", "180"))
SOLVERS = ("codex-solver", "lmstudio-solver")

DIM = "\033[2m"
BOLD = "\033[1m"
GREEN = "\033[32m"
RED = "\033[31m"
YELLOW = "\033[33m"
CYAN = "\033[36m"
RESET = "\033[0m"
W = 68


@dataclass(slots=True)
class SolveState:
    user_session: str
    room_id: str
    problem: str
    started_at: float
    last_successful: dict[str, Any] | None = None
    last_reply: dict[str, Any] | None = None


agent = AsyncAgent(
    "orchestrator",
    manifest=Manifest(
        description="Runs a room-based collaborative solve between Codex and LM Studio",
        commands=[
            CommandSpec(
                name="solve",
                description="Solve a problem through a shared Tailbus room with alternating solver turns",
                parameters_schema=json.dumps(
                    {
                        "type": "object",
                        "properties": {
                            "problem": {
                                "type": "string",
                                "description": "Problem statement for the solvers",
                            }
                        },
                        "required": ["problem"],
                    }
                ),
            )
        ],
        tags=["orchestration", "rooms", "collaboration"],
        version="2.0.0",
    ),
    socket=os.environ.get("TAILBUS_SOCKET", "/tmp/tailbusd.sock"),
)

pending_turns: dict[str, asyncio.Future[dict[str, Any]]] = {}


def header(title: str) -> None:
    pad = max(0, W - len(title) - 3)
    print(f"\n{DIM}── {RESET}{BOLD}{title} {DIM}{'─' * pad}{RESET}\n", flush=True)


def banner(text: str) -> None:
    lines = text.split("\n")
    width = max(len(line) for line in lines) + 4
    print(f"\n{DIM}╭{'─' * width}╮{RESET}", flush=True)
    for line in lines:
        print(f"{DIM}│{RESET}  {line}{' ' * (width - len(line) - 2)}{DIM}│{RESET}", flush=True)
    print(f"{DIM}╰{'─' * width}╯{RESET}\n", flush=True)


def say(msg: str) -> None:
    print(f"  {DIM}orchestrator{RESET}  {msg}", flush=True)


def parse_json(payload: str) -> dict[str, Any] | None:
    try:
        value = json.loads(payload)
    except json.JSONDecodeError:
        return None
    return value if isinstance(value, dict) else None


def trim(text: str, limit: int = 80) -> str:
    return text if len(text) <= limit else text[: limit - 3] + "..."


async def post_room_payload(room_id: str, payload: dict[str, Any]) -> None:
    await agent.post_room_message(
        room_id,
        json.dumps(payload),
        content_type="application/json",
        trace_id=str(payload.get("turn_id", "")),
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


async def request_turn(state: SolveState, *, target: str, round_no: int, instruction: str, response_type: str) -> dict[str, Any]:
    turn_id = str(uuid.uuid4())
    payload = {
        "kind": "turn_request",
        "turn_id": turn_id,
        "round": round_no,
        "target": target,
        "problem": state.problem,
        "instruction": instruction,
        "response_type": response_type,
        "requested_by": agent.handle,
        "requested_at": int(time.time()),
    }
    future: asyncio.Future[dict[str, Any]] = asyncio.get_running_loop().create_future()
    pending_turns[turn_id] = future
    await post_room_payload(state.room_id, payload)
    say(f"{CYAN}→{RESET} @{target} [{response_type}]")
    say(f"   {DIM}{trim(instruction, 96)}{RESET}")
    try:
        reply = await asyncio.wait_for(future, timeout=TURN_TIMEOUT)
        state.last_reply = reply
        if reply.get("status") == "ok" and reply.get("content"):
            state.last_successful = reply
            say(f"{GREEN}←{RESET} @{target} ({reply.get('elapsed_sec', 0):.1f}s)")
        else:
            say(f"{YELLOW}!{RESET} @{target} returned {reply.get('status', 'unknown')}")
        return reply
    except asyncio.TimeoutError:
        timeout_payload = {
            "kind": "turn_timeout",
            "turn_id": turn_id,
            "target": target,
            "round": round_no,
            "seconds": TURN_TIMEOUT,
            "recorded_at": int(time.time()),
        }
        await post_room_payload(state.room_id, timeout_payload)
        say(f"{RED}✗{RESET} @{target} timed out after {TURN_TIMEOUT:.0f}s")
        reply = {
            "kind": "solver_reply",
            "turn_id": turn_id,
            "author": target,
            "round": round_no,
            "response_type": response_type,
            "status": "timeout",
            "content": "",
            "error": f"Timed out after {TURN_TIMEOUT:.0f}s",
        }
        state.last_reply = reply
        return reply
    finally:
        pending_turns.pop(turn_id, None)


def select_final_text(state: SolveState) -> str:
    if state.last_reply and state.last_reply.get("status") == "ok" and state.last_reply.get("content"):
        return str(state.last_reply["content"])
    if state.last_successful and state.last_successful.get("content"):
        return str(state.last_successful["content"])
    if state.last_reply and state.last_reply.get("error"):
        return f"Pair solver did not produce a successful answer.\n\nLast error: {state.last_reply['error']}"
    return "Pair solver did not produce a successful answer."


def transcript_lines(events: list[RoomEvent]) -> list[str]:
    lines: list[str] = []
    for event in events:
        if event.event_type != "message_posted":
            lines.append(f"- seq {event.room_seq}: {event.event_type} by `{event.sender_handle}`")
            continue
        payload = parse_json(event.payload)
        if not payload:
            text = event.payload.strip() or "<empty>"
            lines.append(f"- seq {event.room_seq}: `{event.sender_handle}` posted `{trim(text, 120)}`")
            continue
        kind = payload.get("kind", "unknown")
        if kind == "problem_opened":
            lines.append(f"- seq {event.room_seq}: problem opened by `{event.sender_handle}`")
        elif kind == "turn_request":
            lines.append(
                f"- seq {event.room_seq}: turn request to `{payload.get('target', '?')}`"
                f" round {payload.get('round', '?')} [{payload.get('response_type', '?')}]"
            )
            lines.append(f"  instruction: {payload.get('instruction', '')}")
        elif kind == "solver_reply":
            status = payload.get("status", "unknown")
            lines.append(
                f"- seq {event.room_seq}: reply from `{payload.get('author', event.sender_handle)}`"
                f" round {payload.get('round', '?')} status `{status}`"
            )
            content = str(payload.get("content", "")).strip()
            if content:
                lines.append(content)
        elif kind == "turn_timeout":
            lines.append(
                f"- seq {event.room_seq}: timeout waiting for `{payload.get('target', '?')}`"
                f" in round {payload.get('round', '?')}"
            )
        elif kind == "final_summary":
            lines.append(f"- seq {event.room_seq}: final summary selected by `{event.sender_handle}`")
            content = str(payload.get("final_answer", "")).strip()
            if content:
                lines.append(content)
        else:
            lines.append(f"- seq {event.room_seq}: `{event.sender_handle}` posted `{kind}`")
    return lines


def write_output(problem: str, room_id: str, events: list[RoomEvent], final_answer: str) -> str:
    OUTPUT_DIR.mkdir(parents=True, exist_ok=True)
    stamp = time.strftime("%Y%m%d-%H%M%S")
    out_path = OUTPUT_DIR / f"solution-{stamp}.md"
    with out_path.open("w", encoding="utf-8") as handle:
        handle.write(f"# Problem\n\n{problem}\n\n")
        handle.write(f"# Room\n\n- room_id: `{room_id}`\n- retained_events: {len(events)}\n\n")
        handle.write(f"# Final Answer\n\n{final_answer}\n\n")
        handle.write("# Transcript\n\n")
        for line in transcript_lines(events):
            handle.write(f"{line}\n")
    return str(out_path)


async def run_pair_solve(state: SolveState) -> str:
    await post_room_payload(
        state.room_id,
        {
            "kind": "problem_opened",
            "problem": state.problem,
            "opened_by": agent.handle,
            "max_rounds": MAX_ROUNDS,
            "opened_at": int(time.time()),
        },
    )

    header("Room")
    say(f"room {CYAN}{state.room_id}{RESET}")
    say(f"members: {', '.join('@' + handle for handle in ('orchestrator',) + SOLVERS)}")

    for round_no in range(1, MAX_ROUNDS + 1):
        header(f"Round {round_no}")

        if round_no == 1:
            codex_instruction = "You go first. Propose an initial solution with brief reasoning."
        else:
            codex_instruction = "Read the room transcript. Refine LM Studio's latest answer and tighten correctness."
        await request_turn(
            state,
            target="codex-solver",
            round_no=round_no,
            instruction=codex_instruction,
            response_type="proposal" if round_no == 1 else "refinement",
        )

        if round_no == MAX_ROUNDS:
            lm_instruction = "Read the room transcript and give the final answer. Be concise and complete."
            response_type = "final"
        else:
            lm_instruction = "Read the room transcript. Critique Codex's latest answer and improve it."
            response_type = "critique"
        await request_turn(
            state,
            target="lmstudio-solver",
            round_no=round_no,
            instruction=lm_instruction,
            response_type=response_type,
        )

    final_answer = select_final_text(state)
    await post_room_payload(
        state.room_id,
        {
            "kind": "final_summary",
            "selected_by": agent.handle,
            "final_answer": final_answer,
            "selected_at": int(time.time()),
        },
    )
    return final_answer


async def handle_user_request(msg: Message) -> None:
    try:
        data = json.loads(msg.payload)
        args = data.get("arguments", data)
        if isinstance(args, str):
            args = json.loads(args)
        problem = str(args.get("problem", "")).strip()
    except (AttributeError, TypeError, json.JSONDecodeError):
        await agent.resolve(
            msg.session,
            json.dumps({"error": "Expected JSON with a 'problem' field"}),
            content_type="application/json",
        )
        return

    if not problem:
        await agent.resolve(
            msg.session,
            json.dumps({"error": "No problem provided"}),
            content_type="application/json",
        )
        return

    banner(f"Problem: \"{trim(problem, 72)}\"")
    started = time.monotonic()

    try:
        room_id = await agent.create_room(f"pair-solver: {trim(problem, 48)}", members=list(SOLVERS))
        state = SolveState(user_session=msg.session, room_id=room_id, problem=problem, started_at=started)
        final_answer = await run_pair_solve(state)
        await agent.close_room(room_id)
        events = await agent.replay_room(room_id)
        out_file = write_output(problem, room_id, events, final_answer)
        elapsed = time.monotonic() - started
        header("Result")
        for line in final_answer.splitlines() or [final_answer]:
            print(f"  {line}", flush=True)
        print(f"\n  {DIM}saved to {out_file}{RESET}", flush=True)
        banner(f"Done in {elapsed:.1f}s")
        await agent.resolve(
            msg.session,
            json.dumps(
                {
                    "status": "complete",
                    "room_id": room_id,
                    "solution": final_answer,
                    "output_file": out_file,
                    "rounds": MAX_ROUNDS,
                    "total_time": round(elapsed, 1),
                }
            ),
            content_type="application/json",
        )
    except Exception as exc:
        await agent.resolve(
            msg.session,
            json.dumps({"error": str(exc)}),
            content_type="application/json",
        )


@agent.on_message
async def handle(msg: Message | RoomEvent) -> None:
    if isinstance(msg, RoomEvent):
        await handle_room_event(msg)
        return
    if msg.message_type != "session_open":
        return
    await handle_user_request(msg)


async def main() -> None:
    banner("Pair Solver — room orchestrator")
    say(f"solvers: {', '.join('@' + solver for solver in SOLVERS)}")
    say(f"max_rounds: {MAX_ROUNDS}")
    say(f"turn_timeout: {TURN_TIMEOUT:.0f}s")
    say(f"output: {OUTPUT_DIR}")
    async with agent:
        await agent.register()
        say("registered, waiting for problems...")
        await agent.run_forever()


if __name__ == "__main__":
    asyncio.run(main())
