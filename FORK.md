# Bifrost Plus (fork of maximhq/bifrost)

This repository is a fork of [maximhq/bifrost](https://github.com/maximhq/bifrost) that adds:

- **OAuth subscription providers** `antigravity` (Google Antigravity / Cloud Code Assist) and `kiro` (AWS Kiro). Each key is one signed-in account. You can sign in from the dashboard (Google OAuth for Antigravity; a device code with AWS Builder ID, Google or GitHub for Kiro) or paste a refresh token or Kiro token JSON. Access tokens refresh automatically, and rotated credentials are written back to the key. See [Antigravity](docs/providers/supported-providers/antigravity.mdx) and [Kiro](docs/providers/supported-providers/kiro.mdx).
- **Per-provider key rotation** (`key_selection`): `weighted_random` (default), `round_robin` (weighted, optional `sticky_limit`), `least_used` and `fill_first`, plus cross-request cooldowns for keys that hit a rate limit, an exhausted quota or a rejected credential. Available for every provider. See [Key Rotation](docs/providers/key-rotation.mdx).

The provider ports are based on [OpenCodex](https://github.com/lidge-jun/opencodex) (MIT, see [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)).

Everything else is upstream Bifrost. Upstream's documentation applies unchanged.

---

## Branches

| Branch | Contents | Rule |
|--------|----------|------|
| `dev` | Exact mirror of upstream `dev` | Never commit here. It only fast-forwards to upstream. |
| `plus` | Upstream `dev` + this fork's features | All fork work lands here. Make it the repository's default branch. |
| `sync/upstream-dev` | Upstream merge in progress | Created by the sync workflow and merged into `plus` through a pull request. |

`git diff dev plus` is always exactly what the fork changes.

---

## Build and run locally

Requirements: Go 1.27, a C compiler (CGO for SQLite), Node 22+ for the dashboard.

```bash
scripts/fork/build.sh              # go.work + dashboard + binary -> ./tmp/bifrost-http
scripts/fork/build.sh --skip-ui    # binary only (keeps the dashboard that is already built)
```

The fork changes `core`, `framework` and `transports` together. The modules still carry the upstream `github.com/maximhq/bifrost/...` paths, and their `go.mod` files require the **published** upstream versions. Builds therefore have to go through a Go workspace (`go.work`, gitignored) that points every module at this checkout. `scripts/fork/workspace.sh` creates it. Do **not** use `make setup-workspace` or `go work sync`: they rewrite every `go.mod`/`go.sum`, and those edits conflict with each upstream sync.

Minimal `./data/config.json`:

```json
{
  "setup_token": "change-me",
  "config_store": { "enabled": true, "type": "sqlite", "config": { "path": "./data/config.db" } },
  "governance": {
    "virtual_keys": [{
      "name": "local", "id": "vk-local", "value": "sk-bf-local", "is_active": true,
      "provider_configs": [
        { "provider": "antigravity", "allowed_models": ["*"], "key_ids": ["*"], "weight": 1 },
        { "provider": "kiro",        "allowed_models": ["*"], "key_ids": ["*"], "weight": 1 }
      ]
    }]
  }
}
```

```bash
./tmp/bifrost-http -app-dir ./data -port 8080
```

1. Open http://localhost:8080 and enter the setup token.
2. Go to **Model Providers → Add New Provider**, pick **Antigravity (Google OAuth)** or **Kiro (AWS OAuth)**, then **Add Key → Sign in**. Add one key per account.
3. Choose the rotation strategy in **Edit Provider Config → Key Rotation**.

```bash
curl http://localhost:8080/v1/chat/completions \
  -H 'x-bf-vk: sk-bf-local' -H 'Content-Type: application/json' \
  -d '{"model":"kiro/claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}]}'
```

### Docker

```bash
docker build -f transports/Dockerfile.local -t bifrost-plus .
docker run -p 8080:8080 -v "$PWD/data:/app/data" bifrost-plus
```

Use `transports/Dockerfile.local`, which builds from the local modules. `make docker-image LOCAL=1` does the same. The default `transports/Dockerfile` and the upstream release pipeline compile against the published upstream modules, and the resulting binary does **not** contain the fork's features.

---

## Syncing with upstream

### Automatic (GitHub Actions)

`.github/workflows/fork-sync-upstream.yml` runs every day at 03:23 UTC, and on demand from the Actions tab:

1. It fast-forwards `dev` to upstream `dev`.
2. It merges upstream into `sync/upstream-dev`, which is cut from `plus`.
3. It runs `scripts/fork/verify.sh` on the merged tree.
4. It opens or updates a pull request into `plus`, and says in the description whether verification passed. Merge it with a **merge commit**, not squash or rebase, so the next sync only carries new upstream commits.
5. If the merge has conflicts it cannot resolve, it opens or comments on an issue that lists the conflicted files.

`.github/workflows/fork-verify.yml` runs the same verification, plus the dashboard build and its unit tests, on pushes to `plus` and on pull requests into it.

One-time setup (after pushing `plus` and making it the default branch, so GitHub picks up the `fork-*` workflows):

1. **Enable Actions.** Forks start with Actions disabled: open the **Actions** tab and enable them.
2. **Secret `FORK_SYNC_TOKEN`.** Create a fine-grained personal access token for this repository with read/write on Contents, Pull requests, Issues and **Workflows**. Most upstream syncs touch `.github/workflows/`, and GitHub refuses those pushes from the default `GITHUB_TOKEN`. Pull requests opened with the PAT also trigger `fork-verify.yml`.
3. **Settings → Actions → General.** Allow GitHub Actions to create pull requests. Without the PAT this is required; with it, it is harmless.
4. **Disable upstream's workflows in this fork.** They expect upstream's secrets and infrastructure (releases, Docker Hub, codecov, live provider tests):

   ```bash
   gh workflow list -R buiducnhat/bifrost --all --json path -q '.[].path' \
     | grep -v '/fork-' | xargs -n1 basename \
     | xargs -I{} gh workflow disable {} -R buiducnhat/bifrost
   ```

   Re-run it after a sync that adds new upstream workflows.

### Manual

```bash
git checkout plus
scripts/fork/sync-upstream.sh          # fetch, fast-forward dev, merge into plus, verify
scripts/fork/sync-upstream.sh --push   # ...and push dev and plus to origin
```

Options: `--upstream-branch`, `--mirror`, `--branch`, `--no-verify`, `--push` (see `--help`). The script:

- adds the `upstream` remote if it is missing;
- refuses to run on a dirty tree, and if `dev` has commits that are not upstream;
- turns on `git rerere`, so a conflict you resolve once is resolved the same way on every later sync;
- regenerates `docs/openapi/openapi.json` (needs `python3` with PyYAML) once it is the only conflict left.

Exit codes: `0` synced or already up to date; `1` conflicts left to resolve by hand (the merge stays in progress); `2` usage or repository-state error; `3` merged, but verification failed.

### What verification covers

`scripts/fork/verify.sh` (add `--ui` for the dashboard):

- builds and vets `core`, `framework` and `transports`;
- tests the rotation engine and the core retry hooks, both providers, the `key_selection` plumbing, the fork migration, the sign-in endpoints and credential write-back;
- checks the API collection fixtures.

It deliberately does not run the rest of upstream's test suite. Upstream `dev` has tests that also fail there; when one does, check it against `dev` before assuming the fork broke it.

---

## Resolving sync conflicts

Conflicts only happen where the fork edits a file that upstream also changed. Most fork code lives in files upstream does not have:

- `core/providers/antigravity/` and `core/providers/kiro/`
- `core/keyselectors/rotation.go`, `core/keyrotation.go`, `core/schemas/keyselection.go` and `core/schemas/keycredentials.go`
- `framework/configstore/forkmigrations.go`
- `transports/bifrost-http/handlers/oauth_subscriptions.go` and `transports/bifrost-http/lib/credential_updater.go` (credential write-back: `BaseAccount` implements `schemas.KeyCredentialStore`, so `BifrostConfig` and `bifrost.Init` stay as upstream has them)
- `ui/.../keySelectionFormFragment.tsx`, `ui/.../oauthSubscriptionSignIn.tsx` and `ui/lib/store/apis/oauthSubscriptionsApi.ts`
- the docs pages, `scripts/fork/`, the `fork-*.yml` workflows and this file

The edits to upstream files are small, additive hooks:

| File | Fork edit | How to resolve |
|------|-----------|----------------|
| `core/bifrost.go` | provider imports and factory `case`s, the `keyRotator` field and its `Init` line, `keyObserver` calls in `executeRequestWithRetries`, the `requestWorker` observer and `selectKeyWithStrategy` call, `SelectKeyForProviderRequestType` | Keep upstream's change and re-add the fork lines around it |
| `core/schemas/bifrost.go`, `core/schemas/provider.go` | provider constants and `StandardProviders` entries; the `ProviderConfig.KeySelection` field | Keep both sides |
| `core/utils.go` | `validateKey` cases | Keep both |
| `framework/configstore/{clientconfig,rdb}.go`, `tables/provider.go` | `KeySelection` next to every `PromptCache` | Keep both; mirror any new `PromptCache` site |
| `transports/bifrost-http/handlers/{providers,provider_keys}.go`, `lib/account.go` | `key_selection` next to `prompt_cache`; credential validation | Keep both |
| `transports/bifrost-http/server/server.go` | sign-in handler creation and route registration (2 lines) | Keep both |
| `transports/config.schema.json`, `helm-charts/bifrost/values.schema.json` | `key_selection` beside `prompt_cache`; provider entries | Keep both; `TestKeySelectionValidateMatchesConfigSchema` (run by `verify.sh`) checks the `key_selection` schema against the Go validation |
| `ui/...` (provider constants, types, schemas, key form, config sheet) | provider entries, `key_selection` field, sign-in section, tab | Keep both |
| `docs/docs.json`, `docs/openapi/**/*.yaml`, `overview.mdx`, `keys-management.mdx` | nav entries, endpoint and schema docs | Keep both |
| `docs/openapi/openapi.json` | generated | Never edit by hand: `cd docs/openapi && python3 bundle.py` after the YAML is resolved |
| `tests/e2e/api/collections/bifrost-api-management.postman_collection.json` | redaction fixtures for the two providers | Keep both |
| `README.md`, `THIRD_PARTY_NOTICES.md` | fork notice and OpenCodex credit | Keep both |

After resolving: `git add -A && git commit --no-edit && scripts/fork/verify.sh`. Then push `plus` and `dev`.

### Conventions that keep syncs cheap

- Put new fork code in new files. Hook it into upstream files with as few lines as possible.
- Register configstore migrations in `framework/configstore/forkmigrations.go`. They are appended after upstream's steps at package init, so `migrations.go` stays identical to upstream.
- Do not edit the module `changelog.md` files; upstream rewrites them every release. Record fork changes in the changelog below.
- Never commit to `dev`, and never rebase `plus` onto upstream: merge, so history that has been pushed stays stable.
- Never run `go work sync` or `make setup-workspace`, and never commit `go.work`.
- When upstream adds a new place where provider-level settings are copied, add `KeySelection` beside it: after each sync, `grep -rn PromptCache framework transports --include='*.go'` and compare with the `KeySelection` sites.

---

## Fork changelog

### Unreleased

- **feat (core):** `antigravity` provider. Uses a Google Antigravity subscription through Cloud Code Assist, with the key value holding a Google OAuth refresh token, either bare or as JSON with `project_id`/`email`. Supports chat, chat streaming, Responses (served through chat) and list models (live `fetchAvailableModels` with a built-in fallback catalog). Refreshes tokens and discovers the project automatically; discovered projects and rotated refresh tokens are written back when the `Account` implements `schemas.KeyCredentialStore` (the HTTP gateway's does; Go SDK users can implement `UpdateKeyCredential` on their own account).
- **feat (core):** `kiro` provider. Uses a Kiro subscription through the Kiro runtime, with the key value holding a social refresh token or the Kiro IDE `kiro-auth-token.json` JSON (social or AWS SSO OIDC refresh). Supports chat, chat streaming, Responses (served through chat) and a static model list. Throttling, monthly quota and suspension are classified so rotation reacts to them, including errors sent inside the event stream.
- **feat (core):** provider-level key rotation `ProviderConfig.KeySelection` (`key_selection`): `weighted_random`, `round_robin`, `least_used` and `fill_first`, with `sticky_limit` and per-key cooldowns (per model for rate limits and quota, whole key for rejected credentials). An upstream `Retry-After` overrides `cooldown_seconds` (default 60; 0 disables). State is in memory, per process.
- **feat (framework):** the config store persists `key_selection` in `config_providers.key_selection_json` (migration `add_key_selection_json_column`).
- **feat (transports):** sign-in endpoints `POST /api/oauth-subscriptions/antigravity/{start,complete}` and `POST /api/oauth-subscriptions/kiro/{start,poll}`; refreshed credentials are written back to the key. Keys whose value is `env.VAR` are never overwritten.
- **feat (ui):** Antigravity and Kiro providers with sign-in helpers in the key form, and a **Key Rotation** tab in the provider settings.
- **chore:** fork tooling (`scripts/fork/`, `fork-sync-upstream.yml`, `fork-verify.yml`) and this guide.
