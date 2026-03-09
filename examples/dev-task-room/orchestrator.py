#!/usr/bin/env python3
"""Orchestrator for the dev-task-room example."""

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

from dev_task_common import (
    BOLD,
    CYAN,
    DIM,
    GREEN,
    RED,
    RESET,
    YELLOW,
    build_review_units,
    build_output_markdown,
    format_match_line,
    is_context_limit_error,
    parse_command_payload,
    parse_json,
    replay_room_with_retry,
    render_markdown_transcript,
    say,
    split_review_unit,
)

OUTPUT_DIR = Path(os.environ.get("OUTPUT_DIR", Path(__file__).resolve().parent / "output"))
STATE_DIR = OUTPUT_DIR / "state"
TURN_TIMEOUT = float(os.environ.get("TURN_TIMEOUT", "600"))

REQUIRED_CAPABILITIES: tuple[str, ...] = (
    "dev.implement",
    "dev.review",
    "dev.workspace.apply",
)

agent = AsyncAgent(
    "task-orchestrator",
    manifest=Manifest(
        description="Coordinates arbitrary development tasks between implementer, critic, and local workspace agent",
        commands=[
            CommandSpec(
                name="run_task",
                description="Run a development task in a shared Tailbus room",
                parameters_schema=json.dumps(
                    {
                        "type": "object",
                        "properties": {
                            "task": {"type": "string"},
                            "title": {"type": "string"},
                        },
                        "required": ["task"],
                    }
                ),
            )
        ],
        tags=["workflow", "rooms", "discovery", "development"],
        version="1.0.0",
        capabilities=["workflow.orchestrate"],
        domains=["engineering"],
        input_types=["application/json"],
        output_types=["application/json"],
    ),
    socket=os.environ.get("TAILBUS_SOCKET", "/tmp/tailbusd.sock"),
)

pending_turns: dict[str, dict[str, Any]] = {}


def short(text: str, limit: int = 92) -> str:
    return text if len(text) <= limit else text[: limit - 3] + "..."


def now_ts() -> int:
    return int(time.time())


def task_state_path(task_id: str) -> Path:
    return STATE_DIR / f"{task_id}.json"


def write_text_atomic(path: Path, content: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp_path = path.with_suffix(path.suffix + ".tmp")
    tmp_path.write_text(content, encoding="utf-8")
    tmp_path.replace(path)


def persist_task_state(state: dict[str, Any]) -> str:
    state["updated_at"] = now_ts()
    path = task_state_path(str(state["task_id"]))
    state.setdefault("artifacts", {})["state_file"] = str(path)
    try:
        write_text_atomic(path, json.dumps(state, indent=2, sort_keys=True) + "\n")
    except OSError as exc:
        say("task-state", f"{YELLOW}persist failed{RESET}: {exc}")
    return str(path)


def new_task_state(*, task_id: str, title: str, task: str) -> dict[str, Any]:
    state = {
        "version": 1,
        "task_id": task_id,
        "title": title,
        "task": task,
        "status": "starting",
        "phase": "initializing",
        "created_at": now_ts(),
        "updated_at": now_ts(),
        "room_id": "",
        "room_closed_at": None,
        "error": "",
        "discovered": [],
        "turns": [],
        "results": {},
        "artifacts": {},
    }
    persist_task_state(state)
    return state


def set_task_phase(state: dict[str, Any], *, status: str | None = None, phase: str | None = None, error: str | None = None) -> None:
    if status is not None:
        state["status"] = status
    if phase is not None:
        state["phase"] = phase
    if error is not None:
        state["error"] = error
    persist_task_state(state)


def record_discovery(state: dict[str, Any], match: dict[str, Any]) -> None:
    state["discovered"].append(
        {
            "handle": match["handle"],
            "capability": match["capability"],
            "score": match["score"],
            "reasons": list(match.get("reasons", [])),
        }
    )
    persist_task_state(state)


def begin_turn_state(
    state: dict[str, Any],
    *,
    turn_id: str,
    kind: str,
    round_no: int,
    target_handle: str,
    target_capability: str,
    instruction: str,
) -> None:
    state["turns"].append(
        {
            "turn_id": turn_id,
            "kind": kind,
            "round": round_no,
            "target_handle": target_handle,
            "target_capability": target_capability,
            "instruction": instruction,
            "status": "requested",
            "requested_at": now_ts(),
            "last_summary": "working",
            "last_progress_at": None,
            "reply_received_at": None,
            "elapsed_sec": None,
            "error": "",
        }
    )
    state["phase"] = f"waiting:{kind}"
    persist_task_state(state)


def get_turn_state(state: dict[str, Any], turn_id: str) -> dict[str, Any] | None:
    for turn in reversed(state.get("turns", [])):
        if turn.get("turn_id") == turn_id:
            return turn
    return None


def record_turn_progress(state: dict[str, Any], turn_id: str, payload: dict[str, Any]) -> None:
    turn = get_turn_state(state, turn_id)
    if turn is None:
        return
    turn["status"] = "working"
    turn["last_summary"] = str(payload.get("summary", "working"))
    turn["last_progress_at"] = now_ts()
    turn["progress_elapsed_sec"] = payload.get("elapsed_sec", 0)
    turn["progress_count"] = int(turn.get("progress_count", 0)) + 1
    persist_task_state(state)


def record_turn_reply(state: dict[str, Any], turn_id: str, payload: dict[str, Any]) -> None:
    turn = get_turn_state(state, turn_id)
    if turn is None:
        return
    turn["status"] = str(payload.get("status", "unknown"))
    turn["last_summary"] = str(payload.get("summary", turn.get("last_summary", "")))
    turn["reply_kind"] = str(payload.get("kind", ""))
    turn["reply_received_at"] = now_ts()
    turn["elapsed_sec"] = payload.get("elapsed_sec", 0)
    if payload.get("error"):
        turn["error"] = str(payload.get("error", ""))
    if payload.get("decision"):
        turn["decision"] = str(payload.get("decision", ""))
    state["phase"] = f"turn:{turn.get('kind', 'unknown')}:{turn['status']}"
    persist_task_state(state)


def record_turn_timeout(state: dict[str, Any], turn_id: str, *, seconds: float, summary: str) -> None:
    turn = get_turn_state(state, turn_id)
    if turn is None:
        return
    turn["status"] = "timeout"
    turn["last_summary"] = summary
    turn["reply_received_at"] = now_ts()
    turn["elapsed_sec"] = seconds
    turn["error"] = f"Timed out after {seconds:.0f}s without progress"
    state["phase"] = f"turn:{turn.get('kind', 'unknown')}:timeout"
    persist_task_state(state)


def record_result(state: dict[str, Any], key: str, value: Any) -> None:
    state.setdefault("results", {})[key] = value
    persist_task_state(state)


def write_failure_markdown(
    *,
    task_info: dict[str, Any],
    room_id: str,
    error: str,
    events: list[RoomEvent],
) -> str:
    OUTPUT_DIR.mkdir(parents=True, exist_ok=True)
    stamp = time.strftime("%Y%m%d-%H%M%S")
    output_file = OUTPUT_DIR / f"dev-task-{stamp}-failed.md"
    lines = [
        f"# Dev Task Room Failed — {task_info.get('title', '')}",
        "",
        f"- Room: `{room_id}`" if room_id else "- Room: `(not created)`",
        f"- Task: {task_info.get('task', '')}",
        f"- Error: {error}",
        "",
    ]
    if events:
        lines.append(render_markdown_transcript(events))
        lines.append("")
    write_text_atomic(output_file, "\n".join(lines).rstrip() + "\n")
    return str(output_file)


async def post_room(room_id: str, payload: dict[str, Any]) -> None:
    trace_id = str(payload.get("turn_id", payload.get("task_id", "")))
    await agent.post_room_message(
        room_id,
        json.dumps(payload),
        content_type="application/json",
        trace_id=trace_id,
    )


async def handle_room_event(event: RoomEvent) -> None:
    if event.event_type != "message_posted" or event.content_type != "application/json":
        return
    payload = parse_json(event.payload)
    if not payload:
        return
    turn_id = str(payload.get("turn_id", ""))
    if not turn_id:
        return
    pending = pending_turns.get(turn_id)
    if pending is None:
        return
    future = pending["future"]
    expected_kind = pending["expected_kind"]
    if future.done():
        return
    kind = str(payload.get("kind", ""))
    if kind == "turn_progress":
        pending["last_progress"] = time.monotonic()
        summary = str(payload.get("summary", "working"))
        pending["last_summary"] = summary
        elapsed = float(payload.get("elapsed_sec", 0) or 0)
        say(
            agent.handle,
            f"{CYAN}↺{RESET} @{pending.get('target_handle', '?')} r{payload.get('round', '?')} "
            f"{elapsed:.1f}s {short(summary, 120)}",
        )
        task_state = pending.get("task_state")
        if isinstance(task_state, dict):
            record_turn_progress(task_state, turn_id, payload)
        return
    if kind != expected_kind:
        return
    task_state = pending.get("task_state")
    if isinstance(task_state, dict):
        record_turn_reply(task_state, turn_id, payload)
    future.set_result(payload)


def expected_reply_kind(request_kind: str) -> str:
    if request_kind == "workspace_prepare_request":
        return "workspace_prepare_reply"
    if request_kind == "apply_request":
        return "apply_result"
    return request_kind.replace("_request", "_reply")


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


async def post_discovery(room_id: str, task_id: str, match: dict[str, Any]) -> None:
    await post_room(
        room_id,
        {
            "kind": "collaborator_discovered",
            "task_id": task_id,
            "author": agent.handle,
            "target_handle": match["handle"],
            "target_capability": match["capability"],
            "score": match["score"],
            "match_reasons": list(match["reasons"]),
            "recorded_at": int(time.time()),
        },
    )


async def request_turn(
    *,
    room_id: str,
    task_id: str,
    task_state: dict[str, Any],
    round_no: int,
    kind: str,
    target_handle: str,
    target_capability: str,
    instruction: str,
    extra: dict[str, Any] | None = None,
) -> dict[str, Any]:
    turn_id = str(uuid.uuid4())
    payload = {
        "kind": kind,
        "turn_id": turn_id,
        "task_id": task_id,
        "round": round_no,
        "target_handle": target_handle,
        "target_capability": target_capability,
        "instruction": instruction,
        "requested_by": agent.handle,
        "requested_at": int(time.time()),
    }
    if extra:
        payload.update(extra)
    future: asyncio.Future[dict[str, Any]] = asyncio.get_running_loop().create_future()
    pending_turns[turn_id] = {
        "future": future,
        "expected_kind": expected_reply_kind(kind),
        "last_progress": time.monotonic(),
        "last_summary": "working",
        "task_state": task_state,
        "target_handle": target_handle,
    }
    begin_turn_state(
        task_state,
        turn_id=turn_id,
        kind=kind,
        round_no=round_no,
        target_handle=target_handle,
        target_capability=target_capability,
        instruction=instruction,
    )
    await post_room(room_id, payload)
    say(agent.handle, f"{CYAN}→{RESET} @{target_handle} [{target_capability}]")
    say(agent.handle, f"   {DIM}{short(instruction)}{RESET}")
    try:
        while True:
            pending = pending_turns[turn_id]
            idle_for = time.monotonic() - float(pending["last_progress"])
            remaining = TURN_TIMEOUT - idle_for
            if remaining <= 0:
                raise asyncio.TimeoutError
            try:
                # Poll without cancelling the shared future so progress messages can
                # keep extending the turn budget until the real reply arrives.
                reply = await asyncio.wait_for(asyncio.shield(future), timeout=min(remaining, 5.0))
                break
            except asyncio.TimeoutError:
                continue
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
                "task_id": task_id,
                "round": round_no,
                "target_handle": target_handle,
                "target_capability": target_capability,
                "seconds": TURN_TIMEOUT,
                "recorded_at": int(time.time()),
            },
        )
        say(agent.handle, f"{RED}timeout{RESET} waiting for @{target_handle}")
        pending = pending_turns.get(turn_id, {})
        last_summary = str(pending.get("last_summary", "working"))
        say(agent.handle, f"   {DIM}last progress:{RESET} {short(last_summary, 140)}")
        record_turn_timeout(task_state, turn_id, seconds=TURN_TIMEOUT, summary=last_summary)
        return {
            "kind": expected_reply_kind(kind),
            "turn_id": turn_id,
            "author": target_handle,
            "status": "timeout",
            "capability": target_capability,
            "summary": last_summary,
            "error": f"Timed out after {TURN_TIMEOUT:.0f}s without progress",
            "elapsed_sec": TURN_TIMEOUT,
        }
    finally:
        pending_turns.pop(turn_id, None)


def ensure_ok(reply: dict[str, Any], *, label: str) -> None:
    if reply.get("status") != "ok":
        message = reply.get("error") or reply.get("summary") or "unknown error"
        raise RuntimeError(f"{label} failed: {message}")


def final_review_reply(initial: dict[str, Any], revised: dict[str, Any] | None) -> dict[str, Any]:
    return revised or initial


def change_set_paths(change_set: dict[str, Any]) -> list[str]:
    paths = [str(item.get("path", "")).strip() for item in change_set.get("files", [])]
    paths.extend(str(path).strip() for path in change_set.get("deleted_paths", []))
    return sorted(path for path in paths if path)


def review_unit_scope_label(unit: dict[str, Any]) -> str:
    ranges = [str(item) for item in unit.get("scope_ranges", []) if str(item).strip()]
    if ranges:
        return "; ".join(ranges[:2]) + (" ..." if len(ranges) > 2 else "")
    paths = [str(item) for item in unit.get("scope_paths", []) if str(item).strip()]
    return ", ".join(paths) or "unknown scope"


def serialize_review_unit(unit: dict[str, Any]) -> dict[str, Any]:
    return {
        "scope_paths": [str(item) for item in unit.get("scope_paths", [])],
        "scope_ranges": [str(item) for item in unit.get("scope_ranges", [])],
        "sections": [
            {
                "path": str(section.get("path", "")),
                "action": str(section.get("action", "")),
                "old_start_line": int(section.get("old_start_line", 0) or 0),
                "old_end_line": int(section.get("old_end_line", 0) or 0),
                "new_start_line": int(section.get("new_start_line", 0) or 0),
                "new_end_line": int(section.get("new_end_line", 0) or 0),
                "old_content": str(section.get("old_content", "")),
                "new_content": str(section.get("new_content", "")),
            }
            for section in unit.get("sections", [])
        ],
    }


def chunk_review_record(reply: dict[str, Any], unit: dict[str, Any], chunk_id: str) -> dict[str, Any]:
    return {
        "chunk_id": chunk_id,
        "summary": str(reply.get("summary", "")),
        "decision": str(reply.get("decision", "revise")),
        "findings": list(reply.get("findings", [])),
        "required_changes": list(reply.get("required_changes", [])),
        "test_gaps": list(reply.get("test_gaps", [])),
        "scope_paths": list(reply.get("coverage_paths", unit.get("scope_paths", []))),
        "scope_ranges": list(reply.get("coverage_ranges", unit.get("scope_ranges", []))),
    }


def review_split_retry_units(unit: dict[str, Any], reply: dict[str, Any]) -> list[dict[str, Any]] | None:
    error_text = str(reply.get("error", ""))
    if reply.get("status") != "error" or not is_context_limit_error(error_text):
        return None
    replacement = split_review_unit(unit)
    if len(replacement) <= 1:
        return None
    current = json.dumps(serialize_review_unit(unit), sort_keys=True)
    if all(json.dumps(serialize_review_unit(item), sort_keys=True) == current for item in replacement):
        return None
    return replacement


async def execute_chunked_review(
    *,
    room_id: str,
    task_id: str,
    task_state: dict[str, Any],
    round_no: int,
    task_text: str,
    implement_summary: str,
    change_set: dict[str, Any],
    workspace_file_map: dict[str, str],
    critic: dict[str, Any],
) -> dict[str, Any]:
    initial_units = build_review_units(change_set, workspace_file_map)
    if not initial_units:
        raise RuntimeError("review failed: change set contains no reviewable files")

    change_paths = change_set_paths(change_set)
    queue: list[dict[str, Any]] = []
    for idx, unit in enumerate(initial_units, start=1):
        queue.append({"chunk_id": f"chunk-{idx:03d}", "unit": unit, "attempt": 1})
    next_chunk_no = len(queue) + 1

    plan_items = [
        {
            "chunk_id": item["chunk_id"],
            "scope_paths": list(item["unit"].get("scope_paths", [])),
            "scope_ranges": list(item["unit"].get("scope_ranges", [])),
        }
        for item in queue
    ]
    record_result(task_state, "review_plan", plan_items)
    set_task_phase(task_state, phase="review_chunking")

    chunk_reviews: list[dict[str, Any]] = []
    while queue:
        item = queue.pop(0)
        chunk_id = str(item["chunk_id"])
        unit = dict(item["unit"])
        instruction = f"Review chunk {len(chunk_reviews) + 1}: {review_unit_scope_label(unit)}"
        reply = await request_turn(
            room_id=room_id,
            task_id=task_id,
            task_state=task_state,
            round_no=round_no,
            kind="review_request",
            target_handle=str(critic["handle"]),
            target_capability=str(critic["capability"]),
            instruction=instruction,
            extra={
                "task": task_text,
                "review_mode": "chunk",
                "chunk_id": chunk_id,
                "chunk_index": len(chunk_reviews) + 1,
                "chunk_total": len(chunk_reviews) + len(queue) + 1,
                "change_paths": change_paths,
                "review_chunk": serialize_review_unit(unit),
                "scope_paths": list(unit.get("scope_paths", [])),
                "scope_ranges": list(unit.get("scope_ranges", [])),
                "attempt": int(item.get("attempt", 1)),
            },
        )
        if reply.get("status") == "ok":
            chunk_reviews.append(chunk_review_record(reply, unit, chunk_id))
            record_result(task_state, "review_chunks", chunk_reviews)
            record_result(task_state, "review_coverage_paths", sorted({path for review in chunk_reviews for path in review["scope_paths"]}))
            continue

        replacement = review_split_retry_units(unit, reply)
        if replacement:
            say(
                agent.handle,
                f"{YELLOW}shrinking review chunk{RESET} {chunk_id} after context-limit error",
            )
            for offset, split_unit in enumerate(replacement, start=1):
                queue.insert(
                    offset - 1,
                    {
                        "chunk_id": f"chunk-{next_chunk_no:03d}",
                        "unit": split_unit,
                        "attempt": int(item.get("attempt", 1)) + 1,
                    },
                )
                next_chunk_no += 1
            continue

        ensure_ok(reply, label=f"review chunk {chunk_id}")

    set_task_phase(task_state, phase="review_synthesis")
    synthesis_reply = await request_turn(
        room_id=room_id,
        task_id=task_id,
        task_state=task_state,
        round_no=round_no,
        kind="review_request",
        target_handle=str(critic["handle"]),
        target_capability=str(critic["capability"]),
        instruction=f"Synthesize the {len(chunk_reviews)} chunk reviews into a final review decision.",
        extra={
            "task": task_text,
            "review_mode": "synthesis",
            "implement_summary": implement_summary,
            "change_paths": change_paths,
            "chunk_reviews": chunk_reviews,
            "chunk_total": len(chunk_reviews),
        },
    )
    ensure_ok(synthesis_reply, label="review synthesis")
    synthesis_reply["chunk_reviews"] = chunk_reviews
    synthesis_reply["coverage_paths"] = sorted({path for review in chunk_reviews for path in review["scope_paths"]})
    synthesis_reply["coverage_complete"] = len(synthesis_reply["coverage_paths"]) == len(set(change_paths))
    return synthesis_reply


def build_room_title(task: str) -> str:
    trimmed = task if len(task) <= 60 else task[:57] + "..."
    return f"dev-task: {trimmed}"


async def run_task_flow(task_text: str, title: str) -> dict[str, Any]:
    task_id = f"task-{uuid.uuid4().hex[:8]}"
    task_state = new_task_state(task_id=task_id, title=title or task_text, task=task_text)
    discovered = [await discover_handle(cap) for cap in REQUIRED_CAPABILITIES]
    members = [item["handle"] for item in discovered]
    room_id = ""
    room_id = await agent.create_room(build_room_title(title or task_text), members=members)
    task_state["room_id"] = room_id
    set_task_phase(task_state, status="running", phase="room_created")

    implement_reply: dict[str, Any] = {}
    review_reply: dict[str, Any] = {}
    final_implement_reply: dict[str, Any] = {}
    final_review: dict[str, Any] = {}
    apply_reply: dict[str, Any] = {}
    test_result: dict[str, Any] = {}

    try:
        await post_room(
            room_id,
            {
                "kind": "task_opened",
                "task_id": task_id,
                "title": title or task_text,
                "task": task_text,
                "author": agent.handle,
                "opened_at": int(time.time()),
            },
        )
        set_task_phase(task_state, phase="task_opened")
        for item in discovered:
            await post_discovery(room_id, task_id, item)
            say(agent.handle, format_match_line(item))
            record_discovery(task_state, item)

        workspace = next(item for item in discovered if item["capability"] == "dev.workspace.apply")
        implementer = next(item for item in discovered if item["capability"] == "dev.implement")
        critic = next(item for item in discovered if item["capability"] == "dev.review")

        prepare_reply = await request_turn(
            room_id=room_id,
            task_id=task_id,
            task_state=task_state,
            round_no=0,
            kind="workspace_prepare_request",
            target_handle=workspace["handle"],
            target_capability=workspace["capability"],
            instruction="Reset the local fixture workspace and return a snapshot of the current files.",
        )
        ensure_ok(prepare_reply, label="workspace prepare")
        workspace_snapshot = str(prepare_reply.get("workspace_snapshot", ""))
        workspace_file_map = {
            str(path): str(content) for path, content in dict(prepare_reply.get("workspace_file_map", {})).items()
        }
        record_result(
            task_state,
            "workspace",
            {
                "workspace_root": prepare_reply.get("workspace_root", ""),
                "workspace_files": prepare_reply.get("workspace_files", []),
                "workspace_file_map_paths": sorted(workspace_file_map),
                "snapshot_chars": len(workspace_snapshot),
            },
        )
        set_task_phase(task_state, phase="workspace_prepared")

        implement_reply = await request_turn(
            room_id=room_id,
            task_id=task_id,
            task_state=task_state,
            round_no=1,
            kind="implement_request",
            target_handle=implementer["handle"],
            target_capability=implementer["capability"],
            instruction="Implement the task by returning a structured whole-file change set for the bounded workspace.",
            extra={"task": task_text, "workspace_snapshot": workspace_snapshot},
        )
        ensure_ok(implement_reply, label="implementation")
        record_result(task_state, "implement_summary", implement_reply.get("summary", ""))
        set_task_phase(task_state, phase="implementation_ready")

        review_reply = await execute_chunked_review(
            room_id=room_id,
            task_id=task_id,
            task_state=task_state,
            round_no=1,
            task_text=task_text,
            implement_summary=str(implement_reply.get("summary", "")),
            change_set=dict(implement_reply.get("change_set", {})),
            workspace_file_map=workspace_file_map,
            critic=critic,
        )
        ensure_ok(review_reply, label="review")
        record_result(task_state, "review_summary", review_reply.get("summary", ""))
        record_result(task_state, "review_decision", review_reply.get("decision", ""))
        record_result(task_state, "review_chunks", review_reply.get("chunk_reviews", []))
        set_task_phase(task_state, phase="review_complete")

        final_implement_reply = implement_reply
        final_review = review_reply
        if review_reply.get("decision") == "revise":
            final_implement_reply = await request_turn(
                room_id=room_id,
                task_id=task_id,
                task_state=task_state,
                round_no=2,
                kind="implement_request",
                target_handle=implementer["handle"],
                target_capability=implementer["capability"],
                instruction="Revise the change set to address the reviewer findings exactly.",
                extra={
                    "task": task_text,
                    "workspace_snapshot": workspace_snapshot,
                    "review_findings": review_reply.get("findings", []),
                    "required_changes": review_reply.get("required_changes", []),
                    "previous_change_set": implement_reply.get("change_set", {}),
                },
            )
            ensure_ok(final_implement_reply, label="revision")
            record_result(task_state, "revision_summary", final_implement_reply.get("summary", ""))
            final_review = await execute_chunked_review(
                room_id=room_id,
                task_id=task_id,
                task_state=task_state,
                round_no=2,
                task_text=task_text,
                implement_summary=str(final_implement_reply.get("summary", "")),
                change_set=dict(final_implement_reply.get("change_set", {})),
                workspace_file_map=workspace_file_map,
                critic=critic,
            )
            ensure_ok(final_review, label="re-review")
            record_result(task_state, "final_review_summary", final_review.get("summary", ""))
            record_result(task_state, "final_review_decision", final_review.get("decision", ""))
            if final_review.get("decision") == "revise":
                raise RuntimeError("critic still requires revision after the second implementation round")

        apply_reply = await request_turn(
            room_id=room_id,
            task_id=task_id,
            task_state=task_state,
            round_no=3,
            kind="apply_request",
            target_handle=workspace["handle"],
            target_capability=workspace["capability"],
            instruction="Apply the approved change set inside the local fixture workspace and run the fixture tests.",
            extra={"change_set": final_implement_reply.get("change_set", {})},
        )
        ensure_ok(apply_reply, label="apply")
        test_result = dict(apply_reply.get("test_result", {}))
        record_result(task_state, "changed_paths", apply_reply.get("changed_paths", []))
        record_result(task_state, "test_status", test_result.get("status", "unknown"))
        record_result(task_state, "test_summary", test_result.get("summary", ""))
        set_task_phase(task_state, phase="apply_complete")

        await post_room(
            room_id,
            {
                "kind": "final_summary",
                "task_id": task_id,
                "author": agent.handle,
                "summary": final_implement_reply.get("summary", ""),
                "review_summary": final_review.get("summary", ""),
                "apply_status": apply_reply.get("status", "unknown"),
                "test_status": test_result.get("status", "unknown"),
                "recorded_at": int(time.time()),
            },
        )

        events = await replay_room_with_retry(agent, room_id)
        OUTPUT_DIR.mkdir(parents=True, exist_ok=True)
        stamp = time.strftime("%Y%m%d-%H%M%S")
        output_file = OUTPUT_DIR / f"dev-task-{stamp}.md"
        write_text_atomic(
            output_file,
            build_output_markdown(
                task_info={"title": title or task_text, "task": task_text},
                room_id=room_id,
                discovered=discovered,
                implement_reply=final_implement_reply,
                review_reply=final_review_reply(review_reply, final_review),
                apply_result=apply_reply,
                test_result=test_result,
                events=events,
            )
        )
        task_state["artifacts"]["markdown_output"] = str(output_file)
        task_state["results"]["output_file"] = str(output_file)
        task_state["results"]["room_id"] = room_id
        set_task_phase(task_state, status="complete", phase="complete", error="")
        return {
            "status": "complete",
            "room_id": room_id,
            "task_id": task_id,
            "summary": final_implement_reply.get("summary", ""),
            "review": final_review.get("summary", ""),
            "changed_paths": apply_reply.get("changed_paths", []),
            "workspace_root": apply_reply.get("workspace_root", ""),
            "test_status": test_result.get("status", "unknown"),
            "test_summary": test_result.get("summary", ""),
            "output_file": str(output_file),
            "state_file": task_state["artifacts"]["state_file"],
        }
    except Exception as exc:
        set_task_phase(task_state, status="failed", phase="failed", error=str(exc))
        events: list[RoomEvent] = []
        if room_id:
            try:
                events = await replay_room_with_retry(agent, room_id)
            except Exception:
                events = []
        failure_output = write_failure_markdown(
            task_info={"title": title or task_text, "task": task_text},
            room_id=room_id,
            error=str(exc),
            events=events,
        )
        task_state["artifacts"]["failure_output"] = failure_output
        persist_task_state(task_state)
        raise
    finally:
        if room_id:
            say(agent.handle, f"{DIM}closing room:{RESET} {room_id[:8]} status={task_state.get('status', 'unknown')}")
            try:
                await agent.close_room(room_id)
            except Exception as exc:
                task_state["results"]["room_close_error"] = str(exc)
        task_state["room_closed_at"] = now_ts()
        persist_task_state(task_state)


@agent.on_message
async def handle(msg: Message | RoomEvent) -> None:
    if isinstance(msg, RoomEvent):
        await handle_room_event(msg)
        return
    if msg.message_type != "session_open":
        return

    data = parse_command_payload(msg.payload)
    task_text = str(data.get("task") or data.get("prompt") or "").strip()
    title = str(data.get("title") or task_text).strip()
    if not task_text:
        await agent.resolve(
            msg.session,
            json.dumps({"error": "Missing task"}),
            content_type="application/json",
        )
        return

    say(agent.handle, f"task: {BOLD}{short(task_text, 120)}{RESET}")
    started = time.monotonic()
    try:
        result = await run_task_flow(task_text, title)
        result["total_time"] = round(time.monotonic() - started, 1)
        await agent.resolve(msg.session, json.dumps(result), content_type="application/json")
    except Exception as exc:
        say(agent.handle, f"{RED}error{RESET}: {exc}")
        await agent.resolve(
            msg.session,
            json.dumps({"error": str(exc), "status": "failed"}),
            content_type="application/json",
        )


async def main() -> None:
    say(agent.handle, "connecting...")
    async with agent:
        await agent.register()
        say(agent.handle, f"{GREEN}ready{RESET} — command: run_task")
        await agent.run_forever()


if __name__ == "__main__":
    asyncio.run(main())
