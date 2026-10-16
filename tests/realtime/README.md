# Realtime API E2E Tests

End-to-end tests for the Realtime API (`/v1/realtime` and the OpenAI drop-in routes) through a running Bifrost gateway. The gateway's `openai` provider points at a fake OpenAI realtime server the suite runs, so a test controls both ends of a session: what the app sends, what the provider answers, and when either side fails. That makes cases that are hard to produce against the real API easy to write: a provider that drops mid-turn, a garbage frame, a rate limit, a revoked key behind an ephemeral token.

The suite is a Go module of its own, run with `GOWORK=off`, and talks to the gateway over HTTP and WebSocket like a customer's app would. It is free and takes a few seconds.

## Running

```bash
# from the repo root: start a gateway on this folder's profile (the databases land in tests/realtime/data/)
make dev PORT=8187 APP_DIR=$(pwd)/tests/realtime

make test-realtime BIFROST_BASE_URL=http://localhost:8187
make test-realtime BIFROST_BASE_URL=http://localhost:8187 TESTCASE=TestTurn_ToolCallRoundTrip
make test-realtime BIFROST_BASE_URL=http://localhost:8187 PATTERN=Failure
```

`config.json` enforces auth, points `openai` (and a realtime-less `anthropic`) at the fake on `127.0.0.1:9412`, and sets a setup token so the suite can mint virtual keys and pricing overrides through `/api`. Every test skips when the gateway does not answer `/health`.

## How a test reads

Every test is the conversation, top to bottom. `s.client` is the app's socket on the gateway, `s.upstream` is the provider's end of the session the gateway opened, and each line is one hop across the gateway:

```go
func TestTurn_TextReplyIsRelayedAndLogged(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	s := openSession(t)

	s.client.send(userMessage("What is the capital of France?"), responseCreate())
	s.upstream.expect("conversation.item.create", "response.create")

	reply := textReply("resp_1", "Paris.", tokens{input: 12, output: 3})
	s.upstream.send(reply...)
	s.client.expectRelayed(reply...)

	row := s.turnRow()
	assert.Equal(t, "success", row.Status)
	assert.InDelta(t, costOf(tokens{input: 12, output: 3}), row.Cost, 1e-12)
}
```

The rules that keep it that way:

- **The fake does nothing on its own.** It greets a new connection with `session.created`, as OpenAI does, and otherwise only sends what a test tells it to. If a frame shows up on the wire, a line in the test put it there.
- **Frames are asserted in order, with nothing skipped.** `expect` fails on any frame it was not told about, so an extra or missing event is a failure, not noise. To show the gateway did *not* forward something, send a harmless marker (`inputAudioClear()`) after it and expect the marker next.
- **Events come from builders, never inline JSON.** `events_test.go` has one function per event type, named after it, plus whole responses (`textReply`, `toolCallReply`). Raw strings appear only when the point is that they are malformed.
- **Rows are read through `turnRow`.** Name a field in the struct when a test needs it; reach into `row.raw` only for one-off checks.
- **Tests are independent and parallel.** Each session runs on a model name of its own (`newModel`), which the gateway passes upstream when it dials, so the fake can hand every test its own session and the logs can be filtered to it. There is no shared mutable state to toggle: a refusal is per model (`upstream.refuse`), a limit is per virtual key (`createVirtualKey`).
- **One behaviour per test, named `Test<Area>_<WhatHappens>`.** A comment above the test says why the behaviour matters when the name cannot.

## Files

| File | Covers |
|---|---|
| `turn_test.go` | healthy turns: text replies, server-VAD turns started upstream, tool-call round trips, one row per response |
| `failure_test.go` | the provider dropping, hanging up, erroring, rate limiting, refusing the dial or sending garbage; the client leaving mid-turn |
| `protocol_test.go` | client events refused or rewritten: a second concurrent response, malformed JSON, binary frames, model prefixes in `session.update`, transcription sessions |
| `admission_test.go` | credentials refused before the upgrade, bad targets refused in-band, model allowlists, browser subprotocol keys, the OpenAI drop-in routes, key aliases |
| `clientsecret_test.go` | ephemeral tokens: minting, the browser session they open, revocation through the virtual key |
| `governance_test.go` | request limits and budgets stopping the next turn |
| `main_test.go` | setup: the fake, the suite's virtual key and its pricing override |
| `upstream_test.go` | the fake OpenAI realtime server |
| `client_test.go` | the app's socket, `dial` options and `openSession` |
| `events_test.go` | event builders |
| `gateway_test.go` | the management API: virtual keys, pricing, budgets and turn rows |

New scenarios go in the file for their area; a helper goes next to the code it drives (`upstream_test.go`, `client_test.go`, `gateway_test.go`), not in the test file that first needs it.

## Environment

| Variable | Default | Meaning |
|---|---|---|
| `BIFROST_BASE_URL` | `http://localhost:8080` | the gateway |
| `REALTIME_FAKE_ADDR` | `127.0.0.1:9412` | where the fake listens; must match `config.json` |
| `BIFROST_SETUP_TOKEN` | `bifrost-realtime-setup-token` | the `setup_token` in `config.json`, sent on `/api` calls while dashboard auth is off |
| `BIFROST_ADMIN_AUTH` | | the `Authorization` header for `/api` calls on a gateway with dashboard auth |

## Pricing

The suite installs one pricing override, `rt-e2e models`, that prices every `rt-e2e-*` model for realtime at $1 per million input tokens and $2 per million output tokens, so cost assertions are exact (`costOf`). The fake reports all usage as text tokens. The override is removed when the run ends.
