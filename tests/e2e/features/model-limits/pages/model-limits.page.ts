import type { Locator, Page } from '@playwright/test'
import { expect } from '../../../core/fixtures/base.fixture'
import { BasePage } from '../../../core/pages/base.page'
import { fillRequestLimitLines, fillSelect, waitForNetworkIdle } from '../../../core/utils/test-helpers'

const resetPeriodLabels: Record<string, string> = {
  '1m': 'Every Minute',
  '5m': 'Every 5 Minutes',
  '15m': 'Every 15 Minutes',
  '30m': 'Every 30 Minutes',
  '1h': 'Hourly',
  '6h': 'Every 6 Hours',
  '1d': 'Daily',
  '1w': 'Weekly',
  '1M': 'Monthly',
}

export interface ModelLimitConfig {
  provider: string
  modelName: string
  budget?: { maxLimit: number; resetDuration?: string }
  rateLimit?: {
    tokenMaxLimit?: number
    requestMaxLimit?: number
  }
}

/** A model config as GET /api/governance/model-configs returns it, with the fields these tests read. */
export interface ModelConfigRow {
  id: string
  model_name: string
  provider?: string
  budgets?: { max_limit: number; reset_duration: string; scope?: string }[]
  rate_limit?: { token_max_limit?: number; token_reset_duration?: string }
  request_limits?: { request_max_limit: number; request_reset_duration: string; scope?: string }[]
}

function toTestIdPart(value: string): string {
  return value.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '')
}

function escapeRegex(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
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
    return await row.isVisible({ timeout: 5000 }).catch(() => false)
  }

  private async openModelLimitActions(modelName: string, provider: string = 'all'): Promise<void> {
    const row = this.getModelLimitRow(modelName, provider)
    await expect(row).toBeVisible({ timeout: 10000 })
    await row.scrollIntoViewIfNeeded()

    const actionsBtn = this.page.getByTestId(
      `model-limit-button-actions-${toTestIdPart(modelName)}-${toTestIdPart(provider)}`
    )
    await actionsBtn.waitFor({ state: 'visible', timeout: 10000 })
    await actionsBtn.scrollIntoViewIfNeeded()
    await actionsBtn.click()
  }

  private async preserveBudgetUsageIfPrompted(): Promise<void> {
    const dialog = this.page.getByTestId('model-limit-budget-reset-dialog')
    const dialogVisible = dialog.waitFor({ state: 'visible', timeout: 5000 }).then(() => 'dialog' as const)
    const sheetClosed = this.sheet.waitFor({ state: 'hidden', timeout: 5000 }).then(() => 'sheet' as const)

    let winner: 'dialog' | 'sheet'
    try {
      winner = await Promise.race([dialogVisible, sheetClosed])
    } catch {
      return
    }
    if (winner !== 'dialog') return

    await this.page.getByTestId('model-limit-budget-reset-dialog-preserve-btn').click()
    await dialog.waitFor({ state: 'hidden', timeout: 3000 })
  }

  private async waitForSheetClosedAfterSave(): Promise<void> {
    const closed = await expect(this.sheet)
      .not.toBeVisible({ timeout: 5000 })
      .then(() => true)
      .catch(() => false)
    if (!closed) {
      await this.page.keyboard.press('Escape')
      await expect(this.sheet).not.toBeVisible({ timeout: 5000 })
    }
  }

  private async setBudget(config?: { maxLimit: number; resetDuration?: string }): Promise<void> {
    if (!config) return

    const budgetLines = this.page.locator('[data-testid^="model-limit-budget-lines-line-"]')
    const existingCount = await budgetLines.count()
    if (existingCount === 0) {
      await this.page.getByTestId('model-limit-budget-lines-add-btn').click()
    }

    const budgetInput = this.page.getByTestId('model-limit-budget-lines-amount-0')
    await budgetInput.click()
    await budgetInput.fill('')
    await budgetInput.pressSequentially(String(config.maxLimit))

    if (config.resetDuration) {
      const resetPeriodLabel = resetPeriodLabels[config.resetDuration]
      if (!resetPeriodLabel) {
        throw new Error(`unsupported resetDuration: ${config.resetDuration}`)
      }
      // Every budget line also shows the scope select after the reset period.
      const budgetLine = this.page.getByTestId('model-limit-budget-lines-line-0')
      await budgetLine.getByRole('combobox').first().click()
      await this.page.getByRole('option', { name: resetPeriodLabel }).click()
    }
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

    // Model select - type and pick the requested model from the results
    const modelSelectContainer = this.sheet.getByTestId('model-limit-model-select')
    const modelInput = modelSelectContainer.getByRole('combobox')
    await modelInput.click()
    // ModelSelector searches server-side from the input inside its popover.
    await this.page.getByPlaceholder('Search models...').fill(config.modelName)
    const targetOption = this.page.getByRole('option', { name: config.modelName, exact: true }).first()
    await expect(targetOption).toBeVisible({ timeout: 10000 })
    await targetOption.click()
    await this.page.keyboard.press('Tab')
    const selectedModelName = config.modelName

    await this.setBudget(config.budget)

    if (config.rateLimit?.tokenMaxLimit !== undefined) {
      await this.page.locator('#modelTokenMaxLimit').fill(String(config.rateLimit.tokenMaxLimit))
    }
    if (config.rateLimit?.requestMaxLimit !== undefined) {
      await fillRequestLimitLines(this.page, 'model-limit-request-limit-lines', [{ maxLimit: config.rateLimit.requestMaxLimit }])
    }

    const saveBtn = this.page.getByRole('button', { name: /Create Limit/i })
    await expect(saveBtn).toBeEnabled({ timeout: 10000 })
    await saveBtn.click()
    await this.waitForSheetClosedAfterSave()
    await expect(this.getModelLimitRow(selectedModelName, config.provider)).toBeVisible({ timeout: 15000 })
    return selectedModelName
  }

  async editModelLimit(modelName: string, provider: string, updates: Partial<ModelLimitConfig>): Promise<void> {
    await this.openModelLimitActions(modelName, provider)
    const editBtn = this.page.getByTestId(`model-limit-button-edit-${toTestIdPart(modelName)}-${toTestIdPart(provider)}`)
    await editBtn.waitFor({ state: 'visible', timeout: 10000 })
    await editBtn.click()
    await expect(this.sheet).toBeVisible({ timeout: 5000 })
    await this.waitForSheetAnimation()

    await this.setBudget(updates.budget)

    if (updates.rateLimit?.tokenMaxLimit !== undefined) {
      const tokenInput = this.page.locator('#modelTokenMaxLimit')
      await tokenInput.clear()
      await tokenInput.fill(String(updates.rateLimit.tokenMaxLimit))
    }
    if (updates.rateLimit?.requestMaxLimit !== undefined) {
      // Line 0 is updated in place, keeping its counter, or added when the limit had none.
      await fillRequestLimitLines(this.page, 'model-limit-request-limit-lines', [{ maxLimit: updates.rateLimit.requestMaxLimit }])
    }

    const saveBtn = this.page.getByRole('button', { name: /Save Changes|Create Limit/i })
    await expect(saveBtn).toBeEnabled({ timeout: 10000 })
    await saveBtn.click()
    await this.preserveBudgetUsageIfPrompted()
    await this.waitForSheetClosedAfterSave()
    await expect(this.getModelLimitRow(modelName, provider)).toBeVisible({ timeout: 15000 })
  }

  async deleteModelLimit(modelName: string, provider: string = 'all'): Promise<void> {
    const exists = await this.modelLimitExists(modelName, provider)
    if (!exists) return

    await this.openModelLimitActions(modelName, provider)
    const deleteBtn = this.page.getByTestId(`model-limit-button-delete-${toTestIdPart(modelName)}-${toTestIdPart(provider)}`)
    await deleteBtn.waitFor({ state: 'visible', timeout: 10000 })
    await deleteBtn.click()

    const confirmDialog = this.page.locator('[role="alertdialog"]')
    await confirmDialog.getByRole('button', { name: /Delete/i }).click()
    await expect(this.getModelLimitRow(modelName, provider)).not.toBeVisible({ timeout: 15000 })
  }

  /** The deployment's model config for a model ('*' for all) and provider ('all' for none), if any. */
  async getGlobalModelConfig(modelName: string, provider: string): Promise<ModelConfigRow | undefined> {
    const res = await this.page.request.get(`/api/governance/model-configs?scope=global&search=${encodeURIComponent(modelName)}&limit=100`)
    expect(res.status(), await res.text()).toBe(200)
    const rows = ((await res.json()).model_configs ?? []) as ModelConfigRow[]
    return rows.find((mc) => mc.model_name === modelName && (mc.provider || 'all') === provider)
  }

  /** Removes the deployment's model config for a tier: what a test created there, once it is done with it. */
  async removeGlobalModelConfig(modelName: string, provider: string): Promise<void> {
    const current = await this.getGlobalModelConfig(modelName, provider)
    if (current) {
      await this.page.request.delete(`/api/governance/model-configs/${current.id}`)
    }
  }

  /** Opens the create sheet on a tier: a provider ('all' keeps All Providers) and a model ('*' keeps All Models). */
  async openCreateSheet(modelName: string, provider: string): Promise<void> {
    await this.createBtn.click()
    await expect(this.sheet).toBeVisible({ timeout: 5000 })
    await this.waitForSheetAnimation()
    if (provider !== 'all') {
      await fillSelect(this.page, '[data-testid="model-limit-provider-select"]', provider)
    }
    if (modelName !== '*') {
      await this.sheet.getByTestId('model-limit-model-select').getByRole('combobox').click()
      await this.page.getByPlaceholder('Search models...').fill(modelName)
      const option = this.page.getByRole('option', { name: modelName, exact: true }).first()
      await expect(option).toBeVisible({ timeout: 10000 })
      await option.click()
      await this.page.keyboard.press('Tab')
    }
  }

  /** Opens the edit sheet of a listed model limit. */
  async openEditSheet(modelName: string, provider: string): Promise<void> {
    await this.openModelLimitActions(modelName, provider)
    const editBtn = this.page.getByTestId(`model-limit-button-edit-${toTestIdPart(modelName)}-${toTestIdPart(provider)}`)
    await editBtn.waitFor({ state: 'visible', timeout: 10000 })
    await editBtn.click()
    await expect(this.sheet).toBeVisible({ timeout: 5000 })
    await this.waitForSheetAnimation()
  }

  async closeSheet(): Promise<void> {
    if (await this.sheet.isVisible().catch(() => false)) {
      await this.page.keyboard.press('Escape')
      await expect(this.sheet).not.toBeVisible({ timeout: 5000 }).catch(() => {})
    }
  }
}