#!/usr/bin/env python3

from __future__ import annotations

import asyncio

from incident_common import run_specialist


if __name__ == "__main__":
    asyncio.run(
        run_specialist(
            handle="status-agent",
            description="Drafts customer-facing status updates from incident-room evidence",
            capability="statuspage.compose",
            domains=["communications"],
            tags=["incident", "status", "comms"],
        )
    )
