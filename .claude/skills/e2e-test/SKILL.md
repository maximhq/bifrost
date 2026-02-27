
---
name: e2e-test
description: Write, run, debug, audit, and auto-update Playwright E2E tests for the Bifrost UI. Use when asked to create new E2E tests, add test coverage, fix flaky tests, debug failing tests, audit test correctness, update tests after UI changes, or sync tests with modified components. Invoked with /e2e-test <FEATURE_NAME>, /e2e-test fix <SPEC_FILE>, /e2e-test sync, or /e2e-test audit.
allowed-tools: Read, Grep, Glob, Bash, Edit, Write, Task, AskUserQuestion, TodoWrite
---

# Playwright E2E Testing

Write, run, debug, and auto-update Playwright E2E tests following Bifrost's established patterns and conventions. Automatically detects UI changes and updates affected tests.

## Usage

```
/e2e-test <FEATURE_NAME>              # Create or update tests for a feature
/e2e-test fix <SPEC_FILE>             # Debug and fix a failing test
/e2e-test run <FEATURE_NAME>          # Run tests for a specific feature
/e2e-test run                         # Run all E2E tests
/e2e-test sync                        # Detect UI changes and update affected tests
/e2e-test sync <FEATURE_NAME>         # Sync tests for a specific feature with UI changes
/e2e-test audit                       # Audit all specs for incorrect/weak assertions
/e2e-test audit <FEATURE_NAME>        # Audit a specific feature's specs
```

## Workflow Overview

1. **Understand the feature** - Read the UI code to understand what needs testing
2. **Check existing tests** - Review existing test patterns for the feature or similar features
3. **Identify data-testid attributes** - Find selectors in the UI code
4. **Write/update tests** - Follow the established patterns (page objects, data factories, fixtures)
5. **Run the tests** - Execute and verify they pass
6. **Fix failures** - Debug and fix any issues

## Auto-Update Workflow (sync mode)

When invoked with `sync`, or when UI changes are detected, automatically update E2E tests:

### Step 0: Detect What Changed

Detect UI changes by checking git diff against the base branch:

```bash
# Get all changed UI files
git diff main --name-only -- 'ui/'

# Get changed files with diff content for analysis
git diff main -- 'ui/app/workspace/' 'ui/components/'
```

Categorize changes into:
- **data-testid changes** - Renamed, removed, or added test IDs
- **Route/URL changes** - Modified page routes or navigation paths
- **Component structure changes** - New/removed form fields, buttons, tables, dialogs
- **API endpoint changes** - Modified API calls that tests rely on
- **New features** - Entirely new pages or components that need test coverage

### Step 1: Map UI Changes to Affected Tests

For each changed UI file, find the corresponding test files:

```
UI File Path                              → Test Feature Folder
ui/app/workspace/providers/**             → tests/e2e/features/providers/
ui/app/workspace/virtual-keys/**          → tests/e2e/features/virtual-keys/
ui/app/workspace/dashboard/**             → tests/e2e/features/dashboard/
ui/app/workspace/logs/**                  → tests/e2e/features/logs/
ui/app/workspace/mcp-logs/**              → tests/e2e/features/mcp-logs/
ui/app/workspace/mcp-registry/**          → tests/e2e/features/mcp-registry/
ui/app/workspace/routing-rules/**         → tests/e2e/features/routing-rules/
ui/app/workspace/observability/**         → tests/e2e/features/observability/
ui/app/workspace/config/**                → tests/e2e/features/config/
ui/app/workspace/plugins/**              → tests/e2e/features/plugins/
ui/components/sidebar.tsx                 → tests/e2e/core/pages/sidebar.page.ts
ui/components/**                          → May affect multiple test features
```

### Step 2: Analyze Each Change Type and Update

**A. data-testid renamed or removed:**
1. Search the old testid across all test files: `grep -r 'old-testid' tests/e2e/`
2. Update every reference in page objects, selectors, and spec files
3. If a testid was removed without replacement, check if the element still exists with a different selector and add a new testid to the UI

**B. Form fields added/removed:**
1. If a new form field was added to a create/edit form:
   - Add the field to the page object's interface (e.g., `FeatureConfig`)
   - Add a locator for the field in the page object constructor
   - Update the `createFeature()` / `editFeature()` methods to fill the field
   - Update the data factory to include the new field with a default value
   - Add a test case that exercises the new field
2. If a form field was removed:
   - Remove it from the interface, locators, and methods
   - Remove or update test cases that depended on it

**C. New buttons/actions added:**
1. Add locators to the page object
2. Add methods to interact with the new action
3. Add test cases covering the new action

**D. Route changes:**
1. Update `goto()` methods in page objects
2. Update navigation helpers in `core/actions/navigation.ts`
3. Update any hardcoded URLs in test specs

**E. API endpoint changes:**
1. Update `core/actions/api.ts` helpers
2. Update any `waitForResponse()` URL patterns in page objects

**F. New page/feature added:**
1. Create the full test structure (page object, data factory, spec, fixture registration)
2. Follow Step 3 from the main workflow

### Step 3: Validate Updates

After making changes, run the affected tests to verify:

```bash
# Run only the affected feature tests
npx playwright test features/<affected-feature> --reporter=list

# If multiple features affected, run them all
npx playwright test features/providers features/virtual-keys --reporter=list
```

### Step 4: Report Changes

Present a summary to the user:
```
## E2E Test Sync Summary

### UI Changes Detected
- <list of changed UI files>

### Tests Updated
- **<feature>.page.ts**: Updated locators for renamed data-testid, added new field method
- **<feature>.spec.ts**: Added test for new "export" button, updated form fill sequence
- **<feature>.data.ts**: Added new field to factory defaults

### Tests Created
- **<new-feature>/**: Full test suite for new feature (page object, data, spec)

### Tests Requiring Manual Review
- <any changes that couldn't be auto-resolved, e.g., complex interaction flow changes>

### Verification
- Ran `npx playwright test features/<feature>` → X passed, Y failed
```

### Important: Proactive Sync Triggers

**ALWAYS** check for and sync E2E tests when ANY of these happen:

1. **You modify a UI component** that has `data-testid` attributes — check if tests reference those IDs
2. **You add a new `data-testid`** to a component — consider if existing tests should use it
3. **You rename or remove a component/prop** — search for test references and update them
4. **You change a form's fields** (add/remove inputs, change validation) — update page object methods and test data
5. **You modify an API route** that tests call — update API helpers and response wait patterns
6. **You change page navigation/routing** — update `goto()` methods and navigation helpers

To check quickly if tests are affected by your UI change:
```bash
# Find test files that reference any testid from the changed component
grep -rl 'data-testid-from-component' tests/e2e/
```

---

## Audit Workflow (audit mode) — Fix Incorrect Specs

Tests that pass but validate the wrong things are worse than no tests — they give false confidence. When invoked with `audit`, systematically scan specs for correctness issues and fix them.

### Step 0: Scope the Audit

```bash
# Audit all specs
/e2e-test audit

# Audit a specific feature
/e2e-test audit virtual-keys
```

If a specific feature is given, read only that feature's spec, page object, and data files. Otherwise, scan all `features/**/*.spec.ts` files.

### Step 1: Read the UI Code First

For every spec file being audited, **read the actual UI component code** to understand what the UI really does. This is the source of truth — tests must match real behavior, not assumed behavior.

```bash
# For each feature, read the UI code
# e.g. for virtual-keys:
grep -r 'data-testid' ui/app/workspace/virtual-keys/ --include='*.tsx'
```

Understand:
- What fields are in the create/edit form (from the UI code, not the test)
- What the save button actually does (API call, toast, sheet close)
- What the table actually renders (columns, row content, empty state)
- What validation rules exist (required fields, format checks)
- What error states exist (API failures, permission errors)

### Step 2: Scan for Incorrect Assertion Patterns

Check each spec file for these **anti-patterns** (ordered by severity):

#### P0 — Tests That Can Never Fail (always-true assertions)

These are the most dangerous — they provide zero coverage while appearing green.

```typescript
// WRONG: Always true — count is always >= 0
const count = await page.getCount()
expect(count >= 0).toBe(true)

// WRONG: Always true — empty string is a string
const text = await element.textContent()
expect(typeof text).toBe('string')

// WRONG: Always true — isVisible returns a boolean, not asserted correctly
const visible = await element.isVisible()
// (no assertion at all, or expect(visible).toBeDefined())

// WRONG: Catching error means it never fails
try { await doSomething(); expect(true).toBe(true) } catch { expect(true).toBe(true) }
```

**Fix:** Replace with deterministic assertions that verify actual expected state.

#### P1 — Tests That Assert Existence But Not Correctness

The test creates an item and checks it exists, but never verifies the item has the right data.

```typescript
// WEAK: Only checks existence, not content
await page.createVirtualKey({ name: 'Test VK', description: 'My desc', budget: { maxLimit: 100 } })
const exists = await page.virtualKeyExists('Test VK')
expect(exists).toBe(true)
// Never checks that description, budget, or other fields were actually saved correctly
```

**Fix:** After create, open/view the item and verify its fields match what was submitted. Compare against the actual UI state:
```typescript
// BETTER: Verify the data was saved correctly
await page.viewVirtualKey(vkData.name)
await expect(page.descriptionInput).toHaveValue('My desc')
await expect(page.page.locator('#budgetMaxLimit')).toHaveValue('100')
```

#### P2 — Tests That Assert the Wrong Thing

The test name says one thing but asserts something unrelated.

```typescript
// WRONG: Test says "should validate email" but only checks button state
test('should validate email format', async ({ page }) => {
  await page.fillEmail('invalid')
  await expect(page.saveBtn).toBeDisabled() // Tests button, not validation message
})

// WRONG: Test says "should delete" but only checks toast, not actual deletion
test('should delete item', async ({ page }) => {
  await page.deleteItem('foo')
  await page.waitForSuccessToast()
  // Never checks the item is actually gone from the table
})
```

**Fix:** Align assertions with test intent. A delete test must verify the item is gone. A validation test must check the validation message.

#### P3 — Tests With Swallowed Errors / Catch-All Handlers

Tests that catch errors and silently continue, hiding real failures.

```typescript
// WRONG: Error is caught and ignored — test always passes
const isVisible = await element.isVisible().catch(() => false)
if (isVisible) {
  expect(isVisible).toBe(true) // Only asserts when visible, silently passes when not
}

// WRONG: Optional assertion that can be skipped entirely
const providerSection = page.getByText(/Providers/i).first()
const isProviderVisible = await providerSection.isVisible().catch(() => false)
if (isProviderVisible) {
  expect(isProviderVisible).toBe(true) // Tautology when reached, skipped when not
}
```

**Fix:** Remove the catch/conditional and make the assertion unconditional. If the element should be visible, assert it directly:
```typescript
await expect(element).toBeVisible()
```

If the state genuinely depends on external factors, use count-based branching with **both** branches making meaningful assertions:
```typescript
const count = await page.getCount()
if (count === 0) {
  await expect(page.emptyState).toBeVisible()
} else {
  expect(count).toBeGreaterThan(0)
  await expect(page.emptyState).not.toBeVisible()
}
```

#### P4 — Tests That Don't Assert After Actions

Tests that perform actions (clicks, form fills, navigation) but have no assertion afterward.

```typescript
// WRONG: Action with no assertion — test only checks nothing throws
test('should toggle visibility', async ({ page }) => {
  await page.toggleKeyVisibility('my-key')
  await page.toggleKeyVisibility('my-key')
  // No assertion that the visibility state actually changed
})
```

**Fix:** Add assertions verifying the action's observable effect:
```typescript
test('should toggle visibility', async ({ page }) => {
  await page.toggleKeyVisibility('my-key')
  // Verify key value is now visible
  await expect(page.getKeyValueText('my-key')).toBeVisible()

  await page.toggleKeyVisibility('my-key')
  // Verify key value is now hidden again
  await expect(page.getKeyValueText('my-key')).not.toBeVisible()
})
```

#### P5 — Tests Asserting Against Stale State

Tests that read state before an action completes, or compare to a stale snapshot.

```typescript
// WRONG: Reads count before table finishes refreshing
await page.deleteItem('foo')
const count = await page.getCount() // Table hasn't refreshed yet!
expect(count).toBe(previousCount - 1) // May pass by coincidence
```

**Fix:** Wait for the state change to complete before asserting:
```typescript
await page.deleteItem('foo')
await page.waitForItemGone('foo') // Wait for table refresh
const count = await page.getCount()
expect(count).toBe(previousCount - 1)
```

#### P6 — Tests That Duplicate Other Tests Without Additional Value

Multiple tests covering the exact same code path with only cosmetic differences (different name strings, same logic).

```typescript
// REDUNDANT: These three tests do the same thing with different budget values
test('should create VK with small budget', ...)   // creates + checks exists
test('should create VK with medium budget', ...)  // creates + checks exists
test('should create VK with daily budget', ...)   // creates + checks exists
// None of them verify the budget value was saved correctly
```

**Fix:** Either consolidate into a parameterized test, or make each test verify something unique (e.g., verify the actual budget value appears in the UI).

### Step 3: Cross-Check Against UI Code

For each test, verify:

1. **Form fields match** - Does the test fill all required fields from the UI? Does it test fields that actually exist?
2. **Selectors are correct** - Does `data-testid="vk-name-input"` actually exist in the current UI code? Has it been renamed?
3. **Behavior matches** - If the UI shows a confirmation dialog on delete, does the test handle it? If the form has validation, do tests exercise it?
4. **Error paths exist** - Does the UI show error toasts? Are there tests for error scenarios?
5. **New UI capabilities untested** - Has the UI added new features (export, filter, sort, pagination) that have no test coverage?

```bash
# Compare what testids exist in UI vs what tests reference
grep -roh 'data-testid="[^"]*"' ui/app/workspace/<feature>/ | sort -u > /tmp/ui-testids.txt
grep -roh "getByTestId('[^']*')\|getByTestId(\"[^\"]*\")" tests/e2e/features/<feature>/ | sort -u > /tmp/test-testids.txt
# Diff to find gaps
diff /tmp/ui-testids.txt /tmp/test-testids.txt
```

### Step 4: Fix and Strengthen

For each issue found:

1. **Read the UI code** for that specific component/interaction
2. **Understand the expected behavior** from the UI implementation
3. **Rewrite the assertion** to verify actual, observable, correct state
4. **Run the fixed test** to ensure it passes with correct behavior and **would fail** if the behavior broke

### Step 5: Report Findings

Present results to the user:

```
## E2E Audit Report — <feature>

### Issues Found: X

#### P0 — Always-True Assertions (X found)
| Test | File:Line | Issue | Fix |
|------|-----------|-------|-----|
| "should show empty state" | vk.spec.ts:374 | `count >= 0` always true | Use count-based branching |

#### P1 — Missing Data Verification (X found)
| Test | File:Line | Issue | Fix |
|------|-----------|-------|-----|
| "should create with budget" | vk.spec.ts:108 | Only checks `exists`, not budget value | Add field verification after create |

#### P2 — Wrong Assertion Target (X found)
...

#### P3 — Swallowed Errors (X found)
...

#### P4 — Missing Assertions (X found)
...

#### P5 — Stale State (X found)
...

#### P6 — Redundant Tests (X found)
...

### Coverage Gaps
- No test for: <UI feature that exists but has no test>
- Missing error path test for: <error scenario>

### Summary
- X tests fixed
- X tests need manual review (complex interaction flows)
- X new tests added for coverage gaps
```

---

## Project Structure

All E2E tests live in `tests/e2e/`:

```
tests/e2e/
├── playwright.config.ts           # Playwright configuration
├── global-setup.ts                # Global setup (plugin build, MCP servers, Bifrost connectivity)
├── core/                          # Shared utilities & fixtures
│   ├── fixtures/
│   │   ├── base.fixture.ts        # Main fixture - exports `test` and `expect` with all page objects
│   │   └── test-data.fixture.ts   # TestDataFactory for generating unique test data
│   ├── pages/
│   │   ├── base.page.ts           # BasePage class with common methods (toasts, forms, waits)
│   │   └── sidebar.page.ts        # Sidebar navigation
│   ├── actions/
│   │   ├── navigation.ts          # Navigation helpers (goToProviders, goToVirtualKeys, etc.)
│   │   └── api.ts                 # API helpers for setup/cleanup (providersApi, virtualKeysApi, etc.)
│   └── utils/
│       ├── selectors.ts           # Centralized selector definitions
│       └── test-helpers.ts        # Utilities: waitForNetworkIdle, retry, fillSelect, assertToast, etc.
└── features/                      # One folder per feature
    └── <feature>/
        ├── <feature>.spec.ts      # Test cases
        ├── <feature>.data.ts      # Test data factories & sample constants
        └── pages/
            └── <feature>.page.ts  # Page object extending BasePage
```

## Step 1: Understand the Feature

Before writing tests, read the relevant UI code to understand:

- What pages/routes exist for the feature
- What `data-testid` attributes are already in the UI components
- What CRUD operations are available
- What form fields, buttons, and interactive elements exist
- What API endpoints the UI calls

**Search for data-testid in UI code:**
```bash
# Find all data-testid attributes for a feature
grep -r 'data-testid' ui/app/workspace/<feature>/ --include='*.tsx' --include='*.ts'
```

**Check what routes exist:**
```bash
ls ui/app/workspace/
```

## Step 2: Check Existing Tests

Always review existing patterns before writing new tests:

```bash
# List all existing feature test folders
ls tests/e2e/features/

# Read an existing spec for patterns
cat tests/e2e/features/virtual-keys/virtual-keys.spec.ts

# Read existing page objects for patterns
cat tests/e2e/features/virtual-keys/pages/virtual-keys.page.ts
```

## Step 3: Create the Feature Test Structure

For a new feature, create these files:

### 3a. Page Object (`features/<feature>/pages/<feature>.page.ts`)

**CRITICAL RULES:**
- Always extend `BasePage`
- Define all locators in the constructor using `page.getByTestId()`
- Use `data-testid` attributes as the primary selector strategy
- Use `page.getByRole()` as a secondary strategy
- NEVER use brittle CSS selectors or chained parent locators (`.locator('..')`)
- Methods should be async and use semantic waits
- Include `goto()`, CRUD methods, and a `cleanup` method

**Template:**
```typescript
import { Locator, Page, expect } from '@playwright/test'
import { BasePage } from '../../../core/pages/base.page'
import { waitForNetworkIdle } from '../../../core/utils/test-helpers'

// Define interfaces for the feature's data
export interface FeatureConfig {
  name: string
  description?: string
  // ... other fields
}

export class FeaturePage extends BasePage {
  // Main page elements
  readonly createBtn: Locator
  readonly table: Locator

  // Sheet/form elements
  readonly sheet: Locator
  readonly nameInput: Locator
  readonly saveBtn: Locator
  readonly cancelBtn: Locator

  constructor(page: Page) {
    super(page)

    // Use getByTestId for all locators
    this.createBtn = page.getByTestId('create-feature-btn')
    this.table = page.getByTestId('feature-table')
    this.sheet = page.getByTestId('feature-sheet')
    this.nameInput = page.getByTestId('feature-name-input')
    this.saveBtn = page.getByTestId('feature-save-btn')
    this.cancelBtn = page.getByTestId('feature-cancel-btn')
  }

  async goto(): Promise<void> {
    await this.page.goto('/workspace/<feature>')
    await waitForNetworkIdle(this.page)
  }

  async createFeature(config: FeatureConfig): Promise<void> {
    await this.createBtn.click()
    await expect(this.sheet).toBeVisible()
    await this.waitForSheetAnimation()

    // Fill form fields
    await this.nameInput.fill(config.name)

    // Save
    await this.saveBtn.click()
    await this.waitForSuccessToast()
    await this.dismissToasts()
    await expect(this.sheet).not.toBeVisible({ timeout: 5000 })
  }

  async featureExists(name: string): Promise<boolean> {
    const row = this.page.getByTestId(`feature-row-${name}`)
    return (await row.count()) > 0
  }

  async deleteFeature(name: string): Promise<void> {
    const deleteBtn = this.page.getByTestId(`feature-delete-btn-${name}`)
    await deleteBtn.click()

    // Handle confirmation dialog
    const confirmDialog = this.page.locator('[role="alertdialog"]')
    await confirmDialog.waitFor({ state: 'visible', timeout: 5000 })
    const confirmBtn = confirmDialog.getByRole('button', { name: /Delete/i })
    await confirmBtn.click()

    await this.waitForSuccessToast()
    await this.dismissToasts()
  }

  async closeSheet(): Promise<void> {
    const isSheetVisible = await this.sheet.isVisible().catch(() => false)
    if (isSheetVisible) {
      const closeBtn = this.sheet.locator('button[aria-label*="close"], button:has(svg.lucide-x)').first()
      if (await closeBtn.isVisible()) {
        await closeBtn.click()
      }
      await expect(this.sheet).not.toBeVisible({ timeout: 5000 }).catch(() => {})
    }
  }

  async cleanupFeatures(names: string[]): Promise<void> {
    if (names.length === 0) return
    await this.goto()
    await this.closeSheet()
    await this.dismissToasts()
    for (const name of names) {
      try {
        const exists = await this.featureExists(name)
        if (!exists) continue
        await this.closeSheet()
        await this.deleteFeature(name)
      } catch (error) {
        console.error(`[CLEANUP] Failed to delete: ${name}`)
      }
    }
  }
}
```

### 3b. Test Data Factory (`features/<feature>/<feature>.data.ts`)

**CRITICAL RULES:**
- Use `Date.now()` for unique test names
- Provide factory functions with sensible defaults
- Allow partial overrides via the spread pattern
- Create sample constant objects for reusable configurations

**Template:**
```typescript
import { FeatureConfig } from './pages/feature.page'

export function createFeatureData(overrides: Partial<FeatureConfig> = {}): FeatureConfig {
  const timestamp = Date.now()
  return {
    name: `Test Feature ${timestamp}`,
    description: 'E2E test feature',
    // ... sensible defaults
    ...overrides,
  }
}

// Sample configurations for different scenarios
export const SAMPLE_CONFIGS = {
  basic: { /* ... */ },
  advanced: { /* ... */ },
} as const
```

### 3c. Test Spec (`features/<feature>/<feature>.spec.ts`)

**CRITICAL RULES:**
- Import `test` and `expect` from `../../core/fixtures/base.fixture`
- Track created resources in arrays for cleanup in `afterEach`
- Use `test.describe()` blocks for logical grouping
- Use `test.beforeEach()` to navigate to the page
- Use `test.afterEach()` to clean up resources
- Use unique names with `Date.now()` for test data
- Write deterministic assertions (never `expect(count >= 0).toBe(true)`)
- Use `test.describe.configure({ mode: 'serial' })` when tests have write ordering dependencies

**Template:**
```typescript
import { expect, test } from '../../core/fixtures/base.fixture'
import { createFeatureData } from './feature.data'

const createdItems: string[] = []

test.describe('Feature Name', () => {
  test.beforeEach(async ({ featurePage }) => {
    await featurePage.goto()
  })

  test.afterEach(async ({ featurePage }) => {
    await featurePage.closeSheet()
    if (createdItems.length > 0) {
      await featurePage.cleanupFeatures([...createdItems])
      createdItems.length = 0
    }
  })

  test.describe('Creation', () => {
    test('should display create button', async ({ featurePage }) => {
      await expect(featurePage.createBtn).toBeVisible()
    })

    test('should create a basic item', async ({ featurePage }) => {
      const data = createFeatureData({ name: `Basic Test ${Date.now()}` })
      createdItems.push(data.name)

      await featurePage.createFeature(data)

      const exists = await featurePage.featureExists(data.name)
      expect(exists).toBe(true)
    })
  })

  test.describe('Deletion', () => {
    test('should delete item', async ({ featurePage }) => {
      const data = createFeatureData({ name: `Delete Test ${Date.now()}` })
      await featurePage.createFeature(data)

      let exists = await featurePage.featureExists(data.name)
      expect(exists).toBe(true)

      await featurePage.deleteFeature(data.name)
      // No need to track for cleanup since we just deleted it

      exists = await featurePage.featureExists(data.name)
      expect(exists).toBe(false)
    })
  })
})
```

### 3d. Register the Page Object in Fixtures

If creating a brand new feature, add it to `core/fixtures/base.fixture.ts`:

1. Import the page class
2. Add to `BifrostFixtures` type
3. Add fixture definition

```typescript
// In base.fixture.ts
import { FeaturePage } from '../../features/<feature>/pages/<feature>.page'

type BifrostFixtures = {
  // ... existing
  featurePage: FeaturePage
}

export const test = base.extend<BifrostFixtures>({
  // ... existing
  featurePage: async ({ page }, use) => {
    await use(new FeaturePage(page))
  },
})
```

### 3e. Add npm Script (optional)

In `tests/e2e/package.json`, add a feature-specific test script:
```json
{
  "scripts": {
    "test:<feature>": "playwright test features/<feature>"
  }
}
```

## Step 4: Run Tests

```bash
# From tests/e2e/ directory
cd tests/e2e

# Run all tests
npx playwright test

# Run specific feature
npx playwright test features/<feature>

# Run in headed mode (see the browser)
npx playwright test features/<feature> --headed

# Run with Playwright UI (interactive)
npx playwright test --ui

# Run with debug inspector
npx playwright test features/<feature> --debug

# Run a single test by title
npx playwright test -g "should create a basic item"

# From project root via Makefile
make run-e2e FLOW=<feature>
make run-e2e-headed FLOW=<feature>
```

**Environment variables:**
- `BASE_URL` - Override app URL (default: http://localhost:3000)
- `BIFROST_BASE_URL` - Override Bifrost API URL (default: http://localhost:8080)
- `SKIP_WEB_SERVER=1` - Skip auto-starting Next.js dev server
- `CI=1` - Enable CI mode (retries, serial execution)

## Step 5: Debug Failing Tests

### Common Issues and Fixes

**1. Element not found / timeout:**
- Check if the `data-testid` attribute exists in the UI component
- Verify the page has loaded: add `await waitForNetworkIdle(page)` after navigation
- Check for loading spinners: `await page.locator('[data-testid="loading-spinner"]').waitFor({ state: 'hidden' })`

**2. Toast interfering with clicks:**
- Dismiss toasts before interactions: `await page.dismissToasts()` or `await page.forceCloseToasts()`

**3. Sheet/dialog animation not complete:**
- Wait for animation: `await page.waitForSheetAnimation()`
- Wait for sheet visibility: `await expect(sheet).toBeVisible()`

**4. Stale element after table refresh:**
- Re-query the locator after data changes (don't reuse old locator references)
- Use polling patterns like `waitForVirtualKeyGone()` for eventual consistency

**5. Tests pass individually but fail together:**
- Check cleanup: ensure `afterEach` deletes all created resources
- Use unique names with `Date.now()` timestamps
- Check for state pollution between test files

**6. Flaky Radix/Shadcn Select:**
- Use the `fillSelect()` helper from `core/utils/test-helpers.ts`
- Wait for `[role="listbox"]` to appear before clicking options
- Wait for `[role="listbox"]` to disappear after selection

### Debugging Tools

```bash
# View the HTML report of the last run
npx playwright show-report

# Generate test code by recording browser actions
npx playwright codegen http://localhost:3000

# Run with trace viewer (records every action)
npx playwright test --trace on
```

### Trace and Screenshots

- **Screenshots:** Taken automatically on failure, saved to `test-results/`
- **Traces:** Captured on first retry, viewable via `npx playwright show-trace <trace.zip>`
- **Videos:** Retained on failure, saved to `test-results/`

## Mandatory Rules

These rules MUST be followed at all times:

### Selectors
- **ALWAYS** use `data-testid` attributes as the primary selector strategy
- **ALWAYS** use `page.getByTestId()` or `page.getByRole()` - never raw CSS selectors for interactive elements
- **NEVER** use chained parent locators (`.locator('..')`)
- **NEVER** use `{ force: true }` on clicks - fix the underlying visibility issue instead
- If a needed `data-testid` doesn't exist in the UI, add it to the UI component first

### Waits
- **ALWAYS** use semantic waits (`waitFor()`, `expect().toBeVisible()`, `waitForLoadState()`)
- **NEVER** use `page.waitForTimeout()` except as last resort in cleanup/polling (and document why)
- **ALWAYS** wait for page load after navigation: `await waitForNetworkIdle(page)`
- **ALWAYS** wait for sheet animations before interacting with sheet contents

### Test Data
- **ALWAYS** use `Date.now()` or `TestDataFactory.uniqueId()` for unique test names
- **NEVER** use static/hardcoded test data names (causes collisions in parallel runs)
- **ALWAYS** create test data factory functions in `<feature>.data.ts`

### Cleanup
- **ALWAYS** track created resources in arrays and delete them in `afterEach`
- **ALWAYS** close open sheets before cleanup: `await page.closeSheet()`
- **ALWAYS** dismiss toasts before interactive operations
- Cleanup failures should `console.error` and continue, never throw

### Assertions
- **ALWAYS** write deterministic assertions that can actually fail
- **NEVER** write `expect(count >= 0).toBe(true)` - this always passes
- Use count-based branching for state-dependent assertions:
  ```typescript
  if (count === 0) {
    await expect(emptyState).toBeVisible()
  } else {
    expect(count).toBeGreaterThan(0)
  }
  ```

### Imports
- **ALWAYS** import `test` and `expect` from `../../core/fixtures/base.fixture`
- **NEVER** import directly from `@playwright/test` in spec files (use the custom fixture)

### Adding data-testid to UI

When a UI component is missing a required `data-testid`, add it directly. Convention:
```
data-testid="<entity>-<element>-<qualifier>"

Examples:
  data-testid="vk-row-{name}"           # Virtual key table row
  data-testid="vk-edit-btn-{name}"      # Edit button for specific VK
  data-testid="vk-delete-btn-{name}"    # Delete button for specific VK
  data-testid="create-vk-btn"           # Create button
  data-testid="vk-sheet"                # Virtual key form sheet
  data-testid="vk-name-input"           # Name input in VK form
  data-testid="vk-save-btn"             # Save button in VK form
```

## Available BasePage Methods

Every page object inherits these from `BasePage` (`core/pages/base.page.ts`):

| Method | Description |
|--------|-------------|
| `waitForPageLoad()` | Wait for `networkidle` load state |
| `waitForChartsToLoad()` | Wait for charts/data and skeletons to disappear |
| `getToast(type?)` | Get toast locator (success/error/loading/default) |
| `waitForSuccessToast(message?)` | Wait for success toast, optionally match message |
| `waitForErrorToast(message?)` | Wait for error toast, optionally match message |
| `waitForToastsToDisappear(timeout?)` | Wait for all toasts to be gone |
| `dismissToasts()` | Wait for toasts to auto-dismiss |
| `forceCloseToasts()` | Click away + wait to force-dismiss toasts |
| `waitForSheetAnimation()` | Wait for sheet/dialog open animation to complete |
| `waitForStateChange(locator, attr, val)` | Wait for element attribute to match |
| `fillByLabel(label, value)` | Fill input by its label |
| `fillByPlaceholder(placeholder, value)` | Fill input by its placeholder |
| `fillByTestId(testId, value)` | Fill input by data-testid |
| `clickButton(text)` | Click button by visible text |
| `clickByTestId(testId)` | Click element by data-testid |
| `closeDevProfiler()` | Dismiss the Dev Profiler overlay if visible |

## Available Test Helpers (`core/utils/test-helpers.ts`)

| Helper | Description |
|--------|-------------|
| `waitForNetworkIdle(page, timeout?)` | Wait for network idle state |
| `wait(ms)` | Sleep for ms (use sparingly) |
| `retry(fn, { retries, delay })` | Retry async function with backoff |
| `randomString(length?)` | Generate random alphanumeric string |
| `uniqueTestName(prefix)` | Generate unique name with timestamp + random suffix |
| `assertToast(page, text, type)` | Assert toast appears with text |
| `assertUrl(page, pattern)` | Assert page URL matches pattern |
| `fillSelect(page, triggerSelector, optionText)` | Fill Radix/Shadcn Select component |
| `fillMultiSelect(page, inputSelector, values)` | Fill multi-select with array of values |
| `clearAndFill(page, selector, value)` | Atomically clear and fill input |
| `getTableRowCount(page, tableSelector)` | Get count of table body rows |
| `tableContainsRow(page, tableSelector, text)` | Check if table has row with text |
| `waitForTableLoad(page, tableSelector)` | Wait for table visible + spinner gone |
| `screenshotOnError(page, testName, fn)` | Auto-screenshot wrapper for debugging |

## Available API Helpers (`core/actions/api.ts`)

For programmatic setup/cleanup via API (bypassing UI):

| API | Methods |
|-----|---------|
| `providersApi` | `getAll`, `get`, `create`, `update`, `delete` |
| `virtualKeysApi` | `getAll`, `get`, `create`, `update`, `delete` |
| `teamsApi` | `getAll`, `create`, `delete` |
| `customersApi` | `getAll`, `create`, `delete` |
| `cleanupTestData(request, { virtualKeyIds, teamIds, customerIds, providerNames })` | Bulk cleanup |
