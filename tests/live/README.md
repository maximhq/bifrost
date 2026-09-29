# GPT Live E2E Tests

End-to-end tests for GPT Live sessions (`/v1/live/sessions` and `/openai/v1/live/sessions`) through a running Bifrost gateway: WebSocket and WebRTC sessions, sidebands (attach), recordings (content), billing windows, delegations, governance limits and the log row a session leaves.

The suite is a Go module of its own, run with `GOWORK=off`, and talks to the gateway over HTTP like a customer's app would.

## Two upstream modes

| Mode | Upstream | What it is for |
|---|---|---|
| `fake` (default) | a fake OpenAI live server the suite starts on `127.0.0.1:9411` | the full matrix: deterministic, free, no key, seconds to run |
| `real` | `api.openai.com` through the gateway's own key | a paid smoke set that catches protocol drift |

### Fake mode

Start the gateway from this folder's config, which points the `openai` provider at the fake and enforces auth, then run the suite:

```bash
# from the repo root; the databases land in tests/live/data/ (gitignored)
make dev PORT=8080 APP_DIR=$(pwd)/tests/live

make test-live
make test-live TESTCASE=TestBilling_VoiceWindowsAndFinalUsage
make test-live PATTERN=Attach
```

The suite creates its own virtual keys and pricing overrides through the API and removes the keys when each test ends. Prices are pinned so assertions are exact: a voice second costs $0.001, backend tokens $1 and $2 per million.

### Real mode

Start the gateway with a real OpenAI key in scope (the Python integration profile works), then:

```bash
make dev PORT=8080 APP_DIR=$(pwd)/tests/integrations/python

LIVE_UPSTREAM=real make test-live
LIVE_UPSTREAM=real BIFROST_VK=sk-bf-... make test-live   # when the gateway enforces auth
```

Spoken scenarios synthesise speech with macOS `say` and `afconvert` and skip where those are missing. Each real session costs a few cents.

Real sessions must be started on the `/openai/v1/live/sessions` route: on the generic route a bare `gpt-live-1` may resolve to another configured provider (Azure serves `gpt-*` names) that has no live support. The client also keeps the microphone streaming silence after it speaks; OpenAI stalls a session whose audio stops, leaving the assistant mid-sentence and the voice time short.

## Environment

| Variable | Default | Meaning |
|---|---|---|
| `BIFROST_BASE_URL` | `http://localhost:8080` | the gateway |
| `LIVE_UPSTREAM` | `fake` | `fake` or `real` |
| `LIVE_FAKE_ADDR` | `127.0.0.1:9411` | where the fake listens; must match `config.json` |
| `BIFROST_VK` | | credential for real mode when the gateway enforces auth |

Every test skips when the gateway does not answer `/health`.

## Files

| File | Covers |
|---|---|
| `admission_test.go` | refusals before and after the upgrade, model allowlists, wire rewriting, WebRTC create validation |
| `billing_test.go` | voice windows, WebRTC minimum, once-per-response, backend switches, budget exhaustion, upstream drop, client leaving |
| `delegation_test.go` | web search assembly, function-call round trip grouped into one delegation, transcript turns, client delegation (the app runs the backend and speaks the result back) |
| `attach_test.go` | sideband refusals, steering a WebRTC session, sideband policy checks |
| `content_test.go` | recording relay and refusals |
| `logging_test.go` | one row per session with totals, error and dropped sessions |
| `governance_test.go` | one request per session, token limits on backend usage, budgets debited mid-call |
| `load_test.go` | fifty concurrent sessions, each logged once |
| `real_test.go` | the paid smoke set |
| `fakeopenai_test.go`, `client_test.go`, `gateway_test.go`, `speech_test.go` | the fake upstream, the test client, the gateway API helpers, speech synthesis |
