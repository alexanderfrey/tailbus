from __future__ import annotations


def filter_by_status(items: list[dict[str, str]], status: str) -> list[dict[str, str]]:
    if status == "all":
        return list(items)
    return [item for item in items if item.get("status") == status]
