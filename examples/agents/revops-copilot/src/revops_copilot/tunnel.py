"""Tunnel modes: run the four agents behind public URLs, so a remote Bifrost
gateway can reach them.

    revops ngrok         (make agents-ngrok)       needs 4 endpoints: a paid ngrok plan
    revops cloudflared   (make agents-cloudflared) a named Cloudflare Tunnel when
                         cloudflared.yml exists (fixed hostnames), else free quick
                         tunnels (new hostnames on every start)

1. Open the public tunnel(s) and read each agent's URL (ngrok's local API, the
   named tunnel's ingress rules, or each quick tunnel's /quicktunnel endpoint).
2. Start each agent with <AGENT>_PUBLIC_URL set, so its card advertises that URL.
3. Print what to register on Bifrost.

The tunnels are public, so every agent must have an inbound key first.
"""

from __future__ import annotations

import os
import re
import shutil
import signal
import subprocess
import sys
import time
from pathlib import Path
from typing import Any

import httpx
from rich.console import Console

from revops_copilot.common.config import AgentName, AgentSettings

NGROK_API = os.environ.get("NGROK_API_URL", "http://127.0.0.1:4040/api")
NGROK_CONFIG = "ngrok.yml"
CLOUDFLARED_CONFIG = "cloudflared.yml"
CARD_PATH = "/.well-known/agent-card.json"
STARTUP_TIMEOUT_S = 30.0
# cloudflared's metrics server, one per tunnel: 127.0.0.1:<base + i>.
CLOUDFLARED_METRICS_BASE = int(os.environ.get("CLOUDFLARED_METRICS_BASE", "49300"))
CLOUDFLARED_TIMEOUT_S = 60.0

console = Console()


def _port(upstream: str) -> int | None:
    """Port from an upstream like "9100", "localhost:9100" or "http://localhost:9100"."""
    m = re.search(r"(?:^|:)(\d{2,5})/?$", upstream.strip())
    return int(m.group(1)) if m else None


def public_urls(
    ports: dict[AgentName, int], endpoints_doc: dict[str, Any] | None, tunnels_doc: dict[str, Any] | None
) -> dict[AgentName, str]:
    """Map each agent to its public https URL.

    Reads /api/endpoints rows (name, url, upstream.url) and the deprecated
    /api/tunnels rows (name, public_url, config.addr). A row named after the agent
    wins; otherwise the row whose upstream is the agent's port.
    """
    rows: list[tuple[str, str, str]] = []
    for e in (endpoints_doc or {}).get("endpoints") or []:
        rows.append((e.get("name", ""), e.get("url", ""), (e.get("upstream") or {}).get("url", "")))
    for t in (tunnels_doc or {}).get("tunnels") or []:
        rows.append((t.get("name", ""), t.get("public_url", ""), (t.get("config") or {}).get("addr", "")))
    rows = [r for r in rows if r[1].startswith("https://")]

    found: dict[AgentName, str] = {}
    for agent, port in ports.items():
        by_name = [url for name, url, _ in rows if name == agent.value]
        by_port = [url for _, url, upstream in rows if _port(upstream) == port]
        match = (by_name or by_port or [None])[0]
        if match:
            found[agent] = match.rstrip("/")
    return found


def missing_inbound_keys() -> list[str]:
    return [
        f"{a.env_prefix}INBOUND_KEY"
        for a in AgentName
        if not AgentSettings.for_agent(a).inbound_key.get_secret_value()
    ]


def _read_ngrok(client: httpx.Client, ports: dict[AgentName, int]) -> dict[AgentName, str] | None:
    """Current URLs, or None when no ngrok agent is listening."""
    docs: dict[str, Any] = {}
    for path in ("endpoints", "tunnels"):
        try:
            r = client.get(f"{NGROK_API}/{path}")
        except httpx.TransportError:
            return None
        docs[path] = r.json() if r.is_success else None
    return public_urls(ports, docs["endpoints"], docs["tunnels"])


def default_config_from_check(output: str) -> str:
    """The default config path from `ngrok config check` ("Valid configuration file at <path>")."""
    m = re.search(r"configuration file at (.+?)\s*$", output, re.MULTILINE)
    return m.group(1) if m else ""


def _start_ngrok(config: Path) -> subprocess.Popen[bytes]:
    if shutil.which("ngrok") is None:
        raise SystemExit("ngrok is not installed: brew install ngrok, then ngrok config add-authtoken <token>")
    if not config.exists():
        raise SystemExit(f"{config} not found: cp ngrok.yml.example ngrok.yml")
    cmd = ["ngrok", "start", "--all", "--log", "stdout", "--log-level", "warn"]
    # Merge the default config first so its authtoken applies.
    check = subprocess.run(["ngrok", "config", "check"], capture_output=True, text=True)
    default = default_config_from_check(check.stdout + check.stderr)
    if default and Path(default).is_file():
        cmd += ["--config", default]
    cmd += ["--config", str(config)]
    return subprocess.Popen(cmd)


def quick_tunnel_url(doc: dict[str, Any] | None) -> str:
    """https URL from cloudflared's /quicktunnel answer; empty until the tunnel is up."""
    hostname = (doc or {}).get("hostname") or ""
    return f"https://{hostname}" if hostname else ""


def named_tunnel_urls(ports: dict[AgentName, int], config: dict[str, Any]) -> dict[AgentName, str]:
    """Map each agent to https://<hostname> from the ingress rule serving its port."""
    found: dict[AgentName, str] = {}
    for rule in config.get("ingress") or []:
        hostname, service = rule.get("hostname"), str(rule.get("service", ""))
        if not hostname or not service.startswith("http"):
            continue
        for agent, port in ports.items():
            if _port(service) == port and agent not in found:
                found[agent] = f"https://{hostname}"
    return found


def _cloudflared_binary() -> str:
    binary = shutil.which("cloudflared")
    try:
        if binary is None:
            raise OSError("not on PATH")
        subprocess.run([binary, "--version"], capture_output=True, check=True)
    except (OSError, subprocess.CalledProcessError) as e:
        raise SystemExit(f"cloudflared does not run ({e}): brew reinstall cloudflared") from None
    return binary


def _start_named_tunnel(
    ports: dict[AgentName, int], config_path: Path
) -> tuple[dict[AgentName, str], list[subprocess.Popen[bytes]]]:
    """One named tunnel; hostnames are fixed by cloudflared.yml, so registrations survive restarts."""
    import yaml

    binary = _cloudflared_binary()
    urls = named_tunnel_urls(ports, yaml.safe_load(config_path.read_text()) or {})
    missing = [a.value for a in ports if a not in urls]
    if missing:
        raise SystemExit(f"{config_path} has no ingress rule for {', '.join(missing)}; see cloudflared.yml.example")
    metrics = CLOUDFLARED_METRICS_BASE
    proc = subprocess.Popen(
        [binary, "tunnel", "--no-autoupdate", "--loglevel", "warn", "--config", str(config_path),
         "--metrics", f"127.0.0.1:{metrics}", "run"]
    )
    deadline = time.monotonic() + CLOUDFLARED_TIMEOUT_S
    with httpx.Client(timeout=2.0) as client:
        while time.monotonic() < deadline and proc.poll() is None:
            try:
                if client.get(f"http://127.0.0.1:{metrics}/ready").status_code == 200:
                    return urls, [proc]
            except httpx.TransportError:
                pass
            time.sleep(0.5)
    _stop([proc])
    raise SystemExit("the named tunnel did not connect; check the cloudflared output above")


def _start_cloudflared(ports: dict[AgentName, int]) -> tuple[dict[AgentName, str], list[subprocess.Popen[bytes]]]:
    """A named tunnel when cloudflared.yml exists, else one quick tunnel per agent."""
    if Path(CLOUDFLARED_CONFIG).is_file():
        return _start_named_tunnel(ports, Path(CLOUDFLARED_CONFIG))
    binary = _cloudflared_binary()

    metrics = {agent: CLOUDFLARED_METRICS_BASE + i for i, agent in enumerate(ports)}
    procs = [
        subprocess.Popen(
            [binary, "tunnel", "--no-autoupdate", "--loglevel", "warn",
             "--url", f"http://127.0.0.1:{ports[agent]}", "--metrics", f"127.0.0.1:{metrics[agent]}"]
        )
        for agent in ports
    ]
    urls: dict[AgentName, str] = {}
    deadline = time.monotonic() + CLOUDFLARED_TIMEOUT_S
    with httpx.Client(timeout=2.0) as client:
        while time.monotonic() < deadline and len(urls) < len(ports) and all(p.poll() is None for p in procs):
            for agent, port in metrics.items():
                if agent in urls:
                    continue
                try:
                    r = client.get(f"http://127.0.0.1:{port}/quicktunnel")
                    url = quick_tunnel_url(r.json() if r.is_success else None)
                except (httpx.TransportError, ValueError):
                    url = ""
                if url:
                    urls[agent] = url
            time.sleep(0.5)
    if len(urls) < len(ports):
        _stop(procs)
        raise SystemExit(
            "cloudflared did not open all four quick tunnels; check its output above "
            f"(missing: {', '.join(a.value for a in ports if a not in urls)})"
        )
    return urls, procs


def _print_registration(urls: dict[AgentName, str]) -> None:
    # Plain lines, never a table: the card URLs are copied as-is and must not wrap or truncate.
    console.rule("Register on Bifrost: Agents > Add agent")
    for agent, url in urls.items():
        forward = "off" if agent is AgentName.SUPERVISOR else "on"
        console.print(f"[bold]{agent.value}[/]", soft_wrap=True)
        console.print(f"  Agent Card URL                  {url}{CARD_PATH}", soft_wrap=True, highlight=False)
        console.print(f"  x-agent-key header              value of {agent.env_prefix}INBOUND_KEY", soft_wrap=True)
        console.print(f"  Forward Bifrost authentication  {forward}", soft_wrap=True)
    console.print("[dim]Allow by default: off for all four. Ctrl-C stops everything.[/]")


def _raise_interrupt(*_: object) -> None:
    raise KeyboardInterrupt


def _stop(procs: list[subprocess.Popen[bytes]]) -> None:
    for p in procs:
        if p.poll() is None:
            p.terminate()
    for p in procs:
        try:
            p.wait(timeout=5)
        except subprocess.TimeoutExpired:
            p.kill()


def _ngrok_urls(
    ports: dict[AgentName, int], config: Path
) -> tuple[dict[AgentName, str], list[subprocess.Popen[bytes]]]:
    procs: list[subprocess.Popen[bytes]] = []
    with httpx.Client(timeout=2.0) as client:
        urls = _read_ngrok(client, ports)
        if urls is not None and len(urls) < len(ports):
            raise SystemExit(
                f"an ngrok agent is already running ({NGROK_API}) without tunnels for "
                + ", ".join(a.value for a in ports if a not in urls)
                + ". Stop it, or add the endpoints from ngrok.yml.example to it."
            )
        if urls is None:
            ngrok = _start_ngrok(config)
            procs.append(ngrok)
            deadline = time.monotonic() + STARTUP_TIMEOUT_S
            while time.monotonic() < deadline and ngrok.poll() is None:
                urls = _read_ngrok(client, ports)
                if urls is not None and len(urls) == len(ports):
                    break
                time.sleep(0.5)
            if urls is None or len(urls) < len(ports):
                _stop(procs)
                raise SystemExit("ngrok did not expose all four agents; check the ngrok output above")
    return urls, procs


def run_agents(provider: str = "ngrok", config: Path = Path(NGROK_CONFIG)) -> int:
    missing = missing_inbound_keys()
    if missing:
        raise SystemExit(
            f"{provider} makes the agents public; set these in .env first (openssl rand -hex 32): " + ", ".join(missing)
        )

    ports = {a: AgentSettings.for_agent(a).port for a in AgentName}
    if provider == "cloudflared":
        urls, procs = _start_cloudflared(ports)
    else:
        urls, procs = _ngrok_urls(ports, config)

    # From here every child (the tunnel and each agent started so far) is stopped on any exit,
    # including an agent that fails to spawn halfway through.
    try:
        _print_registration(urls)
        # Treat SIGTERM (docker stop, process managers) like Ctrl-C so children are stopped.
        signal.signal(signal.SIGTERM, _raise_interrupt)
        # Quick tunnels buffer server-sent events, so live progress cannot cross them.
        quick = provider == "cloudflared" and not Path(CLOUDFLARED_CONFIG).is_file()
        if quick:
            console.print("[dim]Quick tunnels buffer SSE: streaming is off, so jobs show no live sub-steps.[/]")
        for agent, url in urls.items():
            env = os.environ | {f"{agent.env_prefix}PUBLIC_URL": url}
            if quick:
                env["A2A_STREAMING"] = "false"
            procs.append(subprocess.Popen([sys.executable, "-m", "revops_copilot.cli", "serve", agent.value], env=env))

        while True:
            for p in procs:
                if p.poll() is not None:
                    console.print(f"[red]{' '.join(map(str, p.args))} exited ({p.returncode}); stopping all[/]")
                    return p.returncode or 1
            time.sleep(0.5)
    except KeyboardInterrupt:
        return 0
    finally:
        _stop(procs)
