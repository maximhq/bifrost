"""Live progress: one event per action an agent takes.

Agents publish each event as an A2A status update (state WORKING) carrying a
`progress` data part. A caller that delegates relays its subagent's events one
level deeper, so the CLI sees one tree for the whole job:

    revops-copilot  -> support-analyst
                       ├ open /mcp/pylon-readonly    ⑤ 10 tools
                       ├ tool Pylon-search_issues    ⑥ ok

Streaming these events also keeps every hop busy, so a proxy in front of an
agent (Cloudflare cuts idle requests after 100 s) never sees a silent request.
"""

from __future__ import annotations

from collections.abc import Awaitable, Callable
from dataclasses import asdict, dataclass, fields, replace
from typing import Any

KEY = "progress"

# kind: step (local work), llm, mcp (discovery), tool, delegate (A2A), result, denied
# status: start, ok, fail


@dataclass(frozen=True)
class Progress:
    agent: str
    kind: str
    title: str
    detail: str = ""
    surfaces: str = ""
    status: str = "ok"
    depth: int = 0
    elapsed_ms: int | None = None

    def to_data(self) -> dict[str, Any]:
        return {KEY: asdict(self)}

    @classmethod
    def from_data(cls, data: dict[str, Any]) -> Progress | None:
        raw = data.get(KEY)
        if not isinstance(raw, dict):
            return None
        known = {f.name for f in fields(cls)}
        values = {k: v for k, v in raw.items() if k in known}
        # A2A data parts are protobuf Structs, which carry every number as a float.
        for key in ("depth", "elapsed_ms"):
            if isinstance(values.get(key), float):
                values[key] = int(values[key])
        try:
            return cls(**values)
        except TypeError:
            return None

    def nested(self) -> Progress:
        """The same event one level deeper, as a delegating caller relays it."""
        return replace(self, depth=self.depth + 1)


Emit = Callable[[Progress], Awaitable[None]]

_CIRCLED = "①②③④⑤⑥⑦⑧"


def circled(surface: object) -> str:
    """"5-mcp-discovery" -> "⑤"; anything else -> ""."""
    head = str(surface).split("-", 1)[0]
    return _CIRCLED[int(head) - 1] if head.isdigit() and 1 <= int(head) <= 8 else ""
