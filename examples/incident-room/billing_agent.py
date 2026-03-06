#!/usr/bin/env python3

from __future__ import annotations

import asyncio

from incident_common import run_specialist


if __name__ == "__main__":
    asyncio.run(
        run_specialist(
            handle="billing-agent",
            description="Checks whether billing or account state is implicated in the incident",
            capability="billing.account.lookup",
            domains=["finance"],
            tags=["incident", "billing", "finance"],
        )
    )
