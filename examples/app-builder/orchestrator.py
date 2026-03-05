#!/usr/bin/env python3
"""Orchestrator agent — coordinates 3 AI coders to build an app collaboratively.

Flow:
  1. Plan: Ask @lmstudio-coder to break the app into files
  2. Code: For each file, delegate to all 3 coders in parallel
  3. Consensus: Ask @lmstudio-coder to merge/pick the best
  4. Write: Save merged files to disk

Environment variables:
    TAILBUS_SOCKET  — daemon Unix socket (default: /tmp/tailbusd.sock)
    OUTPUT_DIR      — base output directory (default: ./output)
"""

import asyncio
import json
import os
import re
import sys
import time

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "../../sdk/python/src"))

from tailbus import AsyncAgent, Manifest, CommandSpec, Message

OUTPUT_DIR = os.environ.get("OUTPUT_DIR", os.path.join(os.path.dirname(__file__), "output"))

CODERS = ["claude-coder", "codex-coder", "lmstudio-coder"]

agent = AsyncAgent(
    "orchestrator",
    manifest=Manifest(
        description="Coordinates 3 AI coders to collaboratively build an app",
        commands=[
            CommandSpec(
                name="build",
                description="Build an app from a natural language description",
                parameters_schema=json.dumps({
                    "type": "object",
                    "properties": {
                        "app": {
                            "type": "string",
                            "description": "Natural language description of the app to build",
                        },
                    },
                    "required": ["app"],
                }),
            ),
        ],
        tags=["orchestration", "code-generation"],
        version="1.0.0",
    ),
    socket=os.environ.get("TAILBUS_SOCKET", "/tmp/tailbusd.sock"),
)

# Track delegated sessions
pending: dict[str, asyncio.Future] = {}

# ── Logging ──────────────────────────────────────────────────────────────────

DIM = "\033[2m"
BOLD = "\033[1m"
GREEN = "\033[32m"
RED = "\033[31m"
YELLOW = "\033[33m"
CYAN = "\033[36m"
RESET = "\033[0m"
W = 62  # output width


def header(title: str):
    """Print a section header."""
    pad = W - len(title) - 3
    print(f"\n{DIM}── {RESET}{BOLD}{title} {DIM}{'─' * pad}{RESET}\n", flush=True)


def banner(text: str):
    """Print a boxed banner."""
    lines = text.split("\n")
    w = max(len(l) for l in lines) + 4
    print(f"\n{DIM}╭{'─' * w}╮{RESET}", flush=True)
    for line in lines:
        print(f"{DIM}│{RESET}  {line}{' ' * (w - len(line) - 2)}{DIM}│{RESET}", flush=True)
    print(f"{DIM}╰{'─' * w}╯{RESET}\n", flush=True)


def say(msg: str):
    """Orchestrator log line."""
    print(f"  {DIM}orchestrator{RESET}  {msg}", flush=True)


def arrow_to(targets: str, desc: str):
    """Show an outbound message."""
    print(f"  {DIM}orchestrator{RESET}  {CYAN}→{RESET} {targets}", flush=True)
    if desc:
        print(f"               {DIM}{desc}{RESET}", flush=True)


def arrow_from(source: str, desc: str):
    """Show an inbound response."""
    print(f"  {DIM}orchestrator{RESET}  {GREEN}←{RESET} {source}: {desc}", flush=True)


def file_header(filename: str, description: str = ""):
    """Print a sub-section for a file."""
    desc_text = f"  {DIM}— {description}{RESET}" if description else ""
    print(f"\n  {DIM}┌{RESET} {BOLD}{filename}{RESET}{desc_text}", flush=True)


def file_footer():
    print(f"  {DIM}└{'─' * (W - 3)}{RESET}", flush=True)


# ── Handler ──────────────────────────────────────────────────────────────────

@agent.on_message
async def handle(msg: Message):
    """Handle responses from coders, or a new build request."""
    if msg.session in pending:
        fut = pending.pop(msg.session)
        if not fut.done():
            fut.set_result(msg.payload)
        return

    if msg.message_type != "session_open":
        return

    t_total = time.monotonic()

    try:
        data = json.loads(msg.payload)
        args = data.get("arguments", data)
        if isinstance(args, str):
            args = json.loads(args)
        app_spec = args.get("app", "")
    except (json.JSONDecodeError, AttributeError, TypeError):
        await agent.resolve(msg.session, json.dumps({
            "error": "Expected JSON with 'app' field describing the app to build",
        }), content_type="application/json")
        return

    if not app_spec:
        await agent.resolve(msg.session, json.dumps({"error": "No app description provided"}), content_type="application/json")
        return

    # Truncate for display
    display_spec = app_spec if len(app_spec) <= 70 else app_spec[:67] + "..."
    banner(f"Building: \"{display_spec}\"")

    # ── Step 1: Plan ──

    header("Planning")
    arrow_to("@lmstudio-coder", "Break this app into files and describe each one.")

    t1 = time.monotonic()
    try:
        raw = await delegate("lmstudio-coder", json.dumps({
            "command": "plan",
            "arguments": {"app_spec": app_spec},
        }), timeout=120)
        plan_data = json.loads(raw)
        if "error" in plan_data:
            raise ValueError(plan_data["error"])
        file_manifest = plan_data.get("files", [])
    except asyncio.TimeoutError:
        say(f"{RED}✗{RESET} @lmstudio-coder timed out after {time.monotonic()-t1:.0f}s")
        await agent.resolve(msg.session, json.dumps({"error": "Planning timed out"}), content_type="application/json")
        return
    except Exception as e:
        say(f"{RED}✗{RESET} planning failed: {e}")
        await agent.resolve(msg.session, json.dumps({"error": f"Planning failed: {e}"}), content_type="application/json")
        return

    if not file_manifest:
        say(f"{RED}✗{RESET} planner returned no files")
        await agent.resolve(msg.session, json.dumps({"error": "Empty file manifest"}), content_type="application/json")
        return

    print(flush=True)
    arrow_from("@lmstudio-coder", f"{len(file_manifest)} files planned ({time.monotonic()-t1:.1f}s)")
    for i, f in enumerate(file_manifest, 1):
        desc = f.get("description", "")
        print(f"               {DIM}{i}. {RESET}{f['filename']}  {DIM}{desc}{RESET}", flush=True)

    # ── Step 2: Code ──

    header("Coding")
    targets = ", ".join(f"@{c}" for c in CODERS)
    say(f"sending each file to {targets} in parallel\n")

    t2 = time.monotonic()
    file_results = {}

    for file_spec in file_manifest:
        filename = file_spec["filename"]
        description = file_spec.get("description", "")
        dependencies = file_spec.get("dependencies", [])

        file_header(filename, description)
        print(f"  {DIM}│{RESET}", flush=True)

        payload = json.dumps({
            "command": "generate",
            "arguments": {
                "filename": filename,
                "description": description,
                "app_spec": app_spec,
                "dependencies": dependencies,
            },
        })

        for coder in CODERS:
            print(f"  {DIM}│{RESET}  {DIM}orchestrator{RESET}  {CYAN}→{RESET} @{coder}", flush=True)

        print(f"  {DIM}│{RESET}", flush=True)

        tasks = [delegate(coder, payload, timeout=120) for coder in CODERS]
        results = await asyncio.gather(*tasks, return_exceptions=True)

        implementations = []
        for coder, result in zip(CODERS, results):
            if isinstance(result, Exception):
                print(f"  {DIM}│{RESET}  {DIM}{coder:>16}{RESET}  {RED}✗{RESET} {type(result).__name__}", flush=True)
                continue
            try:
                result_data = json.loads(result)
                if "error" in result_data:
                    err = result_data["error"]
                    short = err[:60] + "..." if len(err) > 60 else err
                    print(f"  {DIM}│{RESET}  {DIM}{coder:>16}{RESET}  {RED}✗{RESET} {short}", flush=True)
                    continue
                code = result_data.get("code", "")
                kb = len(code) / 1024
                implementations.append({"model": result_data.get("model", coder), "code": code})
                print(f"  {DIM}│{RESET}  {DIM}{coder:>16}{RESET}  {GREEN}✓{RESET} {kb:.1f}kb", flush=True)
            except json.JSONDecodeError:
                print(f"  {DIM}│{RESET}  {DIM}{coder:>16}{RESET}  {RED}✗{RESET} invalid response", flush=True)

        print(f"  {DIM}│{RESET}", flush=True)
        n = len(implementations)
        color = GREEN if n == len(CODERS) else YELLOW if n > 0 else RED
        print(f"  {DIM}│{RESET}  {color}{n}/{len(CODERS)}{RESET} implementations received", flush=True)

        file_results[filename] = {"implementations": implementations, "description": description}
        file_footer()

    # ── Step 3: Consensus ──

    header("Consensus")

    t3 = time.monotonic()
    merged_files = {}

    for filename, data in file_results.items():
        implementations = data["implementations"]

        if not implementations:
            say(f"{filename}: {RED}no implementations — skipping{RESET}")
            continue

        if len(implementations) == 1:
            model = implementations[0]["model"]
            merged_files[filename] = {"code": implementations[0]["code"], "winner": model}
            say(f"{filename}: only @{model} responded — using directly")
            continue

        models_desc = ", ".join(
            f"@{impl['model']} ({len(impl['code'])/1024:.1f}kb)"
            for impl in implementations
        )
        file_header(filename)
        print(f"  {DIM}│{RESET}", flush=True)
        print(f"  {DIM}│{RESET}  {DIM}orchestrator{RESET}  {CYAN}→{RESET} @lmstudio-coder", flush=True)
        print(f"  {DIM}│{RESET}               {DIM}merge {len(implementations)} versions: {models_desc}{RESET}", flush=True)
        print(f"  {DIM}│{RESET}", flush=True)

        tm = time.monotonic()
        try:
            raw = await delegate("lmstudio-coder", json.dumps({
                "command": "merge",
                "arguments": {
                    "filename": filename,
                    "implementations": implementations,
                    "app_spec": app_spec,
                    "file_description": data["description"],
                },
            }), timeout=120)
            merge_data = json.loads(raw)
            if "error" in merge_data:
                winner = implementations[0]["model"] + " (fallback)"
                merged_files[filename] = {"code": implementations[0]["code"], "winner": winner}
                print(f"  {DIM}│{RESET}  {DIM}  lmstudio-coder{RESET}  {YELLOW}!{RESET} merge failed — using @{implementations[0]['model']}", flush=True)
            else:
                winner = merge_data.get("winner", "merged")
                merged_files[filename] = {"code": merge_data.get("code", ""), "winner": winner}
                print(f"  {DIM}│{RESET}  {DIM}  lmstudio-coder{RESET}  {GREEN}✓{RESET} winner: {winner} ({time.monotonic()-tm:.1f}s)", flush=True)
        except asyncio.TimeoutError:
            winner = implementations[0]["model"] + " (fallback)"
            merged_files[filename] = {"code": implementations[0]["code"], "winner": winner}
            print(f"  {DIM}│{RESET}  {DIM}  lmstudio-coder{RESET}  {RED}✗{RESET} timed out — using @{implementations[0]['model']}", flush=True)
        except Exception as e:
            winner = implementations[0]["model"] + " (fallback)"
            merged_files[filename] = {"code": implementations[0]["code"], "winner": winner}
            print(f"  {DIM}│{RESET}  {DIM}  lmstudio-coder{RESET}  {RED}✗{RESET} error — using @{implementations[0]['model']}", flush=True)

        file_footer()

    # ── Step 4: Write ──

    header("Writing")

    slug = re.sub(r'[^a-z0-9]+', '-', app_spec.lower()[:50]).strip('-')
    out_dir = os.path.join(OUTPUT_DIR, slug)
    os.makedirs(out_dir, exist_ok=True)

    written = []
    for filename, data in merged_files.items():
        filepath = os.path.join(out_dir, filename)
        os.makedirs(os.path.dirname(filepath), exist_ok=True)
        with open(filepath, "w") as f:
            f.write(data["code"])
        kb = len(data["code"]) / 1024
        written.append({"filename": filename, "winner": data["winner"], "size": len(data["code"])})
        print(f"  {GREEN}✓{RESET} {filename:<24} {kb:>5.1f}kb  {DIM}winner: {data['winner']}{RESET}", flush=True)

    print(f"\n  {DIM}→{RESET} {out_dir}", flush=True)

    elapsed = time.monotonic() - t_total
    banner(f"Done — {len(written)} files in {elapsed:.1f}s")

    await agent.resolve(msg.session, json.dumps({
        "status": "complete",
        "output_dir": out_dir,
        "files": written,
        "total_time": round(elapsed, 1),
        "app_spec": app_spec,
    }), content_type="application/json")


async def delegate(target: str, payload: str, timeout: float = 120) -> str:
    """Open a session to a target agent and wait for the response."""
    opened = await agent.open_session(target, payload, content_type="application/json")
    fut: asyncio.Future = asyncio.get_running_loop().create_future()
    pending[opened.session] = fut
    try:
        return await asyncio.wait_for(fut, timeout=timeout)
    except asyncio.TimeoutError:
        pending.pop(opened.session, None)
        raise


async def main():
    banner("App Builder — orchestrator")
    say(f"coders: {', '.join('@' + c for c in CODERS)}")
    say(f"output: {OUTPUT_DIR}")
    async with agent:
        await agent.register()
        say("registered, waiting for build requests...")
        await agent.run_forever()


if __name__ == "__main__":
    asyncio.run(main())
