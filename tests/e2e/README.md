# Bifrost E2E Tests

End-to-end tests for the Bifrost UI using Playwright.

## Running Tests

```bash
make run-e2e-ui                                   # whole suite on 4 workers
make run-e2e-ui WORKERS=6                         # more parallelism
make run-e2e-ui FLOW=providers,virtual-keys       # only these feature folders
make run-e2e-ui RERUN=tests/e2e/reports/e2e-results.json   # only what failed last time
make run-e2e-ui SKIP_BUILD=1                      # reuse tmp/e2e/bifrost-http (no UI/Go rebuild)
make run-e2e-ui KEEP=1                            # leave the servers running afterwards to poke at
make run-e2e-ui INTERACTIVE=1                     # Playwright UI mode against fresh servers
make run-e2e-headed FLOW=config                   # visible browsers
make e2e-ui-down                                  # stop KEEP=1 servers and the e2e Postgres
```

`run-e2e` is an alias of `run-e2e-ui`. Secrets (`OPENAI_API_KEY`, `ANTHROPIC_API_KEY`) come from `.env` or Infisical, same as the other targets.

### How a run works

`scripts/run-e2e.mjs` does everything; Playwright never starts servers itself.

1. Builds the UI and `tmp/e2e/bifrost-http` from the working tree.
2. Starts a **dedicated Postgres** (`env/docker-compose.yml`, compose project `bifrost-e2e`, `127.0.0.1:55432`, tmpfs). Your local Postgres on 5432 is never touched. Override the port with `BIFROST_E2E_PG_PORT`.
3. **Schedules** the selected spec files onto `WORKERS` workers: longest file first, each to the currently lightest worker. Weights are the per-file durations the last full run wrote to `reports/e2e-timings.json`; files without a timing are estimated from their test count. The plan is printed and saved to `tmp/e2e/plan.json`.
4. Boots one Bifrost per worker (ports from 18181, override with `BIFROST_E2E_BASE_PORT`), each on its own freshly created database seeded from **`env/config.json`**: providers, the `TestClient001` MCP client, dashboard auth (`admin` / `bifrost-e2e-password`).
5. Seeds LLM logs into workers that got dashboard/logs specs (needs `OPENAI_API_KEY`).
6. Runs Playwright with one browser per worker. Each worker runs its files one at a time against its own server, so specs never share state across workers and there is nothing to group by hand.
7. Writes `reports/e2e-results.json` and prints the re-run command. The previous artifact is kept as `reports/e2e-results.prev.json`.

Server logs: `tmp/e2e/workers/<w>/bifrost.log`.

### Seed state

Put fixed preconditions in `env/config.json` (validated against `transports/config.schema.json`) instead of creating them through the API in setup code. Values can reference env vars with `env.NAME`. Specs should still create the entities they are *testing* through the UI.

### Failure artifact

`reports/e2e-results.json`:

```json
{
  "status": "failed",
  "totals": { "passed": 310, "failed": 2, "flaky": 1, "skipped": 12 },
  "rerun": {
    "command": "make run-e2e-ui RERUN=tests/e2e/reports/e2e-results.json",
    "targets": ["features/governance/governance.spec.ts:46"]
  },
  "failed": [{ "worker": "w2", "file": "...", "line": 46, "title": "...", "error": "...", "attachments": ["test-results/.../trace.zip"] }],
  "flaky": [],
  "errors": []
}
```

`errors` holds failures outside any test (a spec that fails to load, global setup). A failed run with no test targets cannot be replayed with `RERUN`, and neither can a failed login setup (`core/auth.setup.ts`), since the tests behind it were skipped rather than failed; the runner refuses both and asks for a full run. Only one run can use `tmp/e2e` at a time; a second one exits while the first is in progress.

### Against a server you started yourself

```bash
cd tests/e2e && E2E_BASE_URL=http://localhost:8080 BIFROST_ADMIN_USERNAME=... BIFROST_ADMIN_PASSWORD=... npx playwright test features/providers
```

Without a runner plan, everything runs on a single worker against that server.

## Folder Structure

```text
tests/e2e/
├── playwright.config.ts           # Playwright configuration (one project per scheduled worker)
├── plan.ts                        # Reads the runner's worker plan
├── env/                           # docker-compose.yml (Postgres :55432) + seed config.json
├── scripts/run-e2e.mjs            # Orchestrator behind `make run-e2e-ui`
├── reporters/failures.ts          # Writes reports/e2e-results.json
├── core/                          # Shared utilities & fixtures
│   ├── fixtures/                 # Custom test fixtures
│   ├── pages/                    # Base page objects
│   ├── actions/                  # Reusable actions
│   └── utils/                    # Utilities and helpers
└── features/                     # Feature-specific tests
    ├── providers/                # Provider tests
    ├── virtual-keys/             # Virtual key tests
    ├── dashboard/                # Dashboard tests
    ├── logs/                     # LLM logs tests
    ├── mcp-logs/                 # MCP logs tests
    ├── mcp-registry/             # MCP registry tests
    ├── routing-rules/            # Routing rules tests
    ├── plugins/                  # Plugins tests
    ├── observability/            # Observability connectors tests
    └── config/                   # Config settings tests
```

## Writing Tests

### Using Page Objects

```typescript
import { test, expect } from '../../core/fixtures/base.fixture'

test('should create provider', async ({ providersPage }) => {
  await providersPage.goto()
  await providersPage.selectProvider('openai')
  // ...
})
```

### Test Data

Use factory functions from the `*.data.ts` files for generating test data:

```typescript
import { createProviderKeyData } from './providers.data'

const keyData = createProviderKeyData({ name: 'My Key' })
```

## Configuration

Environment variables:
- `E2E_BASE_URL` - Run everything against one existing server instead of runner-started workers
- `WORKERS` - Number of isolated Bifrost+browser workers (default: 4)
- `BIFROST_E2E_PG_PORT` - Port for the dedicated Postgres (default: 55432)
- `SEED_MODEL` - Model used to seed logs (default: openai/gpt-4o-mini)
- `CI` - Set to true in CI environments (enables retries)

## Debugging

```bash
# Run with Playwright Inspector (anything after -- goes to playwright)
node scripts/run-e2e.mjs --features providers -- --debug

# Generate code with Codegen against a worker left up by KEEP=1 (w1 = :18181)
npm run codegen
```

## Best Practices

### Wait Strategies

Use semantic waits instead of hardcoded timeouts:

```typescript
// ✅ Good: Semantic waits
await page.waitForLoadState('networkidle')
await element.waitFor({ state: 'visible' })
await expect(element).toBeVisible({ timeout: 5000 })

// ❌ Bad: Hardcoded timeouts (flaky and slow)
await page.waitForTimeout(2000)
```

### Selectors

Use `data-testid` attributes for robust selectors:

```typescript
// ✅ Good: Test IDs are resilient to UI changes
page.locator('[data-testid="chart-log-volume"]')
page.getByTestId('create-btn')

// ❌ Bad: Brittle chained parent selectors
page.locator('text=Volume').locator('..').locator('..')
```

### Resource Cleanup

Always clean up resources created during tests:

```typescript
// ✅ Good: Clean up after assertions
test('should create item', async ({ page }) => {
  await page.createItem(data)
  expect(await page.itemExists(data.name)).toBe(true)
  // Cleanup
  await page.deleteItem(data.name)
})
```

### Deterministic Assertions

Avoid conditional logic that always passes:

```typescript
// ❌ Bad: Always passes (count >= 0 is always true)
const count = await page.getCount()
expect(count >= 0).toBe(true)

// ✅ Good: Deterministic assertion
const count = await page.getCount()
if (count === 0) {
  expect(emptyState).toBeVisible()
} else {
  expect(count).toBeGreaterThan(0)
  expect(emptyState).not.toBeVisible()
}
```

## Anti-Patterns to Avoid

1. **`waitForTimeout()`** - Always use semantic waits instead
2. **`{ force: true }`** - Fix underlying visibility issues instead
3. **Chained parent locators** (`.locator('..')`) - Use `data-testid` attributes
4. **Conditional assertions that always pass** - Write deterministic tests
5. **Static test data names** - Use timestamps for uniqueness
6. **Missing cleanup** - Delete created resources to prevent pollution

## Troubleshooting

### Tests Failing Intermittently

1. Replace `waitForTimeout()` with proper semantic waits
2. Ensure toasts are dismissed: `await page.dismissToasts()`
3. Add `waitForPageLoad()` after navigation
4. Wait for sheets/modals to complete animation: `await page.waitForSheetAnimation()`

### Tests Pass Individually but Fail Together

1. Add cleanup for created resources
2. Use unique names with `Date.now()` timestamps
3. Check for leftover state from previous tests

### Element Not Clickable

1. Ensure element is visible: `await element.waitFor({ state: 'visible' })`
2. Scroll element into view: `await element.scrollIntoViewIfNeeded()`
3. Dismiss overlaying toasts: `await page.dismissToasts()`
4. Don't use `{ force: true }` - fix the root cause