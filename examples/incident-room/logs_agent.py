#!/usr/bin/env python3

from __future__ import annotations

import asyncio

from incident_common import run_specialist


if __name__ == "__main__":
    asyncio.run(
        run_specialist(
            handle="logs-agent",
            description="Searches synthetic operational logs for incident evidence",
            capability="ops.logs.search",
            domains=["operations"],
            tags=["incident", "logs", "ops"],
        )
    )
