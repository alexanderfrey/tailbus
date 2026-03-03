"""Dataclasses and JSON serialization for the tailbus stdio bridge protocol."""

from __future__ import annotations

import json
from dataclasses import dataclass, field
from typing import Any, Union

__all__ = [
    "CommandSpec",
    "Manifest",
    "Registered",
    "Opened",
    "Sent",
    "Resolved",
    "Message",
    "Introspected",
    "HandleEntry",
    "HandleList",
    "SessionInfo",
    "SessionList",
    "Error",
    "Response",
    "serialize_command",
    "parse_response",
]


# ── Outbound (command helpers) ──────────────────────────────────────


@dataclass(frozen=True, slots=True)
class CommandSpec:
    """A command that an agent exposes."""

    name: str
    description: str
    parameters_schema: str = ""

    def to_dict(self) -> dict[str, Any]:
        d: dict[str, Any] = {"name": self.name, "description": self.description}
        if self.parameters_schema:
            d["parameters_schema"] = self.parameters_schema
        return d


@dataclass(frozen=True, slots=True)
class Manifest:
    """Service manifest describing an agent's capabilities."""

    description: str = ""
    commands: tuple[CommandSpec, ...] = ()
    tags: tuple[str, ...] = ()
    version: str = ""

    def to_dict(self) -> dict[str, Any]:
        d: dict[str, Any] = {}
        if self.description:
            d["description"] = self.description
        if self.commands:
            d["commands"] = [c.to_dict() for c in self.commands]
        if self.tags:
            d["tags"] = list(self.tags)
        if self.version:
            d["version"] = self.version
        return d

    @classmethod
    def from_dict(cls, data: dict[str, Any] | None) -> Manifest | None:
        if data is None:
            return None
        commands = tuple(
            CommandSpec(
                name=c["name"],
                description=c.get("description", ""),
                parameters_schema=c.get("parameters_schema", ""),
            )
            for c in data.get("commands", [])
        )
        return cls(
            description=data.get("description", ""),
            commands=commands,
            tags=tuple(data.get("tags", [])),
            version=data.get("version", ""),
        )


# ── Inbound (parsed responses) ─────────────────────────────────────


@dataclass(frozen=True, slots=True)
class Registered:
    """Confirmation that the agent was registered."""

    handle: str


@dataclass(frozen=True, slots=True)
class Opened:
    """Confirmation that a session was opened."""

    session: str
    message_id: str
    trace_id: str


@dataclass(frozen=True, slots=True)
class Sent:
    """Confirmation that a message was sent."""

    message_id: str


@dataclass(frozen=True, slots=True)
class Resolved:
    """Confirmation that a session was resolved."""

    message_id: str


@dataclass(frozen=True, slots=True)
class Message:
    """An incoming message from another agent."""

    session: str
    from_handle: str
    to_handle: str
    payload: str
    content_type: str
    message_type: str
    trace_id: str
    message_id: str
    sent_at: int


@dataclass(frozen=True, slots=True)
class Introspected:
    """Result of introspecting a handle."""

    handle: str
    found: bool
    manifest: Manifest | None = None


@dataclass(frozen=True, slots=True)
class HandleEntry:
    """A single entry in a handle list."""

    handle: str
    manifest: Manifest | None = None


@dataclass(frozen=True, slots=True)
class HandleList:
    """List of registered handles."""

    entries: tuple[HandleEntry, ...] = ()


@dataclass(frozen=True, slots=True)
class SessionInfo:
    """Information about an active session."""

    session: str
    from_handle: str
    to_handle: str
    state: str


@dataclass(frozen=True, slots=True)
class SessionList:
    """List of active sessions."""

    sessions: tuple[SessionInfo, ...] = ()


@dataclass(frozen=True, slots=True)
class Error:
    """An error response from the bridge."""

    error: str
    request_type: str


Response = Union[
    Registered,
    Opened,
    Sent,
    Resolved,
    Message,
    Introspected,
    HandleList,
    SessionList,
    Error,
]


# ── Serialization / Parsing ────────────────────────────────────────


def serialize_command(cmd: dict[str, Any]) -> bytes:
    """Encode a command dict as a JSON line (bytes with trailing newline)."""
    return json.dumps(cmd, separators=(",", ":")).encode("utf-8") + b"\n"


_PARSERS: dict[str, Any] = {}


def _register_parser(type_name: str):  # type: ignore[no-untyped-def]
    def decorator(fn):  # type: ignore[no-untyped-def]
        _PARSERS[type_name] = fn
        return fn
    return decorator


@_register_parser("registered")
def _parse_registered(d: dict[str, Any]) -> Registered:
    return Registered(handle=d["handle"])


@_register_parser("opened")
def _parse_opened(d: dict[str, Any]) -> Opened:
    return Opened(
        session=d["session"],
        message_id=d["message_id"],
        trace_id=d.get("trace_id", ""),
    )


@_register_parser("sent")
def _parse_sent(d: dict[str, Any]) -> Sent:
    return Sent(message_id=d["message_id"])


@_register_parser("resolved")
def _parse_resolved(d: dict[str, Any]) -> Resolved:
    return Resolved(message_id=d["message_id"])


@_register_parser("message")
def _parse_message(d: dict[str, Any]) -> Message:
    return Message(
        session=d["session"],
        from_handle=d["from"],
        to_handle=d["to"],
        payload=d.get("payload", ""),
        content_type=d.get("content_type", "text/plain"),
        message_type=d.get("message_type", "message"),
        trace_id=d.get("trace_id", ""),
        message_id=d.get("message_id", ""),
        sent_at=d.get("sent_at", 0),
    )


@_register_parser("introspected")
def _parse_introspected(d: dict[str, Any]) -> Introspected:
    return Introspected(
        handle=d["handle"],
        found=d.get("found", False),
        manifest=Manifest.from_dict(d.get("manifest")),
    )


@_register_parser("handles")
def _parse_handles(d: dict[str, Any]) -> HandleList:
    entries = tuple(
        HandleEntry(
            handle=e["handle"],
            manifest=Manifest.from_dict(e.get("manifest")),
        )
        for e in d.get("entries", [])
    )
    return HandleList(entries=entries)


@_register_parser("sessions")
def _parse_sessions(d: dict[str, Any]) -> SessionList:
    sessions = tuple(
        SessionInfo(
            session=s["session"],
            from_handle=s["from"],
            to_handle=s["to"],
            state=s.get("state", ""),
        )
        for s in d.get("sessions", [])
    )
    return SessionList(sessions=sessions)


@_register_parser("error")
def _parse_error(d: dict[str, Any]) -> Error:
    return Error(error=d["error"], request_type=d.get("request_type", "unknown"))


def parse_response(line: str) -> Response:
    """Parse a JSON line from the bridge into a typed response dataclass."""
    d = json.loads(line)
    resp_type = d.get("type", "")
    parser = _PARSERS.get(resp_type)
    if parser is None:
        raise ValueError(f"unknown response type: {resp_type!r}")
    return parser(d)  # type: ignore[no-any-return]
