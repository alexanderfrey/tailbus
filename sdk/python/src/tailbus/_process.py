"""Subprocess lifecycle management for the tailbus bridge."""

from __future__ import annotations

import asyncio
import shutil

from ._errors import BinaryNotFoundError

__all__ = [
    "find_binary",
    "build_command",
    "start_process",
    "stop_process",
]


def find_binary(binary: str) -> str:
    """Resolve the tailbus binary path, raising BinaryNotFoundError if missing."""
    path = shutil.which(binary)
    if path is None:
        raise BinaryNotFoundError(binary)
    return path


def build_command(binary: str, socket: str) -> list[str]:
    """Build the command list for launching the bridge subprocess."""
    return [binary, "-socket", socket, "agent"]


async def start_process(cmd: list[str]) -> asyncio.subprocess.Process:
    """Start the bridge subprocess with stdin/stdout piped and stderr inherited."""
    return await asyncio.create_subprocess_exec(
        *cmd,
        stdin=asyncio.subprocess.PIPE,
        stdout=asyncio.subprocess.PIPE,
        stderr=None,  # inherit
    )


async def stop_process(process: asyncio.subprocess.Process) -> None:
    """Gracefully stop the bridge subprocess.

    Close stdin, wait up to 5s, then terminate, wait 2s, then kill.
    """
    if process.returncode is not None:
        return

    # Close stdin to signal EOF
    if process.stdin is not None:
        try:
            process.stdin.close()
        except Exception:
            pass

    try:
        await asyncio.wait_for(process.wait(), timeout=5.0)
        return
    except asyncio.TimeoutError:
        pass

    process.terminate()
    try:
        await asyncio.wait_for(process.wait(), timeout=2.0)
        return
    except asyncio.TimeoutError:
        pass

    process.kill()
    await process.wait()
