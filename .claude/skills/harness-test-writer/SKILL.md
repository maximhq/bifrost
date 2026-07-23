---
name: harness-test-writer
description: Add regression test cases to the Bifrost provider harness (the Postman collection run via `make run-provider-harness-test`) based on a merged PR or a GitHub issue. Fetches the PR/issue, traces the affected wire path in the codebase, checks existing harness coverage, designs cases following harness conventions, inserts them into tests/e2e/api/collections/provider-harness.json without reformatting the file, and validates via the augment and filter scripts. Invoked with /harness-test-writer <PR# | issue# | URL> or /harness-test-writer (prompts for a reference).
allowed-tools: Read, Grep, Glob, Bash, WebFetch, Task, AskUserQuestion, TodoWrite, Edit, Write
---

# Provider Harness Test Writer

Turn a PR or GitHub issue into regression coverage in the provider harness: the Postman
collection at `tests/e2e/api/collections/provider-harness.json`, executed by newman via
`make run-provider-harness-test`.

This is NOT the Go test harness in `core/internal/llmtests/` (that one is run via
`make test-core PROVIDER=...`). If the user seems to want Go-level scenario tests,
confirm before proceeding.

## Step 1 - Resolve the reference

The argument may be a PR number, an issue number, or a GitHub URL.

- URL containing `/pull/` => PR. URL containing `/issues/` => issue.
- A bare number is ambiguous: try `gh pr view <N> --repo maximhq/bifrost` first; if it
  404s, try `gh issue view <N>`. If BOTH exist and refer to different things, ask the
  user which one they mean with AskUserQuestion.
- No argument at all => ask the user for the PR/issue reference.

Fetch full context:

```bash
gh pr view <N> --repo maximhq/bifrost --json title,body,state,files,baseRefName
gh pr diff <N> --repo maximhq/bifrost
# or
gh issue view <N> --repo maximhq/bifrost --json title,body,state,labels,comments
```

For a PR, also note any `Closes #X` issue and fetch that issue too - the issue usually
contains the client-visible reproduction (exact request shapes, error bodies, status
codes) that the harness case must mirror.

## Step 2 - Understand the wire-level behavior to pin

The harness tests Bifrost from the outside: HTTP requests against gateway routes, with
assertions on status codes and response/SSE bodies. Translate the PR/issue into that
frame:

1. Which route(s)? (`/v1/chat/completions`, `/v1/responses`, drop-ins like
   `/openai`, `/anthropic`, `/bedrock`, `/genai`, passthrough routes, ...)
2. Which provider(s) and model(s)?
3. What request shape triggers the bug/feature? Reconstruct it from the issue repro or
   from the code path in the diff (read the changed functions and their callers).
4. What is the observable failure signature before the fix (status code, error body
   substring) and the expected behavior after?

Trace the chain in code with Grep/Read until you can state it in one sentence, e.g.:
"`x-bf-compat` header -> compat plugin marks `ChangeRequestType=ResponsesRequest` ->
`ToResponsesRequest()` -> `ToOpenAIResponsesRequest` strips role -> OpenAI 200".

Useful switches the harness relies on:
- `x-bf-compat` header: `true` enables all compat features; a JSON array like
  `["convert_chat_to_responses"]` enables only specific ones (parsed in
  `transports/bifrost-http/lib/ctx.go`, consumed by `plugins/compat/main.go`). Prefer
  the targeted array form in regression cases so only the feature under test is active.
  Note: when the suite runs with `COMPAT=on`, a collection-level prerequest script
  upserts `x-bf-compat: true` over any per-request value - the targeted form matters
  for `COMPAT=off` runs, which is exactly when a forced-conversion case needs it.
- `x-bf-passthrough-extra-params: true` for passthrough extra-param cases.

## Step 3 - Check existing coverage

Search the collection for the feature's keywords before writing anything:

```bash
cd tests/e2e/api
grep -c '<keyword>' collections/provider-harness.json
node -e "
const c = require('./collections/provider-harness.json');
function walk(items, path) { for (const it of items) {
  if (it.item) walk(it.item, path + '/' + it.name);
  else if (JSON.stringify(it).includes('<keyword>')) console.log(path + ' :: ' + it.name);
} }
walk(c.item, '');"
```

Also skim `HARNESS_COVERAGE_BACKLOG.md` - if the gap is listed there as `[ ]`, flip it
to `[x]` as part of the change (and if you find adjacent gaps worth noting, leave them,
do not scope-creep).

If coverage already exists, report where and stop - do not add duplicates.

## Step 4 - Design the cases

Conventions (match the existing collection exactly):

- **Folder**: issue-pinned regressions get their own top-level folder named
  `<N>. <Short Title> (#<issue> / PR #<pr>)` where `<N>` is the next unused top-level
  number (folders 17, 18, 19 are prior examples). Give the folder a `description`
  explaining the bug, the production path, and what each case pins.
- **Case names** must contain the provider keyword and model so
  `runners/filter-collection.mjs` PROVIDER filtering catches them - check
  `PROVIDER_KEYWORDS` in that file (e.g. openai matches "openai", "gpt-"). Suffix each
  name with ` - #<issue>`.
- **Coverage shape**: typically 2-3 cases - the real production route (non-streaming),
  a streaming variant if the route streams, and where applicable a second route that
  pins the same invariant independently (e.g. native `/v1/responses` alongside the
  converted `/v1/chat/completions` path).
- **Test scripts** (Postman `event[].script.exec`, plain ES5 JavaScript):
  - Start with an infra guard so auth/rate/server noise skips instead of false-failing:
    `if ([401, 403, 429, 500, 502, 503, 504].indexOf(pm.response.code) !== -1) { return; }`
    Do NOT guard on 400 when a 400 IS the regression signature - that must fail loudly.
  - Assert the specific failure signature is absent (error substring, param name) AND
    that the happy path succeeded (status below 400, expected fields present).
  - Include the response text in failure messages:
    `pm.expect(pm.response.code, 'failed: ' + pm.response.text()).to.be.below(400);`
- **Variables**: use `{{baseUrl}}` for the gateway. Inline `provider/model` strings
  (e.g. `openai/gpt-4o-mini`) like the cross-cut folders do; only use variables such as
  `{{bedrockModel}}`, `{{genaiModel}}`, `{{vertexModel}}` where existing folders do.
- Keep request bodies minimal and cheap (small `max_tokens`/`max_output_tokens`,
  gpt-4o-mini-class models) - the harness runs as a paid live sweep.

Present the designed cases (names, route, body, assertions) to the user for approval
before editing the collection.

## Step 5 - Insert without reformatting

CRITICAL: `provider-harness.json` is ~1.5MB and is NOT byte-stable under
`JSON.stringify(JSON.parse(raw), null, 2)` (escape differences). Never rewrite the
whole file - the diff must contain only your added lines.

Use a Node script that appends the new folder textually before the closing `]` of the
top-level `item` array:

```js
const raw = fs.readFileSync(PATH, 'utf8');
const before = JSON.parse(raw);
// abort if the folder already exists (idempotence)
const indented = JSON.stringify(folder, null, 2).split('\n').map(l => '    ' + l).join('\n');
const tail = '\n  ]\n}';
if (!raw.endsWith(tail)) throw new Error('unexpected file tail');
const out = raw.slice(0, -tail.length) + ',\n' + indented + tail;
const after = JSON.parse(out); // must parse; item count must be before + 1
fs.writeFileSync(PATH, out);
```

Adding cases INSIDE an existing folder is harder to do textually; if that is truly the
right placement, locate the folder's closing bracket precisely and verify the diff is
additions-only afterward. Default to a new top-level folder for issue regressions.

## Step 6 - Validate

All from `tests/e2e/api/`:

```bash
git diff --stat   # must show only additions in provider-harness.json (+ backlog md if touched)
node runners/augment-provider-harness.mjs --source collections/provider-harness.json --out /tmp/aug.json
node runners/filter-collection.mjs --source /tmp/aug.json --out /tmp/filtered.json --provider <provider>
node -e "const c=require('/tmp/filtered.json'); const f=c.item.find(i=>i.name.startsWith('<N>.')); console.log('kept:', !!f, f && f.item.length);"
```

The augment script only regenerates its own "(generated)" folders, so a new top-level
folder passes through untouched - but run it anyway to catch parse breakage.

Do NOT auto-run the live suite: it starts a gateway and makes paid provider calls.
Report the run command and offer to execute it:

```bash
make run-provider-harness-test PROVIDER=<provider> FEATURE="<distinctive keyword from your case names>"
```

## Step 7 - Report

Summarize: what the PR/issue changed, the coverage gap found (cite evidence, e.g.
"tool_call_id appeared zero times in the collection"), each added case and what it
pins, validation results, and the run command. Leave the change unstaged - never
commit.
