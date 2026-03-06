#!/usr/bin/env python3

from __future__ import annotations

import asyncio

from incident_common import run_specialist


if __name__ == "__main__":
    asyncio.run(
        run_specialist(
            handle="metrics-agent",
            description="Checks synthetic latency, saturation, and error metrics for incidents",
            capability="ops.metrics.query",
            domains=["operations"],
            tags=["incident", "metrics", "ops"],
        )
    )
