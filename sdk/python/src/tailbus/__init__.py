"""tailbus — Python SDK for the tailbus agent communication mesh.

Async usage::

    from tailbus import AsyncAgent, Manifest

    async with AsyncAgent("my-agent") as agent:
        await agent.register()
        opened = await agent.open_session("other", "hello")
        await agent.send(opened.session, "follow-up")
        await agent.resolve(opened.session, "done")

Sync usage::

    from tailbus import SyncAgent

    with SyncAgent("my-agent") as agent:
        agent.register()
        opened = agent.open_session("other", "hello")
        agent.send(opened.session, "follow-up")
        agent.resolve(opened.session, "done")
"""

from ._agent import AsyncAgent
from ._errors import (
    AlreadyRegisteredError,
    BinaryNotFoundError,
    BridgeDiedError,
    BridgeError,
    NotRegisteredError,
    TailbusError,
)
from ._protocol import (
    CommandSpec,
    Error,
    HandleEntry,
    HandleList,
    Introspected,
    Manifest,
    Message,
    Opened,
    RoomCreated,
    RoomEvent,
    RoomInfo,
    RoomList,
    RoomMembers,
    RoomOpResult,
    RoomPosted,
    RoomReplay,
    Registered,
    Resolved,
    Sent,
    SessionInfo,
    SessionList,
)
from ._sync import SyncAgent

__all__ = [
    # Agents
    "AsyncAgent",
    "SyncAgent",
    # Protocol types
    "CommandSpec",
    "Manifest",
    "Registered",
    "Opened",
    "Sent",
    "Resolved",
    "Message",
    "RoomEvent",
    "RoomInfo",
    "RoomCreated",
    "RoomPosted",
    "RoomReplay",
    "RoomMembers",
    "RoomList",
    "RoomOpResult",
    "Introspected",
    "HandleEntry",
    "HandleList",
    "SessionInfo",
    "SessionList",
    "Error",
    # Errors
    "TailbusError",
    "BinaryNotFoundError",
    "BridgeError",
    "BridgeDiedError",
    "NotRegisteredError",
    "AlreadyRegisteredError",
]
