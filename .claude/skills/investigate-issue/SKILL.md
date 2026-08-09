---
name: investigate-issue
description: Investigate a GitHub issue by fetching details, analyzing the codebase, researching documentation, and presenting an actionable implementation plan with test guidance. Use when asked to investigate, analyze, triage, or plan work for a GitHub issue. Invoked with /investigate-issue <ISSUE_ID> or /investigate-issue (prompts for ID).
allowed-tools: Read, Grep, Glob, Bash, WebSearch, WebFetch, mcp__context7__resolve-library-id, mcp__context7__query-docs, Task, AskUserQuestion, TodoWrite, Edit, Write
---

# Investigate GitHub Issue

Fetch a GitHub issue, analyze the report, search the codebase for relevant code, research external documentation, and present a comprehensive implementation plan with side-effect analysis and test guidance. 

**Your final report MUST contain all of these sections:**
1. Issue Details (from Step 1)
2. Classification (from Step 2)
3. Codebase Analysis + Documentation Research (from Step 3, including sub-step 3e)
4. Impact Analysis (from Step 4)
5. Test Plan (from Step 5)
6. Regression Rerun Scope (from Step 5e) -- which existing tests must rerun, and why
7. TDD: Failing Tests (from Step 5f) -- Bug issues only: the test source AND the actual
   red output from the applicable Makefile recipe, pasted verbatim, BEFORE asking to
   implement
8. Full Presentation (Step 6 template)

If any section is missing, go back and complete it before presenting the report.

For Bug issues the approval gate is NOT "may I write a test?" -- the failing test is already
written and shown to be red. The gate is "may I write the fix?"

## Usage

```
/investigate-issue <ISSUE_ID>           # Investigate issue by number
/investigate-issue                      # Prompts for issue ID
```

## Workflow Overview

1. **Get the issue** -- Fetch full issue details from GitHub
2. **Classify the issue** -- Determine type (bug, feature, docs) and affected areas
3. **Search the codebase and research docs** -- Find relevant code, then research the libraries it depends on via Context7 and WebSearch
4. **Analyze impact** -- Cross-reference codebase findings with documentation to identify side effects, dependencies, and breaking changes
5. **Suggest tests** -- If changes touch `core/`, recommend specific LLM and MCP test additions
5b. **Scope the reruns** -- Use coverage to determine which existing tests exercise the lines
    you plan to change, and tier them by necessity
5c. **Go red (Bug issues)** -- Write the regression test(s) and run them to confirm failure for
    the right reason, BEFORE presenting the plan
6. **Present the plan** -- Show findings, the red test output, the rerun scope, and the
   recommended fix
7. **Implement with approval** -- After approval, apply the fix and show red-to-green

## Step 1: Fetch the Issue

If no issue ID is provided, ask the user:
```
What is the GitHub issue number you want to investigate?
```

The repository is always `maximhq/bifrost`.

Fetch full issue details:
```bash
# Get issue with all metadata
gh issue view <ISSUE_ID> --repo maximhq/bifrost --json number,title,body,labels,assignees,state,comments,author,createdAt,updatedAt

# Get issue comments for additional context
gh issue view <ISSUE_ID> --repo maximhq/bifrost --json comments --jq '.comments[].body'
```

If the issue does not exist or `gh` fails:
- Check authentication: `gh auth status`
- Verify the issue number is valid: `gh issue list --repo maximhq/bifrost --limit 5 --json number,title`
- Report the error and ask the user for a corrected issue ID

## Step 2: Classify the Issue

### 2a. Determine Issue Type

Parse the issue title prefix and labels to classify:

| Title Prefix | Label | Type | Investigation Focus |
|---|---|---|---|
| `[Bug]:` | `bug` | Bug | Reproduce, find root cause, identify fix |
| `[Feature]:` | `enhancement` | Feature | Design approach, find insertion points |
| `[Docs]:` | `documentation` | Docs | Find affected doc pages, verify accuracy |
| (none) | (any) | General | Read body carefully, infer type from content |

### 2b. Determine Affected Areas

Use the issue's labels and body content to map to codebase areas. The issue templates include an "Affected area(s)" field with these values:

| Area Label | Codebase Directories | Key Files |
|---|---|---|
| Core (Go) | `core/`, `core/schemas/`, `core/providers/`, `core/mcp/` | `core/bifrost.go`, `core/utils.go` |
| Framework | `framework/`, `framework/configstore/`, `framework/logstore/` | `framework/config.go`, `framework/list.go` |
| Transports (HTTP) | `transports/bifrost-http/` | `transports/bifrost-http/` |
| Plugins | `plugins/` (governance, jsonparser, litellmcompat, etc.) | Plugin-specific `go.mod` files |
| UI (React) | `ui/`, `ui/app/workspace/`, `ui/components/` | Feature-specific workspace pages |
| Docs | `docs/` | `docs/docs.json`, feature-specific `.mdx` files |

If the issue body mentions specific providers (e.g., "openai", "anthropic", "gemini"), also search:
```bash
ls core/providers/
# Maps to: core/providers/<provider_name>/
```

If the issue mentions MCP, agents, or tools, also search:
```bash
ls core/mcp/
# Key: core/mcp/agent.go, core/mcp/codemode.go, core/mcp/toolmanager.go
```

### 2c. Extract Key Information

From the issue body, extract and summarize:
- **What is reported** -- The specific problem or request
- **Reproduction steps** -- If bug, how to trigger it
- **Expected vs actual behavior** -- What should happen vs what happens
- **Version info** -- Which version is affected
- **Environment details** -- OS, Go version, Node version, etc.
- **Code snippets** -- Any code the reporter included
- **Error messages** -- Stack traces, logs, error output

## Step 3: Search the Codebase

Systematically search for all code relevant to the issue. Use multiple search strategies:

### 3a. Keyword Search

Extract key terms from the issue and search:
```bash
# Search for error messages mentioned in the issue
grep -rn "exact error message" core/ framework/ transports/ ui/

# Search for function/type names mentioned
grep -rn "FunctionName\|TypeName" --include='*.go' core/
grep -rn "componentName\|functionName" --include='*.ts' --include='*.tsx' ui/

# Search for API endpoints mentioned
grep -rn "/api/endpoint" transports/ ui/
```

### 3b. Structural Search by Area

Based on the affected area, do targeted exploration:

**For Core (Go) issues:**
```bash
# Find the specific provider if mentioned
ls core/providers/<provider>/
grep -rn "relevantFunction" core/providers/<provider>/

# Check schemas for relevant types
grep -rn "TypeName" core/schemas/ --include='*.go'

# Check the main bifrost.go for relevant handlers
grep -n "functionName\|handlerName" core/bifrost.go
```

**For MCP/Agent issues:**
```bash
# Search agent code
grep -rn "keyword" core/mcp/ --include='*.go'

# Check codemode if relevant
ls core/mcp/codemode/
grep -rn "keyword" core/mcp/codemode/ --include='*.go'
```

**For UI issues:**
```bash
# Find the workspace page
ls ui/app/workspace/<feature>/

# Search for components
grep -rn "keyword" ui/app/workspace/<feature>/ --include='*.tsx' --include='*.ts'
grep -rn "keyword" ui/components/ --include='*.tsx' --include='*.ts'

# Check for data-testid attributes (relevant for E2E impact)
grep -rn 'data-testid' ui/app/workspace/<feature>/ --include='*.tsx'
```

**For Framework issues:**
```bash
grep -rn "keyword" framework/ --include='*.go'
ls framework/configstore/ framework/logstore/ framework/plugins/
```

**For Plugin issues:**
```bash
# Identify which plugin
ls plugins/
grep -rn "keyword" plugins/<plugin_name>/ --include='*.go'
```

**For Docs issues:**
```bash
# Find the affected documentation page
find docs/ -name "*.mdx" | head -30
grep -rn "keyword" docs/ --include='*.mdx'
```

### 3c. Dependency Tracing

For any function or type identified as needing changes, trace its callers and dependents:
```bash
# Find all callers of a function
grep -rn "FunctionName(" --include='*.go' core/ framework/ transports/ plugins/

# Find all implementations of an interface
grep -rn "InterfaceName" --include='*.go' core/ framework/

# Find all imports of a package
grep -rn '"github.com/maximhq/bifrost/core/schemas"' --include='*.go' .
```

### 3d. Find Related Tests

```bash
# Find existing tests for the affected code
grep -rn "TestFunctionName\|Test.*Relevant" --include='*_test.go' core/ framework/ transports/

# Check LLM tests
grep -rn "keyword" core/internal/llmtests/ --include='*.go'

# Check MCP tests
grep -rn "keyword" core/internal/mcptests/ --include='*_test.go'

# Check E2E tests if UI is affected
grep -rn "keyword" tests/e2e/ --include='*.ts'
```

### 3e. Research External Documentation

Now that you've found the relevant code, research the external libraries it depends on. This informs the impact analysis in Step 4.

**Identify libraries from the code you found:**
```bash
# Check go.mod for dependencies relevant to the issue
cat core/go.mod | grep -v "^$" | grep -v "//"

# For UI issues, check package.json
cat ui/package.json | jq '.dependencies, .devDependencies'
```

**Query Context7 for each relevant library:**
```
# Step 1: Resolve the library ID
mcp__context7__resolve-library-id(
  libraryName: "<library name from go.mod or package.json>",
  query: "<issue title + key terms>"
)

# Step 2: Query the docs with the resolved ID
mcp__context7__query-docs(
  libraryId: "<result from step 1>",
  query: "<specific question about behavior relevant to the issue>"
)
```

Common libraries: `mark3labs/mcp-go` (MCP protocol), `stretchr/testify` (test assertions), `react` (UI framework), `playwright` (E2E testing), provider SDKs (OpenAI, Anthropic, etc.)

**Search the web for additional context:**
```
WebSearch: "<error message or behavior from the issue> <library name>"
WebSearch: "<library name> best practices <relevant pattern>"
```

**Record your findings in this table** (you will copy it into the final report in Step 6):

| Source | Query Used | Key Finding | Link |
|--------|-----------|-------------|------|
| Context7: `<library>` | `<query>` | `<what was learned, or "No relevant docs found">` | `<link if available>` |
| WebSearch | `<query>` | `<what was learned, or "No relevant results">` | `<URL>` |

If no useful results are found for a library, still include a row with "No relevant documentation found" and note what was searched. This transparency helps the user understand research coverage.

## Step 4: Analyze Impact

Using both the codebase search results (Step 3a-3d) and the documentation research (Step 3e), analyze the impact of the proposed changes. For each file, note whether library documentation revealed any constraints or best practices that affect the implementation.

### 4a. Direct Changes Required

For each file that needs modification, document:
- **File path** (absolute)
- **What to change** (function, type, handler, component)
- **Why** (ties back to the issue)
- **How** (specific approach -- add parameter, modify logic, new function, etc.)
- **Library constraints** (from Step 3e research -- any API contracts, deprecations, or version requirements)

### 4b. Side Effects Analysis

For each proposed change, trace the blast radius:

**Code side effects:**
```bash
# Who calls this function?
grep -rn "FunctionToChange(" --include='*.go' core/ framework/ transports/ plugins/

# Who uses this type?
grep -rn "TypeToChange" --include='*.go' core/ framework/ transports/ plugins/

# What tests exercise this code?
grep -rn "FunctionToChange\|TestRelated" --include='*_test.go' core/ framework/ transports/
```

**Schema side effects (if changing types in core/schemas/):**
- Check all providers that implement the schema
- Check framework code that serializes/deserializes the type
- Check UI code that consumes the API response
- Check plugin code that uses the schema

**API side effects (if changing endpoints):**
- Check all UI pages that call the endpoint
- Check E2E tests that hit the endpoint
- Check documentation that references the endpoint

**UI side effects (if changing components):**
- Check all pages that use the component
- Check E2E tests with selectors targeting the component
- Check if data-testid attributes change

### 4c. Breaking Change Assessment

Classify the change:
- **Non-breaking** -- Internal refactor, bug fix with same API surface
- **Minor breaking** -- New required parameter with default, deprecation
- **Major breaking** -- Changed API signature, removed field, behavioral change

## Step 5: Test Recommendations

### 5a. General Test Guidance

For ANY code change, identify:
- Existing tests that need updating
- New test cases that should be added
- Edge cases to cover

### 5b. LLM Tests (when changes touch `core/` or `core/providers/`)

The LLM test infrastructure lives in `core/internal/llmtests/`. Key patterns:

**Test structure:**
- Each scenario is a self-contained Go file with a `Run{Scenario}Test()` function
- Signature: `func Run{Scenario}Test(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig)`
- Tests run against both Chat Completions and Responses APIs (dual-API testing)

**How to add a new LLM test:**
1. Create a new file in `core/internal/llmtests/` named after the scenario (e.g., `new_scenario.go`)
2. Implement `RunNewScenarioTest()` following the signature pattern
3. Register it in `core/internal/llmtests/tests.go` by adding to the `testScenarios` slice
4. Add a `Scenarios` flag in `ComprehensiveTestConfig` to enable/disable it
5. Use `validation_presets.go` expectations (e.g., `BasicChatExpectations()`, `ToolCallExpectations()`)
6. Use the retry framework from `test_retry_framework.go` for validation failures

**Example test recommendation format:**
```
New LLM test: Run{X}Test in core/internal/llmtests/{x}.go
- Scenario: <what it tests>
- Expectations: <which validation preset to use>
- Dual-API: Yes, test both Chat Completions and Responses API paths
- Register in: tests.go testScenarios slice
- Config flag: Scenarios.{X} in ComprehensiveTestConfig
```

**Running LLM tests:**
```bash
make test-core PROVIDER=<provider> TESTCASE=<TestName>
make test-core PROVIDER=<provider> PATTERN=<substring>
```

### 5c. MCP Tests (when changes touch `core/mcp/`)

The MCP test infrastructure lives in `core/internal/mcptests/`. Key patterns:

**Test structure:**
- Standard Go test functions: `func TestScenario(t *testing.T)`
- Setup via `SetupAgentTest(t, AgentTestConfig{...})` which returns `(manager, mocker, ctx)`
- Mock LLM responses via `DynamicLLMMocker` with a response queue
- Agent tests use `MockLLMCaller` with pre-defined response sequences

**How to add a new MCP test:**
1. Identify which test file category it belongs to:
   - `agent_*_test.go` -- Agent loop behavior tests
   - `tool_*_test.go` -- Tool execution tests
   - `connection_*_test.go` -- Client connection tests
   - `codemode_*_test.go` -- CodeMode-specific tests
2. Create the test function following existing patterns
3. Use `AgentTestConfig` for declarative setup:
   ```go
   manager, mocker, ctx := SetupAgentTest(t, AgentTestConfig{
       InProcessTools:   []string{"echo", "calculator"},
       AutoExecuteTools: []string{"*"},
       MaxDepth:         5,
   })
   ```
4. Use assertion helpers: `AssertAgentCompletedInTurns()`, `AssertToolExecutedInTurn()`
5. Use fixture helpers from `fixtures.go` for mock responses

**Example test recommendation format:**
```
New MCP test: Test{X} in core/internal/mcptests/{category}_test.go
- Category: agent | tool | connection | codemode
- Setup: AgentTestConfig with {tools} and {config}
- Mock responses: {describe the LLM response sequence}
- Assertions: {what to verify}
```

**Running MCP tests:**
```bash
make test-mcp TESTCASE=<TestName>
make test-mcp TYPE=<category> PATTERN=<substring>
```

### 5d. E2E Tests (when changes touch `ui/`)

If UI changes are involved, recommend E2E test updates following the patterns in `tests/e2e/`:
- Page objects in `tests/e2e/features/<feature>/pages/`
- Specs in `tests/e2e/features/<feature>/`
- Reference the `/e2e-test` skill for full E2E test creation workflow
- **Never marshal API payloads to a `Record`/`Map`** — construct payloads as object literals with fields in the intended order and pass directly to Playwright's `request.post({ data })`. Marshaling through intermediate maps can reorder fields, breaking backend validation and snapshot comparisons.

```bash
make run-e2e FLOW=<feature>
```

### 5e. Determine Rerun Scope from Coverage

Do not guess which tests are affected -- measure it. Package-level coverage is too coarse:
it proves the package is tested, not that any test executes the specific lines you are
changing. Attribute coverage per test.

**Step 1 -- Establish the changed-line set.** For each file in the Implementation Plan, list
the functions/methods you will modify AND the line ranges within them. Step 3 filters the
coverage profile by line range, so symbol names alone are not enough.

**Step 2 -- Find candidate tests.** Every test in the changed file's package, plus every test
in the packages of its callers (from the Step 4b `grep` for callers):

Enumerate by PACKAGE, not by file or by symbol name. All `_test.go` files in a directory
compile into one test binary, and many tests reach the target through helpers or fixtures
rather than naming it -- so a textual grep for `<TargetSymbol>` under-reports badly (in
`core/mcp`, 60 test functions exist but only one file names `MCPManager`).

```bash
# 1. Changed file's own package -- every test file, not just the matching one
grep -hn "^func Test" <pkg>/*_test.go

# 2. Caller packages -- map Step 4b's caller hits to directories, then enumerate each
grep -rl "<TargetSymbol>" --include='*.go' core/ framework/ transports/ plugins/ \
  | xargs -n1 dirname | sort -u \
  | while read -r d; do grep -Hn "^func Test" "$d"/*_test.go 2>/dev/null; done
```

Over-inclusion is fine here -- Step 3 narrows the list by measurement. Omission is not:
a dropped candidate never gets coverage-checked and silently disappears from the report.

**Step 3 -- Attribute coverage per test.** Run each candidate with its OWN profile and check
whether it actually reaches the target symbol. A profile from the whole package cannot tell
you this.

Coverage attribution is the ONE place raw `go test` is allowed: no Makefile recipe accepts
`-coverprofile`. This profile is a measurement tool only -- it is never the red/green
evidence in the report. Red and green validation always runs through the Make target
(Step 5f).

```bash
cd <module>
# -coverpkg is REQUIRED: by default a test only instruments its OWN package, so a
# caller-package test would show zero blocks for the target and be wrongly dropped.
go test ./<candidate-pkg>/... -run '^<TestName>$' -covermode=count \
  -coverpkg=./... -coverprofile=<scratchpad>/cover-<TestName>.out

# Filter the raw profile to the changed line range. Do NOT use `go tool cover -func`:
# it reports a per-FUNCTION percentage, so a test covering another branch of the same
# function reads as "covered" while your changed line has count=0.
awk -v file="<changed-file>" -v lo=<startLine> -v hi=<endLine> 'NR>1 {
  split($1, a, ":"); split(a[2], r, ","); split(r[1], s, "."); split(r[2], e, ".");
  if (a[1] ~ file && s[1] <= hi && e[1] >= lo)
    printf "  lines %s-%s  count=%s\n", s[1], e[1], $3
}' <scratchpad>/cover-<TestName>.out
```

Profile line grammar: `<import-path>/file.go:startLine.startCol,endLine.endCol numStmt count`.
Pass `<changed-file>` to `file` exactly as it appears in the diff, with no extra extension:
`~` is a regex match against that import-path-qualified field, so a repo-relative path such as
`bifrost-http/lib/streamreader.go` is the right granularity. A bare `streamreader.go` would
also match a same-named file in any other package `-coverpkg=./...` pulled in, and a doubled
extension (`streamreader.go.go`) matches nothing -- which is indistinguishable from "no test
covers this change" and silently drops every required rerun. The last
field is the execution count. A non-zero count on ANY block overlapping your changed range
means that test executes the code you are changing -- it is a required rerun. All-zero (or
no overlapping block) means it does not.

For UI changes, use the framework's own dependency graph instead of Go coverage:
```bash
cd ui && ./node_modules/.bin/vitest related <changed-file> --run
```
Use the lockfile-pinned binary, not `npx vitest`: with a non-TTY stdin (which is every agent
run) npm assumes `--yes` and silently installs whatever version the registry currently calls
latest when `ui/node_modules` is missing, bypassing `ui/package-lock.json`.

**Step 4 -- Tier the results.** Report reruns in three tiers so the user can see what is
mandatory versus precautionary:

| Tier | Definition | Obligation |
|------|-----------|------------|
| **Tier 1 -- Must rerun** | Coverage shows the test executes a changed line | Blocking. A failure here is caused by your change |
| **Tier 2 -- Should rerun** | Same package, or asserts a contract your change touches, but does not cover the changed line | Run before handing off. Cheap and catches contract drift |
| **Tier 3 -- Downstream** | Tests in packages of callers/dependents identified in Step 4b | Run if Tier 1 or 2 revealed anything, or if the change altered an exported signature or shared invariant |

Explicitly list any test you are NOT rerunning and why (e.g. requires live provider
credentials, unrelated subsystem). Silent omission reads as "everything passed."

Note that provider-gated suites (`make test-core PROVIDER=...`) hit live APIs. If a Tier 1
test is provider-gated, say so -- the user decides whether to spend the call.

### 5f. Write and Run the Failing Tests -- Red (Bug issues only)

For issues classified **Bug** in Step 2a, write the regression test(s) and confirm red
*before* presenting the plan. This is a change from asking permission first: a plan whose
test has not been run is a hypothesis, and reviewers cannot distinguish a real reproduction
from a plausible one.

1. Write the test(s) from the Step 5 Test Plan, targeting the actual root cause from Step 3/4.
   Write them against the CURRENT code -- do not write the fix.
2. Run them with the correct Makefile recipe (never raw `go test` for suites that have one).
3. Confirm the failure is for the **expected reason**: the missing behavior, wrong bytes, or
   absent field. A compile error, typo, nil panic, or unrelated assertion is NOT a valid red
   -- fix the test and rerun until the failure is the real defect.
4. Capture the verbatim failure output. Do not paraphrase or summarize it.
5. **Redact before it leaves the scratchpad.** Provider-gated suites hit live APIs with real
   credentials (`OPENAI_API_KEY`, `AWS_SECRET_ACCESS_KEY`, and ~20 others), and upstream
   401/403 bodies echoed through `bifrostErr.Error.Message` can carry the key or auth header.
   Keep the raw output in `<scratchpad>/` -- never commit it, never paste it. Produce a
   redacted transcript for the report, replacing each secret with `[REDACTED: <what>]` so the
   removal is visible. Redaction is narrow: it MUST preserve the command, the failing
   assertion with actual-vs-expected values, and the stack trace. If redacting would remove
   the evidence itself, say so rather than trimming the assertion.

If the test unexpectedly PASSES, stop. The diagnosis in Step 3/4 is wrong or incomplete.
Return to Step 3 and say so plainly in the report rather than adjusting the test until it
fails.

Creating test files is the one write action permitted before plan approval, because the test
is itself the evidence. Do not touch any non-test source file at this stage.

## Step 6: Present Findings

Present everything to the user in this structured format:

```
## Issue Investigation: #<ID> -- <Title>

### Issue Classification
- **Type:** Bug / Feature / Docs
- **Severity:** <from issue if present>
- **Affected areas:** <list>
- **Labels:** <list>

### Summary
<2-3 sentence summary of what the issue is about and what needs to happen>

### Codebase Analysis

#### Relevant Files Found
| File | Relevance | What Needs to Change |
|------|-----------|---------------------|
| `<absolute path>` | <why this file matters> | <specific change needed> |
| ... | ... | ... |

#### Current Behavior
<Describe what the code currently does, with code snippets>

#### Root Cause (for bugs) / Design Gap (for features)
<Explain why the issue exists>

### Documentation Research

Copy the research table from Step 3e here. If you skipped Step 3e, go back and do it now before presenting.

#### Libraries & References Consulted
| Source | Query Used | Key Finding | Link |
|--------|-----------|-------------|------|
| <from Step 3e table> | <query> | <finding> | <link> |

#### How Documentation Informs the Plan
<For each change above, note any library constraints, best practices, or API contracts discovered in Step 3e that shaped the approach. Reference specific Change numbers.>

### Implementation Plan

#### Changes Required (in order)

**Change 1: <File path>**
- **What:** <specific modification>
- **Why:** <ties to issue>
- **Function/Component:** `<name>`
- **Approach:** <how to implement>

**Change 2: <File path>**
- ...

#### Side Effects
| Change | Affected Code | Risk | Mitigation |
|--------|--------------|------|------------|
| Change 1 | `<caller/dependent>` | <risk level> | <how to mitigate> |

#### Breaking Changes
- <list any breaking changes, or "None expected">

### Test Plan

#### Existing Tests to Update
| Test | File | What to Change |
|------|------|---------------|
| `TestName` | `<path>` | <modification needed> |

#### New Tests to Add
| Test | File | What It Covers |
|------|------|---------------|
| `TestNewScenario` | `<path>` | <scenario description> |

### TDD: Failing Tests (Red)

<Bug issues only. If the issue is a Feature or Docs, write "N/A -- <type> issue, red-before-green
does not apply per AGENTS.md" and omit the rest of this section.>

#### Test Source
<The full source of each new/extended test, as written. Not a description -- the actual code.>

#### Red Output (verbatim, redacted)
```
<paste the failing test output, including the command that produced it. Secrets replaced
with [REDACTED: <what>] -- nothing else altered. Raw output stays in <scratchpad>/.>
```

#### Why This Failure Is the Real Defect
<Explain how the assertion that failed maps to the root cause in the Codebase Analysis. State
explicitly that this is not a compile error, typo, or unrelated panic.>

### Regression Rerun Scope

<From Step 5e. Coverage-attributed, not guessed.>

| Tier | Test | File | Covers Changed Line? | Why It Reruns |
|------|------|------|---------------------|---------------|
| 1 | `TestX` | `<path>` | Yes -- `<symbol>` count N | <what it guards> |
| 2 | `TestY` | `<path>` | No -- same package | <contract it asserts> |
| 3 | `TestZ` | `<path>` | No -- caller package | <downstream invariant> |

**Rerun commands:**
```bash
<exact commands, tier by tier>
```

**Not rerunning:** <list with reasons, or "None -- full scope covered above">

<If changes touch core/>
#### LLM Test Additions
<Specific LLM test recommendations per Section 5b format>

#### MCP Test Additions
<Specific MCP test recommendations per Section 5c format>
</If>

<If changes touch ui/>
#### E2E Test Additions
<Recommend using /e2e-test skill for full test creation>
</If>

### Estimated Complexity
- **Scope:** Small (1-2 files) / Medium (3-5 files) / Large (6+ files)
- **Risk:** Low / Medium / High

---

<If Bug>
The failing test above is committed to disk and confirmed red. No source file has been
modified.
</If>
<If Feature or Docs>
No source file has been modified. Red-before-green does not apply to <type> issues per
AGENTS.md; tests are written alongside the implementation in Step 7.
</If>

**Implement the fix now?** (yes / no / modify plan)
```

## Step 7: Implement with Per-Change Approval

Once the user approves the plan:

### 7a. Bug issues: the red already happened (per AGENTS.md "Testing" section)

For **Bug** issues the failing test was written and confirmed red in Step 5f, before the
approval gate. Step 7 therefore starts at the fix, not the test:

1. Apply the fix changes from the plan (Step 7c below).
2. Re-run the exact same command from the Step 6 "Red Output" block and confirm it now passes.
3. Present red-then-green as a pair: the command, the prior failure, the new pass.
4. Run the Tier 1 and Tier 2 reruns from the Regression Rerun Scope. Report every result,
   including any pre-existing failure unrelated to this change -- say so explicitly rather
   than omitting it.

Do not modify the test while implementing the fix. If the test needs to change to pass, the
diagnosis was wrong: stop and tell the user rather than editing the test to match the code.

This does not apply to Feature or Docs issues -- AGENTS.md scopes "red before green" to bug
fixes. For those, tests are added per the Test Plan after the implementation, and Step 5f is
skipped.

### 7b. Create a Todo List

Create a todo item for each change in the plan. For Bug issues the regression test already
exists and was confirmed red in Step 5f, before the approval gate -- so it enters the list
already completed and the first pending task is the fix. Do not re-write or modify it (see 7a):
```
1. Write failing test: <description> -- completed    (Bug issues only; confirmed red in Step 5f)
2. Change 1: <description> -- pending
3. Change 2: <description> -- pending
4. Update existing test: <description> -- pending
5. Verify all tests pass -- pending
```
For Feature and Docs issues, omit item 1 entirely: Step 5f is skipped for those, and the new
tests from the Test Plan are added after the implementation, as their own pending items.

### 7c. For Each Change

Before making any edit, present the change to the user:

```
## Change <N>/<Total>: <File path>

**What:** <description of the change>

**Current code:**
<existing code that will be modified>

**Proposed change:**
<new code after modification>

**Apply this change?** (yes / no / modify)
```

Wait for user approval before applying. If user says "no", skip and move to the next change. If user says "modify", discuss and adjust.

### 7d. After All Changes

Once all approved changes are applied:

1. Run relevant tests:
   ```bash
   # For core changes
   make test-core PROVIDER=<relevant_provider> PATTERN=<relevant_test>

   # For MCP changes
   make test-mcp PATTERN=<relevant_test>

   # For framework changes
   cd framework && go test ./...

   # For UI changes
   make run-e2e FLOW=<feature>
   ```

2. Report results to the user, including the red-then-green transcript/summary for Bug issues (what failed before, what passes now)
3. If tests fail, investigate and propose fixes (with approval)

## Error Handling

### Issue Not Found
```
Issue #<ID> was not found in maximhq/bifrost.
- Verify the issue number is correct
- Run: gh issue list --repo maximhq/bifrost --limit 10 --json number,title
```

### gh CLI Not Authenticated
```
GitHub CLI is not authenticated. Run:
  gh auth login
Then retry /investigate-issue <ID>
```

### No Relevant Code Found
If codebase search yields no results:
1. Broaden the search terms
2. Search for related concepts instead of exact matches
3. Ask the user for more context about where the code might live
4. Check if this is a net-new feature with no existing code

### External Documentation Not Found
If Context7 or WebSearch yield no useful results in Step 3e:
1. Still include a row in the research table with "No relevant documentation found" and note what was searched
2. Proceed with codebase analysis alone
3. Flag areas where documentation review might be needed before implementation

## Project Directory Reference

Quick reference for navigating the Bifrost codebase:

```
bifrost/
├── core/                          # Go core library
│   ├── bifrost.go                 # Main Bifrost implementation (~195K)
│   ├── schemas/                   # All Go types/schemas
│   ├── providers/                 # Provider implementations (openai, anthropic, gemini, etc.)
│   ├── mcp/                       # MCP protocol implementation
│   │   ├── agent.go               # Agent mode
│   │   ├── codemode/              # CodeMode (Starlark-based)
│   │   └── toolmanager.go         # Tool management
│   └── internal/
│       ├── llmtests/              # LLM integration tests (~48 files)
│       │   ├── setup.go           # Test initialization
│       │   ├── tests.go           # Test orchestrator (scenario registry)
│       │   ├── validation_presets.go  # Reusable expectations
│       │   └── test_retry_framework.go  # Retry logic
│       └── mcptests/              # MCP/Agent tests (~40 files)
│           ├── setup_test.go      # Test infrastructure
│           ├── agent_test_helpers.go  # AgentTestConfig + SetupAgentTest
│           └── fixtures.go        # Mock servers & fixtures
├── framework/                     # Framework layer
│   ├── configstore/               # Configuration storage
│   ├── logstore/                  # Log storage
│   ├── plugins/                   # Plugin system
│   └── streaming/                 # Streaming utilities
├── transports/
│   └── bifrost-http/              # HTTP transport + Docker
├── ui/                            # React + Vite UI
│   ├── app/workspace/             # Feature pages
│   └── components/                # Shared components
├── plugins/                       # Go plugins (governance, otel, etc.)
├── docs/                          # Mintlify documentation
├── tests/e2e/                     # Playwright E2E tests
└── Makefile                       # Build & test commands
```

## Makefile Test Commands Reference 
**FOLLOW THIS EXACTLY TO RUN TESTS**

```bash
make test-core PROVIDER=<name>              # Run core tests for a provider
make test-core PROVIDER=<name> TESTCASE=<X> # Run specific test
make test-core PROVIDER=<name> PATTERN=<X>  # Run tests matching pattern
make test-mcp                               # Run all MCP tests
make test-mcp TYPE=<category>               # Run MCP tests by category
make test-mcp TESTCASE=<TestName>           # Run specific MCP test
make run-e2e FLOW=<feature>                 # Run E2E tests for feature
make run-e2e                                # Run all E2E tests
```
