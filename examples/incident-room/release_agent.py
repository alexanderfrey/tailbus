#!/usr/bin/env python3

from __future__ import annotations

import asyncio

from incident_common import run_specialist


if __name__ == "__main__":
    asyncio.run(
        run_specialist(
            handle="release-agent",
            description="Reports recent deploy and config history relevant to an incident",
            capability="ops.release.history",
            domains=["operations"],
            tags=["incident", "release", "ops"],
        )
    )
