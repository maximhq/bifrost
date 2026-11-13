"""Environment-driven settings.

Settings are read with the agent's env prefix. Only revops-copilot holds a
Bifrost identity; every agent has an inbound key:

    REVOPS_COPILOT_VK=sk-bf-...            (or REVOPS_COPILOT_ENTRA_CLIENT_ID/_SECRET)
    SUPPORT_ANALYST_INBOUND_KEY=...
"""

from __future__ import annotations

from enum import StrEnum

from pydantic import Field, SecretStr
from pydantic_settings import BaseSettings, SettingsConfigDict


class AuthMode(StrEnum):
    """How an agent proves who it is to Bifrost."""

    VK = "vk"  # Bifrost virtual key, works on OSS and enterprise
    ENTRA = "entra"  # Entra app-only token mapped to a Bifrost service account (enterprise)


class AgentName(StrEnum):
    """Agent names. They match the Agent Gateway registration names."""

    SUPERVISOR = "revops-copilot"
    SUPPORT_ANALYST = "support-analyst"
    REPORT_WRITER = "report-writer"
    PUBLISHER = "publisher"

    @property
    def env_prefix(self) -> str:
        return self.value.upper().replace("-", "_") + "_"


class GlobalSettings(BaseSettings):
    """Settings shared by every agent."""

    model_config = SettingsConfigDict(env_file=".env", extra="ignore")

    bifrost_url: str = Field(default="http://localhost:8080")
    auth_mode: AuthMode = Field(default=AuthMode.VK)
    model: str = Field(default="openai/gpt-4o-mini")

    # Entra workload identity (enterprise). The scope is the Bifrost SSO app's
    # App ID URI (or client id) followed by /.default.
    entra_tenant_id: str = Field(default="")
    entra_scope: str = Field(default="")

    # Bifrost's MCP server. Empty: every agent opens /mcp and sees every tool the
    # identity's access profile grants. A Virtual MCP slug narrows all agents to it.
    mcp_slug: str = Field(default="")

    # Drive folder the publisher uploads into.
    drive_folder_id: str = Field(default="")

    # Tool names as Bifrost exposes them (<client>-<tool>). Discovered with
    # `make preflight`; override when the upstream server renames a tool.
    drive_upload_tool: str = Field(default="")

    # Below Bifrost's provider timeout (900 s by default), so the gateway, not the
    # client, decides when a model call is too slow.
    request_timeout_s: float = Field(default=600.0)

    # A2A streaming: live progress, and no idle request for a proxy to cut.
    # Quick tunnels buffer SSE, so `make agents-cloudflared` without a named
    # tunnel sets A2A_STREAMING=false.
    a2a_streaming: bool = Field(default=True)

    # How a caller follows a delegated task's progress:
    #   stream SendStreamingMessage: live steps, and Bifrost logs ONE operation per
    #          call holding every step and the final answer.
    #   poll   send it non-blocking, then GetTask every a2a_poll_interval_s. Works
    #          through any gateway, including a Bifrost whose cached agent card
    #          still says no streaming, but Bifrost then logs the send (SUBMITTED,
    #          no output) and each GetTask as separate operations.
    a2a_progress: str = Field(default="stream")
    a2a_poll_interval_s: float = Field(default=1.5)


class AgentSettings(BaseSettings):
    """One agent's identity and listen address, read with its env prefix."""

    model_config = SettingsConfigDict(env_file=".env", extra="ignore")

    host: str = Field(default="127.0.0.1")
    port: int = Field(default=0)
    public_url: str = Field(default="")

    # VK mode
    vk: SecretStr = Field(default=SecretStr(""))

    # Entra mode: client secret, certificate, or managed identity (no secret and no cert).
    entra_client_id: str = Field(default="")
    entra_client_secret: SecretStr = Field(default=SecretStr(""))
    entra_certificate_path: str = Field(default="")

    # Shared secret Bifrost sends on every call to this agent (runtime_auth on the
    # registration). Requests without it are refused, so the gateway cannot be bypassed.
    inbound_key: SecretStr = Field(default=SecretStr(""))

    @classmethod
    def with_prefix(cls, env_prefix: str) -> AgentSettings:
        return cls(_env_prefix=env_prefix)  # type: ignore[call-arg]

    @classmethod
    def for_agent(cls, name: AgentName) -> AgentSettings:
        settings = cls.with_prefix(name.env_prefix)
        if settings.port == 0:
            settings.port = DEFAULT_PORTS[name]
        if not settings.public_url:
            settings.public_url = f"http://{settings.host}:{settings.port}"
        return settings


DEFAULT_PORTS: dict[AgentName, int] = {
    AgentName.SUPERVISOR: 9100,
    AgentName.SUPPORT_ANALYST: 9101,
    AgentName.REPORT_WRITER: 9102,
    AgentName.PUBLISHER: 9103,
}
