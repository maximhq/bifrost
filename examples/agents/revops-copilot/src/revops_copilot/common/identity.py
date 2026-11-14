"""How an agent authenticates to Bifrost.

Bifrost rejects a request that carries both a workload identity token and a
virtual key, so each credential produces exactly one auth header.

* VK mode: `x-bf-vk: <virtual key>`.
* Entra mode: `Authorization: Bearer <app-only token>`. Bifrost verifies it
  against the SSO tenant, finds the service account mapped to the service
  principal's object id, and applies that account's access profile.
* Forwarded: only revops-copilot holds an identity. With
  forward_accepted_credential on a subagent's registration, Bifrost forwards the
  caller's credential in the header it arrived in, and the subagent reuses it.
"""

from __future__ import annotations

from collections.abc import Mapping
from typing import Protocol

import asyncio

from azure.core.credentials import TokenCredential

from revops_copilot.common.config import AgentSettings, AuthMode, GlobalSettings

VK_HEADER = "x-bf-vk"
AUTHORIZATION_HEADER = "Authorization"
# Headers Bifrost accepts a credential in, and so forwards it in. Never the
# inbound key (x-agent-key): that is Bifrost's proof to the agent, not an identity.
FORWARDED_HEADERS = (VK_HEADER, "authorization", "x-api-key")


class IdentityError(RuntimeError):
    """The agent's identity is not configured."""


class Credential(Protocol):
    """Produces the auth header an agent sends to Bifrost."""

    async def auth_headers(self) -> dict[str, str]: ...

    async def bearer(self) -> str:
        """The value for clients that only take an API key (the OpenAI SDK)."""
        ...

    async def close(self) -> None: ...


class VirtualKeyCredential:
    def __init__(self, vk: str) -> None:
        if not vk:
            raise IdentityError("virtual key is empty")
        self._vk = vk

    async def auth_headers(self) -> dict[str, str]:
        return {VK_HEADER: self._vk}

    async def bearer(self) -> str:
        return self._vk

    async def close(self) -> None:
        return None


class EntraCredential:
    """Client-credentials token for the Bifrost SSO app.

    Uses azure-identity's sync credentials (HTTP over requests, already a
    dependency); the async ones need aiohttp. azure-identity caches the token and
    refreshes it before expiry, so the thread hop below runs about once an hour.
    """

    def __init__(self, token_credential: TokenCredential, scope: str) -> None:
        if not scope:
            raise IdentityError("ENTRA_SCOPE is empty; set it to <bifrost-sso-app-id-uri>/.default")
        self._credential = token_credential
        self._scope = scope

    async def bearer(self) -> str:
        token = await asyncio.to_thread(self._credential.get_token, self._scope)
        return token.token

    async def auth_headers(self) -> dict[str, str]:
        return {AUTHORIZATION_HEADER: f"Bearer {await self.bearer()}"}

    async def close(self) -> None:
        close = getattr(self._credential, "close", None)
        if close is not None:
            close()


class ForwardedCredential:
    """The caller's credential as Bifrost forwarded it: same header, same value.

    A forwarded Entra token is not refreshed; it outlives one task.
    """

    def __init__(self, header: str, value: str) -> None:
        self._header = header
        self._value = value

    async def auth_headers(self) -> dict[str, str]:
        return {self._header: self._value}

    async def bearer(self) -> str:
        scheme, _, token = self._value.partition(" ")
        return token if self._header.lower() == "authorization" and scheme.lower() == "bearer" else self._value

    async def close(self) -> None:
        return None


def forwarded_credential(headers: Mapping[str, str]) -> ForwardedCredential | None:
    """The credential Bifrost forwarded on an inbound A2A call, if any."""
    lowered = {k.lower(): v for k, v in headers.items()}
    for header in FORWARDED_HEADERS:
        if value := lowered.get(header, "").strip():
            return ForwardedCredential(header, value)
    return None


def _entra_token_credential(g: GlobalSettings, a: AgentSettings) -> TokenCredential:
    from azure.identity import (
        CertificateCredential,
        ClientSecretCredential,
        ManagedIdentityCredential,
    )

    if not a.entra_client_id:
        raise IdentityError("ENTRA_CLIENT_ID is empty for this agent")
    secret = a.entra_client_secret.get_secret_value()
    if secret or a.entra_certificate_path:
        if not g.entra_tenant_id:
            raise IdentityError("ENTRA_TENANT_ID is empty")
    if secret:
        return ClientSecretCredential(g.entra_tenant_id, a.entra_client_id, secret)
    if a.entra_certificate_path:
        return CertificateCredential(g.entra_tenant_id, a.entra_client_id, a.entra_certificate_path)
    # No secret and no certificate: the agent runs on Azure with a
    # user-assigned managed identity whose client id is entra_client_id.
    return ManagedIdentityCredential(client_id=a.entra_client_id)


def credential_for(env_prefix: str, g: GlobalSettings, a: AgentSettings) -> Credential:
    """Build the credential for one identity from its settings.

    `env_prefix` is only used in error messages (e.g. "SUPPORT_ANALYST_").
    """
    if g.auth_mode is AuthMode.VK:
        vk = a.vk.get_secret_value()
        if not vk:
            raise IdentityError(f"{env_prefix}VK is empty (AUTH_MODE=vk)")
        return VirtualKeyCredential(vk)
    try:
        return EntraCredential(_entra_token_credential(g, a), g.entra_scope)
    except IdentityError as e:
        raise IdentityError(f"{env_prefix}*: {e}") from e
