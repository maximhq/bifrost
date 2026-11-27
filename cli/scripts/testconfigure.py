#!/usr/bin/env python3
"""Build and exercise the shipped CLI as subprocesses (Python 3.11+, stdlib only).

Run: python3 scripts/testconfigure.py -v
No personal configuration, keyring credentials, live gateway, or provider is used.
"""
import json
import os
import shutil
from pathlib import Path
import re
import subprocess
import tempfile
import threading
import tomllib
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlsplit

CLI_ROOT = Path(__file__).resolve().parents[1]
AGENTS = ("claude", "codex", "opencode", "gemini", "pi", "antigravity")
KEY = "sk-bf-subprocess-integration"


def isolated_env(home):
    env = {key: value for key, value in os.environ.items()
           if not key.startswith("BIFROST_") and key not in {
               "HOME", "USERPROFILE", "CODEX_HOME", "XDG_CONFIG_HOME",
               "OPENCODE_CONFIG", "OPENCODE_CONFIG_CONTENT", "CLAUDE_CONFIG_DIR",
               "PI_CODING_AGENT_DIR", "ZDOTDIR", "SHELL"}}
    env.update(HOME=str(home), USERPROFILE=str(home), SHELL="/bin/bash",
               CODEX_HOME=str(home / "codex-home"),
               XDG_CONFIG_HOME=str(home / "xdg"),
               BIFROST_STATE_PATH=str(home / "bifrost" / "state.json"),
               BIFROST_VIRTUAL_KEY=KEY,
               BIFROST_AGENT_TOKEN="sso-must-not-be-persisted",
               BIFROST_MANAGEMENT_KEY="management-must-not-be-persisted")
    return env


def config_paths(home, agent, existing=False):
    return {
        "claude": [home / ".claude/settings.json"],
        "codex": [home / "codex-home/config.toml"],
        "opencode": [home / ("xdg/opencode/opencode.jsonc" if existing
                             else "xdg/opencode/opencode.json")],
        "gemini": [home / ".gemini/settings.json", home / ".gemini/.env"],
        "pi": [home / ".pi/agent/models.json", home / ".pi/agent/settings.json", home / ".pi/agent/auth.json"],
        "antigravity": [home / ".gemini/antigravity-cli/settings.json", home / ".bashrc"],
    }[agent]


class CLIProcessTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.build_dir = tempfile.TemporaryDirectory(prefix="bifrost-cli-build-")
        cls.binary = Path(cls.build_dir.name) / ("bifrost.exe" if os.name == "nt" else "bifrost")
        env = dict(os.environ, GOWORK="off")
        result = subprocess.run(["go", "build", "-o", str(cls.binary), "."],
                                cwd=CLI_ROOT, env=env, capture_output=True, text=True, timeout=120)
        if result.returncode:
            cls.build_dir.cleanup()
            raise RuntimeError(f"CLI build failed:\n{result.stderr}")

    @classmethod
    def tearDownClass(cls):
        cls.build_dir.cleanup()

    def cli(self, env, *args, succeeds=True, base="http://127.0.0.1:1"):
        result = subprocess.run([str(self.binary), "--base-url", base, *args],
                                env=env, capture_output=True, text=True, timeout=15)
        self.assertEqual(result.returncode == 0, succeeds,
                         f"bifrost {args}: exit={result.returncode}\n{result.stdout}\n{result.stderr}")
        return result

    def check_configured(self, home, agent, paths, model):
        for path in paths:
            body = path.read_text()
            if not (agent == "pi" and path.name == "auth.json") and not (agent == "antigravity" and path.name == ".bashrc"):
                self.assertIn(model, body, str(path))
            self.assertNotIn("sso-must-not-be-persisted", body)
            self.assertNotIn("management-must-not-be-persisted", body)
            if os.name != "nt":
                self.assertEqual(path.stat().st_mode & 0o777, 0o600)
        if agent == "codex":
            cfg = tomllib.loads(paths[0].read_text())
            self.assertEqual(cfg["model_provider"], "bifrost")
            provider = cfg["model_providers"]["bifrost"]
            self.assertEqual(provider["experimental_bearer_token"], KEY)
            self.assertEqual(provider["base_url"], "http://127.0.0.1:1/openai/v1")
            self.assertFalse(provider["requires_openai_auth"])
            self.assertNotIn("auth", provider)
            self.assertEqual((home / "codex-home/auth.json").read_bytes(), b'{"login":"preserve-me"}')
        else:
            cfg = json.loads(paths[0].read_text())
            if agent == "claude":
                self.assertEqual(cfg["env"]["ANTHROPIC_AUTH_TOKEN"], KEY)
                self.assertEqual(cfg["env"]["ANTHROPIC_BASE_URL"], "http://127.0.0.1:1/anthropic")
            elif agent == "opencode":
                self.assertEqual(cfg["model"], "bifrost/" + model)
                options = cfg["provider"]["bifrost"]["options"]
                self.assertEqual(options["apiKey"], KEY)
                self.assertEqual(options["baseURL"], "http://127.0.0.1:1/openai/v1")
            elif agent == "pi":
                provider = cfg["providers"]["bifrost"]
                self.assertEqual(provider["api"], "openai-completions")
                self.assertEqual(provider["baseUrl"], "http://127.0.0.1:1/openai/v1")
                self.assertEqual(json.loads(paths[1].read_text())["defaultModel"], model)
                self.assertEqual(json.loads(paths[2].read_text())["bifrost"]["key"], KEY)
            elif agent == "antigravity":
                self.assertEqual(cfg["modelProvider"], "gemini")
                self.assertEqual(cfg["customModelsConfig"]["customModels"][model]["modelName"], "gemini-api://" + model)
                self.assertIn("GEMINI_API_KEY='" + KEY + "'", paths[1].read_text())
                self.assertEqual(paths[1].read_text().count("# >>> bifrost antigravity >>>"), 1)
            else:
                self.assertEqual(cfg["security"]["auth"]["selectedType"], "gemini-api-key")
                self.assertIn(f"GEMINI_API_KEY='{KEY}'", paths[1].read_text())
                self.assertIn("GOOGLE_GEMINI_BASE_URL='http://127.0.0.1:1/genai'", paths[1].read_text())
        receipts = list((home / "bifrost/receipts").glob("*.json"))
        self.assertEqual(len(receipts), 1)
        self.assertFalse(json.loads(receipts[0].read_text())["pending"])
        if os.name != "nt":
            self.assertEqual(receipts[0].stat().st_mode & 0o777, 0o600)

    def test_project_configuration_independent_parsers(self):
        for agent in ("claude", "codex", "opencode", "pi"):
            with self.subTest(agent=agent), tempfile.TemporaryDirectory() as tmp:
                home = Path(tmp)
                env = isolated_env(home)
                for flag, value in (("--project-id", "project-old"), ("--project-name", 'customer-"quoted"')):
                    self.cli(env, "configure", agent, "--model", "test/model", flag, value)
                path = config_paths(home, agent)[0]
                body = path.read_text()
                self.assertNotIn("project-old", body)
                if agent == "codex":
                    headers = tomllib.loads(body)["model_providers"]["bifrost"]["http_headers"]
                else:
                    data = json.loads(body)
                    if agent == "claude":
                        headers = dict(line.split(": ", 1) for line in data["env"]["ANTHROPIC_CUSTOM_HEADERS"].splitlines())
                    elif agent == "opencode":
                        headers = data["provider"]["bifrost"]["options"]["headers"]
                    else:
                        headers = data["providers"]["bifrost"]["headers"]
                self.assertEqual(headers, {"x-bf-project-name": 'customer-"quoted"'})
                self.cli(env, "unconfigure", agent)
                self.assertFalse(path.exists())

    def test_configure_reconfigure_refuse_edits_and_restore(self):
        for agent in AGENTS:
            for existing in (False, True):
                with self.subTest(agent=agent, existing=existing), tempfile.TemporaryDirectory() as directory:
                    home = Path(directory)
                    env = isolated_env(home)
                    paths = config_paths(home, agent, existing)
                    originals = {}
                    if existing:
                        for index, path in enumerate(paths):
                            original = b'{\n "theme": "preserved"\n}\n'
                            if agent == "opencode":
                                original = b'{\n // original comment\n "theme": "preserved",\n}\n'
                            if agent == "codex":
                                original = b'model = "old"\n[model_providers.bifrost.auth]\ncommand = "stale-helper"\n'
                            if index == 1 and agent in ("gemini", "antigravity"):
                                original = b"# original env\nKEEP='preserved'\nGEMINI_API_KEY='old-key'\n"
                            path.parent.mkdir(parents=True, exist_ok=True)
                            path.write_bytes(original)
                            path.chmod(0o640)
                            originals[path] = original
                    if agent == "codex":
                        auth = home / "codex-home/auth.json"
                        auth.parent.mkdir(parents=True, exist_ok=True)
                        auth.write_bytes(b'{"login":"preserve-me"}')
                    for model in ("provider/first", "provider/second"):
                        self.cli(env, "configure", agent, "--model", model)
                        self.check_configured(home, agent, paths, model)
                    edited = paths[0].read_bytes() + b"\n"
                    paths[0].write_bytes(edited)
                    for args in (("unconfigure", agent), ("configure", agent, "--model", "rejected")):
                        result = self.cli(env, *args, succeeds=False)
                        self.assertIn("changed after Bifrost configured it", result.stderr)
                        self.assertEqual(paths[0].read_bytes(), edited)
                    self.cli(env, "configure", agent, "--model", "provider/forced", "--force")
                    self.check_configured(home, agent, paths, "provider/forced")
                    paths[0].write_bytes(paths[0].read_bytes() + b"\n")
                    self.cli(env, "unconfigure", agent, "--force")
                    for path in paths:
                        if existing:
                            self.assertEqual(path.read_bytes(), originals[path])
                            if os.name != "nt":
                                self.assertEqual(path.stat().st_mode & 0o777, 0o640)
                        else:
                            self.assertFalse(path.exists(), str(path))
                    self.assertEqual(list((home / "bifrost/receipts").glob("*.json")), [])
                    self.cli(env, "unconfigure", agent, succeeds=False)

    def test_missing_key_and_model_leave_no_configuration(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            env = isolated_env(home)
            env["BIFROST_VIRTUAL_KEY"] = ""
            for agent in AGENTS:
                result = self.cli(env, "configure", agent, "--model", "provider/model", succeeds=False)
                self.assertIn("a virtual key is required", result.stderr)
                for path in config_paths(home, agent):
                    self.assertFalse(path.exists())
            env["BIFROST_VIRTUAL_KEY"] = KEY
            result = self.cli(env, "configure", "opencode", succeeds=False)
            self.assertIn("requires a model", result.stderr)
            self.assertFalse(config_paths(home, "opencode")[0].exists())
            self.assertEqual(list(home.rglob("*-opencode.json")), [])

    def test_explicit_opencode_path_and_normal_restore(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            env = isolated_env(home)
            path = home / "custom/opencode.json"
            path.parent.mkdir(parents=True)
            original = b'{"theme":"keep"}\n'
            path.write_bytes(original)
            env["OPENCODE_CONFIG"] = str(path)
            self.cli(env, "configure", "opencode", "--model", "provider/model")
            self.check_configured(home, "opencode", [path], "provider/model")
            self.assertFalse(config_paths(home, "opencode")[0].exists())
            self.cli(env, "unconfigure", "opencode")
            self.assertEqual(path.read_bytes(), original)

    def test_malformed_settings_roll_back_without_losing_original(self):
        for agent in ("claude", "opencode", "gemini", "pi", "antigravity"):
            with self.subTest(agent=agent), tempfile.TemporaryDirectory() as directory:
                home = Path(directory)
                env = isolated_env(home)
                path = config_paths(home, agent)[0]
                path.parent.mkdir(parents=True)
                original = b"{invalid user settings"
                path.write_bytes(original)
                result = self.cli(env, "configure", agent, "--model", "provider/model", succeeds=False)
                self.assertIn("parse", result.stderr)
                self.assertEqual(path.read_bytes(), original)
                self.assertEqual(list((home / "bifrost/receipts").glob("*.json")), [])

    @unittest.skipIf(os.name == "nt", "POSIX executable fixtures")
    def test_new_agent_aliases_launch_and_restore(self):
        for agent in ("pi", "antigravity", "anti-gravity", "agy", "AGUI"):
            with self.subTest(agent=agent), tempfile.TemporaryDirectory() as directory:
                home = Path(directory)
                env = isolated_env(home)
                env.pop("BIFROST_AGENT_TOKEN", None)
                bin_dir = home / "bin"
                bin_dir.mkdir()
                executable = bin_dir / ("pi" if agent == "pi" else "agy")
                executable.write_text("#!" + shutil.which("python3") + "\n" +
                    "import os,json,sys,pathlib\n" +
                    "p=pathlib.Path(os.environ['HOME'])\n" +
                    "if pathlib.Path(sys.argv[0]).name=='pi':\n" +
                    " d=pathlib.Path(os.environ['PI_CODING_AGENT_DIR']); c=json.loads((d/'models.json').read_text()); assert c['providers']['bifrost']['api']=='openai-completions'; assert json.loads((d/'auth.json').read_text())['bifrost']['key']==os.environ['OPENAI_API_KEY']\n" +
                    "else:\n" +
                    " c=json.loads((p/'.gemini/antigravity-cli/settings.json').read_text()); assert c['modelProvider']=='gemini'; assert os.environ['GEMINI_API_KEY']=='" + KEY + "'\n" +
                    "assert sys.argv[-2:]==['--print','argument with spaces']\n" +
                    "print('AGENT_FIXTURE_OK')\n")
                executable.chmod(0o700)
                env["PATH"] = str(bin_dir) + os.pathsep + env["PATH"]
                result = self.cli(env, agent, "--skip-verify", "--model", "provider/model",
                                  "--print", "argument with spaces")
                self.assertIn("AGENT_FIXTURE_OK", result.stdout)
                canonical = "pi" if agent == "pi" else "antigravity"
                for path in config_paths(home, canonical):
                    self.assertFalse(path.exists(), str(path))
                self.cli(env, "configure", agent, "--model", "provider/saved")
                # The bare alias must also launch using persisted model settings.
                result = self.cli(env, agent, "--skip-verify", "--print", "argument with spaces")
                self.assertIn("AGENT_FIXTURE_OK", result.stdout)
                self.cli(env, "unconfigure", agent)

    @unittest.skipUnless(os.environ.get("BIFROST_TEST_NATIVE_AGENTS") == "1",
                         "set BIFROST_TEST_NATIVE_AGENTS=1 with Pi and agy installed")
    def test_native_agents_stream_from_local_gateway(self):
        received = []

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(b'{"data":[{"id":"provider/test","object":"model"}]}')

            def do_POST(self):
                body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", "0"))))
                received.append((self.path, self.headers, body))
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.end_headers()
                if self.path.startswith("/genai/"):
                    events = [{"candidates": [{"content": {"role": "model", "parts": [{"text": "native fixture ok"}]}, "finishReason": "STOP"}], "usageMetadata": {"promptTokenCount": 1, "candidatesTokenCount": 3, "totalTokenCount": 4}}]
                else:
                    events = [{"id": "fixture", "object": "chat.completion.chunk", "created": 1, "model": "provider/test", "choices": [{"index": 0, "delta": {"role": "assistant", "content": "native fixture ok"}, "finish_reason": None}]}, {"id": "fixture", "object": "chat.completion.chunk", "created": 1, "model": "provider/test", "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}]}]
                for event in events:
                    self.wfile.write(("data: " + json.dumps(event) + "\n\n").encode())
                if not self.path.startswith("/genai/"):
                    self.wfile.write(b"data: [DONE]\n\n")

            def log_message(self, *args):
                pass

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            for agent, binary_name in (("pi", "pi"), ("antigravity", "agy")):
                with self.subTest(agent=agent), tempfile.TemporaryDirectory() as directory:
                    home = Path(directory)
                    env = isolated_env(home)
                    env.pop("BIFROST_AGENT_TOKEN", None)
                    native = os.environ.get("BIFROST_TEST_PI_BINARY") if agent == "pi" else None
                    native = native or shutil.which(binary_name)
                    self.assertTrue(native, f"Install {binary_name} for native integration tests")
                    bin_dir = home / "bin"
                    bin_dir.mkdir()
                    (bin_dir / binary_name).symlink_to(Path(native).resolve())
                    env["PATH"] = str(bin_dir) + os.pathsep + env["PATH"]
                    before = len(received)
                    result = self.cli(env, agent, "--model", "provider/test", "--", "--print", "Say native fixture ok",
                                      base=f"http://127.0.0.1:{server.server_port}")
                    self.assertIn("native fixture ok", result.stdout)
                    # A configured alias must also work without Bifrost model flags.
                    self.cli(env, "configure", agent, "--model", "provider/test",
                             base=f"http://127.0.0.1:{server.server_port}")
                    result = self.cli(env, agent, "--", "--print", "Say native fixture ok",
                                      base=f"http://127.0.0.1:{server.server_port}")
                    self.assertIn("native fixture ok", result.stdout)
                    self.cli(env, "unconfigure", agent, "--force")
                    requests = received[before:]
                    if agent == "pi":
                        matching = [r for r in requests if r[0] == "/openai/v1/chat/completions"]
                        self.assertTrue(matching, requests)
                        self.assertEqual(matching[0][1].get("Authorization"), "Bearer " + KEY)
                        self.assertEqual(matching[0][2]["model"], "provider/test")
                    else:
                        matching = [r for r in requests if r[0].startswith("/genai/v1beta/models/provider/test:streamGenerateContent")]
                        self.assertTrue(matching, requests)
                        self.assertEqual(matching[0][1].get("X-Goog-Api-Key"), KEY)
                    for path in config_paths(home, agent):
                        if agent == "antigravity" and path.name == "settings.json" and path.exists():
                            # The native CLI saves its own onboarding preferences.
                            saved = json.loads(path.read_text())
                            self.assertNotIn("modelProvider", saved)
                            self.assertNotIn("model", saved)
                            self.assertNotIn("customModelsConfig", saved)
                        else:
                            self.assertFalse(path.exists(), str(path))
        finally:
            server.shutdown()
            server.server_close()
            thread.join(timeout=5)

    def test_all_supplemental_management_operations_over_http(self):
        received = []

        class Handler(BaseHTTPRequestHandler):
            def handle_request(self):
                received.append((self.command, self.path, dict(self.headers),
                                 self.rfile.read(int(self.headers.get("Content-Length", "0")))))
                rejected = self.path.startswith("/api/rss/items?reject=1")
                self.send_response(403 if rejected else 200)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(b'{"error":{"message":"integration forbidden"}}' if rejected
                                 else b'{"integration":"ok"}')

            do_GET = do_POST = do_PUT = do_DELETE = do_PATCH = handle_request

            def log_message(self, *_):
                pass

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            with tempfile.TemporaryDirectory() as directory:
                env = isolated_env(Path(directory))
                base = f"http://127.0.0.1:{server.server_port}"
                document = json.loads((CLI_ROOT / "internal/operations/management.json").read_text())
                for path, item in document["paths"].items():
                    for method, operation in item.items():
                        with self.subTest(operation=operation["operationId"]):
                            args = ["api", "call", operation["operationId"], "--yes",
                                    "--body", '{"probe":"integration"}', "--query", "tag=one",
                                    "--query", "tag=two", "--output", "raw"]
                            for parameter in re.findall(r"\{([^}]+)\}", path):
                                args += ["--param", parameter + "=value/part"]
                            before = len(received)
                            result = self.cli(env, *args, base=base)
                            self.assertEqual(json.loads(result.stdout), {"integration": "ok"})
                            self.assertEqual(len(received), before + 1)
                            actual_method, actual_url, headers, body = received[-1]
                            self.assertEqual(actual_method, method.upper())
                            self.assertEqual(urlsplit(actual_url).path, re.sub(r"\{[^}]+\}", "value%2Fpart", path))
                            self.assertEqual(parse_qs(urlsplit(actual_url).query)["tag"], ["one", "two"])
                            self.assertEqual(json.loads(body), {"probe": "integration"})
                            auth = operation.get("x-cli-auth", "management")
                            expected = {"none": None, "agent": "Bearer sso-must-not-be-persisted",
                                        "management": "Bearer management-must-not-be-persisted"}[auth]
                            self.assertEqual(headers.get("Authorization"), expected)
                named = [
                    (["rss-feeds", "list"], "GET", "/api/rss/feeds", False),
                    (["jobs", "cancel", "job/one", "--yes"], "POST", "/api/sidekiq/jobs/job%2Fone/cancel", False),
                    (["agent-gateway", "get", "agent/one"], "GET", "/api/agents/agent%2Fone", False),
                    (["adaptive-routing", "get"], "GET", "/api/adaptive-routing/config", False),
                    (["budget-requests", "list"], "GET", "/api/governance/budget-requests", False),
                    (["org-chart", "summary"], "GET", "/api/governance/org-chart/summary", False),
                    (["identity-providers", "list"], "GET", "/api/scim/providers", False),
                    (["provisioning", "reconcile", "--yes"], "POST", "/api/scim/provision/reconcile", True),
                    (["teams", "attach-access-profile", "team/one"], "POST", "/api/governance/teams/team%2Fone/access-profile", True),
                    (["mcp-clients", "refresh-tools", "client/one"], "POST", "/api/mcp/client/client%2Fone/refresh-tools", False),
                    (["warp-config", "update"], "PUT", "/api/warp/config", True),
                ]
                payload = Path(directory) / "request.json"
                payload.write_text('{"probe":"file-input"}')
                for args, method, path, has_body in named:
                    with self.subTest(command=args):
                        if has_body:
                            args += ["--file", str(payload)]
                        result = self.cli(env, *args, "--output", "raw", base=base)
                        self.assertEqual(json.loads(result.stdout), {"integration": "ok"})
                        actual_method, actual_path, headers, body = received[-1]
                        self.assertEqual((actual_method, actual_path), (method, path))
                        self.assertEqual(headers.get("Authorization"), "Bearer management-must-not-be-persisted")
                        self.assertEqual(body, payload.read_bytes() if has_body else b"")
                result = self.cli(env, "api", "call", "listRSSItems", "--query", "reject=1",
                                  base=base, succeeds=False)
                self.assertEqual(result.returncode, 3, "HTTP 403 must exit with authorization failure")
                self.assertIn("integration forbidden", result.stderr)
                count = len(received)
                result = self.cli(env, "api", "call", "cancelSidekiqJob", "--param", "id=job", base=base, succeeds=False)
                self.assertIn("without --yes", result.stderr)
                self.cli(env, "api", "call", "cancelSidekiqJob", "--param", "id=job", "--dry-run", base=base)
                self.assertEqual(len(received), count, "refusal/dry-run sent HTTP requests")
        finally:
            server.shutdown()
            server.server_close()
            thread.join(timeout=5)


if __name__ == "__main__":
    unittest.main()
