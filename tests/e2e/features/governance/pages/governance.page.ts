import { Locator, Page } from '@playwright/test'
import { expect } from '../../../core/fixtures/base.fixture'
import { BasePage } from '../../../core/pages/base.page'
import { fillSelect, waitForNetworkIdle } from '../../../core/utils/test-helpers'

export interface TeamConfig {
  name: string
  /** Assign by customer id (from API). Prefer customerName for UI-only flow. */
  customerId?: string
  /** Assign by customer name in the create-team dropdown (UI-only, no API). */
  customerName?: string
  budget?: { maxLimit: number; resetDuration?: string }
  rateLimit?: {
    tokenMaxLimit?: number
    tokenResetDuration?: string
    requestMaxLimit?: number
    requestResetDuration?: string
  }
}

export interface CustomerConfig {
  name: string
  budget?: { maxLimit: number; resetDuration?: string }
  rateLimit?: {
    tokenMaxLimit?: number
    tokenResetDuration?: string
    requestMaxLimit?: number
    requestResetDuration?: string
  }
}

export class GovernancePage extends BasePage {
  // Teams
  readonly teamsCreateBtn: Locator
  readonly teamsTable: Locator
  readonly teamDialog: Locator
  readonly teamNameInput: Locator

  // Customers
  readonly customersCreateBtn: Locator
  readonly customersTable: Locator
  readonly customerDialog: Locator
  readonly customerNameInput: Locator

  constructor(page: Page) {
    super(page)

    this.teamsCreateBtn = page.getByTestId('create-team-btn').or(page.getByTestId('team-button-add'))
    this.teamsTable = page.getByTestId('teams-table')
    this.teamDialog = page.getByTestId('team-dialog-content')
    this.teamNameInput = page.getByTestId('team-name-input')

    this.customersCreateBtn = page.getByTestId('customer-button-create')
    this.customersTable = page.getByTestId('customer-table-container')
    this.customerDialog = page.getByTestId('customer-dialog-content')
    this.customerNameInput = page.getByTestId('customer-name-input')
  }

  async gotoTeams(): Promise<void> {
    await this.page.goto('/workspace/governance/teams')
    await waitForNetworkIdle(this.page)
  }

  async gotoCustomers(): Promise<void> {
    await this.page.goto('/workspace/governance/customers')
    await waitForNetworkIdle(this.page)
  }

  getTeamRow(name: string): Locator {
    return this.page.getByTestId(`team-row-${name}`)
  }

  /** Customer cell for a team row (use for asserting assigned customer in UI). */
  getTeamRowCustomerCell(teamName: string): Locator {
    return this.page.getByTestId(`team-row-${teamName}-customer`)
  }

  async teamExists(name: string): Promise<boolean> {
    const row = this.getTeamRow(name)
    return (await row.count()) > 0
  }

  async createTeam(config: TeamConfig): Promise<void> {
    await this.teamsCreateBtn.click()
    await expect(this.teamDialog).toBeVisible({ timeout: 5000 })
    await this.waitForSheetAnimation()

    await this.teamNameInput.fill(config.name)

    if (config.customerId !== undefined || config.customerName !== undefined) {
      const trigger = this.page.getByTestId('team-customer-select-trigger')
      await trigger.click()
      if (config.customerId === '') {
        await this.page.getByTestId('team-customer-option-none').click()
      } else if (config.customerName !== undefined) {
        const customerOption = this.page
          .locator('[data-testid^="team-customer-option-"]')
          .filter({ hasText: config.customerName })
        await customerOption.waitFor({ state: 'visible', timeout: 5000 })
        await customerOption.click()
      } else if (config.customerId !== undefined && config.customerId !== '') {
        const customerOption = this.page.getByTestId(`team-customer-option-${config.customerId}`)
        await customerOption.waitFor({ state: 'visible', timeout: 5000 })
        await customerOption.click()
      }
    }

    if (config.budget?.maxLimit !== undefined) {
      const budgetInput = this.page.getByTestId('budget-max-limit-input')
      await budgetInput.fill(String(config.budget.maxLimit))
    }

    const saveBtn = this.teamDialog.getByRole('button', { name: /Create Team/i })
    await expect(saveBtn).toBeEnabled()
    await saveBtn.click()
    await this.waitForSuccessToast()
    await expect(this.teamDialog).not.toBeVisible({ timeout: 10000 })
  }

  async deleteTeam(name: string): Promise<void> {
    const deleteBtn = this.page.getByTestId(`team-delete-btn-${name}`)
    await deleteBtn.click()

    const confirmDialog = this.page.locator('[role="alertdialog"]')
    await confirmDialog.getByRole('button', { name: /Delete/i }).click()
    await this.waitForSuccessToast()
    await expect.poll(() => this.teamExists(name), { timeout: 10000 }).toBe(false)
  }

  async deleteTeams(names: string[]): Promise<void> {
    if (names.length === 0) return

    for (const name of names) {
      const row = this.getTeamRow(name)
      await row.waitFor({ state: 'visible', timeout: 10000 })
      await row.getByTestId(`team-checkbox-${name}`).click()
    }

    const bulkDeleteBtn = this.page.getByTestId('teams-bulk-delete-btn')
    await bulkDeleteBtn.waitFor({ state: 'visible', timeout: 5000 })
    await bulkDeleteBtn.click()

    const confirmBtn = this.page.getByTestId('teams-confirm-bulk-delete-btn')
    await confirmBtn.waitFor({ state: 'visible', timeout: 5000 })
    await confirmBtn.click()

    await this.waitForSuccessToast()
    for (const name of names) {
      await expect.poll(() => this.teamExists(name), { timeout: 10000 }).toBe(false)
    }
  }

  async closeTeamDialog(): Promise<void> {
    if (await this.teamDialog.isVisible().catch(() => false)) {
      await this.teamDialog.getByRole('button', { name: /Cancel/i }).click()
      await expect(this.teamDialog).not.toBeVisible({ timeout: 10000 })
    }
  }

  getCustomerRow(name: string): Locator {
    return this.customersTable.getByTestId(`customer-row-${name}`)
  }

  async customerExists(name: string): Promise<boolean> {
    const row = this.getCustomerRow(name)
    return (await row.count()) > 0
  }

  async createCustomer(config: CustomerConfig): Promise<void> {
    await this.customersCreateBtn.click()
    await expect(this.customerDialog).toBeVisible({ timeout: 5000 })
    await this.waitForSheetAnimation()

    await this.customerNameInput.fill(config.name)

    if (config.budget?.maxLimit !== undefined) {
      const budgetInput = this.page.getByTestId('budget-max-limit-input')
      await budgetInput.fill(String(config.budget.maxLimit))
    }

    const saveBtn = this.customerDialog.getByRole('button', { name: /Create Customer/i })
    await expect(saveBtn).toBeEnabled()
    await saveBtn.click()
    await this.waitForSuccessToast()
    await expect(this.customerDialog).not.toBeVisible({ timeout: 10000 })
  }

  async deleteCustomer(name: string): Promise<void> {
    const row = this.getCustomerRow(name)
    const deleteBtn = row.locator('[data-testid^="customer-button-delete-"]')
    await deleteBtn.click()

    const confirmBtn = this.page.getByTestId('customer-button-delete-confirm')
    await confirmBtn.waitFor({ state: 'visible', timeout: 5000 })
    await confirmBtn.click()
    await this.waitForSuccessToast()
    await expect.poll(() => this.customerExists(name), { timeout: 10000 }).toBe(false)
  }

  async deleteCustomers(names: string[]): Promise<void> {
    if (names.length === 0) return

    for (const name of names) {
      const row = this.getCustomerRow(name)
      await row.waitFor({ state: 'visible', timeout: 10000 })
      await row.getByTestId(`customer-checkbox-${name}`).click()
    }

    const bulkDeleteBtn = this.page.getByTestId('customers-bulk-delete-btn')
    await bulkDeleteBtn.waitFor({ state: 'visible', timeout: 5000 })
    await bulkDeleteBtn.click()

    const confirmBtn = this.page.getByTestId('customers-confirm-bulk-delete-btn')
    await confirmBtn.waitFor({ state: 'visible', timeout: 5000 })
    await confirmBtn.click()

    await this.waitForSuccessToast()
    for (const name of names) {
      await expect.poll(() => this.customerExists(name), { timeout: 10000 }).toBe(false)
    }
  }

  async editTeam(name: string, updates: Partial<TeamConfig>): Promise<void> {
    const editBtn = this.page.getByTestId(`team-edit-btn-${name}`)
    await editBtn.click()
    await expect(this.teamDialog).toBeVisible({ timeout: 5000 })
    await this.waitForSheetAnimation()

    if (updates.name) {
      await this.teamNameInput.clear()
      await this.teamNameInput.fill(updates.name)
    }

    if (updates.customerId !== undefined) {
      const trigger = this.page.getByTestId('team-customer-select-trigger')
      await trigger.click()
      if (updates.customerId === '') {
        await this.page.getByTestId('team-customer-option-none').click()
      } else {
        await this.page.getByTestId(`team-customer-option-${updates.customerId}`).click()
      }
    }

    if (updates.budget?.maxLimit !== undefined) {
      const budgetInput = this.page.getByTestId('budget-max-limit-input')
      await budgetInput.clear()
      await budgetInput.fill(String(updates.budget.maxLimit))
    }

    const saveBtn = this.teamDialog.getByRole('button', { name: /Save|Update/i })
    await expect(saveBtn).toBeEnabled()
    await saveBtn.click()
    await this.waitForSuccessToast()
    await expect(this.teamDialog).not.toBeVisible({ timeout: 10000 })
  }

  async editCustomer(name: string, updates: Partial<CustomerConfig>): Promise<void> {
    const row = this.getCustomerRow(name)
    const editBtn = row.locator('[data-testid^="customer-button-edit-"]')
    await editBtn.click()
    await expect(this.customerDialog).toBeVisible({ timeout: 5000 })
    await this.waitForSheetAnimation()

    if (updates.name) {
      await this.customerNameInput.clear()
      await this.customerNameInput.fill(updates.name)
    }

    const saveBtn = this.customerDialog.getByRole('button', { name: /Save|Update/i })
    await expect(saveBtn).toBeEnabled()
    await saveBtn.click()
    await this.waitForSuccessToast()
    await expect(this.customerDialog).not.toBeVisible({ timeout: 10000 })
  }
}
