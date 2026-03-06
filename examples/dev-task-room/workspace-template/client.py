from __future__ import annotations

import json
import time
from dataclasses import dataclass
from typing import Any, Callable
from urllib.request import urlopen


Fetcher = Callable[[str], Any]
Sleeper = Callable[[float], None]


@dataclass(frozen=True)
class RetryPolicy:
    """Encapsulates the exponential backoff retry parameters."""

    max_retries: int = 3
    backoff_factor: float = 0.5
    max_backoff: float | None = None

    def __post_init__(self) -> None:
        if self.max_retries < 1:
            raise ValueError("max_retries must be at least 1")
        if self.backoff_factor < 0:
            raise ValueError("backoff_factor must be >= 0")
        if self.max_backoff is not None and self.max_backoff < 0:
            raise ValueError("max_backoff must be >= 0")


def _calculate_backoff_delay(backoff_factor: float, attempt: int, max_backoff: float | None) -> float:
    base_delay = backoff_factor * (2 ** (attempt - 1))
    if max_backoff is None:
        return base_delay
    return min(base_delay, max_backoff)


def get_json(
    url: str,
    fetcher: Fetcher | None = None,
    *,
    max_retries: int = 3,
    backoff_factor: float = 0.5,
    max_backoff: float | None = None,
    sleep_func: Sleeper | None = None,
) -> Any:
    """Fetch JSON data, retrying with exponential backoff on transient failures."""

    policy = RetryPolicy(
        max_retries=max_retries,
        backoff_factor=backoff_factor,
        max_backoff=max_backoff,
    )
    fetch = fetcher or urlopen
    sleeper = sleep_func or time.sleep

    last_error: Exception | None = None
    for attempt in range(1, policy.max_retries + 1):
        try:
            with fetch(url) as response:
                return json.loads(response.read().decode("utf-8"))
        except Exception as exc:
            last_error = exc
            if attempt == policy.max_retries:
                raise
            delay = _calculate_backoff_delay(policy.backoff_factor, attempt, policy.max_backoff)
            sleeper(delay)

    assert last_error is not None
    raise last_error
