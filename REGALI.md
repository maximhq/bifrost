# REGALI fork of Bifrost

This repository is REGALITECH's fork of [maximhq/bifrost](https://github.com/maximhq/bifrost).
Only the operational rules that differ from upstream live here; everything else follows upstream's
own docs (`README.md`, `AGENTS.md`).

**Guiding rule: keep the tree as close to upstream as possible.** Every upstream-tracked file we edit
or delete becomes a merge conflict on every future sync. Where a setting can achieve the same result
(disabling a workflow, toggling a repository option), prefer the setting over a code change.

## Branches

| Branch | Role |
| --- | --- |
| `main` (this repo) | REGALI's line: upstream code plus REGALI changes. |
| `dev` (upstream) | maximhq's default branch — the ref we sync from. |

`main` is protected by an active repository ruleset (`main`): pull request required (0 approvals),
no force push, no branch deletion, no bypass actors. Upstream syncs therefore also go through a PR.
This matches the rulesets on `etla_app`, `clomoni_api` and `aeon_app`.

## What diverges from upstream

As of 2026-08-20, relative to upstream commit `0a8a38c9` (2026-08-15):

| Change | Paths |
| --- | --- |
| Fish Audio provider (speech + realtime WS) | `core/providers/fishaudio/`, `core/schemas/`, `core/internal/llmtests/`, `framework/streaming/audio.go`, `plugins/logging/operations.go`, `transports/bifrost-http/`, `transports/config.schema.json`, `ui/lib/constants/`, `docs/providers/supported-providers/fishaudio.mdx` |
| Fish Audio catalog pricing derived from the datasheet's OpenRouter rows | `framework/modelcatalog/datasheet/derive.go`, `framework/modelcatalog/datasheet/derive_test.go`, `framework/modelcatalog/datasheet/testdata/pricing-openrouter-fishaudio.json`, `framework/modelcatalog/datasheet/sync.go` (one call site) |
| Metronome builtin exporter and external usage delivery (2026-09) | `plugins/metronome/`, `transports/bifrost-http/{handlers/fishaudio_usage.go,server/plugins.go,lib/config.go}`, `transports/go.mod`, `transports/config.schema.json`, `transports/Dockerfile.local`, `Makefile`, `docs/plugins/metronome.mdx`, `docs/docs.json`, `examples/configs/withmetronome/` |
| Metronome CI coverage and release smoke test | `.github/workflows/metronome-tests.yml`, `.github/workflows/scripts/{setup-go-workspace.sh,validate-schema-sync.sh,test-metronome-image.sh}`, `.github/dependabot.yml`, `.github/workflows/regali-docker-release.yml` |
| Hosted System One credit pricing: `typesafe_config.usd_per_credit`, credits × price spend, fail-closed without a price | `core/providers/typesafe/credits.go`, `core/schemas/{provider,chatcompletions}.go`, `framework/configstore/`, `framework/modelcatalog/datasheet/cost.go`, `plugins/metronome/main.go`, `transports/config.schema.json`, `transports/bifrost-http/{handlers/providers.go,lib/account.go}`, `docs/providers/supported-providers/typesafe.mdx`, `docs/plugins/metronome.mdx` |
| TypeSafe decisions: replay-safe Metronome billing, per-tenant idempotency keys, exact-body native route; Metronome exports decision input tokens | `core/providers/typesafe/replay.go` (new), `core/providers/typesafe/typesafe.go`, `core/schemas/decision.go`, `core/decisionemulation.go`, `core/bifrost.go` (fallback copy), `transports/bifrost-http/integrations/typesafe.go`, `plugins/metronome/`, `framework/modelcatalog/datasheet/decision_cost_test.go` (new), `docs/plugins/metronome.mdx` |
| GHCR release workflow (new file) | `.github/workflows/regali-docker-release.yml` |
| `FISH_AUDIO_API_KEY` plumbed into upstream test jobs | `.github/workflows/pr-tests.yml`, `.github/workflows/release-pipeline.yml` |
| Snyk workflow deleted (not usable in a fork) | `.github/workflows/snyk.yml` |
| CODEOWNERS deleted | `.github/CODEOWNERS` — it assigned every path to `@maximhq/bifrost-admin`, a team that does not exist in this org, so it was inert. No other REGALITECH repository uses CODEOWNERS. |
| Issue template config replaced | `.github/ISSUE_TEMPLATE/config.yml` — points upstream product reports at maximhq/bifrost |
| This file (new) | `REGALI.md` |

Keep this table current when the fork gains or drops a change; it is the checklist for resolving
conflicts during an upstream sync.

## Upstream sync

Direction decided 2026-08-20.

**Track upstream `main` at release boundaries.** Sync from the `transports/v*` tags — the gateway
releases, which are also what this fork's own `v*-regali.*` tags are numbered after. Do not track
upstream `dev`: it took 640 commits in the 30 days to 2026-08-20 and carries unreleased work.

**Cadence: at least once a month, up to the newest `transports/v*` tag at that time.** Upstream cut
7 gateway releases between 2026-07-14 and 2026-08-15 — roughly one every 4–5 days — so following
literally every release is more work than it is worth. A month is the ceiling on drift, not a target:
the cost of a sync grows with the size of the gap, so a longer pause is not a saving.

**How.** Merge, not rebase, into `main` through a pull request. Rebasing rewrites the fork's commits
and forces the same conflicts to be resolved again on every replayed commit; a merge resolves them
once. The `main` ruleset requires the PR regardless.

```
git fetch upstream --tags
git switch -c sync/transports-vX.Y.Z main
git merge transports/vX.Y.Z
# resolve conflicts, then open a PR against main
```

**Where the conflicts will be.** Only in files this fork modified that upstream also touched. New
files never conflict. As of 2026-08-20, 17 of the fork's 25 modified files had already changed
upstream within 5 days — `core/bifrost.go`, `core/utils.go`, `core/schemas/bifrost.go`,
`core/internal/llmtests/account.go`, `plugins/logging/operations.go`,
`transports/bifrost-http/handlers/wsrealtime.go`, `transports/bifrost-http/handlers/realtime_turn_pipeline.go`,
`transports/config.schema.json`, `ui/lib/constants/{config.ts,logs.ts}`, `docs/docs.json`,
the generated `docs/openapi/openapi.json`, `core/go.{mod,sum}` and the two workflow files.

The ~1,700 lines under `core/providers/fishaudio/` are new files and cost nothing to carry. Keep new
behaviour in new files and hold edits to upstream files down to the few registration lines they
genuinely need — that is what keeps a sync cheap.

**Verify after merging — no credentials required.** The check that matters is that the Fish Audio
provider still builds and its unit tests pass; `82660c2b` ("Adapt Fish Audio provider to current
upstream dev API") is an instance of upstream changing an API out from under it, and that class of
break shows up at compile time or in the transformation tests.

```
(cd core && go build ./... && go test ./providers/fishaudio/)
(cd framework && go test ./modelcatalog/datasheet/)
```

Upstream reshapes the datasheet loader often; the derivation call site and tests catch a break at
compile or test time.

Provider tests that call a live API skip themselves when their key is absent, so this passes with no
API key set: the four `realtime_test.go` unit tests run, `TestFishAudio` and `TestFishAudioIntegration`
skip, and the package reports `ok`. The same holds for the wider unit layer — upstream's
`test-core-unit` step deliberately excludes every package whose tests import `core/internal/llmtests`,
which is exactly the set that needs credentials.

Hitting the real Fish Audio API is a separate, occasional check, not part of a sync. It needs one
secret (`FISH_AUDIO_API_KEY`) and is worth doing before a release rather than on every merge.

**A sync is not finished in this repository.** Deployment is defined separately, in REGALI's private
infrastructure Terraform repository, which pins both the upstream Helm chart version and the container
image tag. Merging upstream here changes nothing that is running until those pins are raised there.
Treat the two as one unit of work: whoever performs the sync is responsible for the infrastructure
bump, or for handing it over explicitly.

**Upstream v2.0.0 is coming.** `transports/v2.0.0-prerelease3` was tagged 2026-08-13. A major
version will not be an ordinary sync; budget for it separately rather than folding it into a
monthly pass.

## Fish Audio provider: propose upstream

Direction decided 2026-08-20: aim to contribute the Fish Audio provider to maximhq/bifrost.

If upstream accepts it, the provider stops being fork divergence altogether and upstream maintains
its compatibility — the class of work represented by `82660c2b` disappears, and the largest part of
this fork's diff goes away. Upstream's own guides for this: `docs/contributing/adding-a-provider.mdx`,
`docs/contributing/code-conventions.mdx`, `docs/contributing/raising-a-pr.mdx`.

The trade-off is that the implementation goes under public review and the timing depends on
upstream's judgement. Until it lands, the provider stays fork divergence and is synced like
everything else here.

## Releases

Push a tag matching `v*-regali.*` (for example `v1.6.11-regali.1`). `regali-docker-release.yml`
builds `transports/Dockerfile.local` for `linux/amd64` and `linux/arm64` and pushes
`ghcr.io/regalitech/bifrost:<tag>`. It also runs on `workflow_dispatch`, tagging `test-<sha7>`.

Note that other REGALITECH services publish to ECR through the reusable workflows in
[`gha-shared`](https://github.com/REGALITECH/gha-shared). This repository uses GHCR with the built-in
`GITHUB_TOKEN` instead, because the fork is public and needs no extra credentials. Revisit that choice
if the image has to be deployed through the same ECR/ArgoCD path as the other services.

## GitHub Actions

Upstream ships release and publish workflows that fire on pushes to `main`. In a fork they either fail
or attempt maxim's releases, so they are **disabled in repository settings rather than deleted** —
disabling leaves the tree byte-identical to upstream, so syncs stay conflict-free. Re-enable any of
them from Settings → Actions → the workflow → *Enable workflow*.

Active:

| Workflow | Trigger |
| --- | --- |
| `regali-docker-release.yml` | tag `v*-regali.*`, `workflow_dispatch` |
| `run-core-tests.yml` | `workflow_dispatch`, gated on write access |
| `pr-tests.yml` | `workflow_dispatch` |
| `workflow-lint.yml` | pull requests touching `.github/workflows/**` |

Disabled:

| Workflow | Why |
| --- | --- |
| Release Pipeline, Release CLI, Release Migration CLI | upstream release automation, fires on push to `main` |
| NPX Package Publish | publishes maxim's npm packages |
| Release Helm Chart | publishes maxim's Helm chart |
| OpenAPI Bundle | commits generated output back to `main` |
| PR Test Notifier | maxim-specific, failing in the fork |
| Docs Validation | validates maxim's docs site, failing in the fork |
| Scorecard | OpenSSF scorecard for the upstream OSS project |
| Dependabot Alerts to Issues | opens an issue every Monday |
| E2E Tests | only triggers on a stale upstream branch name |
| Dependency Review | every upstream sync PR carries a large dependency diff |

No repository Actions secrets are configured. This does not break anything: tests that call a live
provider skip themselves when their key is absent, and the credential-free unit layer runs regardless.
It only means `pr-tests.yml` and `run-core-tests.yml` currently exercise nothing that needs a key.

If that changes, add only the keys this fork actually needs. Each provider's tests run only when its
own key is present, so the cost follows what you add — and it is not proportional to the number of
keys: upstream enables 3 scenarios for Fish Audio but 57 for OpenAI and 51 for Gemini, including
image generation. Use a credential issued for testing, never a production key.

## Repository settings

- **Visibility: public.** Inherited from the fork relationship and intentionally kept. GitHub cannot
  convert a public fork to private — doing so would require re-creating the repository from a bare
  clone. Assume anything committed here is world-readable.
- **Issues: enabled**, for REGALI's own tracking of fork work.
- **Wiki: enabled**, left at GitHub's default like the rest of the org. It is empty — a fork does
  not inherit the upstream wiki and none was ever created here.
- **Access: no repository-specific grants.** REGALITECH org members get `write` from the org default
  permission and owners get `admin`, the same as every other repository in the org. The org has no
  teams.
- Secret scanning and push protection are enabled; Dependabot security updates are disabled.

## Metronome verification

`make test-metronome` runs the exporter, HTTP handler, and builtin registration
tests with the race detector and no live credentials. The dedicated
`metronome-tests.yml` runs this on every PR to main, independently of the
secret-bearing test approval environment. An unrelated green workflow is not
evidence that these tests ran. The release job also boots each static image
with an environment-backed test key and verifies `metronome` is loaded without sending usage.

The module is fork-only: `transports/go.mod` uses a local replacement until a
separate module publication strategy is adopted. Keep this workspace/replacement
and the explicit CI module lists when syncing upstream. The builtin needs no
extra wiring in `server.go`; its Fish Audio handler resolves it from `lib.Config`.
