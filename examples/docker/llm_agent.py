#!/usr/bin/env python3
"""LLM agent — connects to a local LM Studio (or any OpenAI-compatible API)
and exposes it as a tailbus agent.

Other agents and the web UI can chat with your local LLM through the mesh.

Environment variables:
    TAILBUS_SOCKET  — daemon Unix socket (default: /tmp/tailbusd.sock)
    LLM_BASE_URL    — OpenAI-compatible API base URL (default: http://host.docker.internal:1234/v1)
    LLM_MODEL       — model name (default: use whatever is loaded)
"""

import asyncio
import json
import os
import sys
import urllib.request
import urllib.error

sys.path.insert(0, "/sdk/python/src")

from tailbus import AsyncAgent, Manifest, Message

LLM_BASE_URL = os.environ.get("LLM_BASE_URL", "http://host.docker.internal:1234/v1")
LLM_MODEL = os.environ.get("LLM_MODEL", "")

agent = AsyncAgent(
    "assistant",
    manifest=Manifest(
        description="LLM assistant powered by LM Studio — ask anything",
        tags=["llm", "assistant"],
        version="1.0.0",
    ),
    socket=os.environ.get("TAILBUS_SOCKET", "/tmp/tailbusd.sock"),
)

# Per-session conversation history for multi-turn context
histories: dict[str, list] = {}


def chat_completion(messages: list[dict]) -> str:
    """Call the OpenAI-compatible chat/completions endpoint (sync, no deps)."""
    body = {"messages": messages, "temperature": 0.7, "max_tokens": 1024}
    if LLM_MODEL:
        body["model"] = LLM_MODEL

    data = json.dumps(body).encode()
    req = urllib.request.Request(
        f"{LLM_BASE_URL}/chat/completions",
        data=data,
        headers={"Content-Type": "application/json"},
    )
    try:
        with urllib.request.urlopen(req, timeout=120) as resp:
            result = json.loads(resp.read())
            return result["choices"][0]["message"]["content"]
    except urllib.error.URLError as e:
        return f"[LLM error] Could not reach LM Studio at {LLM_BASE_URL}: {e.reason}"
    except Exception as e:
        return f"[LLM error] {e}"


@agent.on_message
async def handle(msg: Message):
    """Forward messages to LM Studio and return the response."""
    user_text = msg.payload

    # Try to extract message from JSON payload
    try:
        data = json.loads(user_text)
        if isinstance(data, dict):
            user_text = data.get("message", data.get("payload", user_text))
    except (json.JSONDecodeError, TypeError):
        pass

    # Build conversation history for this session
    if msg.session not in histories:
        histories[msg.session] = [
            {"role": "system", "content": "You are a helpful assistant on the tailbus agent mesh. Be concise."}
        ]
    histories[msg.session].append({"role": "user", "content": str(user_text)})

    # Call LM Studio (run in thread to not block the event loop)
    loop = asyncio.get_running_loop()
    reply = await loop.run_in_executor(None, chat_completion, list(histories[msg.session]))

    histories[msg.session].append({"role": "assistant", "content": reply})

    await agent.resolve(msg.session, reply)

    # Clean up old histories (keep max 50 sessions)
    if len(histories) > 50:
        oldest = list(histories.keys())[0]
        del histories[oldest]


async def main():
    print(f"[assistant] starting, LLM API: {LLM_BASE_URL}", flush=True)
    async with agent:
        await agent.register()
        print("[assistant] registered, listening for messages...", flush=True)
        await agent.run_forever()


if __name__ == "__main__":
    asyncio.run(main())
