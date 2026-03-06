from __future__ import annotations

import json
from typing import Any, Callable
from urllib.request import urlopen


Fetcher = Callable[[str], Any]


def get_json(url: str, fetcher: Fetcher | None = None) -> Any:
    fetch = fetcher or urlopen
    with fetch(url) as response:
        return json.loads(response.read().decode("utf-8"))
