# Realtime API E2E Tests

End-to-end tests for the Realtime API (`/v1/realtime` and the OpenAI drop-in routes) through a running Bifrost gateway, against the real providers: OpenAI's realtime models, the same models on Azure OpenAI, and Amazon Nova 2 Sonic on Bedrock. Each test drives a session the way an app does, over HTTP and WebSocket, and checks what the gateway relays, refuses and logs.

The suite is a Go module of its own, run with `GOWORK=off`. It spends real tokens, a few cents a run: text turns on `gpt-realtime-mini`, a few seconds of audio, and short Nova Sonic sessions.

## Running

The gateway reads provider credentials from the environment: `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `AZURE_API_KEY` / `AZURE_ENDPOINT`, and `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` / `AWS_REGION`. The Azure resource needs a realtime deployment and a transcription deployment, named `gpt-realtime` and `gpt-4o-transcribe` by default. The region must offer Nova 2 Sonic, for example `us-east-1`, and the account needs access to `amazon.nova-2-sonic-v1:0`. Bedrock API keys cannot sign Nova's bidirectional stream, so the AWS credentials must be IAM ones.

```bash
# from the repo root: start a gateway on this folder's profile (the databases land in tests/realtime/data/)
make dev PORT=8187 APP_DIR=$(pwd)/tests/realtime

make test-realtime BIFROST_BASE_URL=http://localhost:8187
make test-realtime BIFROST_BASE_URL=http://localhost:8187 TESTCASE=TestTurn_ToolCallRoundTrip
make test-realtime BIFROST_BASE_URL=http://localhost:8187 PATTERN=Nova
```

`config.json` enforces auth, configures `openai`, `bedrock`, `azure` and a realtime-less `anthropic` from those variables, and sets a setup token so the suite can mint virtual keys through `/api`. Every test skips when the gateway does not answer `/health`, except under `make test-realtime`, which fails the run.

## How a test reads

Every test is the conversation, top to bottom, from the app's side:

```go
func TestTurn_TextReplyIsRelayedAndLogged(t *testing.T) {
	requireGateway(t)
	t.Parallel()
	s := openSession(t)
	s.configure(textSession())

	done := s.ask("What is the capital of France?")

	row := s.turnRow()
	assert.Equal(t, "success", row.Status)
	assert.Equal(t, replyText(done), row.outputText())
	assert.Equal(t, usageOf(done).input, row.TokenUsage.Input)
}
```

The rules that keep it that way:

- **Assert what the gateway owns exactly, and what the model says loosely.** Frames the gateway sends itself (its errors, its refusals) are asserted in order with `expect`. Provider streams are read with `until`, which skips the deltas and fails on any error event. Model output is checked for a keyword (`paris`, `weather`), never word for word.
- **Rows must match what the client was told.** A turn row's output is the response's text or transcript, and its tokens are the usage in that `response.done`.
- **Keep it cheap and deterministic.** OpenAI sessions answer in text (`textSession`) unless the test is about audio, and the instructions ask for five words at most. A tool call is forced with `tool_choice: required`.
- **Tests are independent and parallel.** Each session runs on a virtual key minted for it (`createVirtualKey`, deleted when the test ends), which is also how a test finds its rows (`turnRows`). Limits and budgets are per key.
- **One behaviour per test, named `Test<Area>_<WhatHappens>`.** A comment above the test says why the behaviour matters when the name cannot.

Provider failures that cannot be produced on demand against a real provider, such as a dropped connection mid-turn, a rate limit or a malformed frame, are out of this suite's reach; they belong in the handler unit tests.

## Files

| File | Covers |
|---|---|
| `turn_test.go` | healthy OpenAI turns: text replies, server-VAD turns started by speech, tool-call round trips, one row per response |
| `nova_test.go` | Nova 2 Sonic: text and voice turns, and its fixed session configuration |
| `azure_test.go` | Azure OpenAI: text and voice turns, transcription sessions, deployment aliases, client secrets, an unknown deployment |
| `failure_test.go` | the client leaving mid-turn, a provider error event, a model the provider refuses |
| `protocol_test.go` | client events refused or rewritten: a second concurrent response, malformed JSON, binary frames, model prefixes in `session.update`, transcription sessions |
| `admission_test.go` | credentials refused before the upgrade, bad targets refused in-band, model allowlists, browser subprotocol keys, the OpenAI drop-in routes, key aliases |
| `clientsecret_test.go` | ephemeral tokens: minting, the browser session they open, revocation through the virtual key |
| `governance_test.go` | request limits and budgets stopping the next turn |
| `main_test.go` | setup and the models under test |
| `client_test.go` | the app's socket, `dial` options, audio streaming and `openSession` |
| `events_test.go` | client event builders and provider event readers |
| `gateway_test.go` | the management API: virtual keys, budgets and turn rows |
| `testdata/ask-weather.wav` | "What's the weather like in Paris right now?", 24 kHz 16-bit mono PCM |

New scenarios go in the file for their area; a helper goes next to the code it drives (`client_test.go`, `gateway_test.go`), not in the test file that first needs it.

## Environment

| Variable | Default | Meaning |
|---|---|---|
| `BIFROST_BASE_URL` | `http://localhost:8080` | the gateway |
| `REALTIME_OPENAI_MODEL` | `gpt-realtime-mini` | the OpenAI realtime model; the `rt-alias` alias in `config.json` points at `gpt-realtime-mini` |
| `REALTIME_TRANSCRIBE_MODEL` | `gpt-4o-mini-transcribe` | the transcription model for audio input |
| `REALTIME_NOVA_MODEL` | `amazon.nova-2-sonic-v1:0` | the Bedrock speech model |
| `REALTIME_AZURE_MODEL` | `gpt-realtime` | the Azure realtime deployment; the `rt-azure-alias` alias in `config.json` points at `gpt-realtime` |
| `REALTIME_AZURE_TRANSCRIBE_MODEL` | `gpt-4o-transcribe` | the Azure transcription deployment |
| `BIFROST_SETUP_TOKEN` | `bifrost-realtime-setup-token` | the `setup_token` in `config.json`, sent on `/api` calls while dashboard auth is off |
| `BIFROST_ADMIN_AUTH` | | the `Authorization` header for `/api` calls on a gateway with dashboard auth |
| `REALTIME_REQUIRE_GATEWAY` | | set by `make test-realtime`: fail instead of skipping when the gateway does not answer `/health` |
