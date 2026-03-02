import { Locator, Page } from '@playwright/test'
import { expect } from '../../../core/fixtures/base.fixture'
import { BasePage } from '../../../core/pages/base.page'
import { fillSelect, waitForNetworkIdle } from '../../../core/utils/test-helpers'

export interface TeamConfig {
  name: string
  customerId?: string
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
    this.customerDialog = page.getByTestId('customer-dialog')
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

  async teamExists(name: string): Promise<boolean> {
    const row = this.getTeamRow(name)
    return (await row.count()) > 0
  }

  async createTeam(config: TeamConfig): Promise<void> {
    await this.teamsCreateBtn.click()
    await expect(this.teamDialog).toBeVisible({ timeout: 5000 })
    await this.waitForSheetAnimation()

    await this.teamNameInput.fill(config.name)

    if (config.customerId !== undefined) {
      const trigger = this.page.getByTestId('team-customer-select-trigger')
      await trigger.click()
      if (config.customerId === '') {
        await this.page.getByTestId('team-customer-option-none').click()
      } else {
        await this.page.getByTestId(`team-customer-option-${config.customerId}`).click()
      }
    }

    if (config.budget?.maxLimit !== undefined) {
      const budgetInput = this.page.getByTestId('budget-max-limit-input')
      await budgetInput.fill(String(config.budget.maxLimit))
    }

    const saveBtn = this.page.getByRole('button', { name: /Create Team/i })
    await saveBtn.click()
    await this.waitForSuccessToast()
    await expect(this.teamDialog).not.toBeVisible({ timeout: 5000 })
  }

  async deleteTeam(name: string): Promise<void> {
    const deleteBtn = this.page.getByTestId(`team-delete-btn-${name}`)
    await deleteBtn.click()

    const confirmDialog = this.page.locator('[role="alertdialog"]')
    await confirmDialog.getByRole('button', { name: /Delete/i }).click()
    await this.waitForSuccessToast()
  }

  async closeTeamDialog(): Promise<void> {
    if (await this.teamDialog.isVisible().catch(() => false)) {
      await this.page.getByRole('button', { name: /Cancel/i }).click()
      await expect(this.teamDialog).not.toBeVisible({ timeout: 5000 })
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

    const saveBtn = this.page.getByRole('button', { name: /Create Customer/i })
    await saveBtn.click()
    await this.waitForSuccessToast()
    await expect(this.customerDialog).not.toBeVisible({ timeout: 5000 })
  }

  async deleteCustomer(name: string): Promise<void> {
    const row = this.getCustomerRow(name)
    const deleteBtn = row.locator('[data-testid^="customer-button-delete-"]')
    await deleteBtn.click()

    const confirmBtn = this.page.getByTestId('customer-button-delete-confirm')
    await confirmBtn.waitFor({ state: 'visible', timeout: 5000 })
    await confirmBtn.click()
    await this.waitForSuccessToast()
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

    const saveBtn = this.page.getByRole('button', { name: /Save|Update/i })
    await saveBtn.click()
    await this.waitForSuccessToast()
    await expect(this.teamDialog).not.toBeVisible({ timeout: 5000 })
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

    const saveBtn = this.page.getByRole('button', { name: /Save|Update/i })
    await saveBtn.click()
    await this.waitForSuccessToast()
    await expect(this.customerDialog).not.toBeVisible({ timeout: 5000 })
  }
}
