#!/usr/bin/env python3
"""LM Studio coder agent — plans, generates code, and merges implementations.

Uses a local LM Studio instance (OpenAI-compatible API) for all LLM work.
This agent handles three roles:
  - plan: Break an app spec into a file manifest
  - generate: Write code for a single file
  - merge: Pick/merge the best from multiple implementations

Environment variables:
    TAILBUS_SOCKET  — daemon Unix socket (default: /tmp/tailbusd.sock)
    LLM_BASE_URL    — OpenAI-compatible API (default: http://localhost:1234/v1)
    LLM_MODEL       — model name (default: whatever is loaded in LM Studio)
"""

import asyncio
import json
import os
import sys
import time
import urllib.request
import urllib.error

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "../../sdk/python/src"))

from tailbus import AsyncAgent, Manifest, CommandSpec, Message

LLM_BASE_URL = os.environ.get("LLM_BASE_URL", "http://localhost:1234/v1")
LLM_MODEL = os.environ.get("LLM_MODEL", "")

agent = AsyncAgent(
    "lmstudio-coder",
    manifest=Manifest(
        description="Plans, generates code, and merges implementations using a local LLM",
        commands=[
            CommandSpec(
                name="plan",
                description="Break an app spec into a JSON file manifest",
                parameters_schema=json.dumps({
                    "type": "object",
                    "properties": {
                        "app_spec": {"type": "string", "description": "Description of the app to build"},
                    },
                    "required": ["app_spec"],
                }),
            ),
            CommandSpec(
                name="generate",
                description="Generate code for a single file",
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
            CommandSpec(
                name="merge",
                description="Pick or merge the best from multiple implementations",
                parameters_schema=json.dumps({
                    "type": "object",
                    "properties": {
                        "filename": {"type": "string"},
                        "implementations": {
                            "type": "array",
                            "items": {
                                "type": "object",
                                "properties": {
                                    "model": {"type": "string"},
                                    "code": {"type": "string"},
                                },
                            },
                        },
                        "app_spec": {"type": "string"},
                        "file_description": {"type": "string"},
                    },
                    "required": ["filename", "implementations"],
                }),
            ),
        ],
        tags=["llm", "code-generation"],
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
TAG = f"  {DIM}lmstudio-coder{RESET}"


def say(msg: str):
    print(f"{TAG}  {msg}", flush=True)


# ── LLM ──────────────────────────────────────────────────────────────────────

PLAN_SYSTEM = """You are a software architect. Given an app description, break it into files.

Return ONLY a JSON array. Each element:
{"filename": "path/to/file.ext", "description": "What this file does", "dependencies": ["other/file.ext"]}

Rules:
- Include all necessary files (HTML, CSS, JS, config, etc.)
- Order so dependencies come first
- Keep it minimal — no unnecessary files
- No markdown fences, no explanation — just the JSON array"""

GENERATE_SYSTEM = """You are an expert programmer. Generate the complete code for a single file.

Return ONLY the file contents — no markdown fences, no explanation, no filename header.
The code must be production-ready, well-structured, and complete."""

MERGE_SYSTEM = """You are a senior code reviewer. You are given multiple implementations of the same file from different AI coders.

Your job:
1. Compare all implementations
2. Pick the best one OR merge the strongest parts from each
3. Return ONLY the final merged code — no markdown fences, no explanation

Choose based on: correctness, completeness, code quality, and adherence to the spec."""


def llm_call(system: str, user: str, temperature: float = 0.3, max_tokens: int = 4096) -> str:
    """Call LM Studio (blocking — run in executor)."""
    body: dict = {
        "messages": [
            {"role": "system", "content": system},
            {"role": "user", "content": user},
        ],
        "temperature": temperature,
        "max_tokens": max_tokens,
    }
    if LLM_MODEL:
        body["model"] = LLM_MODEL

    data = json.dumps(body).encode()
    req = urllib.request.Request(
        f"{LLM_BASE_URL}/chat/completions",
        data=data,
        headers={"Content-Type": "application/json"},
    )
    try:
        with urllib.request.urlopen(req, timeout=180) as resp:
            result = json.loads(resp.read())
            content = result["choices"][0]["message"]["content"]
            # Strip think tags (Qwen-style models)
            if "<think>" in content:
                parts = content.split("</think>")
                content = parts[-1].strip() if len(parts) > 1 else content
            return content
    except urllib.error.URLError as e:
        return f"[LLM error] Could not reach LM Studio at {LLM_BASE_URL}: {e.reason}"
    except Exception as e:
        return f"[LLM error] {e}"


def strip_fences(text: str) -> str:
    """Remove markdown code fences if present."""
    s = text.strip()
    if s.startswith("```"):
        s = s.split("\n", 1)[1].rsplit("```", 1)[0]
    return s


def extract_json_array(text: str) -> list | None:
    """Try multiple strategies to extract a JSON array from LLM output."""
    # Strategy 1: direct parse
    try:
        result = json.loads(text.strip())
        if isinstance(result, list):
            return result
    except json.JSONDecodeError:
        pass

    # Strategy 2: inside markdown fences
    if "```" in text:
        parts = text.split("```")
        for part in parts[1::2]:
            inner = part.split("\n", 1)[1] if "\n" in part else part
            try:
                result = json.loads(inner.strip())
                if isinstance(result, list):
                    return result
            except json.JSONDecodeError:
                continue

    # Strategy 3: find first [ ... ] substring
    start = text.find("[")
    end = text.rfind("]")
    if start != -1 and end > start:
        try:
            result = json.loads(text[start:end + 1])
            if isinstance(result, list):
                return result
        except json.JSONDecodeError:
            pass

    return None


def do_plan(app_spec: str) -> str:
    user_prompt = f"Build this app:\n\n{app_spec}\n\nReturn the JSON file manifest."
    return llm_call(PLAN_SYSTEM, user_prompt, temperature=0.2, max_tokens=2048)


def do_generate(filename: str, description: str, app_spec: str, dependencies: list[str]) -> str:
    deps_text = ", ".join(dependencies) if dependencies else "none"
    user_prompt = (
        f"App: {app_spec}\n\n"
        f"File: {filename}\n"
        f"Purpose: {description}\n"
        f"Dependencies: {deps_text}\n\n"
        f"Generate the complete file contents."
    )
    return llm_call(GENERATE_SYSTEM, user_prompt, temperature=0.3, max_tokens=4096)


def do_merge(filename: str, implementations: list[dict], app_spec: str, file_description: str) -> str:
    parts = [f"File: {filename}\nSpec: {app_spec}\nPurpose: {file_description}\n"]
    for impl in implementations:
        parts.append(f"\n--- Implementation from {impl['model']} ---\n{impl['code']}\n")
    parts.append("\nPick the best or merge the strongest parts. Return only the final code.")
    return llm_call(MERGE_SYSTEM, "\n".join(parts), temperature=0.2, max_tokens=4096)


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

    command = data.get("command", "generate")
    loop = asyncio.get_running_loop()
    t0 = time.monotonic()

    if command == "plan":
        app_spec = args.get("app_spec", "")
        say(f"planning app...")
        result = await loop.run_in_executor(None, do_plan, app_spec)
        elapsed = time.monotonic() - t0

        if result.startswith("[LLM error]"):
            say(f"{RED}✗{RESET} LLM error ({elapsed:.1f}s): {result}")
            await agent.resolve(msg.session, json.dumps({"error": result}), content_type="application/json")
            return

        manifest = extract_json_array(result)
        if manifest is not None:
            say(f"{GREEN}✓{RESET} {len(manifest)} files planned ({elapsed:.1f}s)")
            await agent.resolve(msg.session, json.dumps({"files": manifest}), content_type="application/json")
        else:
            say(f"{RED}✗{RESET} couldn't parse plan as JSON ({elapsed:.1f}s)")
            say(f"  raw output: {result[:200]}")
            await agent.resolve(msg.session, json.dumps({"raw": result, "error": "LLM returned invalid JSON for plan"}), content_type="application/json")

    elif command == "generate":
        filename = args.get("filename", "unknown")
        description = args.get("description", "")
        app_spec = args.get("app_spec", "")
        dependencies = args.get("dependencies", [])
        say(f"writing {BOLD}{filename}{RESET}...")

        result = await loop.run_in_executor(None, do_generate, filename, description, app_spec, dependencies)
        elapsed = time.monotonic() - t0

        if result.startswith("[LLM error]"):
            say(f"{RED}✗{RESET} {filename} — LLM error ({elapsed:.1f}s)")
            await agent.resolve(msg.session, json.dumps({"error": result}), content_type="application/json")
            return

        code = strip_fences(result)
        kb = len(code) / 1024
        say(f"{GREEN}✓{RESET} {BOLD}{filename}{RESET} — {kb:.1f}kb ({elapsed:.1f}s)")
        await agent.resolve(msg.session, json.dumps({
            "filename": filename, "code": code, "model": "lmstudio",
        }), content_type="application/json")

    elif command == "merge":
        filename = args.get("filename", "unknown")
        implementations = args.get("implementations", [])
        app_spec = args.get("app_spec", "")
        file_description = args.get("file_description", "")
        models = [impl.get("model", "?") for impl in implementations]
        say(f"merging {BOLD}{filename}{RESET} — comparing {', '.join(models)}...")

        result = await loop.run_in_executor(None, do_merge, filename, implementations, app_spec, file_description)
        elapsed = time.monotonic() - t0

        if result.startswith("[LLM error]"):
            say(f"{RED}✗{RESET} {filename} — merge error ({elapsed:.1f}s)")
            await agent.resolve(msg.session, json.dumps({"error": result}), content_type="application/json")
            return

        code = strip_fences(result)

        winner = "merged"
        for impl in implementations:
            if impl["code"].strip() == code.strip():
                winner = impl["model"]
                break

        say(f"{GREEN}✓{RESET} {BOLD}{filename}{RESET} — winner: {winner} ({elapsed:.1f}s)")
        await agent.resolve(msg.session, json.dumps({
            "filename": filename, "code": code, "winner": winner,
        }), content_type="application/json")

    else:
        await agent.resolve(msg.session, json.dumps({
            "error": f"Unknown command: {command}",
        }), content_type="application/json")


async def main():
    say(f"connecting to LM Studio at {LLM_BASE_URL}")
    async with agent:
        await agent.register()
        say(f"{GREEN}ready{RESET} — commands: plan, generate, merge")
        await agent.run_forever()


if __name__ == "__main__":
    asyncio.run(main())
