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
    return this.page.getByTestId(`model-limit-row-${modelName}-${provider}`)
  }

  async modelLimitExists(modelName: string, provider: string = 'all'): Promise<boolean> {
    const row = this.getModelLimitRow(modelName, provider)
    return (await row.count()) > 0
  }

  async createModelLimit(config: ModelLimitConfig): Promise<void> {
    await this.createBtn.click()
    await expect(this.sheet).toBeVisible({ timeout: 5000 })
    await this.waitForSheetAnimation()

    // Select provider
    await fillSelect(this.page, '[role="combobox"]', config.provider === 'all' ? 'All Providers' : config.provider)

    // Model multiselect - type model name and select
    const modelInput = this.sheet.getByPlaceholder(/Search for a model/i)
    await modelInput.fill(config.modelName)
    await this.page.waitForSelector('[role="option"]', { timeout: 5000 })
    await this.page.getByRole('option').filter({ hasText: config.modelName }).first().click()

    if (config.budget?.maxLimit !== undefined) {
      const budgetInput = this.page.locator('#modelBudgetMaxLimit')
      await budgetInput.fill(String(config.budget.maxLimit))
    }

    const saveBtn = this.page.getByRole('button', { name: /Create Model Limit/i })
    await saveBtn.click()
    await this.waitForSuccessToast()
    await expect(this.sheet).not.toBeVisible({ timeout: 10000 })
  }

  async editModelLimit(modelName: string, provider: string, updates: Partial<ModelLimitConfig>): Promise<void> {
    const editBtn = this.page.getByTestId(`model-limit-button-edit-${modelName}-${provider}`)
    await editBtn.click()
    await expect(this.sheet).toBeVisible({ timeout: 5000 })
    await this.waitForSheetAnimation()

    if (updates.budget?.maxLimit !== undefined) {
      const budgetInput = this.page.locator('#modelBudgetMaxLimit')
      await budgetInput.clear()
      await budgetInput.fill(String(updates.budget.maxLimit))
    }

    const saveBtn = this.page.getByRole('button', { name: /Save Changes|Create Limit/i })
    await saveBtn.click()
    await this.waitForSuccessToast()
    await expect(this.sheet).not.toBeVisible({ timeout: 10000 })
  }

  async deleteModelLimit(modelName: string, provider: string = 'all'): Promise<void> {
    const deleteBtn = this.page.getByTestId(`model-limit-button-delete-${modelName}-${provider}`)
    await deleteBtn.click()

    const confirmDialog = this.page.locator('[role="alertdialog"]')
    await confirmDialog.getByRole('button', { name: /Delete/i }).click()
    await this.waitForSuccessToast()
  }

  async closeSheet(): Promise<void> {
    if (await this.sheet.isVisible().catch(() => false)) {
      await this.page.keyboard.press('Escape')
      await expect(this.sheet).not.toBeVisible({ timeout: 5000 }).catch(() => {})
    }
  }
}
