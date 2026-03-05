#!/usr/bin/env python3
"""Claude Code coder agent — generates code using the Claude Code CLI.

Wraps `claude -p` to leverage Claude's full agentic coding capabilities.

Environment variables:
    TAILBUS_SOCKET  — daemon Unix socket (default: /tmp/tailbusd.sock)
    CLAUDE_TIMEOUT  — max seconds for Claude CLI (default: 120)
"""

import asyncio
import json
import os
import sys
import time

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "../../sdk/python/src"))

from tailbus import AsyncAgent, Manifest, CommandSpec, Message

CLAUDE_TIMEOUT = int(os.environ.get("CLAUDE_TIMEOUT", "120"))

agent = AsyncAgent(
    "claude-coder",
    manifest=Manifest(
        description="Generates code for a single file using Claude Code CLI",
        commands=[
            CommandSpec(
                name="generate",
                description="Generate code for a file given its spec",
                parameters_schema=json.dumps({
                    "type": "object",
                    "properties": {
                        "filename": {"type": "string"},
                        "description": {"type": "string"},
                        "app_spec": {"type": "string"},
                        "dependencies": {"type": "array", "items": {"type": "string"}},
                    },
                    "required": ["filename", "description", "app_spec"],
                }),
            ),
        ],
        tags=["llm", "code-generation", "claude"],
        version="1.0.0",
    ),
    socket=os.environ.get("TAILBUS_SOCKET", "/tmp/tailbusd.sock"),
)

# ── Logging ──────────────────────────────────────────────────────────────────

DIM = "\033[2m"
BOLD = "\033[1m"
GREEN = "\033[32m"
RED = "\033[31m"
RESET = "\033[0m"
TAG = f"  {DIM}  claude-coder{RESET}"


def say(msg: str):
    print(f"{TAG}  {msg}", flush=True)


# ── Claude CLI ───────────────────────────────────────────────────────────────

async def run_claude(prompt: str, timeout: int = CLAUDE_TIMEOUT) -> str:
    """Run claude -p and return the result text."""
    try:
        proc = await asyncio.create_subprocess_exec(
            "claude", "-p", prompt,
            "--output-format", "json",
            "--dangerously-skip-permissions",
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
        )
        stdout, stderr = await asyncio.wait_for(proc.communicate(), timeout=timeout)

        if proc.returncode != 0:
            err = stderr.decode().strip() if stderr else "unknown error"
            return f"[claude error] exit code {proc.returncode}: {err}"

        try:
            output = json.loads(stdout.decode())
            if isinstance(output, dict):
                return output.get("result", output.get("text", stdout.decode()))
            return stdout.decode()
        except json.JSONDecodeError:
            return stdout.decode()

    except asyncio.TimeoutError:
        proc.kill()
        return f"[claude error] timed out after {timeout}s"
    except FileNotFoundError:
        return "[claude error] 'claude' CLI not found"
    except Exception as e:
        return f"[claude error] {e}"


# ── Handler ──────────────────────────────────────────────────────────────────

@agent.on_message
async def handle(msg: Message):
    if msg.message_type != "session_open":
        return

    try:
        data = json.loads(msg.payload)
    except json.JSONDecodeError:
        await agent.resolve(msg.session, json.dumps({"error": "Invalid JSON"}), content_type="application/json")
        return

    args = data.get("arguments", data)
    if isinstance(args, str):
        try:
            args = json.loads(args)
        except (json.JSONDecodeError, AttributeError):
            pass

    filename = args.get("filename", "unknown")
    description = args.get("description", "")
    app_spec = args.get("app_spec", "")
    dependencies = args.get("dependencies", [])

    deps_text = ", ".join(dependencies) if dependencies else "none"
    prompt = (
        f"Generate the complete contents of a file called `{filename}`.\n\n"
        f"App description: {app_spec}\n\n"
        f"This file's purpose: {description}\n\n"
        f"Dependencies/related files: {deps_text}\n\n"
        f"Return ONLY the file contents — no markdown fences, no filename header, "
        f"no explanation. Just the raw code/markup that should be in the file."
    )

    say(f"writing {BOLD}{filename}{RESET}...")
    t0 = time.monotonic()
    result = await run_claude(prompt)
    elapsed = time.monotonic() - t0

    if result.startswith("[claude error]"):
        say(f"{RED}✗{RESET} {filename} — {result} ({elapsed:.1f}s)")
        await agent.resolve(msg.session, json.dumps({"error": result}), content_type="application/json")
        return

    code = result.strip()
    if code.startswith("```"):
        code = code.split("\n", 1)[1].rsplit("```", 1)[0]

    kb = len(code) / 1024
    say(f"{GREEN}✓{RESET} {BOLD}{filename}{RESET} — {kb:.1f}kb ({elapsed:.1f}s)")
    await agent.resolve(msg.session, json.dumps({
        "filename": filename, "code": code, "model": "claude",
    }), content_type="application/json")


async def main():
    say("connecting...")
    async with agent:
        await agent.register()
        say(f"{GREEN}ready{RESET} — command: generate")
        await agent.run_forever()


if __name__ == "__main__":
    asyncio.run(main())
