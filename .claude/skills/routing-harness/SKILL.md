---
name: routing-harness
description: Use when a change touches how a request is routed (routing rules, model routers and the complexity router, the model catalog and key aliases, a model named without a provider, virtual key provider configs and governance load balancing, key selection and key pins, direct keys, session affinity, fallbacks and retries, extra_fields.routing_info, the x-bifrost-routing-info-* headers and the routing log trail) or when asked to add routing tests. Lists the cases each kind of routing change needs, where they go in the routing harness, how to write them and how to run them. Routing cases go in the routing harness, never in the provider harness.
---

# Routing harness

Every end-to-end routing case lives in the routing harness, two generated suites:

- `tests/e2e/api/runners/build-routing-wiring.mjs` generates `tests/e2e/api/collections/bifrost-routing-wiring.postman_collection.json`: which provider, model and key a request is routed to, the refusal when it cannot be, its fallbacks, `routing_info`, and the stored log and routing trail.
- `tests/e2e/api/runners/build-model-catalog-wiring.mjs` generates `tests/e2e/api/collections/bifrost-model-catalog-wiring.postman_collection.json`: provider and key changes reaching the catalog read endpoints, and aliases resolving at inference.

Both collections are committed. Edit the generator, regenerate, and commit both; never hand-edit the JSON. Each runner in `tests/e2e/api/runners/individual/` boots a clean Bifrost (no providers, SQLite config and logs stores) with `--binary` and runs its collection; CI runs both from `.github/workflows/scripts/test-api-integrations.sh`. The runs are live and paid (OpenAI, Anthropic, Gemini, Azure, Bedrock and Vertex through `env.<NAME>` keys): run the free checks in section 5 yourself and hand the live commands to the user.

Read AGENTS.md "Every routing change ships with routing harness cases" first. Cite names, not line numbers: the helpers below are names in the two generators and in `tests/e2e/api/runners/lib/collection-builder.mjs` on `dev`.

## 1. What goes where

| What changed | Test it in |
|---|---|
| Routing a client can observe: the provider, model and key that serve a request, the refusal, fallbacks, `routing_info`, the routing headers, the log row and `routing_engine_logs` | `build-routing-wiring.mjs`, plus Go unit tests |
| Provider and key changes reaching `/api/models`, `/api/models/details` and the base model list; aliases resolving at inference | `build-model-catalog-wiring.mjs`, plus Go unit tests |
| A function's logic, a race, an error path no request can reach | Go unit tests beside the code: `plugins/routing` (rules, model routers, the complexity classifiers and suggestions), `framework/routing`, `framework/modelcatalog`, `plugins/modelcatalogresolver`, `plugins/governance`, `core/keyselectors`, `core/sessionaffinity_test.go` and the key selection and fallback tests in `core` |
| How well the complexity suggestions route a labelled corpus | `tests/cmd/complexitysuggesteval` (offline scoring, not a regression gate) |
| The routing rules, model router, complexity router and model catalog pages | Playwright (`tests/e2e/features/routing-rules/`, `model-routers/`, `complexity-router/`, `model-catalog/`) |
| How a provider converts the request it was routed to or the response it returns | the provider harness (`.claude/skills/harness-test-writer/SKILL.md`) |

A change that reaches both routing and a provider's conversion needs both.

`provider-harness.json` already holds hand-written routing folders (routing rules, governance routing, catalog routing, key pool selection, fallback coverage, the routing error fixture, session affinity, direct keys, model routers). They stay where they are: a change that alters what one of them pins updates it in place, following `harness-test-writer`, and hands over its provider-harness run as well. New routing cases never go there. List them with:

```bash
node -e 'const c=require("./tests/e2e/api/collections/provider-harness.json");for(const f of c.item)if(/rout|session|fallback|direct.key|catalog|alias|model router|complexity/i.test(f.name))console.log(f.name)'
```

`bifrost-v1-vk-routing.postman_collection.json` is an older hand-maintained stub that CI does not run: add no cases there.

## 2. The axes a case is placed on

Every routing change sits on some of these axes. Section 3 says which ones a kind of change must cover; cover every value the change can reach, not only the one named in the issue.

- **Model string:** provider-prefixed (`provider/model`, split only on a known provider), bare (resolved by a rule, a virtual key's load balancing or the catalog), an alias, and a model no provider serves.
- **Entry point:** `/v1/chat/completions`, `/v1/responses` and `/v1/embeddings`, streamed and not; the SDK routes (`/openai`, `/anthropic`, `/genai` and the others), whose integration the catalog resolver prefers; passthrough routes, which fix the provider and skip rules, load balancing and the resolver; batch and files, which need a provider and use only keys marked for the batch API; realtime, which runs the routing hooks once per connection and has no fallbacks.
- **Caller credential:** none, a virtual key (`x-bf-vk`), a direct key (`x-bf-direct-key`); with and without a key pin (`x-bf-api-key-id`, `x-bf-api-key`).
- **Virtual key provider configs:** a weight (a number, 0, and `null` for an allow-list with no balancing), `allowed_models` (`["*"]`, explicit names, `[]`), `blacklisted_models`, `key_ids`, budgets and rate limits, one provider and several.
- **Routing rule:** scope (virtual key, user, team, customer, global) and the order between them; priority; enabled; the CEL inputs (`model`, `provider`, `request_type` with its `_stream` suffix, `headers` and `params` with lowercased keys, the caller's virtual key, team and customer, `budget_used`, `tokens_used`, `complexity_tier`); targets (provider and model, model only, a key pin, weights, `ttft_timeout_ms`); fallbacks in the string and the object (`key_id`) form; chains and `routing_chain_max_depth`.
- **Model router and complexity router:** the classifier (semantic, decision, llm) and its fallback chain, the tiers and each tier's targets and fallbacks, the router's own scope, CEL and priority, session tiers, and the request types that classify (chat, text completions and Responses, streamed and not).
- **Key:** its `models` and blacklist (written in alias names), its aliases (exact, case-insensitive, regex), enabled, weight, the batch API flag; one key and a pool.
- **Session:** `x-bf-session-id` (and the client session headers it falls back to), `x-bf-session-ttl`, `x-bf-session-affinity` off; the route binding and the key binding.
- **Fallback source:** the caller's (the body's `fallbacks`), a rule's or a router's (which replace the caller's), the virtual key's auto-attached ones, and the catalog's inferred ones.
- **Upstream outcome:** success, a caller fault (400, 409, 413, 415, 422), a 429 with and without a retry hint, a 5xx, a dropped connection, a slow first token, a refused key.
- **Where the outcome shows:** `routing_info` (provider, model, key, `resolved_key_alias`, `is_fallback`, `primary_*`, `requested_*`), the `x-bifrost-routing-info-*` headers, the error's status, message and type, `fallback_attempts`, the log row (selected key, virtual key, `fallback_index`, `parent_request_id`) and `routing_engine_logs`.
- **Where the config comes from and when it changes:** created through the API, edited, disabled and deleted between requests, and from `config.json` at boot.

## 3. The cases each kind of change needs

Each block lists what the cases must prove and which generator they go in. For every kind of change, also cover: the change on and back off, the change taking effect on the next request without a restart, and every entry point it reaches. Where a block needs a step the generator does not have yet, add the step in the same PR (section 6).

**Routing rules**: CEL, scope, priority, targets, fallbacks, chains (routing wiring):
- A match and a no-match for each CEL input the change reads, and a header or field that is absent not matching.
- Each scope that can hold the rule and the order between scopes; priority within a scope and the 409 for a duplicate priority.
- Each target shape: provider and model; model only, which the virtual key then balances across its providers; a key pin, which wins over the caller's pin; weights across targets; a first-token cutoff on a stream.
- Rule fallbacks replacing the caller's, in both forms; a fallback the virtual key refuses (refused on its attempt, with a trail warning); a fallback whose provider was deleted (skipped with a trail warning).
- Chains: the next rule sees the new provider and model, each rule fires once, and the depth limit.
- A rule created, edited, disabled and deleted between requests; an invalid CEL answered 400 on create and on update.
- The trail (`assertRoutingTrail`): the scope chain, the values a no-match saw, the target chosen; `routing_info.requested_*` keeps the caller's route.

**Model routers and the complexity router** (routing wiring; no steps yet):
- Each classifier picking each tier; its fallback chain when it is unavailable or below its confidence or similarity floor.
- A tier's targets and fallbacks; a tier with no target keeping the rule's choice; the first matching router winning; a router after a rule seeing the rule's model, and clearing the rule's key pin and first-token cutoff when it changes the provider.
- `complexity_tier` in a rule's CEL; the request types that classify and one that does not.
- Session tiers only rising, and a tool continuation reusing its turn's tier.
- The classifier's cost in `extra_fields.routing_metadata`; the model router's trail entries.
- The router and complexity config APIs: validation, defaults, reset and status. A suggestions change also shows that nothing routes differently until a suggested phrase is approved.

**The model catalog, key aliases and a model named without a provider** (catalog wiring for the read endpoints, routing wiring for the route):
- A provider or key added, updated, disabled, re-enabled and deleted, reaching `/api/models` and `/api/models/details`, and the next request's route.
- Aliases: exact, case-insensitive and regex; one alias on two keys, each resolving its own target; the resolved name routed directly refused when only the alias is allowed; allow-lists and blacklists written in alias names; `routing_info.resolved_key_alias`.
- A bare model resolved through the catalog: the candidates, the integration route's own provider preferred, the others as inferred fallbacks, which stop after a caller-fault error but not after no key supporting the model.
- Discovery failing or disabled keeping the allow-listed models; a keyless provider; the base model list unchanged by key edits.
- The catalog bounding a virtual key's `["*"]`.

**Virtual key routing and governance load balancing** (routing wiring):
- Weighted, zero and allow-list-only configs; `allowed_models` as `["*"]`, explicit names and `[]`; blacklisted models; `key_ids`.
- A bare model balanced across the providers that can serve it (`providerDistribution`), skipping one that cannot (a key gate, a blacklist, a disabled key) even at the highest weight; the auto-attached fallbacks.
- A caller-named provider skipping the balancing.
- A candidate over its budget or rate limit dropping out of the pick, and 402 or 429 when nothing else can serve.
- Each refusal's exact status and message: 401 when a key is required, 403 for a blocked provider or model, 400 when no key can serve the model.
- An update applying to the next request; weight validation on update.

**Key selection, key pins and direct keys** (routing wiring; direct keys need steps):
- A pool of two or more keys: the share under weights (`keyDistribution`, membership, never an exact split), a disabled key, a key whose `models` or blacklist excludes the request; the errors when nothing is left (400 no key supports the model, 503 no eligible keys, 502 every key's credential refused).
- A pin by ID and by name, and an unknown pin's error; a rule's pin winning over the caller's; a fallback's own `key_id`.
- Batch and file requests using only batch API keys.
- A direct key with the server setting on and off: it skips the pool and the virtual key's key filter, is held to the literal model name, is dropped on a fallback, and a provider refusing it returns that provider's error whatever `max_retries` is.

**Session affinity** (routing wiring; no steps yet):
- The session headers, the TTL and `x-bf-session-affinity` off.
- A bound route moved to the front of the chain only when the caller named no provider; a caller key pin on the head provider blocking the move; a stale binding dropped.
- The key binding: pools of two or more keys, the first attempt only, never realtime, batch or files.
- What binds: the route that served; a followed binding that failed is dropped; a cancellation keeps bindings; a fallback served with a direct key binds nothing.
- A session over a rule's pick and over a virtual key's pick.

**Fallbacks, retries and upstream errors** (routing wiring; errors come from the scripted upstream, `providerType: "fixture"`):
- Each fallback source, and which one replaces which.
- Tried in order until one serves; one the virtual key refuses; a provider with no config skipped; `is_fallback` and `primary_*`; the fallback's own log row (`assertFallbackRow`).
- An exhausted chain returning the primary's error and headers with `fallback_attempts`, on `/v1` and on the SDK routes.
- Retries: a 429 rotating keys after its hint, a 5xx or a dropped connection retrying the same key, a refused key marked dead with an extra attempt, `max_retries`.
- A stream dropped mid-way and a slow first token against a first-token cutoff.

**An entry point or request type that reaches routing** (routing wiring):
- The change on each `/v1` route and stream it reaches and on the SDK routes that carry it, with the route in the scenario title and the shape a real client on that route sends.
- Passthrough, batch and files, and realtime where the change reaches them, each with what it skips.

**`routing_info`, the routing headers and the routing log** (routing wiring):
- Every field the change touches, set on a success and on errors after a route is chosen, and absent on errors before a route is chosen; the `x-bifrost-routing-info-*` headers agreeing with the body (`expectHeaderProviderRef`); the log row's key, virtual key and fallback index (`assertLog`); `routing_engine_logs` (`assertRoutingTrail`).

**The routing management APIs**: routing rules, model routers, the complexity config (routing wiring):
- Each invalid field's exact 400 and each conflict's 409; create, read, update and delete round trip; the change reaching the next request; the same object from `config.json` reading back the same as one created through the API (needs runner support, section 6).

## 4. Writing a case

- **Generator and ID:** routing goes in `build-routing-wiring.mjs`, catalog reads in `build-model-catalog-wiring.mjs`. A scenario is `{ id, title, description, steps }`. The `id` is kebab-case and unique, and it names every run-scoped resource the scenario creates. The `title` is the Newman folder name; `collections/collection-scripts.test.mjs` finds some scenarios by their title, so renaming one means updating that test.
- **Routing wiring steps:** `addProvider` (`ref`, `providerType` from `PROVIDERS`, `providerKind: "standard"`, keys built with `key()`; `providerType: "fixture"` is the scripted upstream, whose behaviours a key reaches through an alias such as `upstream-520-once~<tag>-{{run_id}}`), `createVK` (configs built with `vkProvider()`), `updateVK`, `assertVKWeight`, `createRule` (`cel`, `targets`, `fallbacks`), `updateKey`, `deleteProvider`, `route` (`model`, `bareModel`, `routeRef`, `useVk`, `pinKey`, `fallbacks`, `endpoint`, `requestId`, and the `expect*` checks), `keyDistribution`, `providerDistribution`, `assertLog`, `assertFallbackRow`, `assertRoutingTrail`, `aliasPricing`, `assertUpstreamHits` (the attempts the scripted upstream counted for one model), and `cleanup` last. A scenario has at most one virtual key and one rule, and rules are global today.
- **Catalog wiring steps:** `addProvider`, `addKey`, `updateKey`, `deleteKey`, `deleteProvider`, `assertModels`, `assertModelDetails`, `captureModel`, `assertProviders`, `assertBaseModels`, `assertInference`. A contract in `scenariosFor` runs once for each entry in its `PROVIDERS`; one in `GLOBAL_SCENARIOS` runs once.
- **Sent once, judged once:** `route` and the distribution steps are sent once and judged once. Before the first of them, one step per newly added provider polls until the catalog lists its models. Only catalog, log, trail and pricing reads poll, so a route that shows up only on a retry fails. A new step that sends inference joins `inferenceSteps`.
- **Names:** providers are `catwiring-rt-<id>[-<ref>]-{{run_id}}`. A model a rule's CEL matches is a run-scoped alias (`catwiring-<tag>-{{run_id}}`), so the rule never sees another run's traffic. A standard provider (`providerKind: "standard"`, and Azure, Bedrock and Vertex always) is the server's global one, which is why the runners boot a clean server.
- **Teardown** is generated: the rule, the virtual key, then every provider the scenario added; a failing step jumps to it. A new step that creates something adds its delete there.
- **Credentials:** keys are always `env.<NAME>`, never written into the collection. A new provider adds its entry to `PROVIDERS` and its variables to the README's list.
- **Assert facts, exactly:** the status, the type (`expectErrorType`), the provider and key, and the message (`expectErrorSubstr`, which is a substring match, so give enough of the message that a different error cannot contain it). Distributions assert membership (`expectOnly`, `expectNever`, `expectAll`, `expectKeyIds`), never exact splits. Never return early on an error status, and never assert a 500 as the expected answer: a case for an exhausted chain or for retries scripts its primary with the error fixture's `upstream-503` or a 4xx, so the primary's error it expects back is never a 500.
- **A known bug:** the generators have no skip. Add the case in the PR that fixes the bug, asserting the intended answer.
- **Red before green:** state in the report which assertion fails on the code before the change and why, so the user's run before and after the change confirms it.
- **Regenerate:** `node tests/e2e/api/runners/build-routing-wiring.mjs` and `node tests/e2e/api/runners/build-model-catalog-wiring.mjs` reproduce the committed files byte for byte on unchanged code, so `git diff` shows only your scenarios.

## 5. Running

Run these yourself:

```bash
# Go first: the packages the change touches
(cd plugins/routing && go test ./...)
(cd plugins/governance && go test ./...)
(cd plugins/modelcatalogresolver && go test ./...)
(cd framework && go test ./modelcatalog/... ./routing/...)
(cd core && go test .)

# the generators: regenerate, then read the diff and confirm every changed scenario is one you meant to change
node tests/e2e/api/runners/build-routing-wiring.mjs
node tests/e2e/api/runners/build-model-catalog-wiring.mjs
git diff tests/e2e/api/collections/

# the collection script tests (offline)
node tests/e2e/api/collections/collection-scripts.test.mjs
```

No Makefile recipe runs these packages' tests by name (`make test-governance` runs the separate `tests/governance` module, not `plugins/governance`), so run them with `go test` as written; the Makefile-only rule in `resolve-pr-comments` is about provider tests. Report a `--- SKIP` as skipped, never as passed: the governance package's Postgres tests skip themselves without a local Postgres.

The live runs are the user's to trigger: end the report with the ones the change reaches, every placeholder filled in. Each runner needs Bash 4 or later (on macOS, Homebrew's bash), a free port, and these keys in the shell it starts from, since the server it boots inherits them: `OPENAI_API_KEY`, `ANTHROPIC_API_KEY` and `GEMINI_API_KEY` for both, plus `AZURE_API_KEY`, `AZURE_ENDPOINT`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_REGION`, `VERTEX_PROJECT_ID`, `VERTEX_PROJECT_NUMBER` and `VERTEX_CREDENTIALS` for the routing wiring.

```bash
# build from the local modules: a plain `make build` sets GOWORK=off and uses the published core, framework and plugins
[ -f go.work ] || make setup-workspace
make build LOCAL=1
lsof -nP -iTCP:<port> -sTCP:LISTEN
tests/e2e/api/runners/individual/run-newman-routing-wiring-tests.sh --binary tmp/bifrost-http --port <port>
tests/e2e/api/runners/individual/run-newman-model-catalog-wiring-tests.sh --binary tmp/bifrost-http --port <port>
```

The runners take no folder filter, so each runs its whole collection. Report the scenarios you added or changed, the runs the user should start, and the provider-harness run for any existing routing folder the change touched.

## 6. Adding a new value to an axis

A new entry point, request type, routing header, rule scope, CEL input, target field, fallback source, classifier, key field, `routing_info` field or provider is a new value on one of the axes in section 2. Its own new scenarios are not enough:

1. **Add it to every existing scenario that enumerates the axis.** Grep for an existing value of the axis to find them: an entry point in the `route` step's `endpoint` map and the scenarios that run on more than one route (`exhausted-fallback-headers`); a provider in both generators' `PROVIDERS` (the catalog contracts run once per entry); a `routing_info` field in `routeAssertLines`; a log field in `logAssertLines`. A scenario that deliberately leaves the new value out says why in its `description`.
2. **Update this skill.** Add the value to its axis in section 2 and to every block in section 3 that names that axis. A new kind of change gets its own block in section 3. Keep the AGENTS.md rule and the review configs (`.coderabbit.yaml`, `.greptile/`) in step when the rule itself changes.
3. **Extend the generator when it cannot express the case, in the same PR.** Add a case to `expandScenario`'s switch (an unknown step type throws), built from the `lib/collection-builder.mjs` helpers: `request`, `item` and `events`, `singleTest` for a request sent once, `pollTest` for a read that waits, `mutationTest` and `cleanupTest`. Join `inferenceSteps` if it sends inference, add the teardown for what it creates, and pin script logic that is easy to get wrong with a test in `collections/collection-scripts.test.mjs`, as the alias collision, key split and `updateVK` tests do.
4. **Extend the runner when the clean server cannot produce the setup.** A scripted upstream (errors, retry hints, slow first tokens, dropped streams) is `runners/provider-error-fixture.mjs`, which answers by model name: the runner starts it and a custom provider's `base_url` points at it. A server setting or plugin config the clean server lacks (direct keys, a model router's classifier, a `config.json` object to read back) goes in the config `runners/lib/clean-bifrost.sh` writes, or is set through the management API inside the scenario.

Not covered today, so a change that reaches one of these extends the generator for it in the same PR: streams, `/v1/responses` beyond the `route` step's `endpoint: "responses"` (used by the 520 retry scenario only), `/v1/embeddings`, `/genai` and the other SDK routes, passthrough, batch and files, realtime; session affinity; direct keys; key pins and the `routing_info` checks on `/anthropic`; retries and upstream errors other than a 5xx retried on the same key; rule scopes other than global, priority, chains, rules edited, disabled or deleted mid-scenario, key-pinned targets, and more than one rule or virtual key; virtual key budgets and rate limits, teams and customers, `key_ids` on update; model routers and the complexity router; `deleteKey` in the routing wiring; anything loaded from `config.json`.
