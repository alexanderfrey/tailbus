#!/usr/bin/env python3
"""Researcher agent — investigates topics and delegates to critic + writer.

Flow: user asks a question → researcher generates findings → sends to critic
for review → sends findings + critique to writer for final output → returns
the polished result to the user.

Demonstrates multi-agent collaboration through the tailbus mesh, all powered
by a single local LLM (LM Studio).
"""

import asyncio
import json
import os
import sys
import urllib.request
import urllib.error

sys.path.insert(0, "/sdk/python/src")
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "..", "sdk", "python", "src"))

from tailbus import AsyncAgent, Manifest, Message

LLM_BASE_URL = os.environ.get("LLM_BASE_URL", "http://host.docker.internal:1234/v1")
LLM_MODEL = os.environ.get("LLM_MODEL", "")

SYSTEM_PROMPT = """You are a research analyst. When given a topic, you investigate it thoroughly and produce structured findings.

Your output should be clear, factual, and organized with:
- Key findings (3-5 bullet points)
- Supporting details
- Open questions that need further investigation

Be concise but thorough. Do NOT include any thinking/reasoning process — just provide the findings directly."""

agent = AsyncAgent(
    "researcher",
    manifest=Manifest(
        description="Research analyst — investigates topics, delegates to critic and writer",
        tags=["llm", "research", "orchestrator"],
        version="1.0.0",
    ),
    socket=os.environ.get("TAILBUS_SOCKET", "/tmp/tailbusd.sock"),
)

pending_responses: dict[str, asyncio.Future] = {}


def llm_call(messages: list[dict]) -> str:
    body = {"messages": messages, "temperature": 0.7, "max_tokens": 2048}
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
            content = result["choices"][0]["message"]["content"]
            # Strip thinking tags if present (Qwen-style)
            if "<think>" in content:
                parts = content.split("</think>")
                content = parts[-1].strip() if len(parts) > 1 else content
            return content
    except Exception as e:
        return f"[LLM error] {e}"


async def delegate(target: str, payload: str, timeout: float = 90) -> str:
    opened = await agent.open_session(target, payload)
    fut: asyncio.Future = asyncio.get_running_loop().create_future()
    pending_responses[opened.session] = fut
    try:
        return await asyncio.wait_for(fut, timeout=timeout)
    except asyncio.TimeoutError:
        pending_responses.pop(opened.session, None)
        return f"[timeout] {target} did not respond in {timeout}s"


@agent.on_message
async def handle(msg: Message):
    # Check if this is a response to our delegation
    if msg.session in pending_responses:
        fut = pending_responses.pop(msg.session)
        if not fut.done():
            fut.set_result(msg.payload)
        return

    topic = msg.payload
    try:
        data = json.loads(topic)
        if isinstance(data, dict):
            topic = data.get("message", data.get("topic", topic))
    except (json.JSONDecodeError, TypeError):
        pass

    print(f"[researcher] investigating: {topic}", flush=True)

    # Step 1: Research the topic
    loop = asyncio.get_running_loop()
    findings = await loop.run_in_executor(None, llm_call, [
        {"role": "system", "content": SYSTEM_PROMPT},
        {"role": "user", "content": f"Research the following topic and provide your findings:\n\n{topic}"},
    ])
    print(f"[researcher] findings ready, sending to critic...", flush=True)

    # Step 2: Send findings to critic for review
    critique = await delegate("critic", json.dumps({
        "topic": str(topic),
        "findings": findings,
    }))
    print(f"[researcher] critique received, sending to writer...", flush=True)

    # Step 3: Send everything to writer for final output
    final = await delegate("writer", json.dumps({
        "topic": str(topic),
        "findings": findings,
        "critique": critique,
    }))
    print(f"[researcher] final output received, responding to user", flush=True)

    await agent.resolve(msg.session, final)


async def main():
    print(f"[researcher] starting, LLM API: {LLM_BASE_URL}", flush=True)
    async with agent:
        await agent.register()
        print("[researcher] registered, listening for messages...", flush=True)
        await agent.run_forever()


if __name__ == "__main__":
    asyncio.run(main())
