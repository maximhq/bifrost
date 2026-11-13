# Realtime API E2E Tests

End-to-end tests for the Realtime API (`/v1/realtime` and the OpenAI drop-in routes) and incremental speech sessions (`/v1/audio/speech/stream`) through a running Bifrost gateway, against the real providers: OpenAI's realtime models, the same models on Azure OpenAI, Amazon Nova 2 Sonic on Bedrock, and ElevenLabs' Scribe and agents. Each test drives a session the way an app does, over HTTP and WebSocket, and checks what the gateway relays, refuses and logs.

The suite is a Go module of its own, run with `GOWORK=off`. It spends real tokens, a few cents a run: text turns on `gpt-realtime-mini`, a few seconds of audio, and short Nova Sonic sessions.

The Gemini, ElevenLabs and Soniox tests (`TestLive*`, `TestElevenlabs*`, `TestSoniox*`, `TestSpeech*`) also run against fake upstreams, with no provider keys and no cost; see [Fake mode](#fake-mode). Vertex AI has no fake, so its `TestLive*` cases run against the real service only. The OpenAI, Azure and Nova tests need the real providers.

## Running

The gateway reads provider credentials from the environment: `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `AZURE_API_KEY` / `AZURE_ENDPOINT`, and `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` / `AWS_REGION`, `ELEVENLABS_API_KEY` and `SONIOX_API_KEY`. The Azure resource needs a realtime deployment and a transcription deployment, named `gpt-realtime` and `gpt-4o-transcribe` by default. The region must offer Nova 2 Sonic, for example `us-east-1`, and the account needs access to `amazon.nova-2-sonic-v1:0`. Bedrock API keys cannot sign Nova's bidirectional stream, so the AWS credentials must be IAM ones.

```bash
# from the repo root: start a gateway on this folder's profile (the databases land in tests/realtime/data/)
make dev PORT=8187 APP_DIR=$(pwd)/tests/realtime

make test-realtime BIFROST_BASE_URL=http://localhost:8187
make test-realtime BIFROST_BASE_URL=http://localhost:8187 TESTCASE=TestTurn_ToolCallRoundTrip
make test-realtime BIFROST_BASE_URL=http://localhost:8187 PATTERN=Nova
```

Gemini and Vertex Live also need `GEMINI_API_KEY`, and `VERTEX_PROJECT_ID` with `VERTEX_CREDENTIALS`, a service account's JSON. The Vertex key serves `gemini-3.8-live` in the `us` multi-region; the `global` location cannot serve Live.

`config.json` enforces auth, configures `openai`, `bedrock`, `azure`, `elevenlabs`, `soniox`, `gemini`, `vertex` and a realtime-less `anthropic` from those variables, and sets a setup token so the suite can mint virtual keys through `/api`. Every test skips when the gateway does not answer `/health`, except under `make test-realtime`, which fails the run.

## Fake mode

`REALTIME_UPSTREAM=fake` runs the suite against fake providers that the test process hosts on one listener, `127.0.0.1:9421` (`REALTIME_FAKE_ADDR`). The gateway runs on `fake/config.json`, whose `gemini`, `elevenlabs` and `soniox` providers point their `base_url` at it, with literal fake credentials.

| Fake | Serves | Understands |
|---|---|---|
| Gemini Live | the Gemini Developer API Live endpoint | text and audio turns, tool calls, resumption, goAway, interruption |
| Soniox | real-time transcription (`/transcribe-websocket`) and text-to-speech (`/tts-websocket`) | endpoint detection, finalize, up to five streams, cancel, timestamps |
| ElevenLabs | Scribe (`/v1/speech-to-text/realtime`), the text-to-dialogue socket that serves incremental speech (`/v1/text-to-dialogue/stream-input`), agents (`/v1/convai/conversation`) and the REST endpoints of text to speech and the model list | VAD and manual commits, timestamps, flush, close, an agent that echoes, audio and JSON answers under ElevenLabs' response headers |

None of them recognises speech. The transcription fakes answer any speech with one fixed sentence (`fakeTranscript`), a word every 250 ms of it, and the text-to-speech fakes answer text with silent audio, a few characters at a time. They tell speech from silence by amplitude, so the suite's recording and the silence after it behave as they do against the real services. Each speaks the frames the provider code was written against, and the Gemini fake the wire the real service was probed to speak: JSON in binary frames, `response*` usage, goAway and the close that follows it.

Vertex AI has no fake. The gateway derives its Live host from the key's region and authenticates with a Google OAuth token, so a fake could only be reached through a product change, and the Vertex cases run in real mode only. A scenario that runs on both providers is one subtest per provider the mode serves (`eachNative`, `eachTranslated`): Gemini in fake mode, Vertex and Gemini in real mode.

```bash
# from the repo root: a gateway on the fake profile (its databases land in tests/realtime/fake/data/)
make dev PORT=8187 APP_DIR=$(pwd)/tests/realtime/fake

make test-realtime REALTIME_UPSTREAM=fake BIFROST_BASE_URL=http://localhost:8187 PATTERN='TestLive|TestSoniox|TestElevenlabs|TestSpeech'

# or the default selection, building the gateway from this checkout and starting it: fake | real | long
# (the default selection is the Gemini, Vertex, Soniox and ElevenLabs tests, REALTIME_PATTERN picks others, and long runs only TestLiveLong_*)
# (it first deletes the *.db files in the profile's data directory, which loses a database kept from a manual run)
./.github/workflows/scripts/test-realtime.sh fake
```

Only the tests that start with `requireUpstream` run in fake mode; every `requireGateway` test needs a real provider and skips. `requireFake` marks a scenario only a fake can script, such as a provider that drops the connection.

A test reaches a fake through the gateway alone, and finds its own upstream session by a marker it puts in a field the gateway passes through:

| Session | Marker and directive travel in |
|---|---|
| Gemini Live | the system instruction (`WaitSession`); directives in the prompt: `[fake:slow]`, `[fake:drop]`, `[fake:close 1011 reason]`, `[fake:goaway]`, `[fake:silent]` |
| Soniox transcription and speech | `client_reference_id`; directives `[fake:drop]`, `[fake:error 402]`, `[fake:silent]` |
| Scribe | `language_code`, as `fake-<marker>` or `fake-<marker>-drop` or `fake-<marker>-error` |
| ElevenLabs speech | the voice, in the same form |
| ElevenLabs agents | the agent id, which must start with `fake-agent`; any other id is closed with ElevenLabs' own code 3000 |

The same scenarios run in both modes, so a divergence between a fake and a provider is a failing real run: fake-only assertions are guarded by `fake != nil` and say what the fake adds.

The long sessions (`make test-realtime-long`, `REALTIME_LONG=1`) run only against the real providers. They hold a native session open across Google's ten-minute connection limit, which takes about eleven minutes.

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

Provider failures that cannot be produced on demand against a real provider, such as a dropped connection mid-turn, a rate limit or a malformed frame, are out of reach for the real providers; they belong in the handler unit tests. Fake mode scripts them for Gemini, ElevenLabs and Soniox.

## Files

| File | Covers |
|---|---|
| `turn_test.go` | healthy OpenAI turns: text replies, server-VAD turns started by speech, tool-call round trips, one row per response |
| `nova_test.go` | Nova 2 Sonic: text and voice turns, and its fixed session configuration |
| `elevenlabs_test.go` | ElevenLabs: Scribe transcription sessions translated to OpenAI events and logged, commits, the settings that become Scribe's query, the `bifrost.*` extension events sent only to a session that opts in, error frames and drops, agent sessions relayed, a Scribe model refused without the transcription intent, request limits, budgets, allowlists and concurrent sessions (both modes except where a fake is needed); and an ElevenLabs client pointed at the gateway (`TestElevenlabs_Native*`, fake mode): every REST endpoint on the `/elevenlabs` base URL, the key in `xi-api-key`, every option reaching ElevenLabs as sent and the client's credential never, ElevenLabs-shaped refusals, limits, budgets, allowlists and the rows and cost they log |
| `soniox_test.go` | Soniox: real-time transcription sessions translated to OpenAI events and logged, a client commit ending a segment, the words extension event sent only to a session that opts in, the settings that become the start frame, error frames and drops, an async model refused, request limits, budgets, allowlists and concurrent sessions |
| `speechsession_test.go` | incremental speech sessions on ElevenLabs and Soniox: flushed segments spoken in order and billed by characters sent, the text and settings the provider receives, alignment, a cancel acknowledged while the session keeps speaking, ElevenLabs redialling after one, provider errors and drops, segments kept in order when the provider speaks them together, refusals before anything opens, request limits, budgets, allowlists and concurrent sessions |
| `azure_test.go` | Azure OpenAI: text and voice turns, transcription sessions, deployment aliases, client secrets, an unknown deployment |
| `failure_test.go` | the client leaving mid-turn, a provider error event, a model the provider refuses |
| `protocol_test.go` | client events refused or rewritten: a second concurrent response, malformed JSON, binary frames, model prefixes in `session.update`, transcription sessions |
| `admission_test.go` | credentials refused before the upgrade, bad targets refused in-band, model allowlists, browser subprotocol keys, the OpenAI drop-in routes, key aliases |
| `clientsecret_test.go` | ephemeral tokens: minting, the browser session they open, revocation through the virtual key |
| `governance_test.go` | request limits and budgets stopping the next turn |
| `livenative_test.go` | Gemini and Vertex Live clients on `/genai` (Vertex in real mode only): relayed turns and the credentials upstream, every auth method, model spellings, refusals, limits and allowlists, resumption, tool calls, spoken input, provider closes and goAway, barge-in, concurrent sessions |
| `livetranslated_test.go` | Gemini and Vertex Live through `/v1/realtime` (Vertex in real mode only): translated turns, `session.update` becoming the setup, tool calls, spoken input, limits and allowlists, concurrent sessions |
| `livelong_test.go` | a native session across Google's ten-minute connection limit (`REALTIME_LONG=1`, real providers) |
| `liveclient_test.go` | the Live client of the native route: auth methods, setup, turns, audio, replies |
| `fakelive_test.go` | the fake Gemini Live upstream of fake mode, and the sockets and helpers the other fakes share |
| `fakesoniox_test.go` | the fake Soniox upstream: transcription and text-to-speech |
| `fakeelevenlabs_test.go` | the fake ElevenLabs upstream: Scribe, text-to-dialogue, agents and REST |
| `fake/config.json` | the gateway profile of fake mode |
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
| `SPEECH_ELEVENLABS_VOICE` | `21m00Tcm4TlvDq8ikWAM` | the ElevenLabs voice speech sessions speak with |
| `REALTIME_AZURE_TRANSCRIBE_MODEL` | `gpt-4o-transcribe` | the Azure transcription deployment |
| `BIFROST_SETUP_TOKEN` | `bifrost-realtime-setup-token` | the `setup_token` in `config.json`, sent on `/api` calls while dashboard auth is off |
| `BIFROST_ADMIN_AUTH` | | the `Authorization` header for `/api` calls on a gateway with dashboard auth |
| `REALTIME_REQUIRE_GATEWAY` | | set by `make test-realtime`: fail instead of skipping when the gateway does not answer `/health` |
| `REALTIME_UPSTREAM` | `real` | `fake` runs the Gemini, ElevenLabs and Soniox tests against fake upstreams; see Fake mode |
| `REALTIME_FAKE_ADDR` | `127.0.0.1:9421` | where the fake listens; `fake/config.json` names this address, so change both |
| `REALTIME_LONG` | | set by `make test-realtime-long`: run the 11-minute sessions |
