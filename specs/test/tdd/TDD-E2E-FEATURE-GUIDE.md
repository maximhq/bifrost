# TDD: E2E Feature Test Writing Guide

**Version:** 1.0  
**Status:** Active  
**Source:** Phân tích trực tiếp `tests/e2e/features/`  
**Áp dụng cho:** Developers thêm hoặc sửa E2E UI tests

---

## 1. Feature Test Structure

Mỗi feature trong `tests/e2e/features/` có cấu trúc chuẩn:

```
features/<feature-name>/
├── <feature>.spec.ts          # Test cases (test logic)
├── <feature>.data.ts          # Test data factories (fixtures)
└── pages/
    └── <feature>.page.ts      # Page Object Model (UI interactions)
```

### 1.1 Ví dụ: Providers Feature

```
features/providers/
├── providers.spec.ts          # 786 lines, 30+ test cases
├── providers.data.ts          # createProviderKeyData(), createCustomProviderData()
└── pages/
    └── providers.page.ts      # ProvidersPage extends BasePage
```

---

## 2. Page Object Model (POM)

### 2.1 Quy tắc tạo Page Object

Mỗi page object **phải** extend `BasePage`:

```typescript
// features/my-feature/pages/my-feature.page.ts
import { BasePage } from '../../../core/pages/base.page'
import { Page, Locator } from '@playwright/test'

export class MyFeaturePage extends BasePage {
  // ─── URL & Navigation ──────────────────────────────────────────────────────
  readonly url = '/workspace/my-feature'

  async goto() {
    await this.page.goto(this.url)
    await this.waitForPageLoad()
  }

  // ─── Static Locators (computed once, cached as properties) ─────────────────
  get createButton(): Locator {
    return this.page.getByTestId('create-item-btn')
  }

  get itemList(): Locator {
    return this.page.getByTestId('item-list')
  }

  get emptyState(): Locator {
    return this.page.getByTestId('empty-state')
  }

  // ─── Dynamic Locators (methods returning Locator) ──────────────────────────
  getItemRow(name: string): Locator {
    return this.page.getByTestId(`item-row-${name}`)
    // hoặc: return this.page.getByRole('row').filter({ hasText: name })
  }

  // ─── Action Methods ─────────────────────────────────────────────────────────
  async openCreateSheet(): Promise<void> {
    await this.createButton.click()
    // Wait for sheet animation to complete
    await this.waitForSheetAnimation()
  }

  async createItem(data: ItemData): Promise<void> {
    await this.openCreateSheet()
    await this.page.getByLabel('Name').fill(data.name)
    await this.page.getByRole('button', { name: 'Save' }).click()
    await this.waitForSuccessToast()
  }

  async deleteItem(name: string): Promise<void> {
    const row = this.getItemRow(name)
    await row.getByRole('button', { name: 'Delete' }).click()
    // Handle confirmation dialog
    await this.page.getByRole('button', { name: 'Confirm' }).click()
    await this.waitForSuccessToast('deleted')
  }

  // ─── State Queries ──────────────────────────────────────────────────────────
  async itemExists(name: string, timeout = 5000): Promise<boolean> {
    try {
      const row = this.getItemRow(name)
      await row.waitFor({ state: 'visible', timeout })
      return true
    } catch {
      return false
    }
  }
}
```

### 2.2 Providers Page — Patterns thực tế

Từ `providers.page.ts`, các patterns phổ biến:

```typescript
// 1. Chọn provider từ sidebar bằng data-testid
async selectProvider(name: string): Promise<void> {
  await this.page.getByTestId(`provider-item-${name}`).click()
  await this.waitForPageLoad()
}

// 2. Kiểm tra existence với custom timeout
async keyExists(keyName: string, timeout = 5000): Promise<boolean> {
  try {
    await this.getKeyRow(keyName).waitFor({ state: 'visible', timeout })
    return true
  } catch { return false }
}

// 3. Tab navigation trong config sheet
async selectConfigTab(tab: 'performance' | 'network' | 'proxy' | 'debugging' | 'governance'): Promise<void> {
  await this.openConfigSheet()
  await this.page.getByTestId(`provider-tab-${tab}`).click()
}

// 4. Fill numeric inputs (React controlled components)
async fillNumberInput(locator: Locator, value: string): Promise<void> {
  await locator.click()
  await locator.fill('')
  await locator.pressSequentially(value)  // Triggers React onChange per character
}
```

---

## 3. Test Data Factories

### 3.1 Quy tắc Factory

```typescript
// features/providers/providers.data.ts
import type { ProviderKeyConfig, CustomProviderConfig } from '../../core/fixtures/test-data.fixture'

export function createProviderKeyData(
  overrides: Partial<ProviderKeyConfig> = {}
): ProviderKeyConfig {
  return {
    name: `Key-${Date.now()}`,        // LUÔN dùng timestamp
    value: 'sk-test-placeholder',
    weight: 1.0,
    models: [],
    ...overrides,
  }
}

export function createCustomProviderData(
  overrides: Partial<CustomProviderConfig> = {}
): CustomProviderConfig {
  return {
    name: `provider-${Date.now()}`,   // Timestamp để tránh collision
    baseProviderType: 'openai',
    baseUrl: 'https://api.example.com/v1',
    authType: 'api_key',
    ...overrides,
  }
}
```

### 3.2 TestDataFactory (từ core)

Ngoài ra có thể dùng `TestDataFactory` từ `core/fixtures/test-data.fixture.ts`:

```typescript
import { testWithData } from '../../core'

testWithData('create vk with budget', async ({ testData }) => {
  const vkConfig = testData.createVirtualKeyWithBudget(
    { maxLimit: 100, resetDuration: '1M' },
    { name: testData.uniqueId('vk') }
  )
  // vkConfig.name = 'vk-<uuid>-1' (unique per test run)
})
```

**Khi nào dùng TestDataFactory vs manual timestamp:**
- `TestDataFactory` → Khi cần nhiều resources unique trong cùng 1 test (counter-based)
- `Date.now()` → Khi chỉ cần 1-2 unique names, simple cases

---

## 4. Spec File Patterns

### 4.1 Cấu trúc file cơ bản

```typescript
// features/my-feature/my-feature.spec.ts
import { expect, test } from '../../core/fixtures/base.fixture'
import { createItemData } from './my-feature.data'

// ─── Module-level resource tracking for cleanup ──────────────────────────────
// CHỈ dùng khi test.describe sử dụng mode: 'serial' và afterEach cleanup
const createdItems: string[] = []

test.describe('My Feature', () => {
  // Serial mode cho features có shared state (sidebar navigation, CRUD)
  test.describe.configure({ mode: 'serial' })

  test.beforeEach(async ({ myFeaturePage }) => {
    await myFeaturePage.goto()
  })

  test.afterEach(async ({ myFeaturePage }) => {
    // Cleanup resources
    for (const itemName of [...createdItems]) {
      try {
        if (await myFeaturePage.itemExists(itemName, 2000)) {
          await myFeaturePage.deleteItem(itemName)
        }
      } catch (error) {
        console.error(`[CLEANUP ERROR] ${itemName}: ${error}`)
      }
    }
    createdItems.length = 0
  })

  test.describe('CRUD', () => {
    test('should create an item', async ({ myFeaturePage }) => {
      const data = createItemData({ name: `Test-Item-${Date.now()}` })
      createdItems.push(data.name)

      await myFeaturePage.createItem(data)

      expect(await myFeaturePage.itemExists(data.name)).toBe(true)
    })

    test('should delete an item', async ({ myFeaturePage }) => {
      // Create first
      const data = createItemData({ name: `Delete-Test-${Date.now()}` })
      await myFeaturePage.createItem(data)

      // Delete (không push vào createdItems vì test này xóa)
      await myFeaturePage.deleteItem(data.name)

      // Verify gone (dùng short timeout)
      expect(await myFeaturePage.itemExists(data.name, 1000)).toBe(false)
    })
  })
})
```

### 4.2 Fixtures trong test function

```typescript
// Import từ core (không import trực tiếp Playwright)
import { test, expect } from '../../core'
// hoặc
import { expect, test } from '../../core/fixtures/base.fixture'

// Available fixtures (đã pre-injected)
test('example', async ({
  page,                // Raw Playwright page
  providersPage,       // ProvidersPage instance
  virtualKeysPage,     // VirtualKeysPage instance
  dashboardPage,       // DashboardPage instance
  sidebarPage,         // SidebarPage instance
  mcpRegistryPage,     // MCPRegistryPage instance
  pluginsPage,         // PluginsPage instance
  governancePage,      // GovernancePage instance
  modelLimitsPage,     // ModelLimitsPage instance
  configSettingsPage,  // ConfigSettingsPage instance
  routingRulesPage,    // RoutingRulesPage instance
  observabilityPage,   // ObservabilityPage instance
  mcpSettingsPage,     // MCPSettingsPage instance
  mcpToolGroupsPage,   // MCPToolGroupsPage instance
  mcpAuthConfigPage,   // MCPAuthConfigPage instance
  logsPage,            // LogsPage instance
  mcpLogsPage,         // MCPLogsPage instance
  request,             // APIRequestContext (for API calls in tests)
}) => {
  // ...
})
```

**Note:** `closeDevProfiler` fixture là **auto fixture** — tự động dismiss Dev Profiler overlay mà không cần khai báo trong function params.

### 4.3 Serial vs Parallel trong cùng file

```typescript
// SERIAL group (cho CRUD operations)
test.describe('Provider Keys', () => {
  test.describe.configure({ mode: 'serial' })
  // Tests trong này chạy tuần tự
})

// PARALLEL group (cho read-only, independent tests)  
test.describe('Provider Views', () => {
  // Không configure → default parallel
  test('should display providers', async ({ providersPage }) => {
    // Independent test
  })
})
```

---

## 5. Các Test Groups của Providers Feature

Đây là ví dụ đầy đủ từ `providers.spec.ts` (786 lines, 30 tests):

| Test Group | Mode | Tests | Focus |
|-----------|------|-------|-------|
| `Provider Navigation` | serial | 3 | Sidebar display, URL params |
| `Provider Keys` | serial | 5 | Key CRUD in key table |
| `Custom Providers` | serial | 5 | Custom provider creation/deletion |
| `Form Validation` | serial | 2 | Required fields validation |
| `Provider Key Management` | serial (separate describe) | 4 | Edit, delete, toggle keys |
| `Provider Configuration` | default | 2 | Config tabs visibility |
| `Performance Tuning` | default | 5 | Concurrency, buffer, raw request |
| `Proxy Configuration` | default | 3 | Proxy types and fields |
| `Network Configuration` | default | 4 | Timeout, retries, backoff |
| `Governance (Budget & Rate Limits)` | default | 4 | Budget/rate UI in provider tab |
| `Debugging Tab` | default | 2 | Debug tab visibility |
| `vLLM Provider` | default | 1 | vLLM-specific key fields |

**Tổng: 40 tests trong 1 feature file**

---

## 6. Newman API Test Structure

### 6.1 Runner Architecture

Mỗi `run-newman-*.sh` runner thực hiện:

```bash
# Pattern chuẩn:
1. echo banner
2. Kiểm tra newman installed
3. Kiểm tra collection file tồn tại
4. Parse command-line flags: --verbose, --html, --json, --bail, --db-verify
5. Build MCP server nếu cần (http-no-ping-server trên :3001)
6. Build plugin .so nếu cần (hello-world)
7. Chạy: newman run <collection> --timeout-script 120000 --timeout 900000 -r cli[,html][,json][,dbverify]
8. Exit code propagation
9. Trap EXIT → cleanup MCP server
```

### 6.2 Newman Options

| Flag | Mô tả | Dùng khi |
|------|-------|---------|
| `--verbose` | Show full request/response | Debugging |
| `--html` | Generate `newman-reports/<name>/report.html` | CI artifacts |
| `--json` | Generate `newman-reports/<name>/report.json` | Programmatic parsing |
| `--all-reports` | cli + html + json | Full CI reporting |
| `--bail` | Stop on first failure | Fail-fast mode |
| `--db-verify` | Enable DB verification après tests | Data integrity checks |
| `--db-url <dsn>` | Override main DB connection | Custom env |
| `--logs-db-url <dsn>` | Override logs DB connection | Custom env |
| `--config-path <p>` | Bifrost config.json path for auto DB detect | Custom config location |

### 6.3 DB Verification Reporter

`newman-reporter-dbverify` là custom reporter verify data tồn tại trong DB sau API call.

```bash
# Install (nếu node_modules chưa có)
cd tests/e2e/api && npm install

# Run với DB verify
./runners/run-newman-api-tests.sh \
  --db-verify \
  --db-url "postgresql://user:pass@localhost:5432/bifrost" \
  --logs-db-url "postgresql://user:pass@localhost:5432/bifrost_logs" \
  --html
```

### 6.4 Integration Test Runners

```bash
# All API integration tests (openai, anthropic, bedrock, composite)
./runners/run-all-integration-tests.sh

# Inference features (streaming, tools, vision, etc.)
./runners/run-newman-inference-features-tests.sh

# Specific provider integration
./runners/individual/run-newman-openai-integration.sh
./runners/individual/run-newman-anthropic-integration.sh
./runners/individual/run-newman-bedrock-integration.sh
```

---

## 7. Đăng ký Feature Page trong Custom Fixture

Khi tạo feature mới, phải đăng ký page object trong `core/fixtures/base.fixture.ts`:

### Bước 1: Tạo Page Object

```typescript
// features/my-feature/pages/my-feature.page.ts
export class MyFeaturePage extends BasePage { ... }
```

### Bước 2: Import trong base.fixture.ts

```typescript
import { MyFeaturePage } from '../../features/my-feature/pages/my-feature.page'
```

### Bước 3: Thêm vào BifrostFixtures type

```typescript
type BifrostFixtures = {
  // ...existing fixtures...
  myFeaturePage: MyFeaturePage    // ← Thêm
}
```

### Bước 4: Thêm fixture factory

```typescript
export const test = base.extend<BifrostFixtures>({
  // ...existing...
  myFeaturePage: async ({ page }, use) => {
    await use(new MyFeaturePage(page))
  },
})
```

### Bước 5: Export từ core/index.ts (nếu cần)

```typescript
export { MyFeaturePage } from './features/my-feature/pages/my-feature.page'
```

---

## 8. Playwright Configuration — Thêm Feature vào Đúng Group

Khi thêm feature mới, phải thêm vào đúng project group trong `playwright.config.ts`:

### Group 1: Chromium (Parallel) — Read-Only hoặc Independent Tests

```typescript
{
  name: 'chromium',
  testMatch: [
    '**/dashboard/**/*.spec.ts',
    '**/logs/**/*.spec.ts',
    '**/my-feature/**/*.spec.ts',  // ← Thêm nếu tests là independent
  ]
}
```

### Group 2: Chromium-Serial — Stateful Tests (Modify Shared Data)

```typescript
{
  name: 'chromium-serial',
  testMatch: [
    '**/providers/**/*.spec.ts',
    '**/virtual-keys/**/*.spec.ts',
    '**/plugins/**/*.spec.ts',
    '**/mcp-registry/**/*.spec.ts',
    '**/model-limits/**/*.spec.ts',
    '**/my-feature/**/*.spec.ts',  // ← Thêm nếu tests modify shared Bifrost state
  ]
}
```

### Group 3: Chromium-Config — Chạy Cuối Cùng

```typescript
{
  name: 'chromium-config',
  testMatch: ['**/config/**/*.spec.ts'],
  dependencies: ['chromium', 'chromium-serial']  // KHÔNG thay đổi
}
```

**Rule:** Nếu test của bạn tạo/xóa providers, VKs, plugins → Serial. Nếu chỉ read → Parallel.

---

## 9. Selector Strategy

### 9.1 Thứ tự ưu tiên selector

```typescript
// 1. BEST: data-testid (explicit, stable)
page.getByTestId('provider-item-openai')
page.getByTestId('create-key-btn')

// 2. GOOD: Role + name (accessible, semantic)
page.getByRole('button', { name: 'Add Key' })
page.getByRole('dialog')
page.getByRole('row').filter({ hasText: 'my-key-name' })

// 3. ACCEPTABLE: Label (for form inputs)
page.getByLabel('API Key Value')
page.getByLabel(/Timeout/i)  // Regex cho case-insensitive

// 4. AVOID: CSS classes/text (brittle)
// page.locator('.btn-primary')           ← AVOID
// page.locator('text=Save Changes')      ← AVOID (use getByRole instead)
```

### 9.2 Selectors thực tế từ providers.spec.ts

```typescript
// Navigation
page.getByTestId(`provider-item-${name}`)     // Sidebar item
page.getByTestId('provider-tab-performance')   // Config tab
page.getByTestId('key-weight-value')           // Key weight in table

// Form elements
page.locator('#providerBudgetMaxLimit')         // Specific form ID (governance tab)
page.locator('#providerTokenMaxLimit')          // Token limit input

// Dynamic role-based
page.getByRole('option', { name: /HTTP/i })    // Dropdown option
page.getByRole('button', { name: 'Dismiss' })  // Dev profiler dismiss
```

---

## 10. Toast Assertions

Bifrost UI dùng **Sonner** toast library. BasePage cung cấp helpers:

```typescript
// Wait for specific toast type
await expect(myFeaturePage.getToast('success')).toBeVisible()
await expect(myFeaturePage.getToast('error')).toHaveText('Validation failed')

// Wait with message check
await myFeaturePage.waitForSuccessToast('Provider created')
await myFeaturePage.waitForErrorToast('Failed to save')

// Cleanup (dismiss before next action)
await myFeaturePage.dismissToasts()
await myFeaturePage.forceCloseToasts()  // Click body + wait auto-dismiss

// Dalam cleanup (skip toast wait để tránh failure)
await providersPage.deleteProvider(providerData.name, { skipToastWait: true })
```

**Anti-pattern toast:**
```typescript
// ❌ WRONG: Brittle text matching
await expect(page.locator('.toast')).toContainText('success')

// ✅ CORRECT: Use typed selectors
await expect(myPage.getToast('success')).toBeVisible({ timeout: 10000 })
```

---

## 11. Chạy E2E Tests Locally

```bash
# Prerequisites: Bifrost dev server running
make dev  # Starts UI (:3000) + API (:8080)

# Run tất cả E2E tests
make run-e2e

# Run specific feature
make run-e2e FLOW=providers
make run-e2e FLOW=virtual-keys
make run-e2e FLOW=plugins

# Run với Playwright UI mode (interactive, visual)
make run-e2e-ui

# Run với visible browser (headed mode)
make run-e2e-headed

# Run single test file
cd tests/e2e && npx playwright test features/providers/providers.spec.ts

# Run specific test by name
cd tests/e2e && npx playwright test --grep "should add a new key"

# Debug mode (pause on fail, devtools)
cd tests/e2e && npx playwright test --debug features/providers/providers.spec.ts
```

**Environment for local run:**
```bash
export BASE_URL=http://localhost:3000
export BIFROST_BASE_URL=http://localhost:8080
# BIFROST_BASE_URL triggers MCP server setup + LLM log seeding (30 logs)
```

---

## 12. CI/CD Integration Details

### 12.1 Trigger Conditions

```yaml
on:
  push:
    paths:
      - 'ui/**'
      - 'tests/e2e/**'
      - '.github/workflows/e2e-tests.yml'
  pull_request:
    paths: [same as above]
  workflow_dispatch:
    inputs:
      flows: 
        description: 'Comma-separated flows (e.g., providers,config,plugins)'
        required: false
```

### 12.2 Per-Flow Job Matrix

Mỗi FLOW chạy như 1 job riêng (parallel):
- `providers` → chromium-serial project
- `virtual-keys` → chromium-serial project  
- `plugins` → chromium-serial project
- `dashboard` → chromium project
- `logs` → chromium project
- `governance` → chromium project
- `config` → chromium-config project (depends on all others)
- etc.

### 12.3 CI Behavior Differences

| Setting | Local | CI |
|---------|-------|-----|
| Retries | 0 | 2 |
| Workers | Unlimited | 1 per job |
| `forbidOnly` | false | **true** (fails if `test.only` left in code) |
| Artifacts | Only on failure | Stored 7 days |
| Web server | Auto-start Next.js | Skip (already running) |
