# Bifrost E2E Tests

End-to-end tests for the Bifrost UI using Playwright.

## Setup

```bash
# Install dependencies
npm install

# Install Playwright browsers
npx playwright install
```

## Running Tests

```bash
# Run all E2E tests
make run-e2e

# Run specific feature tests
make run-e2e FLOW=providers
make run-e2e FLOW=virtual-keys
make run-e2e FLOW=dashboard
make run-e2e FLOW=logs
make run-e2e FLOW=mcp-logs
make run-e2e FLOW=mcp-registry
make run-e2e FLOW=routing-rules
make run-e2e FLOW=observability
make run-e2e FLOW=config
make run-e2e FLOW=plugins

# Run tests in headed mode (visible browser)
make run-e2e-headed

# Run tests with Playwright UI
make run-e2e-ui

# Run specific feature tests via npm
npm run test:providers
npm run test:virtual-keys
npm run test:dashboard
npm run test:logs
npm run test:mcp-logs
npm run test:mcp-registry
npm run test:routing-rules
npm run test:observability
npm run test:config
npm run test:plugins

# View test report
npm run report
```

### Parallel flows on CI

The GitHub Actions workflow **E2E Tests** (`.github/workflows/e2e-tests.yml`) runs each flow in a **separate job in parallel**, since flows are independent. It triggers on push/PR when `ui/`, `tests/e2e/`, or the workflow file change. You can also run it manually (Actions → E2E Tests → Run workflow) and optionally pass a comma-separated list of flows (e.g. `providers,config,plugins`) to run only those.

## Folder Structure

```text
tests/e2e/
├── playwright.config.ts           # Playwright configuration
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
- `BASE_URL` - Base URL of the application (default: http://localhost:3000)
- `CI` - Set to true in CI environments

## Debugging

```bash
# Run with Playwright Inspector
npm run test:debug

# Generate code with Codegen
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
