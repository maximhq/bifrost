---
name: run-ci-tests
description: Run Bifrost's release-pipeline test jobs locally exactly as CI runs them (test-core-unit, test-framework, test-plugins, test-bifrost-http, test-migrations, and only on explicit request the core provider Go tests and the test-core provider harness), sourcing secrets from Infisical without ever exposing them, with per-job env parity, Docker port-conflict handling, and a markdown evidence report. Invoked with /run-ci-tests [all | core-unit framework plugins bifrost-http migrations core-providers core-harness]; all excludes core-providers and core-harness unless the user explicitly confirms them.
allowed-tools: Read, Grep, Glob, Bash, AskUserQuestion, TodoWrite
---

# Run CI Tests Locally

Reproduce the `release-pipeline.yml` test jobs on a developer machine, AS IS: the same
scripts, the same flags, the same per-job environment. Do not "fix" or rewrite the CI
scripts; adapt the environment around them and disclose every adaptation in the report.

## Usage

```text
/run-ci-tests                      # = all (everything except the two provider suites)
/run-ci-tests core-unit framework  # a subset, run serially in this order
/run-ci-tests core-providers       # Go provider tests (llmtests-backed packages); explicit yes required (real spend)
/run-ci-tests core-harness         # newman provider harness in tests/e2e/api; explicit yes required (real spend)
```

All heavy lifting is in `<skill-dir>/bin/run-suite.sh`, where `<skill-dir>` is the base directory
Claude Code prints when this skill is invoked (`.claude/skills/run-ci-tests` under the repo root).
Run it from anywhere; it locates the repo root itself. Logs default to `tmp/ci-local/` (gitignored).
Always call it by that absolute path, as the procedure below is written; the Bash tool's cwd
persists across calls and relative paths drift.

## CI job map (what "the tests" are)

| CI job | What CI runs | Local command | Docker | Ports | CI env (secrets) |
|---|---|---|---|---|---|
| test-core-unit | `make setup-mcp-tests`, then a bash loop `go test -timeout 20m` over core packages minus llmtests-backed ones (`working-directory: core`) | `run-suite.sh core-unit` (extracts and runs both steps verbatim from the YAML) | none | none | **none** |
| build-gateway | `make build-ui` + `go build -o tmp/bifrost-http` | `run-suite.sh build-gateway` | none | none | none |
| test-framework | `scripts/test-framework.sh`: `tests/docker-compose.yml up`, `go test --race -coverprofile ./...` in `framework/` | `run-suite.sh framework` | tests/docker-compose.yml | 5432 9000 6379 8001 6334 5081 6380 7000/17000 7100/17100 | ~41 provider keys |
| test-plugins | `scripts/test-all-plugins.sh`: same compose, per plugin `go test -v -timeout 20m -coverprofile` | `run-suite.sh plugins` | tests/docker-compose.yml | same as framework | same as framework |
| test-bifrost-http | `scripts/test-bifrost-http.sh`: helm schema check, hello-world plugin build, `go test --race -v -timeout 40m ./...` in `transports/`, then boots `tmp/bifrost-http` against 10 app-dir configs with `.github/workflows/configs/docker-compose.yml` | `run-suite.sh bifrost-http` (`SKIP_GATEWAY_BUILD=1`, needs build-gateway first) | .github/workflows/configs | 5432 9000 6379 8001 6333 6334 18080 | MAXIM_API_KEY, MAXIM_LOGGER_ID, CODECOV_TOKEN, BIFROST_ENCRYPTION_KEY only |
| test-migrations | `scripts/run-migration-tests.sh postgres`: downloads the previous 3 stable `transports/v*` release binaries (relative to `transports/version`), seeds faker data, migrates with the current binary | `run-suite.sh migrations` runs **both** `postgres` and `sqlite` (repo owner's rule; CI does postgres only), after `migrations-plan` + `CONFIRM_MIGRATION_VERSION=1` | configs compose (postgres only) | 5432 (8089 auto-bumps) | none |
| core provider Go tests (not a CI job; `make test-core`) | every core package backed by `core/internal/llmtests` (`providers/*` plus the other packages that import `internal/llmtests`), i.e. exactly what test-core-unit skips; real provider calls | `run-suite.sh core-providers` with `CONFIRM_PROVIDERS=1` (`GOWORK=off go test -v -timeout 20m`, whole Infisical env, no per-job filter) | none | none | all provider keys + model/URL knobs the tests read (`FIREWORKS_*_MODEL`, `VLLM_*`, `OLLAMA_BASE_URL`, `SGL_BASE_URL`, ...) |
| test-core (newman provider harness) | `scripts/test-core.sh`: core `go build` + the Postman collection in `tests/e2e/api` via newman against a live gateway (~90 min, RERUN_FAILED retries x2) | `run-suite.sh core-harness` with `CONFIRM_HARNESS=1` | none (sqlite) | `$PORT` (8080) | ~45 provider keys |

The env column is authoritative because `bin/ci-env.py` reads it from the job's `env:` block at
run time; nothing in this skill hardcodes secret names.

Terminology the repo owner uses: "core tests" / "provider tests" = the Go tests in `core/` including the
llmtests-backed packages (`core-unit` + `core-providers`); "core harness" = the newman/Postman collection
under `tests/` (`core-harness`). Both spend real provider credits and always need an explicit yes.

## Hard rules

1. **Secrets never enter the conversation.** Never `infisical export`/`infisical secrets` to
   stdout, never `env`, never `cat .env`, never print a log section that could echo headers.
   Only variable *names* may be shown (`ci-env.py` prints names; `report.py` echoes only test tool output).
2. **Never install or download anything without asking** (gcloud, coreutils, brew packages,
   SDK tarballs). Preflight tells you what is missing; relay it and let the user decide.
3. **Always ask before**: running the provider harness (real spend), stopping any Docker
   container, closing any dev server, or building the UI while a `next dev` server runs from `ui/`.
   Never `kill` a process that holds a port; ask the user to close it.
4. Docker-backed suites run **serially** (lane A): framework/plugins share `tests/docker-compose.yml`,
   bifrost-http/migrations-postgres share the configs compose, all bind the same fixed host ports,
   and every script runs `docker compose down` on exit, so overlapping them tears down a sibling's
   stack. Non-Docker suites (core-unit, migrations-sqlite, the harness) can run alongside lane A;
   `run-suite.sh all` does exactly that (lane B). Do not bypass the scripts to share one stack:
   framework and plugin tests would then share Redis/Weaviate/Postgres state and `--race` CPU.
5. Do not edit anything under `.github/workflows/`. Environment adaptations only, all disclosed.
6. **Keep the caller updated.** Long suites run in the background; do not go silent until the final
   notification. Arm a `Monitor` on the chain log (suite transitions, `FAIL` package/test lines, a
   progress summary every ~5 min) and post a short status at every suite transition, at every
   failure as it appears, and roughly every 10 minutes during a long suite (packages ok/FAIL so far,
   tests PASS/FAIL/SKIP). Never `sleep`-poll in Bash.
7. Never revert the user's tree. `make setup-mcp-tests` (`npm install` in `examples/mcps/*`) may
   rewrite `package-lock.json` files; report it, do not `git checkout` it.

## Procedure

1. **Preflight**: `<skill-dir>/bin/run-suite.sh preflight <suites...>`. Exit 2 means a user decision is needed
   (the message says which). Resolve with the conflict playbook below, then re-run preflight.
   A `local Go differs from CI's go-version` warning is also a decision: the suites will download the
   go.mod toolchain (see the Go version row), so get the user's OK first.
2. **Build once**: `<skill-dir>/bin/run-suite.sh build-gateway` (CI's build-gateway job). Rebuild when the
   transports/UI changed since `tmp/bifrost-http` was built (preflight prints its mtime).
3. **Migrations need two answers first.** Run `<skill-dir>/bin/run-suite.sh migrations-plan`, which prints the
   release under test (read from `transports/version`; the script has no override) and the previous
   stable versions it will migrate from. **Always ask the user which version they are releasing** and
   confirm the plan matches (a prerelease suffix on `transports/version` still selects the previous stable
   tags, which is correct; a different release line or count means `transports/version` or
   `VERSIONS_TO_TEST` must change first, by the user). Only then run with `CONFIRM_MIGRATION_VERSION=1`,
   which executes the script for **postgres and sqlite** (two logs: `test-migrations-postgres`,
   `test-migrations-sqlite`). CI only runs postgres; both are required here.
4. **Run**: `<skill-dir>/bin/run-suite.sh all` (two lanes, see rule 4) or individual suites, each in the
   background with its log tailed only for milestone lines (`grep -E '^(ok|FAIL|---|✅|❌)'`),
   never `cat` whole logs. Manual order for lane A: `build-gateway` → `bifrost-http` →
   `migrations-postgres` → `framework` → `plugins`; lane B alongside: `core-unit`, `migrations-sqlite`.
   Use `VERBOSE=1` for `core-unit` and `framework` (CI's commands there omit `-v`, so without it
   the log has no per-test evidence). It only changes output, and the report says so.
5. **Report**: `<skill-dir>/bin/run-suite.sh report --out tmp/ci-local/report.md`, then present it (see below).
6. **Restore**: `<skill-dir>/bin/run-suite.sh restore` re-starts containers that preflight stopped, and remind
   the user to re-enable anything they turned off (AirPlay Receiver, their dev server).

## Conflict playbook 

| Situation | What to do |
|---|---|
| Port 8080 (harness) held by a `main`/`bifrost-http` process | It is the enterprise dev gateway run by air. **Ask the user to close it**; do not kill it and do not silently pick another port (the harness would otherwise "pass" against the wrong gateway, because `harness_start_gateway` polls `/health` before checking its own PID). `PORT=<n>` is acceptable only if the user prefers it. |
| 5432/9000/6379/8001 held by `bifrost-postgres`, `configs-weaviate-1`, `configs-redis-stack-1` | Those are the enterprise `examples/configs` dev stack. Ask, then `docker stop` them (`STOP_CONFLICTING=1` lets preflight do it and records them for `restore`). If the permission classifier blocks `docker stop`, ask the user to run it with `! docker stop ...`. |
| Compose project-name collision | `.github/workflows/configs/` and the enterprise `examples/configs/` both resolve to project `configs`; a plain `down` would delete the user's containers. The runner sets `COMPOSE_PROJECT_NAME=bifrost-ci-configs` for the two scripts that use that file. |
| Port 7000 held by `ControlCe` | macOS AirPlay Receiver. `tests/docker-compose.yml` binds `redis-cluster` to 7000 and `up -d` fails. Ask the user: System Settings > General > AirDrop & Handoff > AirPlay Receiver > off. |
| Secrets | Source **both** `/local` and `/github/oss-repo` (nested `infisical run`, the github path wins on overlap because it mirrors the Actions secrets). Whatever a job declares that neither path provides stays unset and the tests gated on it skip, exactly as they would with a missing Actions secret. `ci-env.py` prints the UNRESOLVED names; put them in the report. |
| `make build-ui` | It `rm -rf ui/.next` and copies into `transports/bifrost-http/ui`. A vite dev server from `ui/` is unaffected; a `next dev` server is not. Check `lsof -iTCP:3000` cwd first. |
| No `timeout(1)` (macOS) | `test-bifrost-http.sh` calls `timeout 120s`. The runner prepends `bin/timeout` (a shim, exit 124 on expiry) only when none is on PATH. `brew install coreutils` + gnubin on PATH is the alternative; ask first. |
| Docker pulls hang (`compose up` sits on `X Pulling` for minutes, no image events, even `docker pull hello-world` hangs) | Docker Desktop's pull path is wedged, not the tests: host `curl https://registry-1.docker.io/v2/` returns 401 fine while `docker info` shows `HTTP Proxy: http.docker.internal:3128`. Do not keep waiting. Kill the hung `docker compose ... up` (SIGKILL; SIGTERM is ignored) so the script's trap runs, and say so in the status update: that process is the runner's own child and the trap only downs the runner's own compose project (`tests` or `bifrost-ci-configs`), so hard rule 3, which covers the user's containers and servers, does not gate it. Then ask the user to fix Docker Desktop (restart / Settings > Resources > Proxies) or to pull the image themselves. List exactly which images are missing first: `docker image inspect <ref>` per `image:` line of both compose files (local only, no network). |
| A digest-pinned image cannot be pulled but the unpinned tag is present locally | First compare digests: `docker image inspect --format '{{join .RepoDigests " "}}' <tag>` against the `@sha256:...` on the compose `image:` line. If the pinned digest is among them, compose already has that image and will not pull it, so the hang is elsewhere: do not un-pin. If it differs, the local image is not the one CI tests against; only with the user's explicit OK (state both digests when asking): back up the compose file, `sed` the `@sha256:...` off that one `image:` line, run, then restore the file byte-for-byte from the backup and verify `git diff --quiet` on it. It is the one exception to "never edit .github/workflows" and must appear in the report's deviations with the pinned digest, the digest actually used, and a note that the affected suite's evidence is against a non-CI image. |
| gcloud missing | Optional. Only the harness's Vertex direct-provider token-parity cells use it, and the Makefile skips those legs when no token can be minted. Do not install unprompted. |
| Go version | CI pins `go-version` in the workflow (preflight prints both). `setup-go-workspace.sh` exports `GOTOOLCHAIN=auto`, so a mismatch downloads the go.mod toolchain rather than failing, and that export overrides anything set in the environment. A mismatch in preflight is therefore a hard rule 2 gate: relay it and get the user's OK for the download (or have them install the matching Go) before running any suite. Never edit the script to change this (rule 5). |
| Leftover `bifrost-ci-configs-postgres-1` after migrations | `run-migration-tests.sh` starts postgres with `up -d postgres` and never downs it (CI runners are ephemeral). The runner's preflight tears down leftover CI-project containers itself (`ci_project_down`), so running framework/plugins next just works; if you invoke the script by hand, `docker compose -f .github/workflows/configs/docker-compose.yml -p bifrost-ci-configs down` first. |
| Package list passed as one argument (`FAIL github.com/a github.com/b ...` on a single line) | The Bash tool's shell is zsh, which does not word-split unquoted `$pkgs`. Run such commands through `bash -c` or use the runner (bash). |
| `go.work` | Untracked but usually present locally; the CI scripts skip init when it exists. |

## Deviations to disclose in every report

- macOS `timeout` shim (if used), `COMPOSE_PROJECT_NAME` override, `VERBOSE=1` (`-v` output only),
  unresolved CI env names per job, containers stopped/restored, anything the user turned off,
  any image digest un-pinned for the run (and that the file was restored),
  suites not run (the harness unless confirmed) and why, files the CI steps modified in the tree,
  and any CI secret resolved from the shell profile rather than Infisical (ci-env.py reports
  `NAME<-SOURCE` for renames such as `MAXIM_LOGGER_ID<-MAXIM_LOG_REPO_ID`).

## Report format (what makes the result trustworthy)

`report.py` builds it from the logs, not from memory. Present, in this order:

1. Summary table: suite, exit code, duration, packages ok/FAIL, tests PASS/FAIL/SKIP, log path.
2. Traceability per suite: the CI job it mirrors, the exact local command, the env-parity line
   (`N/M declared env names resolved`, `UNRESOLVED: ...`).
3. Evidence per suite: every `go test` package line; every FAIL with its first output lines;
   every SKIP grouped by the reason the test printed (so nothing is silently untested);
   milestones (the 10 bifrost-http configs each "started" + "endpoints ok", plugin pass lines,
   migration versions/validations).
4. Deviations (section above) and what was NOT run.
5. Where the raw logs are, so the user can verify any line.

State plainly if anything failed; never round a red run up to "passed with caveats".

## Triage methods for provider-test failures

- A `panic: test timed out` prints every goroutine; the Bifrost frames of the stuck goroutines say exactly where a
  stream hung. Read that before theorising.
- Suspect a stale model pin first. Verify against the provider's own model list with the key
  (`curl .../models | jq '.data[].id'` inside `infisical run`; print ids only, never keys) before touching code.
- Aggregator providers (Groq, OpenRouter, HuggingFace, Nebius, ...) serve several model families. A converter
  change motivated by one family must be probed against every family that provider's test config uses before it
  lands; prefer per-family gating over provider-wide behaviour when families disagree.
- Reasoning models bill thoughts against `max_tokens`; a scenario that returns empty content on a small budget, or
  a later step that fails on a truncated history, is usually budget rather than converter. Capture the raw request
  and response before blaming a converter: `WithRawRequestResponse` variants carry both in `ExtraFields`.
- Re-run a single failing scenario with `-run 'TestX/XTests/<Scenario>$' -count=3` before calling it flaky; the
  retry framework already retries several times, so a failure that survives is usually deterministic.
- `go test` prints package output in listing order, so a slow early package holds back later results; per-package
  progress is only visible once that package finishes.
- For provider-specific live checks, do not use the developer's `make dev`/`dev-pulse` instance: its config store
  can carry routing rules that silently rewrite provider/model, so a probe aimed at one provider is answered by
  another. Trail shows it (`[Routing] Routing rule matched`, `Applied routing decision`). Build
  `transports/bifrost-http` from the working tree, seed an app dir from `tests/integrations/python/config.json`
  (jq-rewrite the two sqlite paths, as `harness_seed_app_dir` does), start it on a spare port under
  `infisical run`, confirm in its log that `attempting ... request for provider X` names the intended provider,
  and kill it when done.

## Files

- `bin/run-suite.sh`: preflight / suites / report / restore (documented at the top of the file).
- `bin/ci-env.py`: per-job env parity wrapper; parses the job's `env:` from the workflow, keeps
  only declared names, prints resolved/unresolved names, never values.
- `bin/report.py`: log parser and markdown renderer (handles parallel `-v` output attribution).
- `bin/timeout`: GNU-timeout shim for macOS (kills the command's whole process group, exit 124), used only when none exists on PATH.
