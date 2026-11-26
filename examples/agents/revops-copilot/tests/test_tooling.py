"""Scenario files, the scenario checker, and trace row parsing."""

from __future__ import annotations

from pathlib import Path
from typing import Any

import pytest
import yaml

from revops_copilot import scenarios, trace
from revops_copilot.common.policy import Hop, PolicyDenied, Surface

SCENARIOS = sorted((Path(__file__).resolve().parents[1] / "scenarios").glob("*.yaml"))


def test_every_surface_has_a_scenario() -> None:
    declared = {yaml.safe_load(p.read_text())["surface"] for p in SCENARIOS}
    for surface in Surface:
        if surface is Surface.UNKNOWN:
            continue
        assert surface.value in declared, f"no scenario for {surface}"


@pytest.mark.parametrize("path", SCENARIOS, ids=lambda p: p.stem)
def test_scenarios_are_well_formed(path: Path) -> None:
    spec = yaml.safe_load(path.read_text())
    assert spec["kind"] in {"ask", "a2a", "llm", "mcp_list", "mcp_call"}
    assert spec.get("description")
    expect = spec.get("expect", {})
    if "surface" in expect:
        Surface(expect["surface"])
    if spec["kind"] == "mcp_call":
        assert spec.get("tool")  # slug is optional: no slug means Bifrost's /mcp


def test_load_expands_env(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("BLOCKED_MODEL", "openai/gpt-x")
    assert scenarios._load("model-not-allowed")["model"] == "openai/gpt-x"


def test_check_matches_surface_and_patterns() -> None:
    denied = PolicyDenied(Surface.AGENT_GRANT, Hop.A2A, 403, "agent_blocked", "agent_blocked")
    assert scenarios._check({"expect": {"surface": "2-agent-grant"}}, denied, "")
    assert not scenarios._check({"expect": {"surface": "3-llm-governance"}}, denied, "")
    assert not scenarios._check({"expect": {"allowed": True}}, denied, "")
    pii = {"expect": {"absent_patterns": [r"[\w.]+@[\w.]+"]}}
    assert scenarios._check(pii, None, "contact <REDACTED>")
    assert not scenarios._check(pii, None, "contact jane@example.com")


def test_model_not_allowed_needs_the_model_blocked_code(monkeypatch: pytest.MonkeyPatch) -> None:
    # A budget or rate limit refusal is the same surface; only model_blocked proves the allowlist fired.
    monkeypatch.setenv("BLOCKED_MODEL", "openai/gpt-x")
    spec = scenarios._load("model-not-allowed")
    budget = PolicyDenied(Surface.LLM_GOVERNANCE, Hop.LLM, 429, "budget exceeded", "budget_exceeded")
    blocked = PolicyDenied(Surface.LLM_GOVERNANCE, Hop.LLM, 403, "model not allowed", "model_blocked")
    assert not scenarios._check(spec, budget, "")
    assert scenarios._check(spec, blocked, "")


def test_trace_reads_nested_fields_defensively() -> None:
    row = {"timestamp": "t1", "status": "error", "error_details": {"error": {"message": "guardrail_intervention: PII"}}}
    assert trace._get(row, "error_details.error.message").startswith("guardrail")
    assert trace._checks("mcp") == "1,5,6,7"
    assert "⑦ refused" in trace._fired("mcp", row)
    assert trace._items({"logs": [row, "junk"]}, "data", "logs") == [row]


def test_serve_without_identity_exits_with_one_line(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    from revops_copilot import cli

    monkeypatch.chdir(tmp_path)  # no .env here
    monkeypatch.setattr(cli, "load_dotenv", lambda **_: None)
    monkeypatch.setenv("AUTH_MODE", "vk")
    # Only the entry agent holds an identity; subagents boot without one.
    monkeypatch.delenv("REVOPS_COPILOT_VK", raising=False)
    with pytest.raises(SystemExit) as exc:
        cli.main(["serve", "revops-copilot"])
    message = str(exc.value.code)
    assert "revops-copilot" in message and "REVOPS_COPILOT_VK" in message and "BIFROST_SETUP.md" in message


def test_tunnel_reads_ngrok_endpoints_by_name_then_port() -> None:
    from revops_copilot import tunnel
    from revops_copilot.common.config import AgentName

    endpoints = {
        "endpoints": [
            {"name": "revops-copilot", "url": "https://rc.ngrok.app", "upstream": {"url": "http://localhost:9100"}},
            {"name": "other", "url": "https://wrong.ngrok.app", "upstream": {"url": "http://localhost:9101"}},
            {"name": "support-analyst", "url": "https://sa.ngrok.app/", "upstream": {"url": "9101"}},
        ]
    }
    # Deprecated /api/tunnels shape, matched by upstream port.
    tunnels = {"tunnels": [{"name": "x", "public_url": "https://rw.ngrok.app", "config": {"addr": "localhost:9102"}}]}
    ports = {a: p for a, p in zip(AgentName, (9100, 9101, 9102, 9103), strict=True)}
    urls = tunnel.public_urls(ports, endpoints, tunnels)
    assert urls == {
        AgentName.SUPERVISOR: "https://rc.ngrok.app",
        AgentName.SUPPORT_ANALYST: "https://sa.ngrok.app",
        AgentName.REPORT_WRITER: "https://rw.ngrok.app",
    }


def test_tunnel_refuses_public_agents_without_inbound_keys(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    from revops_copilot import tunnel

    monkeypatch.chdir(tmp_path)
    for p in ("REVOPS_COPILOT_", "SUPPORT_ANALYST_", "REPORT_WRITER_"):
        monkeypatch.setenv(f"{p}INBOUND_KEY", "k")
    monkeypatch.delenv("PUBLISHER_INBOUND_KEY", raising=False)
    assert tunnel.missing_inbound_keys() == ["PUBLISHER_INBOUND_KEY"]


def _answers(*values: str):  # type: ignore[no-untyped-def]
    it = iter(values)
    return lambda _prompt: next(it)


def test_preflight_asks_for_the_gateway_url(monkeypatch: pytest.MonkeyPatch) -> None:
    import os

    from revops_copilot import preflight

    monkeypatch.delenv("BIFROST_URL", raising=False)
    # Empty and invalid answers are asked again; a trailing slash is dropped.
    url = preflight.ask_gateway_url(_answers("", "bifrost.example.com", "https://bifrost.example.com/"), tty=True)
    assert url == "https://bifrost.example.com"
    assert os.environ["BIFROST_URL"] == url


def test_preflight_offers_the_configured_url_as_default(monkeypatch: pytest.MonkeyPatch) -> None:
    from revops_copilot import preflight

    monkeypatch.setenv("BIFROST_URL", "https://configured.example")
    assert preflight.ask_gateway_url(_answers(""), tty=True) == "https://configured.example"


def test_preflight_without_a_terminal_needs_bifrost_url(monkeypatch: pytest.MonkeyPatch) -> None:
    from revops_copilot import preflight

    monkeypatch.setenv("BIFROST_URL", "https://configured.example")
    assert preflight.ask_gateway_url(_answers(), tty=False) == "https://configured.example"
    monkeypatch.delenv("BIFROST_URL")
    with pytest.raises(SystemExit, match="BIFROST_URL"):
        preflight.ask_gateway_url(_answers(), tty=False)


async def test_preflight_probes_the_listen_port_and_shows_the_advertised_url(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    import httpx
    import respx

    from revops_copilot import preflight

    monkeypatch.chdir(tmp_path)
    # A public URL in the environment must not change where liveness is probed.
    monkeypatch.setenv("REVOPS_COPILOT_PUBLIC_URL", "https://rc.ngrok.app")
    rows: list[tuple[str, bool | None, str]] = []
    report = preflight.Report()
    monkeypatch.setattr(report, "add", lambda check, ok, detail="": rows.append((check, ok, detail)))
    card = {"supportedInterfaces": [{"url": "https://rc.ngrok.app/"}]}
    with respx.mock(assert_all_called=False) as mock:
        mock.get("http://127.0.0.1:9100/.well-known/agent-card.json").respond(200, json=card)
        for port in (9101, 9102, 9103):
            mock.get(f"http://127.0.0.1:{port}/.well-known/agent-card.json").mock(side_effect=httpx.ConnectError("down"))
        await preflight._local_agents(report)

    assert rows[0] == ("revops-copilot: listening on :9100", True, "card advertises https://rc.ngrok.app/")
    assert rows[1][0] == "support-analyst: listening on :9101"
    assert rows[1][1] is False  # agents down is a failure, not a skip
    assert "make agents-ngrok" in rows[1][2]


def test_tunnel_finds_the_default_ngrok_config_from_config_check() -> None:
    from revops_copilot import tunnel

    out = "Valid configuration file at /Users/me/Library/Application Support/ngrok/ngrok.yml\n"
    assert tunnel.default_config_from_check(out) == "/Users/me/Library/Application Support/ngrok/ngrok.yml"
    # Help text or an error is never mistaken for a path.
    assert tunnel.default_config_from_check("NAME:\n  config - update or migrate ngrok's configuration file\n") == ""


def test_tunnel_reads_the_cloudflared_quick_tunnel_hostname() -> None:
    from revops_copilot import tunnel

    assert tunnel.quick_tunnel_url({"hostname": "calm-river-1234.trycloudflare.com"}) == (
        "https://calm-river-1234.trycloudflare.com"
    )
    # Not ready yet: cloudflared answers with an empty hostname until the tunnel is up.
    assert tunnel.quick_tunnel_url({"hostname": ""}) == ""
    assert tunnel.quick_tunnel_url(None) == ""


def test_tunnel_reads_named_tunnel_hostnames_from_ingress() -> None:
    from revops_copilot import tunnel
    from revops_copilot.common.config import AgentName

    config = {
        "tunnel": "revops",
        "credentials-file": "/home/me/.cloudflared/abc.json",
        "ingress": [
            {"hostname": "revops-copilot.example.com", "service": "http://127.0.0.1:9100"},
            {"hostname": "support-analyst.example.com", "service": "http://localhost:9101"},
            {"hostname": "report-writer.example.com", "service": "http://127.0.0.1:9102/"},
            {"hostname": "publisher.example.com", "service": "http://127.0.0.1:9103"},
            {"service": "http_status:404"},
        ],
    }
    ports = {a: p for a, p in zip(AgentName, (9100, 9101, 9102, 9103), strict=True)}
    assert tunnel.named_tunnel_urls(ports, config) == {
        AgentName.SUPERVISOR: "https://revops-copilot.example.com",
        AgentName.SUPPORT_ANALYST: "https://support-analyst.example.com",
        AgentName.REPORT_WRITER: "https://report-writer.example.com",
        AgentName.PUBLISHER: "https://publisher.example.com",
    }
    # A rule for another port, or the catch-all, never maps to an agent.
    assert tunnel.named_tunnel_urls({AgentName.PUBLISHER: 9999}, config) == {}


def test_tui_formats_a_progress_line_with_depth_surface_and_time() -> None:
    from revops_copilot import tui
    from revops_copilot.common.progress import Progress

    line = tui.format_line(Progress(agent="support-analyst", kind="tool", title="Pylon-search_issues",
                                    detail="212 rows", surfaces="⑥", depth=1, elapsed_ms=812))
    assert line.startswith("  ├ ")
    assert "Pylon-search_issues" in line and "212 rows" in line and "⑥" in line and "0.8s" in line
    top = tui.format_line(Progress(agent="revops-copilot", kind="delegate", title="support-analyst", depth=0))
    assert top.startswith("● ") and "support-analyst" in top


def test_tui_marks_a_running_step() -> None:
    from revops_copilot import tui
    from revops_copilot.common.progress import Progress

    line = tui.format_line(Progress(agent="a", kind="llm", title="decide next step (2/12)", status="start", depth=2))
    assert line.startswith("    ├ … LLM decide next step (2/12)")


async def test_preflight_reports_a_token_failure_as_a_row(monkeypatch: pytest.MonkeyPatch) -> None:
    from types import SimpleNamespace

    from revops_copilot import preflight

    class BadCredential:
        async def auth_headers(self) -> dict[str, str]:
            raise RuntimeError("AADSTS7000215: Invalid client secret provided.")

        async def close(self) -> None:
            return None

    session = SimpleNamespace(credential=BadCredential(), settings=SimpleNamespace(bifrost_url="http://bifrost.test"))
    session.headers = session.credential.auth_headers
    monkeypatch.setattr(preflight, "caller_session", lambda: session)
    rows: list[tuple[str, bool | None, str]] = []
    report = preflight.Report()
    monkeypatch.setattr(report, "add", lambda check, ok, detail="": rows.append((check, ok, detail)))
    assert await preflight._identity(report) is None
    assert rows == [("identity token", False, "RuntimeError: AADSTS7000215: Invalid client secret provided.")]


async def test_preflight_ungranted_check_needs_a_refusal(monkeypatch: pytest.MonkeyPatch) -> None:
    from contextlib import asynccontextmanager
    from types import SimpleNamespace

    from revops_copilot import preflight

    class EmptyListing:
        async def list_tools(self) -> Any:
            return SimpleNamespace(tools=[])

    @asynccontextmanager
    async def mcp(_: str) -> Any:
        yield EmptyListing()

    # The listing succeeded: discovery was not refused, even though no tools came back.
    session = SimpleNamespace(mcp=mcp, mcp_path=lambda slug: f"/mcp/{slug}")
    rows: list[tuple[str, bool | None, str]] = []
    report = preflight.Report()
    monkeypatch.setattr(report, "add", lambda check, ok, detail="": rows.append((check, ok, detail)))
    await preflight._mcp(report, session, preflight.UNGRANTED_SLUG, expect_access=False)  # type: ignore[arg-type]
    assert [ok for _, ok, _ in rows] == [False]


async def test_preflight_records_an_unreachable_gateway_as_rows(monkeypatch: pytest.MonkeyPatch) -> None:
    from types import SimpleNamespace

    from revops_copilot import preflight
    from revops_copilot.common.config import GlobalSettings

    closed = "http://127.0.0.1:1"  # nothing listens on port 1: every probe gets a connection error

    class Credential:
        async def auth_headers(self) -> dict[str, str]:
            return {"x-bf-vk": "sk-bf-test"}

        async def close(self) -> None:
            return None

    session = SimpleNamespace(credential=Credential(), settings=SimpleNamespace(bifrost_url=closed),
                              agent_url=lambda agent: f"{closed}/agents/a2a/{agent.value}")
    session.headers = session.credential.auth_headers
    monkeypatch.setattr(preflight, "caller_session", lambda: session)
    rows: list[tuple[str, bool | None, str]] = []
    report = preflight.Report()
    monkeypatch.setattr(report, "add", lambda check, ok, detail="": rows.append((check, ok, detail)))

    await preflight._admin(report, GlobalSettings(bifrost_url=closed), "admin-token")
    assert await preflight._identity(report) is None
    await preflight._gateway(report, session)  # type: ignore[arg-type]
    assert rows and all(ok is False and "ConnectError" in detail for _, ok, detail in rows)
    assert any(check.startswith("identity") for check, _, _ in rows)


async def test_ask_exits_nonzero_when_the_task_did_not_complete(monkeypatch: pytest.MonkeyPatch) -> None:
    from types import SimpleNamespace

    from revops_copilot import cli, tui
    from revops_copilot.common.a2aserver import AgentReply

    class Closable:
        async def close(self) -> None:
            return None

    class FakeSession:
        scope = SimpleNamespace(context_id="ctx-1")
        settings = SimpleNamespace(bifrost_url="http://bifrost.test")
        credential = Closable()
        emit = None

        async def aclose(self) -> None:
            return None

    async def failed_task(*_: object, **__: object) -> AgentReply:
        return AgentReply("publisher failed", [], {}, None, "TASK_STATE_FAILED")

    monkeypatch.setattr(cli, "caller_session", FakeSession)
    monkeypatch.setattr(tui, "call_agent", failed_task)
    code, session_id = await tui.run_job("upload the report")
    assert code == 1
    assert session_id == "ctx-1"


def test_preflight_refuses_plaintext_for_a_remote_gateway(monkeypatch: pytest.MonkeyPatch) -> None:
    # The admin token and the caller credential would cross the network in clear text.
    from revops_copilot import preflight

    monkeypatch.delenv("BIFROST_URL", raising=False)
    url = preflight.ask_gateway_url(_answers("http://bifrost.example.com", "https://bifrost.example.com"), tty=True)
    assert url == "https://bifrost.example.com"
    monkeypatch.setenv("BIFROST_URL", "http://bifrost.example.com")
    with pytest.raises(SystemExit, match="https"):
        preflight.ask_gateway_url(_answers(), tty=False)
    # A gateway on this machine stays reachable over plain http.
    for local in ("http://localhost:8080", "http://127.0.0.1:8080", "http://[::1]:8080"):
        monkeypatch.setenv("BIFROST_URL", local)
        assert preflight.ask_gateway_url(_answers(), tty=False) == local


def test_preflight_stores_the_normalized_url_without_a_terminal(monkeypatch: pytest.MonkeyPatch) -> None:
    import os

    from revops_copilot import preflight

    monkeypatch.setenv("BIFROST_URL", "  https://configured.example/  ")
    assert preflight.ask_gateway_url(_answers(), tty=False) == "https://configured.example"
    assert os.environ["BIFROST_URL"] == "https://configured.example"


def test_tunnel_stops_every_child_when_an_agent_fails_to_spawn(monkeypatch: pytest.MonkeyPatch) -> None:
    from types import SimpleNamespace

    from revops_copilot import tunnel
    from revops_copilot.common.config import AgentName

    started: list[object] = []
    stopped: list[object] = []
    tunnel_proc = SimpleNamespace(name="ngrok")
    started.append(tunnel_proc)

    def fake_popen(args: list[str], **_: object) -> object:
        if len(started) == 3:  # the tunnel and two agents are up; the third agent cannot start
            raise OSError("cannot spawn")
        proc = SimpleNamespace(name=args[-1])
        started.append(proc)
        return proc

    urls = {a: f"https://{a.value}.example" for a in AgentName}
    monkeypatch.setattr(tunnel, "missing_inbound_keys", lambda: [])
    monkeypatch.setattr(tunnel, "_ngrok_urls", lambda ports, config: (urls, [tunnel_proc]))
    monkeypatch.setattr(tunnel, "_print_registration", lambda _: None)
    monkeypatch.setattr(tunnel, "_stop", stopped.extend)
    monkeypatch.setattr(tunnel.subprocess, "Popen", fake_popen)
    with pytest.raises(OSError):
        tunnel.run_agents("ngrok")
    assert stopped == started


def test_happy_path_requires_a_completed_job_with_a_published_pdf() -> None:
    from revops_copilot.common.a2aserver import FileBlob

    spec = scenarios._load("happy-path")
    assert spec["expect"].get("completed") and spec["expect"].get("pdf") and spec["expect"].get("published")
    pdf = [FileBlob("r.pdf", b"%PDF", "application/pdf")]
    link = {"publish": {"link": "https://drive.google.com/file/d/x/view"}}
    done = "TASK_STATE_COMPLETED"
    assert scenarios._check(spec, None, "done", state=done, files=pdf, data=link)
    # Not denied is not enough: no PDF, a failed upload or an unfinished task is a FAIL.
    assert not scenarios._check(spec, None, "done", state=done, files=[], data=link)
    assert not scenarios._check(spec, None, "done", state=done, files=pdf, data={"publish": {"link": ""}})
    assert not scenarios._check(spec, None, "done", state="TASK_STATE_FAILED", files=pdf, data=link)


def test_redaction_check_needs_content_to_have_come_back() -> None:
    pii = {"expect": {"absent_patterns": [r"[\w.]+@[\w.]+"]}}
    denied = PolicyDenied(Surface.MCP_GUARDRAIL, Hop.MCP, 400, "blocked", "guardrail_intervention")
    # Nothing came back, so nothing shows that redaction worked.
    assert not scenarios._check(pii, None, "")
    assert not scenarios._check(pii, denied, "")
    assert not scenarios._check(pii, None, "upstream error text", error=True)
    assert scenarios._check(pii, None, "contact <REDACTED>")


async def test_a_scenario_with_an_unset_placeholder_is_skipped(monkeypatch: pytest.MonkeyPatch) -> None:
    from types import SimpleNamespace

    monkeypatch.delenv("BLOCKED_MODEL", raising=False)
    sent: list[dict[str, Any]] = []

    async def execute(spec: dict[str, Any], _: Any) -> Any:
        sent.append(spec)
        return None, "", {}

    async def done() -> None:
        return None

    fake = SimpleNamespace(aclose=done, credential=SimpleNamespace(close=done), scope=SimpleNamespace(context_id="ctx"))
    monkeypatch.setattr(scenarios, "_session", lambda _: fake)
    monkeypatch.setattr(scenarios, "_execute", execute)
    # Unset, expandvars leaves "${BLOCKED_MODEL}" in the request; the scenario must skip instead.
    assert await scenarios.run_scenario("model-not-allowed") == 0
    assert sent == []


@pytest.mark.parametrize(("mode", "built"), [("entra", ["UNMAPPED_"]), ("vk", [])])
async def test_unknown_entra_identity_uses_the_unmapped_caller(monkeypatch: pytest.MonkeyPatch, mode: str, built: list[str]) -> None:
    # caller_vk would pick a VK credential first, so the Entra case needs its own scenario, run only in entra mode.
    monkeypatch.setenv("AUTH_MODE", mode)
    monkeypatch.setenv("UNMAPPED_ENTRA_CLIENT_ID", "unmapped-app")
    seen: list[str] = []

    def caller(prefix: str) -> Any:
        seen.append(prefix)
        raise RuntimeError("stop before any network call")

    monkeypatch.setattr(scenarios, "caller_session", caller)
    await scenarios.run_scenario("unknown-entra-identity")
    assert seen == built
    assert "UNMAPPED" not in scenarios._load("unknown-identity")["description"]


def _mock_bifrost(monkeypatch: pytest.MonkeyPatch, handler: Any) -> None:
    import functools

    import httpx

    monkeypatch.setattr(trace.httpx, "AsyncClient", functools.partial(httpx.AsyncClient, transport=httpx.MockTransport(handler)))


async def test_trace_fails_when_its_queries_fail(monkeypatch: pytest.MonkeyPatch) -> None:
    import httpx

    _mock_bifrost(monkeypatch, lambda _: httpx.Response(401, text="unauthorized"))
    # A failed read must not look like an empty trace to make or a script.
    assert await trace.trace("ctx-1") == 1


async def test_trace_names_the_surface_that_fired(monkeypatch: pytest.MonkeyPatch) -> None:
    import httpx
    from rich.console import Console

    blocked = {"timestamp": "t1", "model": "gpt-x", "status": "error",
               "error_details": {"status_code": 403, "error": {"type": "model_blocked", "message": "model not allowed"}}}
    monitored = {"timestamp": "t2", "model": "gpt-y", "status": "success",
                 "guardrail_debug": {"detections": [{"phase": "output", "action": "redact", "enforced": False}]}}
    down = {"timestamp": "t3", "model": "gpt-z", "status": "error",
            "error_details": {"status_code": 502, "error": {"message": "upstream connect error"}}}
    clean = {"timestamp": "t4", "model": "gpt-w", "status": "success"}

    def handler(request: httpx.Request) -> httpx.Response:
        rows = [blocked, monitored, down, clean] if request.url.path == "/api/logs" else []
        return httpx.Response(200, json={"logs": rows, "data": rows})

    _mock_bifrost(monkeypatch, handler)
    out = Console(record=True, width=240)
    monkeypatch.setattr(trace, "console", out)
    assert await trace.trace("ctx-1") == 0
    lines = {ln.split("│")[3].strip(): ln for ln in out.export_text().splitlines() if ln.count("│") > 4}
    # Model allowlist refusal is surface 3; the output monitor finding is surface 8.
    assert "③" in lines["gpt-x"] and "⑧" not in lines["gpt-x"]
    assert "⑧" in lines["gpt-y"] and "monitor" in lines["gpt-y"]
    # A 502 is not a policy refusal, and a clean call fired nothing.
    assert not any(c in lines["gpt-z"] for c in "①③④⑧")
    assert not any(c in lines["gpt-w"] for c in "①③④⑧")


def test_output_scan_needs_the_recorded_monitor_finding(monkeypatch: pytest.MonkeyPatch) -> None:
    spec = scenarios._load("output-scan")
    assert "BIFROST_ADMIN_TOKEN" in spec.get("requires", [])
    # The call succeeding proves nothing about surface 8; the log's monitor detection does.
    assert not scenarios._check(spec, None, "A fine quarter. Contact jane.doe@example.com.")
    assert scenarios._check(spec, None, "A fine quarter.", detections=1)


async def test_monitor_detections_wait_for_the_log_entry(monkeypatch: pytest.MonkeyPatch) -> None:
    import httpx

    calls: list[str] = []

    def handler(request: httpx.Request) -> httpx.Response:
        calls.append(request.url.params["session_id"])
        # The monitor writes its result a few seconds after the response: empty first, then the finding.
        found = [{"guardrail_debug": {"detections": [{"phase": "output", "enforced": False}]}}]
        return httpx.Response(200, json={"logs": found if len(calls) > 1 else [{}]})

    _mock_bifrost(monkeypatch, handler)
    n = await scenarios._monitor_detections("http://bifrost.test", "tok", "ctx-9", timeout_s=2, interval_s=0.01)
    assert n == 1 and calls[:2] == ["ctx-9", "ctx-9"]
