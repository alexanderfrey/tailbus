"""Tailbus exception hierarchy."""

__all__ = [
    "TailbusError",
    "BinaryNotFoundError",
    "BridgeError",
    "BridgeDiedError",
    "NotRegisteredError",
    "AlreadyRegisteredError",
]


class TailbusError(Exception):
    """Base exception for all tailbus errors."""


class BinaryNotFoundError(TailbusError):
    """The tailbus binary could not be found."""

    def __init__(self, binary: str) -> None:
        self.binary = binary
        super().__init__(f"tailbus binary not found: {binary!r}")


class BridgeError(TailbusError):
    """The bridge returned an error response."""

    def __init__(self, error: str, request_type: str) -> None:
        self.error = error
        self.request_type = request_type
        super().__init__(f"bridge error on {request_type!r}: {error}")


class BridgeDiedError(TailbusError):
    """The bridge process exited unexpectedly."""

    def __init__(self, returncode: int | None = None) -> None:
        self.returncode = returncode
        msg = "bridge process died"
        if returncode is not None:
            msg += f" (exit code {returncode})"
        super().__init__(msg)


class NotRegisteredError(TailbusError):
    """Operation attempted before register() was called."""

    def __init__(self) -> None:
        super().__init__("agent is not registered; call register() first")


class AlreadyRegisteredError(TailbusError):
    """register() was called more than once."""

    def __init__(self) -> None:
        super().__init__("agent is already registered")
