#!/usr/bin/env python3
"""Codex coder agent — generates code using the OpenAI Codex CLI.

Wraps `codex` CLI to leverage OpenAI's coding capabilities.

Environment variables:
    TAILBUS_SOCKET  — daemon Unix socket (default: /tmp/tailbusd.sock)
    CODEX_TIMEOUT   — max seconds for Codex CLI (default: 120)
"""

import asyncio
import json
import os
import sys
import tempfile
import time

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "../../sdk/python/src"))

from tailbus import AsyncAgent, Manifest, CommandSpec, Message

CODEX_TIMEOUT = int(os.environ.get("CODEX_TIMEOUT", "120"))

agent = AsyncAgent(
    "codex-coder",
    manifest=Manifest(
        description="Generates code for a single file using OpenAI Codex CLI",
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
        tags=["llm", "code-generation", "codex"],
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
TAG = f"  {DIM}   codex-coder{RESET}"


def say(msg: str):
    print(f"{TAG}  {msg}", flush=True)


# ── Codex CLI ────────────────────────────────────────────────────────────────

async def run_codex(prompt: str, output_file: str, timeout: int = CODEX_TIMEOUT) -> str:
    """Run codex CLI and return the result."""
    try:
        proc = await asyncio.create_subprocess_exec(
            "codex", "exec", prompt,
            "-o", output_file,
            "--ephemeral",
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
        )
        stdout, stderr = await asyncio.wait_for(proc.communicate(), timeout=timeout)

        if proc.returncode != 0:
            err = stderr.decode().strip() if stderr else "unknown error"
            if os.path.exists(output_file):
                with open(output_file, "r") as f:
                    return f.read()
            return f"[codex error] exit code {proc.returncode}: {err}"

        if os.path.exists(output_file):
            with open(output_file, "r") as f:
                return f.read()

        return stdout.decode() if stdout else "[codex error] no output produced"

    except asyncio.TimeoutError:
        proc.kill()
        return f"[codex error] timed out after {timeout}s"
    except FileNotFoundError:
        return "[codex error] 'codex' CLI not found"
    except Exception as e:
        return f"[codex error] {e}"
    finally:
        if os.path.exists(output_file):
            try:
                os.unlink(output_file)
            except OSError:
                pass


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

    output_file = os.path.join(tempfile.gettempdir(), f"codex_out_{msg.session[:8]}.txt")

    say(f"writing {BOLD}{filename}{RESET}...")
    t0 = time.monotonic()
    result = await run_codex(prompt, output_file)
    elapsed = time.monotonic() - t0

    if result.startswith("[codex error]"):
        say(f"{RED}✗{RESET} {filename} — {result} ({elapsed:.1f}s)")
        await agent.resolve(msg.session, json.dumps({"error": result}), content_type="application/json")
        return

    code = result.strip()
    if code.startswith("```"):
        code = code.split("\n", 1)[1].rsplit("```", 1)[0]

    kb = len(code) / 1024
    say(f"{GREEN}✓{RESET} {BOLD}{filename}{RESET} — {kb:.1f}kb ({elapsed:.1f}s)")
    await agent.resolve(msg.session, json.dumps({
        "filename": filename, "code": code, "model": "codex",
    }), content_type="application/json")


async def main():
    say("connecting...")
    async with agent:
        await agent.register()
        say(f"{GREEN}ready{RESET} — command: generate")
        await agent.run_forever()


if __name__ == "__main__":
    asyncio.run(main())
