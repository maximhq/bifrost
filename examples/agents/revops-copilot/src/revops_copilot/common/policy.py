"""Map a Bifrost refusal to the policy surface that produced it.

Agents never swallow a refusal. They turn it into a `PolicyDenied` and return
it as structured task data, so the supervisor and the CLI can show which
surface fired.
"""

from __future__ import annotations

import json
from dataclasses import asdict, dataclass
from enum import StrEnum
from typing import Any


class Surface(StrEnum):
    """The 8 policy surfaces this example exercises."""

    IDENTITY = "1-ingress-identity"
    AGENT_GRANT = "2-agent-grant"
    LLM_GOVERNANCE = "3-llm-governance"
    LLM_INPUT_GUARDRAIL = "4-llm-input-guardrail"
    MCP_DISCOVERY = "5-mcp-discovery"
    MCP_TOOL_AUTHZ = "6-mcp-tool-authorization"
    MCP_GUARDRAIL = "7-mcp-tool-guardrail"
    LLM_OUTPUT_GUARDRAIL = "8-llm-output-guardrail"
    UNKNOWN = "unknown"


class Hop(StrEnum):
    LLM = "llm"
    MCP = "mcp"
    A2A = "a2a"


# Governance decision codes found in Bifrost's error bodies.
_GOVERNANCE_CODES: dict[str, Surface] = {
    "agent_blocked": Surface.AGENT_GRANT,
    "model_blocked": Surface.LLM_GOVERNANCE,
    "provider_blocked": Surface.LLM_GOVERNANCE,
    "budget_exceeded": Surface.LLM_GOVERNANCE,
    "rate_limited": Surface.LLM_GOVERNANCE,
    "request_limited": Surface.LLM_GOVERNANCE,
    "token_limited": Surface.LLM_GOVERNANCE,
    "mcp_tool_blocked": Surface.MCP_TOOL_AUTHZ,
    "virtual_key_required": Surface.IDENTITY,
    "workload identity not authorized": Surface.IDENTITY,
}

GUARDRAIL_CODE = "guardrail_intervention"


@dataclass
class PolicyDenied(Exception):
    """Bifrost refused a hop."""

    surface: Surface
    hop: Hop
    status: int
    message: str
    code: str = ""

    def __str__(self) -> str:
        return f"[{self.surface}] {self.hop} refused ({self.status} {self.code}): {self.message}"

    def to_data(self) -> dict[str, Any]:
        return {"policy_denied": asdict(self)}

    @classmethod
    def from_data(cls, data: dict[str, Any]) -> PolicyDenied | None:
        raw = data.get("policy_denied")
        if not isinstance(raw, dict):
            return None
        return cls(
            surface=Surface(raw.get("surface", Surface.UNKNOWN)),
            hop=Hop(raw.get("hop", Hop.LLM)),
            status=int(raw.get("status", 0)),
            message=str(raw.get("message", "")),
            code=str(raw.get("code", "")),
        )


def _error_message(body: str) -> tuple[str, str]:
    """Pull (message, type) out of a Bifrost or OpenAI-shaped error body."""
    try:
        parsed = json.loads(body)
    except (ValueError, TypeError):
        return body.strip()[:500], ""
    err = parsed.get("error", parsed) if isinstance(parsed, dict) else {}
    if isinstance(err, str):
        return err[:500], ""
    if not isinstance(err, dict):
        return body.strip()[:500], ""
    return str(err.get("message", body))[:500], str(err.get("type") or parsed.get("type") or "")


def classify(hop: Hop, status: int, body: str, *, phase: str = "input") -> PolicyDenied:
    """Name the surface behind a refusal.

    `phase` is "input" before the upstream call and "output" after it; only
    guardrails need it, because the same error type covers both directions.
    """
    message, err_type = _error_message(body)
    haystack = f"{err_type} {message} {body}".lower()

    if GUARDRAIL_CODE in haystack or "guardrail" in haystack:
        if hop is Hop.MCP:
            surface = Surface.MCP_GUARDRAIL
        elif phase == "output":
            surface = Surface.LLM_OUTPUT_GUARDRAIL
        else:
            surface = Surface.LLM_INPUT_GUARDRAIL
        return PolicyDenied(surface, hop, status, message, GUARDRAIL_CODE)

    for code, surface in _GOVERNANCE_CODES.items():
        if code in haystack:
            return PolicyDenied(surface, hop, status, message, code)

    if status == 401:
        return PolicyDenied(Surface.IDENTITY, hop, status, message, err_type)
    if status == 403 and hop is Hop.MCP:
        # /mcp/<slug> answers 403 for a slug the caller has no grant for, on
        # purpose without saying why, so slugs cannot be probed.
        return PolicyDenied(Surface.MCP_DISCOVERY, hop, status, message, err_type)
    if status == 403 and hop is Hop.A2A:
        return PolicyDenied(Surface.AGENT_GRANT, hop, status, message, err_type)
    if status == 429:
        return PolicyDenied(Surface.LLM_GOVERNANCE, hop, status, message, err_type)
    return PolicyDenied(Surface.UNKNOWN, hop, status, message, err_type)
