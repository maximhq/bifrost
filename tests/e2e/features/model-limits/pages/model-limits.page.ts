import type { Locator, Page } from '@playwright/test'
import { expect } from '../../../core/fixtures/base.fixture'
import { BasePage } from '../../../core/pages/base.page'
import { fillSelect, waitForNetworkIdle } from '../../../core/utils/test-helpers'

export interface ModelLimitConfig {
  provider: string
  modelName: string
  budget?: { maxLimit: number; resetDuration?: string }
  rateLimit?: {
    tokenMaxLimit?: number
    requestMaxLimit?: number
  }
}

function toTestIdPart(value: string): string {
  return value.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '')
}

export class ModelLimitsPage extends BasePage {
  readonly createBtn: Locator
  readonly table: Locator
  readonly sheet: Locator

  constructor(page: Page) {
    super(page)

    this.createBtn = page.getByTestId('model-limits-button-create')
    this.table = page.getByTestId('model-limits-table')
    this.sheet = page.getByTestId('model-limit-sheet')
  }

  async goto(): Promise<void> {
    await this.page.goto('/workspace/model-limits')
    await waitForNetworkIdle(this.page)
  }

  getModelLimitRow(modelName: string, provider: string = 'all'): Locator {
    return this.page.getByTestId(`model-limit-row-${toTestIdPart(modelName)}-${toTestIdPart(provider)}`)
  }

  async modelLimitExists(modelName: string, provider: string = 'all'): Promise<boolean> {
    const row = this.getModelLimitRow(modelName, provider)
    return (await row.count()) > 0
  }

  /**
   * Create a model limit via the sheet: selects provider, selects the requested
   * model (config.modelName) in the search dropdown, fills budget and rate
   * limit, then saves. Returns the selected model name for use in exists/edit/delete.
   */
  async createModelLimit(config: ModelLimitConfig): Promise<string> {
    await this.createBtn.click()
    await expect(this.sheet).toBeVisible({ timeout: 5000 })
    await this.waitForSheetAnimation()

    // Select provider (stable testid; sheet is the only one open)
    await fillSelect(
      this.page,
      '[data-testid="model-limit-provider-select"]',
      config.provider === 'all' ? 'All Providers' : config.provider
    )

    // Model multiselect - search and select requested model deterministically
    const modelSelectContainer = this.sheet.getByTestId('model-limit-model-select')
    const modelInput = modelSelectContainer.locator('input')
    await modelInput.fill(config.modelName)
    await this.page.waitForSelector('[role="option"]', { timeout: 10000 })
    const targetOption = this.page.getByRole('option', { name: config.modelName, exact: true })
    await expect(targetOption).toBeVisible({ timeout: 10000 })
    await targetOption.click()
    const selectedModelName = config.modelName

    if (config.budget?.maxLimit !== undefined) {
      const budgetInput = this.page.locator('#modelBudgetMaxLimit')
      await budgetInput.fill(String(config.budget.maxLimit))
    }

    if (config.rateLimit?.tokenMaxLimit !== undefined) {
      await this.page.locator('#modelTokenMaxLimit').fill(String(config.rateLimit.tokenMaxLimit))
    }
    if (config.rateLimit?.requestMaxLimit !== undefined) {
      await this.page.locator('#modelRequestMaxLimit').fill(String(config.rateLimit.requestMaxLimit))
    }

    const saveBtn = this.page.getByRole('button', { name: /Create Limit/i })
    await saveBtn.click()
    await this.waitForSuccessToast()
    await expect(this.sheet).not.toBeVisible({ timeout: 10000 })
    return selectedModelName
  }

  async editModelLimit(modelName: string, provider: string, updates: Partial<ModelLimitConfig>): Promise<void> {
    const editBtn = this.page.getByTestId(`model-limit-button-edit-${toTestIdPart(modelName)}-${toTestIdPart(provider)}`)
    await editBtn.click()
    await expect(this.sheet).toBeVisible({ timeout: 5000 })
    await this.waitForSheetAnimation()

    if (updates.budget?.maxLimit !== undefined) {
      const budgetInput = this.page.locator('#modelBudgetMaxLimit')
      await budgetInput.clear()
      await budgetInput.fill(String(updates.budget.maxLimit))
    }

    if (updates.rateLimit?.tokenMaxLimit !== undefined) {
      const tokenInput = this.page.locator('#modelTokenMaxLimit')
      await tokenInput.clear()
      await tokenInput.fill(String(updates.rateLimit.tokenMaxLimit))
    }
    if (updates.rateLimit?.requestMaxLimit !== undefined) {
      const requestInput = this.page.locator('#modelRequestMaxLimit')
      await requestInput.clear()
      await requestInput.fill(String(updates.rateLimit.requestMaxLimit))
    }

    const saveBtn = this.page.getByRole('button', { name: /Save Changes|Create Limit/i })
    await saveBtn.click()
    await this.waitForSuccessToast()
    await expect(this.sheet).not.toBeVisible({ timeout: 10000 })
  }

  async deleteModelLimit(modelName: string, provider: string = 'all'): Promise<void> {
    const deleteBtn = this.page.getByTestId(`model-limit-button-delete-${toTestIdPart(modelName)}-${toTestIdPart(provider)}`)
    await deleteBtn.click()

    const confirmDialog = this.page.locator('[role="alertdialog"]')
    await confirmDialog.getByRole('button', { name: /Delete/i }).click()
    await this.waitForSuccessToast()
    await this.page.waitForTimeout(1000)
  }

  async closeSheet(): Promise<void> {
    if (await this.sheet.isVisible().catch(() => false)) {
      await this.page.keyboard.press('Escape')
      await expect(this.sheet).not.toBeVisible({ timeout: 5000 }).catch(() => {})
    }
  }
}
