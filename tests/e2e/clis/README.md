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
| `matrix_test.go` | CLI launch config (`SingleTurnArgs`, `MultiTurnDriver`, `AttachImageArgs`, `EffortArgs`) + per-provider catalog of top models with capability flags (`ExtendedThinking`, `AdaptiveThinking`, `WebSearch`). |
| `runner_test.go` | `runSingleTurn` (Pattern A) + `claudeStreamJSON` driver (Pattern C) + `codexResume`/`opencodeResume` drivers (Pattern B). |
| `scenarios_test.go` | Each scenario as `Turns []Turn`. Single-turn scenarios have one turn, conversation scenarios have N. |
| `bifrost_test.go` | Health check + configured-provider discovery via `/api/providers`. |
| `errordetect_test.go` | Pattern matcher for transport / upstream error markers in transcripts. |
| `fixtures/` | Sample files used by `file-read` (`sample.txt`), `image-qa` (`sample.png`), and `pdf-qa` (`sample.pdf`). |
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
make run-cli-harness-test CLI=claude PROVIDER=anthropic MODEL=opus-5            # one model (substring match)
make run-cli-harness-test CLI=claude PROVIDER=anthropic MODEL=opus-5 SCENARIO=simple-chat
make run-cli-harness-test CLI=codex PROVIDER=openai SCENARIO=image-qa           # image Q&A, run at low+high effort
make run-cli-harness-test CLI=claude PROVIDER=anthropic SCENARIO=pdf-qa
make run-cli-harness-test TESTCASE='TestCLIs/opencode/bedrock/[^/]*nova[^/]*/simple-chat'
make run-cli-harness-test PROVIDER=bedrock MODEL=nova PARALLEL=10 QUIET=1
make run-cli-harness-test SCENARIO=conversation-memory                          # one scenario across the matrix
make run-cli-harness-test BASE_URL=http://localhost:9090                        # non-default Bifrost
make run-cli-harness-test QUIET=1                                               # CI mode
```

Or directly via `go test`:

```bash
cd tests/e2e/clis
# t.Run path is TestCLIs/<cli>/<provider>/<model>/<scenario>, plus a
# trailing /<effort> segment for scenarios that declare Efforts (currently
# image-qa and pdf-qa; e.g. TestCLIs/codex/openai/gpt-5.6-sol/image-qa/low).
GOWORK=off go test -v -run 'TestCLIs/claude/anthropic/claude-opus-5/simple-chat' ./...
GOWORK=off go test -v -run 'TestCLIs/claude/anthropic/[^/]+/conversation-memory' ./...
GOWORK=off go test -v -run 'TestCLIs/codex/openai/[^/]+/image-qa/high' ./...
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
| `BIFROST_E2E_CLIS_MIRROR=1` | unset | Streams each CLI subprocess's raw stdout (`MIRROR=1`). Off by default. |
| `BIFROST_E2E_CLIS_QUIET=1` | unset | Forces the live mirror off; wins over `MIRROR`. Reports still written. |
| `BIFROST_E2E_CLIS_PROGRESS` | unset | `1`/`0` forces the progress table on/off. Unset means on whenever the live mirror is off. |
| `BIFROST_E2E_CLIS_PROGRESS_INTERVAL` | `10s` | Progress tick interval (any `time.ParseDuration` value). `0`/`off` disables ticking; a final table still prints. |

### How each CLI is pointed at Bifrost

`runCell` exports `<CLI>.BaseURLEnv` and `<CLI>.APIKeyEnv` for every cell, but only Claude is actually routed by env alone. The rest is `PreLaunch`:

| CLI | Base URL | Auth |
|-----|----------|------|
| `claude` | `ANTHROPIC_BASE_URL` env | `ANTHROPIC_API_KEY` env |
| `opencode-responses` | generated `OPENCODE_CONFIG` JSON with `<base>/openai/v1` (`opencodeResponsesPreLaunch`) | `apiKey` in that same JSON |
| `codex` | generated `CODEX_HOME/config.toml` (`codexPreLaunch`) | API key, via the generated provider's `env_key = "OPENAI_API_KEY"` |
| `opencode` | generated `OPENCODE_CONFIG` JSON (`opencodePreLaunch`) | `apiKey` in that same JSON |

**Do not try to route codex with `OPENAI_BASE_URL`** — as of `codex-cli 0.146.1` the binary does not read that variable at all (the string is absent from it). Nor does exporting `OPENAI_API_KEY` alone authenticate: codex's built-in `openai` provider sets `requires_openai_auth = true`, so it opens `wss://api.openai.com/v1/responses` expecting ChatGPT OAuth tokens from `auth.json` and never falls back to the key. Both failures land as one confusing symptom, `401 Unauthorized: Missing bearer or basic authentication in header` against `api.openai.com` — i.e. the request never reached Bifrost.

### Two OpenCode entries, one binary

`opencode` and `opencode-responses` run the same binary against different **wire
formats**, because Bifrost converts requests separately per wire format and a
chat/completions client cannot exercise the Responses conversion at all:

| CLI | `npm` package | Bifrost route |
|-----|-----|-----|
| `opencode` | `@ai-sdk/openai-compatible` | `/openai/v1/chat/completions` |
| `opencode-responses` | `@ai-sdk/openai` | `/openai/v1/responses` |

The substitution is documented at https://opencode.ai/docs/providers/. The `/v1`
suffix on the Responses variant's `baseURL` matters for the same reason it does
for codex: the SDK appends `/responses` verbatim, so without it requests land on
Bifrost's bare `/openai/responses` alias instead of the canonical route.

`opencode-responses` is scoped to Anthropic-on-Bedrock only
(`supportsCLIProviderModel`). That is deliberately narrow - it exists to cover a
path nothing else reached. A reasoning-replay conversion defect was reported on
Bedrock's Responses path against a build this harness passed, precisely because
every OpenCode cell was on the chat/completions side and codex - the only other
Responses client - is gated to openai. Pair it with `reasoning-replay`:

```bash
make run-cli-harness-test CLI=opencode-responses SCENARIO=reasoning-replay
```

In CI this is a fourth suite alongside claude/codex/opencode, pinned to
`global.anthropic.claude-sonnet-5` and running `simple-chat` + `reasoning-replay`.
`simple-chat` is the control - it proves the Responses→Bedrock path works at
all, so a `reasoning-replay` failure is attributable to reasoning replay rather
than to the path being broken outright. `reasoning-replay` also runs on
`claude`/`anthropic`, as the control for the report's claim that the Anthropic
Messages path is unaffected.

**It needs Bedrock credentials in the job.** The harness gateway seeds from
`tests/integrations/python/config.json`, whose `bedrock_key_config` reads
`AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` / `AWS_REGION`. Without them the
provider is unconfigured and `TestCLIs` **skips every bedrock cell silently,
with a green suite** - which is exactly how the defect shipped unnoticed. The
`cells produced: 0` guard in `test-cli-harness.sh` is what catches that: an
empty reports subdirectory fails the suite rather than passing it.

**Permission bypass is per-CLI, not universal.** `--dangerously-skip-permissions`
is Claude Code's flag. OpenCode has no equivalent - it appears nowhere in
`opencode run --help` or the [CLI reference](https://opencode.ai/docs/cli/), and
because OpenCode's arg parser is non-strict it was silently discarded rather
than rejected when the harness used to pass it. `opencodePreLaunch` sets
`"permission": "allow"` in the generated config instead (the bare-string form of
`PermissionConfig`, per [the config schema](https://opencode.ai/config.json)),
which grants every tool without a prompt.

`codexPreLaunch` sidesteps both by declaring a `bifrost` provider in a per-cell `CODEX_HOME/config.toml`, with `requires_openai_auth = false` (selects API-key auth), `env_key = "OPENAI_API_KEY"`, `supports_websockets = false` (plain HTTP POST), and `base_url` set to `BIFROST_BASE_URL + "/openai/v1"`. The `/v1` matters: codex appends `/responses` to `base_url` verbatim, so this targets Bifrost's canonical `/openai/v1/responses` rather than its bare `/openai/responses` alias.

**Heads-up on runtime / cost.** Default invocation runs every CLI × provider × model × scenario cell that's not gated out. Native providers carry five top models, while Azure, Bedrock, and Vertex also include the top five Anthropic-routed Claude models. At 30 s–2 min per cell that's hours and meaningful provider quota. Always filter with `CLI=`, `PROVIDER=`, `MODEL=`, or `SCENARIO=` during dev.

## Live mirror (`MIRROR=1`, off by default)

`MIRROR=1` streams each cell's CLI subprocess stdout - the raw stream-JSON
events - to your terminal as it runs, framed by a header per cell:

```
>>> claude × anthropic × conversation-memory  (model=anthropic/claude-sonnet-5, turns=3)
{"type":"system","subtype":"init",...}
{"type":"assistant","message":{...content...}}
{"type":"result","is_error":false,...}
... next user turn ...
<<< claude × anthropic × conversation-memory  (8.214s)
```

Use it to debug one specific cell's wire traffic, and pair it with a narrow
filter. It is off by default because it is thousands of lines per turn for a
JSON-event CLI (OpenCode, Codex), multiplied by however many cells run in
parallel - which buries the per-cell result lines and the progress table, i.e.
the output you actually read.

`VERBOSE=1` separately adds `go test -v` (the `=== RUN` / `=== PAUSE` / `=== CONT`
scaffolding and skip reasons). Failures print without it.

`QUIET=1` (or `BIFROST_E2E_CLIS_QUIET=1`) forces the mirror off and wins over
`MIRROR=1`; it is what CI passes.

## Per-cell results

This is the default output. Each cell prints one line when it starts and one
when it finishes:

```
RUNNING     simple-chat                  opencode  openai/gpt-5.5
PASS        simple-chat                  opencode  openai/gpt-5.5             12.34s
RUNNING     file-read                    opencode  openai/gpt-5.5
FAIL        file-read                    opencode  openai/gpt-5.5              45.1s  expected "FILEOK" in output, got tail:...
SOFT_PASS   image-qa@low                 claude    anthropic/claude-sonnet-5     30s  expected "red" in output
```

Status and scenario lead, since that is what you scan for; the cell's identity
and duration follow, and a truncated failure reason is appended when there is
one. `RUNNING` matters because cells are parallel and a single cell can take
minutes - without it a long run looks hung. Always exactly one line per event:
cells finish concurrently, so anything multi-line would interleave.

### Why the runner always passes `go test -v`

Without `-v`, `go test` buffers a package's entire stdout **and** stderr and
discards it when the package passes - which would swallow these lines and the
progress table both, leaving a multi-minute run with no output whatsoever.

So `-v` is always on, and the Makefile filters out the scaffolding it emits
(`=== RUN` / `=== PAUSE` / `=== CONT`, the `--- PASS/FAIL/SKIP` tree, the
package summary, and the harness's own two skip messages). Every diagnostic -
`t.Fatal` bodies, panics, build errors - passes through untouched. `VERBOSE=1`
bypasses the filter and shows raw `go test -v` output.

## HTML report

`reports/index.html` renders one expandable card per cell, styled to match the
provider harness's viewer (`tests/e2e/api/runners/harness-viewer.mjs`) so the
two read as one family. Anything that did not cleanly pass is expanded on load.

Each card carries the full **conversation** - every prompt sent and every
response received, as a plain `role: content` transcript:

```
user: Remember the secret word: pangolin. Reply with just the word REMEMBERED.
assistant: REMEMBERED

user: What was the secret word I just told you?
assistant: pangolin
```

That conversation is the *extracted* assistant text, not the wire stream: for
the JSON-event CLIs the raw output is thousands of events per turn, and no one
can read a conversation out of it. The raw stream stays one collapsed click away
in the same card, and in the sibling `.transcript.log`.

Unlike the provider viewer this report is static, with no JavaScript - it is
uploaded as a CI artifact and published to R2, so it has to work as a plain file
opened from disk.

Regenerate it from existing summaries without re-running anything:

```bash
make cli-harness-report
```

## Progress table

With the mirror off, the harness prints a tally every 10 seconds instead - one
row per CLI plus a total:

```
Bifrost CLI Harness   Elapsed 4m20s   ETA 14m27s
┌──────────┬───────┬───────────┬──────┬──────┬───────┐
│ Harness  │ Total │ Completed │ Pass │ Fail │ Other │
├──────────┼───────┼───────────┼──────┼──────┼───────┤
│ claude   │    42 │        12 │   11 │    1 │     0 │
│ codex    │    18 │         4 │    4 │    0 │     0 │
│ opencode │    18 │         2 │    1 │    0 │     1 │
├──────────┼───────┼───────────┼──────┼──────┼───────┤
│ TOTAL    │    78 │        18 │   16 │    1 │     1 │
└──────────┴───────┴───────────┴──────┴──────┴───────┘
```

Deliberately the same shape as the provider harness's monitor
(`tests/e2e/api/runners/harness-monitor.mjs`), so both harnesses read the same
way in a job log.

`Total` is the number of cells the `-run` filter actually selected, and it is
accurate within seconds of launch: Go runs every matched subtest body up to its
`t.Parallel()` call before resuming any of them, so all cells register during
that enumeration pass. `Other` collects the statuses that are neither a clean
pass nor a regression - `soft_pass`, `interrupted`, and skips - and the column
is elided entirely when none occurred. `ETA` extrapolates from the completion
rate so far, and is omitted until the first cell finishes.

**The table goes to stderr; everything else goes to stdout.** That split is what
lets CI redirect the verbose output to a log file and keep only the table on the
console (see `.github/workflows/scripts/test-cli-harness.sh`). Locally the two
are interleaved, which is fine on a terminal.

## CLI versions in CI

CI does not pin CLI versions. `resolve-cli-versions` queries npm on every
pipeline run and fans the three most recent stable releases of each CLI into
three independent `test-cli-harness` jobs - `latest`, `latest-1`, `latest-2` -
so a regression can be attributed to a specific upstream release rather than
just "the CLIs changed". `fail-fast` is off, so one bad upstream release cannot
cancel the legs that would have shown the others still passing.

Two consequences worth knowing:

- **`latest` may be a day old.** These are third-party clients being exercised
  against Bifrost, not dependencies being shipped, so no minimum-age floor
  applies. Prereleases are excluded - `npm i -g` does not install them, so the
  release gate should not validate against them.
- **codex floats too**, while `matrix_test.go` documents its `--image` and
  `model_reasoning_effort` assertions specifically against `rust-v0.145.0`. When
  a codex leg fails on those, that comment is the place to start; the assertion
  may simply need re-checking against the newer release.

### Retries

This suite is a required release gate, so each CLI's run retries its failed
cells up to twice (`CLI_RERUN_ATTEMPTS`, default 2) before the leg fails - a
flaky provider timeout should not sink a release, while a real regression still
fails after every attempt. Only cells whose summary records status `fail` are
retried; `soft_pass` and `interrupted` already do not fail the suite, so
re-running them would spend quota to change nothing.

Two details that matter if you touch this:

- **Retry filters alternate within one path level, never across.** `go test
  -run` splits its pattern on `/` and matches each piece against the
  corresponding subtest level, so `(a/b|c/d)` is torn apart at the slashes.
  Failed cells are therefore grouped by parent path and alternated on the final
  segment only - `.../claude-sonnet-5/(file-read|simple-chat)`, and
  `.../image-qa/(high|low)` for effort cells.
- **The reports are the source of truth for pass/fail, not `go test`'s exit
  code.** A retry filter that matched nothing exits 0 while the cell's summary
  still reads `fail`; trusting the exit code there would turn a drifted filter
  into a green suite.

A pass that needed retries emits a `::warning::`, and the pre-retry state is
archived under `tmp/cli-harness-attempts/<cli>/attempt-N/` - without it a suite
that goes green on retry leaves no record of what failed first, which is the
evidence for telling a flake from a degradation.

The version literals in `.github/workflows/scripts/test-cli-harness.sh` are now
only a local-run default and the fallback used if npm is unreachable (in which
case the matrix degrades to a single `pinned` leg and logs a warning, rather
than collapsing to an empty matrix - an empty one would skip the job, and the
downstream release gates accept `skipped`).

## Scenarios

| ID | Turns | What it tests | Cell gate |
|----|-------|---------------|-----------|
| `simple-chat` | 3 | End-to-end smoke; returns a sentinel token, then lowercases it, then restores it - the later turns are answerable only from context. | all models |
| `tool-call` | 3 | CLI invokes its built-in shell tool twice (print a token, then byte-count it), then combines both results without running anything further. | all models |
| `file-read` | 3 | CLI reads `fixtures/sample.txt`, answers a follow-up *without* re-reading, then reads it again. | all models |
| `web-search` | 3 | CLI uses web search to answer a current-events question, then cites its source and converts the result - turns 2-3 need no further searching. | models with `WebSearch: true` |
| `reasoning` | 3 | Multi-step word problem, then two further derivations from the same premises; only run on thinking-capable models. | models with `ExtendedThinking` OR `AdaptiveThinking` |
| `conversation-memory` | 3 | Tells the model a secret word, then asks for it back, then asks it to be used. | all supported CLI/provider/model cells except Bedrock Nova |
| `conversation-refinement` | 3 | Asks for a haiku, then a desert version, then a combined poem. | all supported CLI/provider/model cells except Bedrock Nova |
| `conversation-role-stability` | 3 | Sets a "always end with PIRATE" rule, then asks unrelated questions. | all supported CLI/provider/model cells except Bedrock Nova |
| `reasoning-replay` | 5 | Five chained derivations that each require real arithmetic and depend on the previous answer, so prior-turn **reasoning blocks accumulate and are replayed** on every request. Turn 5 recalls the whole chain. | thinking-capable models (`ExtendedThinking` OR `AdaptiveThinking`) |
| `subagent-delegation` | 2 | Asks the CLI to delegate a task to a subagent (Claude's `Agent`/`Task` tool, Codex's `spawn_agent` collab-tool) and relay its result back; asserts both the tool invocation and the round-tripped token. | `claude` + `codex` only, default model per provider |
| `image-qa` | 1 | Attaches `fixtures/sample.png` (real image, not a path reference) and asks for a rendered token, a written fact, and the color of a drawn shape — genuine vision/OCR, not text extraction. Runs at `low` and `high` effort. | `claude` + `codex` only |
| `pdf-qa` | 1 | CLI reads `fixtures/sample.pdf` via its own file tool and answers questions from its content. Runs at `low` and `high` effort. | `claude` only — Codex's only attachment mechanism (`-i/--image`) is confirmed image-only, no PDF support |

Scenarios gate per-cell via `Supports(cliID, providerID, model)`. A scenario that requires reasoning skips automatically against models with `Reasoning: false`, instead of running and failing.

A scenario can also set `Efforts []string` (e.g. `[]string{"low", "high"}`) to run once per reasoning-effort level as a nested `t.Run` subtest, gated on `model.AdaptiveThinking` and the CLI having an `EffortArgs` mechanism (currently `claude`'s `--effort` flag and `codex`'s `-c model_reasoning_effort=` config override; OpenCode has none). Only wired for single-turn scenarios.

Add a scenario by writing a factory in `scenarios_test.go` and including it in `allScenarios()`. Each scenario is just `{ID, ModelKind, Supports, ErrorIgnore, Turns, Efforts}`. A `Turn` can also set `AttachImage` (absolute path) to attach a real image via the CLI's `AttachImageArgs` mechanism where one exists.

## How "no error" is decided

A cell is `pass` only if **all** of these hold:

1. Every turn's required substrings (`AssertText`) appear in that turn's response.
2. If `AssertTextAny` is set on a turn, at least one of its substrings appears.
3. The CLI subprocess exited cleanly (or, for multi-turn, the stream-JSON `result` event arrived).
4. The combined transcript contains none of the patterns in `errordetect_test.go` after subtracting `ErrorIgnore` substrings.

Cells that don't pass are recorded as one of:

| Status | Meaning |
|--------|---------|
| `fail` | A real failure — assertion, transport error, or error marker. |
| `soft_pass` | Assertions failed but the transcript shows no error marker and non-empty model text — a coarse "the plumbing worked, the model just answered differently" signal. |
| `interrupted` | The cell's subprocess was killed by our own SIGINT handler (Ctrl-C), so its result is meaningless. These are **not** failures — before this status existed, an interrupted sweep left cells reading `claude exit: signal: killed`, indistinguishable from real regressions. |

## Reports

After each run, `reports/` contains one pair per cell:

```
claude__openai__gpt-4o__simple-chat.json                # status, error, durationMs, model
claude__openai__gpt-4o__simple-chat.transcript.log      # combined stdout from all turns
```

Filename stem is `<cli>__<provider>__<model>__<scenario>`. Slashes in model IDs are replaced with `_`.

## Multi-turn implementation notes

- **Claude (Pattern C)**: One long-running `claude -p --input-format stream-json --output-format stream-json --verbose` process per cell. The driver writes one JSON-Lines user message per turn to stdin; for each turn it accumulates `assistant` event text content until a `result` event closes the turn. `--verbose` is required by the SDK when output-format is stream-json. Every claude invocation (single- and multi-turn) gets `HOME` redirected to a fresh per-cell temp dir via `claudePreLaunch` — Claude Code has no `CODEX_HOME`/`XDG_CONFIG_HOME` equivalent, so this is the only way to keep it from reading the real developer's `~/.claude` (CLAUDE.md, settings, auto-memory) instead of a clean slate.
- **Codex (Pattern B)**: One `codex exec` for turn 1, then `codex resume --last` for each subsequent turn, with `CODEX_HOME` redirected to a temp dir so `--last` always means "the last turn this test ran" (not whatever the user did in their real codex install). Each turn is its own process; the conversation persists via codex's session storage in the temp `CODEX_HOME`. That directory is created by `codexPreLaunch` (single- and multi-turn alike) and carries the generated Bifrost provider config; `codexResume.Start` reuses the `CODEX_HOME` it finds in the cell env rather than minting its own, since a second one would shadow the config — `os/exec` resolves a duplicated env key to the last occurrence — and send codex straight back to `api.openai.com`.
- **OpenCode (Pattern B)**: One `opencode run` for turn 1, then `opencode run --continue` for each subsequent turn, with XDG config/data/cache directories redirected to a per-cell temp dir and `OPENCODE_CONFIG` pointed at a generated Bifrost provider config.

## Known limitations

- We don't assert on streaming token-by-token delivery any more — the `--include-partial-messages` flag would let us, but it makes assertions noisier; can be added per-scenario if needed.
- Image attachment is CLI-specific, not a uniform flag: Codex has a real `-i/--image` flag (confirmed via `codex exec --help`); Claude Code has **no** image-attach flag at all (verified against the full CLI reference) — it attaches via the `Turn.Send` prompt referencing an absolute path, which its `Read` tool opens as visual content. `Turn.AttachImage` + `CLI.AttachImageArgs` encode this per-CLI difference; see `image-qa`.
- PDF attachment has no CLI flag on either CLI. Codex's wire protocol (`codex-rs/protocol/src/user_input.rs`, pinned to the installed `rust-v0.145.0`) has no document/PDF variant at all, so `pdf-qa` is Claude-only, via the same path-in-prompt + `Read` tool mechanism as `file-read`.
- Codex stream-JSON bidirectional input isn't used because at time of writing it's less mature than chained `exec` + `resume`. If/when it lands, swap `codexResumeDriver` for a stream-json driver mirroring `claudeStreamJSON`.
- `subagent-delegation` covers Claude's `Agent`/`Task` tool and Codex's `spawn_agent` collab-tool (confirmed via `codex-rs/exec/src/exec_events.rs`'s `collab_tool_call` item shape). OpenCode also ships a real subagent primitive (a lowercase `task` tool backing General/Explore/Scout built-in subagents), but whether its child-session events multiplex into `opencode run --format json`'s stream is unconfirmed — deferred pending a follow-up spike rather than shipping an assertion on a guessed event shape.
- `codexResumeDriver.Send` only passes `--json` on turn 1 (`codex exec --json`); the `resume --last` branch for turn 2+ has no `--json` flag, so only turn 1's output is JSONL-parseable by `extractJSONAssistantText`/`appendAgentToolUseMarker`. Not an issue for `subagent-delegation` (delegation happens in turn 1), but any future codex multi-turn scenario needing marker/tool-call visibility past turn 1 will need `--json` added to the resume args first — check `assertionOutput`'s behavior for the other codex multi-turn scenarios before assuming it works.
