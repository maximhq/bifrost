# E2E CLI tests (Claude Code, Codex & OpenCode through Bifrost)

End-to-end tests that drive coding-assistant CLIs in **non-interactive mode** against a running Bifrost instance and assert that single-turn and multi-turn conversation features work across providers.

This is the CLI analog of `tests/e2e/api/` (which uses Newman/Postman for HTTP). Where the API harness exercises Bifrost's HTTP surface directly, this harness exercises Bifrost *through* the CLIs that customers actually use — but via their scripted interfaces (`claude -p`, `codex exec`), not their TUIs.

## Why non-interactive

The TUIs (`claude` interactive, `codex` interactive, `opencode` interactive) are built for real terminal emulators and rely on capability queries our test harness can't answer. We exercise the *same* model pipeline — same chat, tools, web search, MCP, streaming, reasoning — through `claude -p`, `codex exec`, and `opencode run`, which are first-class scripted interfaces:

- `claude -p` → "Query via SDK, then exit" — [Claude Code CLI reference](https://code.claude.com/docs/en/cli-reference)
- `claude -p --input-format stream-json --output-format stream-json` → bidirectional JSON-Lines for multi-turn conversations in one process
- `codex exec --json` → emits structured JSONL events
- `codex resume --last` → continues the most recent session for chained-process multi-turn
- `opencode run --format json` → emits structured JSON events
- `opencode run --continue` → continues the most recent isolated session for chained-process multi-turn

## Layout

| Path | Description |
|------|-------------|
| `clis_test.go` | `TestCLIs` matrix entry (`cli/provider/model/scenario`) + `TestMain` SIGINT handler. |
| `matrix_test.go` | CLI launch config (`SingleTurnArgs`, `MultiTurnDriver`) + per-provider catalog of top models with capability flags (`Reasoning`, `WebSearch`). |
| `runner_test.go` | `runSingleTurn` (Pattern A) + `claudeStreamJSON` driver (Pattern C) + `codexResume`/`opencodeResume` drivers (Pattern B). |
| `scenarios_test.go` | Each scenario as `Turns []Turn`. Single-turn scenarios have one turn, conversation scenarios have N. |
| `bifrost_test.go` | Health check + configured-provider discovery via `/api/providers`. |
| `errordetect_test.go` | Pattern matcher for transport / upstream error markers in transcripts. |
| `fixtures/` | Sample files used by `file-read`. |
| `reports/` | Per-cell `.json` summary + `.transcript.log` (combined turn outputs). |

All Go files are `_test.go` so they only build under `go test`.

## Prerequisites

1. Go 1.23+.
2. Bifrost running locally (default `http://localhost:8080`) with at least one provider configured. The runner queries `/api/providers` and skips any provider that isn't configured.
3. The CLIs you want to test installed and on `PATH`:
   - `claude` — `npm i -g @anthropic-ai/claude-code`
   - `codex`  — `npm i -g @openai/codex`
   - `opencode` — `npm i -g opencode-ai`

## Run

The canonical entry point is the root `Makefile` recipe:

```bash
# From the repo root
make run-cli-harness-test                                                       # full matrix
make run-cli-harness-test CLI=claude                                            # one CLI
make run-cli-harness-test CLI=opencode PROVIDER=azure                           # one cli×provider pair
make run-cli-harness-test CLI=claude PROVIDER=anthropic                         # one cli×provider pair
make run-cli-harness-test CLI=claude PROVIDER=anthropic MODEL=opus-4-7          # one model (substring match)
make run-cli-harness-test CLI=claude PROVIDER=anthropic MODEL=opus-4-7 SCENARIO=simple-chat
make run-cli-harness-test TESTCASE='TestCLIs/opencode/bedrock/[^/]*nova[^/]*/simple-chat'
make run-cli-harness-test PROVIDER=bedrock MODEL=nova PARALLEL=10 QUIET=1
make run-cli-harness-test SCENARIO=conversation-memory                          # one scenario across the matrix
make run-cli-harness-test BASE_URL=http://localhost:9090                        # non-default Bifrost
make run-cli-harness-test QUIET=1                                               # CI mode
```

Or directly via `go test`:

```bash
cd tests/e2e/clis
# t.Run path is TestCLIs/<cli>/<provider>/<model>/<scenario>
GOWORK=off go test -v -run 'TestCLIs/claude/anthropic/claude-opus-4-7/simple-chat' ./...
GOWORK=off go test -v -run 'TestCLIs/claude/anthropic/[^/]+/conversation-memory' ./...
```

Environment variables:

| Var | Default | Notes |
|-----|---------|-------|
| `BIFROST_BASE_URL` | `http://localhost:8080` | Bifrost base URL. |
| `BIFROST_API_KEY` | `dummy` | Sent as the CLI's API key env. |
| `MODEL` | unset | Substring match on model ID (e.g. `MODEL=opus-4-7`, `MODEL=gpt-4o`). |
| `TESTCASE` | unset | Full Go `-run` expression for targeting one exact subtest path. |
| `PARALLEL` | `4` | Max parallel scenario cells. Use `PARALLEL=10` for a wider live sweep. |
| `BIFROST_E2E_CLIS=skip` | unset | Skips the entire test (useful in CI without setup). |
| `BIFROST_E2E_CLIS_QUIET=1` | unset | Suppresses live mirror; reports still written. |

**Heads-up on runtime / cost.** Default invocation runs every CLI × provider × model × scenario cell that's not gated out. Native providers carry five top models, while Azure, Bedrock, and Vertex also include the top five Anthropic-routed Claude models. At 30 s–2 min per cell that's hours and meaningful provider quota. Always filter with `CLI=`, `PROVIDER=`, `MODEL=`, or `SCENARIO=` during dev.

## Live mirror

By default every cell streams its CLI subprocess's stdout (and the stream-JSON events for multi-turn cells) to your terminal as it runs, framed by a header per cell:

```
>>> claude × anthropic × conversation-memory  (model=anthropic/claude-sonnet-4-5, turns=3)
{"type":"system","subtype":"init",...}
{"type":"assistant","message":{...content...}}
{"type":"result","is_error":false,...}
... next user turn ...
<<< claude × anthropic × conversation-memory  (8.214s)
--- PASS: TestCLIs/claude/anthropic/conversation-memory (8.22s)
```

`QUIET=1` (or `BIFROST_E2E_CLIS_QUIET=1`) suppresses the mirror.

## Scenarios

| ID | Turns | What it tests | Cell gate |
|----|-------|---------------|-----------|
| `simple-chat` | 1 | End-to-end smoke; CLI returns a response containing a sentinel token. | all models |
| `tool-call` | 1 | CLI invokes its built-in shell tool (`--dangerously-skip-permissions` auto-allows). | all models |
| `file-read` | 1 | CLI reads `fixtures/sample.txt` and quotes a fact from it. | all models |
| `web-search` | 1 | CLI uses web search to answer a current-events question. | models with `WebSearch: true` |
| `reasoning` | 1 | Multi-step word problem; only run on thinking-capable models. | models with `ExtendedThinking` OR `AdaptiveThinking` |
| `conversation-memory` | 3 | Tells the model a secret word, then asks for it back, then asks it to be used. | all supported CLI/provider/model cells except Bedrock Nova |
| `conversation-refinement` | 3 | Asks for a haiku, then a desert version, then a combined poem. | all supported CLI/provider/model cells except Bedrock Nova |
| `conversation-role-stability` | 3 | Sets a "always end with PIRATE" rule, then asks unrelated questions. | all supported CLI/provider/model cells except Bedrock Nova |

Scenarios gate per-cell via `Supports(cliID, providerID, model)`. A scenario that requires reasoning skips automatically against models with `Reasoning: false`, instead of running and failing.

Add a scenario by writing a factory in `scenarios_test.go` and including it in `allScenarios()`. Each scenario is just `{ID, ModelKind, Supports, ErrorIgnore, Turns}`.

## How "no error" is decided

A cell is `pass` only if **all** of these hold:

1. Every turn's required substrings (`AssertText`) appear in that turn's response.
2. If `AssertTextAny` is set on a turn, at least one of its substrings appears.
3. The CLI subprocess exited cleanly (or, for multi-turn, the stream-JSON `result` event arrived).
4. The combined transcript contains none of the patterns in `errordetect_test.go` after subtracting `ErrorIgnore` substrings.

## Reports

After each run, `reports/` contains one pair per cell:

```
claude__openai__gpt-4o__simple-chat.json                # status, error, durationMs, model
claude__openai__gpt-4o__simple-chat.transcript.log      # combined stdout from all turns
```

Filename stem is `<cli>__<provider>__<model>__<scenario>`. Slashes in model IDs are replaced with `_`.

## Multi-turn implementation notes

- **Claude (Pattern C)**: One long-running `claude -p --input-format stream-json --output-format stream-json --verbose` process per cell. The driver writes one JSON-Lines user message per turn to stdin; for each turn it accumulates `assistant` event text content until a `result` event closes the turn. `--verbose` is required by the SDK when output-format is stream-json.
- **Codex (Pattern B)**: One `codex exec` for turn 1, then `codex resume --last` for each subsequent turn, with `CODEX_HOME` redirected to a temp dir so `--last` always means "the last turn this test ran" (not whatever the user did in their real codex install). Each turn is its own process; the conversation persists via codex's session storage in the temp `CODEX_HOME`.
- **OpenCode (Pattern B)**: One `opencode run` for turn 1, then `opencode run --continue` for each subsequent turn, with XDG config/data/cache directories redirected to a per-cell temp dir and `OPENCODE_CONFIG` pointed at a generated Bifrost provider config.

## Known limitations

- We don't assert on streaming token-by-token delivery any more — the `--include-partial-messages` flag would let us, but it makes assertions noisier; can be added per-scenario if needed.
- Image input scenarios aren't included v1 — both CLIs support image attachments via flags (`--image` for codex), it's just not wired into the `Turn` struct yet.
- Codex stream-JSON bidirectional input isn't used because at time of writing it's less mature than chained `exec` + `resume`. If/when it lands, swap `codexResumeDriver` for a stream-json driver mirroring `claudeStreamJSON`.
