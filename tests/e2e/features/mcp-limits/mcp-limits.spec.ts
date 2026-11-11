import type { APIRequestContext, Page } from '@playwright/test'
import { expect, test } from '../../core/fixtures/base.fixture'

// The MCP page lists holder-level rows (all models, no provider) by holder: the deployment's has this row id.
const GLOBAL_ROW = 'mcp-limit-row-global-all'

type BudgetRow = { id: string; max_limit: number; reset_duration: string; scope?: string }
type ModelConfigRow = {
  id: string
  model_name: string
  provider?: string
  budgets?: BudgetRow[]
  rate_limit?: { token_max_limit?: number; token_reset_duration?: string; request_max_limit?: number; request_reset_duration?: string }
}

async function globalHolderLevelRow(request: APIRequestContext): Promise<ModelConfigRow | undefined> {
  const res = await request.get('/api/governance/model-configs?scope=global&search=*&limit=100')
  expect(res.status(), await res.text()).toBe(200)
  const rows = ((await res.json()).model_configs ?? []) as ModelConfigRow[]
  return rows.find((mc) => mc.model_name === '*' && !mc.provider)
}

async function createVirtualKey(request: APIRequestContext, name: string, budgets: object[]): Promise<string> {
  const res = await request.post('/api/governance/virtual-keys', { data: { name, budgets } })
  expect(res.status(), await res.text()).toBe(200)
  return (await res.json()).virtual_key.id
}

async function virtualKeyBudgets(request: APIRequestContext, vkID: string): Promise<BudgetRow[]> {
  const res = await request.get(`/api/governance/virtual-keys/${vkID}`)
  expect(res.status(), await res.text()).toBe(200)
  return ((await res.json()).virtual_key.budgets ?? []) as BudgetRow[]
}

const scopesOf = (budgets: BudgetRow[]) => budgets.map((b) => `${b.scope ?? 'shared'}:${b.max_limit}`).sort()

async function saveSheet(page: Page): Promise<void> {
  await page.getByTestId('model-limit-button-submit').click()
  // Editing a budget asks whether to keep its spend; keep it.
  const keep = page.getByTestId('model-limit-budget-reset-dialog-preserve-btn')
  if (await keep.isVisible({ timeout: 2000 }).catch(() => false)) {
    await keep.click()
  }
  await expect(page.getByTestId('model-limit-sheet')).not.toBeVisible({ timeout: 10000 })
}

test.describe('MCP Budgets & Limits', () => {
  test("adds a Shared line on a key's own limits that the key sheet shows, keeping its Model line", async ({ page }) => {
    const name = `e2e-mcp-shared-${Date.now()}`
    const vkID = await createVirtualKey(page.request, name, [
      { max_limit: 30, reset_duration: '1M', scope: 'model' },
      { max_limit: 3, reset_duration: '1d', scope: 'mcp' },
    ])
    const modelLineID = (await virtualKeyBudgets(page.request, vkID)).find((b) => b.scope === 'model')?.id
    expect(modelLineID).toBeTruthy()
    try {
      await page.goto('/workspace/mcp-limits')
      const row = page.getByTestId(`mcp-limit-row-virtual-key-${vkID}`)
      await expect(row).toBeVisible()
      // The MCP page shows the lines MCP tool executions are charged by; the Model line is the Models page's.
      await expect(row).toContainText('$3')
      await expect(row).not.toContainText('$30')

      await page.getByTestId(`mcp-limit-button-actions-virtual-key-${vkID}`).click()
      await page.getByTestId(`mcp-limit-button-edit-virtual-key-${vkID}`).click()
      const sheet = page.getByTestId('model-limit-sheet')
      await expect(sheet).toBeVisible()
      // A holder-level row names no model or provider, and the MCP page edits budgets only.
      await expect(sheet.getByTestId('model-limit-provider-select')).toHaveCount(0)
      await expect(sheet.getByTestId('model-limit-model-select')).toHaveCount(0)
      await expect(sheet.locator('#modelRequestMaxLimit')).toHaveCount(0)
      await expect(sheet.locator('[data-testid^="model-limit-budget-lines-line-"]')).toHaveCount(1)

      // A new line is MCP by default, and the page offers Shared and MCP only.
      await sheet.getByTestId('model-limit-budget-lines-add-btn').click()
      await expect(sheet.getByTestId('model-limit-budget-lines-scope-1-trigger')).toHaveText('MCP')
      await sheet.getByTestId('model-limit-budget-lines-scope-1-trigger').click()
      await expect(page.getByTestId('model-limit-budget-lines-scope-1-option-mcp')).toBeVisible()
      await expect(page.getByTestId('model-limit-budget-lines-scope-1-option-model')).toHaveCount(0)
      await page.getByTestId('model-limit-budget-lines-scope-1-option-shared').click()
      await sheet.getByTestId('model-limit-budget-lines-amount-1').fill('40')
      await saveSheet(page)

      await expect(row).toContainText('$40')
      await expect(row.getByTestId('budget-scope-badge-shared')).toHaveCount(1)

      // One set of limits: the Model line keeps its row, and the Shared line is one of the key's own budgets.
      const budgets = await virtualKeyBudgets(page.request, vkID)
      expect(scopesOf(budgets)).toEqual(['mcp:3', 'model:30', 'shared:40'])
      expect(budgets.find((b) => b.scope === 'model')?.id).toBe(modelLineID)

      await page.goto('/workspace/virtual-keys')
      await page.getByTestId('vk-search-input').fill(name)
      await page.getByTestId(`vk-actions-btn-${name}`).click()
      await page.getByTestId(`vk-edit-btn-${name}`).click()
      const vkSheet = page.getByTestId('vk-sheet-content')
      await expect(vkSheet).toBeVisible()
      await expect(vkSheet.locator('[data-testid^="vk-budget-lines-line-"]')).toHaveCount(3)
      const shared = vkSheet
        .locator('[data-testid^="vk-budget-lines-line-"]')
        .filter({ has: page.getByTestId(/^vk-budget-lines-scope-\d+-trigger$/).filter({ hasText: 'Shared' }) })
      await expect(shared).toHaveCount(1)
      await expect(shared.locator('input').first()).toHaveValue('40')
    } finally {
      await page.request.delete(`/api/governance/virtual-keys/${vkID}`)
    }
  })

  test("removing a key's MCP limits keeps the lines the Models page edits", async ({ page }) => {
    const vkID = await createVirtualKey(page.request, `e2e-mcp-remove-${Date.now()}`, [
      { max_limit: 30, reset_duration: '1M', scope: 'model' },
      { max_limit: 20, reset_duration: '1M' },
      { max_limit: 3, reset_duration: '1d', scope: 'mcp' },
    ])
    try {
      await page.goto('/workspace/mcp-limits')
      const row = page.getByTestId(`mcp-limit-row-virtual-key-${vkID}`)
      await expect(row).toBeVisible()
      await page.getByTestId(`mcp-limit-button-actions-virtual-key-${vkID}`).click()
      await page.getByTestId(`mcp-limit-button-delete-virtual-key-${vkID}`).click()
      await page.locator('[role="alertdialog"]').getByRole('button', { name: /Delete/i }).click()
      await expect(row).not.toBeVisible({ timeout: 10000 })

      expect(scopesOf(await virtualKeyBudgets(page.request, vkID))).toEqual(['model:30'])
    } finally {
      await page.request.delete(`/api/governance/virtual-keys/${vkID}`)
    }
  })

  test.describe('global', () => {
    // The deployment has one holder-level row. These tests create it and remove it, so they leave a
    // server that already has one alone instead of replacing limits they cannot restore exactly.
    test.beforeEach(async ({ page }) => {
      test.skip(!!(await globalHolderLevelRow(page.request)), 'the server already has global limits; this spec would replace them')
    })

    test.afterEach(async ({ page }) => {
      const current = await globalHolderLevelRow(page.request)
      if (current) {
        await page.request.delete(`/api/governance/model-configs/${current.id}`)
      }
    })

    test('adds an MCP budget to the deployment\'s own limits', async ({ page }) => {
      await page.goto('/workspace/mcp-limits')
      await page.getByTestId('mcp-limits-button-create').click()
      const sheet = page.getByTestId('model-limit-sheet')
      await expect(sheet).toBeVisible()
      await expect(sheet.getByTestId('model-limit-model-select')).toHaveCount(0)
      await sheet.getByTestId('model-limit-budget-lines-add-btn').click()
      await expect(sheet.getByTestId('model-limit-budget-lines-scope-0-trigger')).toHaveText('MCP')
      await sheet.getByTestId('model-limit-budget-lines-amount-0').fill('5')
      await saveSheet(page)

      await expect(page.getByTestId(GLOBAL_ROW)).toContainText('$5')
      await page.reload()
      await expect(page.getByTestId(GLOBAL_ROW)).toContainText('$5')
      // Stored on the deployment's holder-level row, the one the Models page's all-models limit edits.
      const stored = await globalHolderLevelRow(page.request)
      expect(stored?.budgets?.map((b) => `${b.scope}:${b.max_limit}`)).toEqual(['mcp:5'])
    })
  })
})
