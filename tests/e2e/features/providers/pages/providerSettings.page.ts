import { Locator, Page, expect } from '@playwright/test'
import { BasePage } from '../../../core/pages/base.page'

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

/**
 * Page object for the provider configuration sheet's tabs (Performance, Proxy,
 * Network, Governance, Debugging). Paired with ProvidersPage in tests that mutate
 * the shared singleton provider row's config, which is why those specs stay serial.
 */
export class ProviderSettingsPage extends BasePage {
  constructor(page: Page) {
    super(page)
  }

  // ============================================
  // Provider Configuration Methods
  // ============================================

  /**
   * Open the provider configuration sheet
   */
  async openConfigSheet(): Promise<void> {
    // If the config sheet is already open, just return
    const dialog = this.page.locator('[role="dialog"]')
    if (await dialog.isVisible().catch(() => false)) {
      return
    }
    const editConfigBtn = this.page.getByRole('button', { name: /Edit Provider Config/i })
    await editConfigBtn.waitFor({ state: 'visible', timeout: 10000 })
    await editConfigBtn.click()
    // Wait for the sheet to appear (SheetContent renders with role="dialog")
    await dialog.waitFor({ state: 'visible' })
    await this.waitForSheetAnimation()
  }

  /**
   * Select a configuration tab
   */
  async selectConfigTab(tabName: 'network' | 'proxy' | 'performance' | 'governance' | 'debugging' | 'web-search'): Promise<void> {
    await this.openConfigSheet()

    const tab = this.page.getByTestId(`provider-tab-${tabName}`)
    // Tabs that don't fit collapse into an overflow menu without their testid.
    const overflow = this.page.getByTestId('tabs-overflow-trigger')
    await tab.or(overflow).first().waitFor()
    if (await tab.isVisible()) {
      await tab.click()
    } else {
      await overflow.click()
      await this.page.getByRole('menuitem', { name: new RegExp(tabName.replace('-', ' '), 'i') }).click()
    }
    await expect(this.page.getByRole('tabpanel')).toBeVisible()
  }

  /**
   * Get the save button for the current config tab
   */
  getConfigSaveBtn(configType: 'network' | 'proxy' | 'performance' | 'governance' | 'debugging'): Locator {
    const buttonNames: Record<string, string> = {
      network: 'Save Network Configuration',
      proxy: 'Save Proxy Configuration',
      performance: 'Save Performance Configuration',
      governance: 'Save Governance Configuration',
      debugging: 'Save Debugging Configuration',
    }
    return this.page.getByRole('button', { name: buttonNames[configType] })
  }

  // ============================================
  // Performance Configuration
  // ============================================

  /**
   * Get concurrency input
   */
  getConcurrencyInput(): Locator {
    return this.page.getByLabel('Concurrency')
  }

  /**
   * Get buffer size input
   */
  getBufferSizeInput(): Locator {
    return this.page.getByLabel('Buffer Size')
  }

  /**
   * Get raw request switch (Debugging tab: "Send Back Raw Request")
   */
  getRawRequestSwitch(): Locator {
    return this.page.getByLabel('Send Back Raw Request').locator('..').locator('button[role="switch"]')
  }

  /**
   * Get raw response switch (Debugging tab: "Send Back Raw Response")
   */
  getRawResponseSwitch(): Locator {
    return this.page.getByLabel('Send Back Raw Response').locator('..').locator('button[role="switch"]')
  }

  /**
   * Fill a React controlled number input by using the native value setter
   * and dispatching an input event. This bypasses React's value tracker
   * to reliably update controlled input components.
   */
  async fillNumberInput(input: Locator, value: string): Promise<void> {
    await input.click()
    await input.press('ControlOrMeta+a')
    await input.pressSequentially(value)
    await input.blur()
  }

  /**
   * Save performance configuration and wait for success toast
   */
  async savePerformanceConfig(): Promise<void> {
    const saveBtn = this.getConfigSaveBtn('performance')
    await saveBtn.click()
    await this.waitForSuccessToast()
  }

  /**
   * Save network configuration and wait for success toast
   */
  async saveNetworkConfig(): Promise<void> {
    const saveBtn = this.getConfigSaveBtn('network')
    await saveBtn.click()
    await this.waitForSuccessToast()
  }

  /**
   * Save debugging configuration and wait for success toast
   */
  async saveDebuggingConfig(): Promise<void> {
    const saveBtn = this.getConfigSaveBtn('debugging')
    await saveBtn.click()
    await this.waitForSuccessToast()
  }

  /**
   * Set performance configuration (concurrency, buffer size only).
   * For raw request/response toggles use setDebuggingConfig.
   */
  async setPerformanceConfig(config: {
    concurrency?: number
    bufferSize?: number
  }): Promise<void> {
    await this.selectConfigTab('performance')

    if (config.concurrency !== undefined) {
      const input = this.getConcurrencyInput()
      await this.fillNumberInput(input, String(config.concurrency))
    }

    if (config.bufferSize !== undefined) {
      const input = this.getBufferSizeInput()
      await this.fillNumberInput(input, String(config.bufferSize))
    }
  }

  /**
   * Set debugging configuration (raw request/response toggles).
   */
  async setDebuggingConfig(config: { rawRequest?: boolean; rawResponse?: boolean }): Promise<void> {
    await this.selectConfigTab('debugging')

    if (config.rawRequest !== undefined) {
      const switchEl = this.getRawRequestSwitch()
      const isChecked = (await switchEl.getAttribute('data-state')) === 'checked'
      if (isChecked !== config.rawRequest) {
        await switchEl.click()
      }
    }

    if (config.rawResponse !== undefined) {
      const switchEl = this.getRawResponseSwitch()
      const isChecked = (await switchEl.getAttribute('data-state')) === 'checked'
      if (isChecked !== config.rawResponse) {
        await switchEl.click()
      }
    }
  }

  // ============================================
  // Proxy Configuration
  // ============================================

  /**
   * Get proxy type select
   */
  getProxyTypeSelect(): Locator {
    return this.page.getByLabel('Proxy Type').locator('..').locator('button[role="combobox"]')
  }

  /**
   * Set proxy configuration
   */
  async setProxyConfig(config: {
    type: 'http' | 'socks5' | 'environment' | 'none'
    url?: string
    username?: string
    password?: string
  }): Promise<void> {
    await this.selectConfigTab('proxy')

    // Select proxy type
    const proxySelect = this.getProxyTypeSelect()
    await proxySelect.click()
    await this.page.getByRole('option', { name: new RegExp(config.type, 'i') }).click()

    // Fill additional fields if not 'none' or 'environment'
    if (config.type === 'http' || config.type === 'socks5') {
      if (config.url) {
        await this.page.getByLabel('Proxy URL').fill(config.url)
      }
      if (config.username) {
        await this.page.getByLabel('Username').fill(config.username)
      }
      if (config.password) {
        await this.page.getByLabel('Password').fill(config.password)
      }
    }
  }

  // ============================================
  // Network Configuration
  // ============================================

  /**
   * Set network configuration
   */
  async setNetworkConfig(config: {
    baseUrl?: string
    timeout?: number
    maxRetries?: number
    initialBackoff?: number
    maxBackoff?: number
  }): Promise<void> {
    await this.selectConfigTab('network')

    if (config.baseUrl !== undefined) {
      const input = this.page.getByLabel(/Base URL/i)
      await input.clear()
      await input.fill(config.baseUrl)
    }

    if (config.timeout !== undefined) {
      const input = this.page.getByLabel(/Timeout/i)
      await input.clear()
      await input.fill(String(config.timeout))
    }

    if (config.maxRetries !== undefined) {
      const input = this.page.getByLabel(/Max Retries/i)
      await input.clear()
      await input.fill(String(config.maxRetries))
    }

    if (config.initialBackoff !== undefined) {
      const input = this.page.getByLabel(/Initial Backoff/i)
      await input.clear()
      await input.fill(String(config.initialBackoff))
    }

    if (config.maxBackoff !== undefined) {
      const input = this.page.getByLabel(/Max Backoff/i)
      await input.clear()
      await input.fill(String(config.maxBackoff))
    }
  }

  // ============================================
  // Governance Configuration (Budget/Rate Limits)
  // ============================================

  /**
   * Set governance configuration (budget and rate limits)
   */
  async setGovernanceConfig(config: {
    aligned?: boolean
    budgets?: Array<{
      amount?: number
      resetPeriod?: string
    }>
    tokenLimit?: number
    requestLimit?: number
  }): Promise<void> {
    await this.selectConfigTab('governance')

    if (config.budgets) {
      const budgetLines = this.page.locator('[data-testid^="provider-governance-budgets-line-"]')
      let existingCount = await budgetLines.count()

      while (existingCount < config.budgets.length) {
        await this.page.getByTestId('provider-governance-budgets-add-btn').click()
        existingCount += 1
      }

      while (existingCount > config.budgets.length) {
        existingCount -= 1
        await this.page.getByTestId(`provider-governance-budgets-remove-${existingCount}`).click()
      }

      for (const [index, budget] of config.budgets.entries()) {
        const amountInput = this.page.getByTestId(`provider-governance-budgets-amount-${index}`)
        await amountInput.click()
        await amountInput.fill('')
        if (budget.amount !== undefined) {
          await amountInput.pressSequentially(String(budget.amount))
        }

        if (budget.resetPeriod) {
          const budgetLine = this.page.getByTestId(`provider-governance-budgets-line-${index}`)
          const resetPeriodLabel = resetPeriodLabels[budget.resetPeriod] ?? budget.resetPeriod
          await budgetLine.getByRole('combobox').click()
          await this.page.getByRole('option', { name: resetPeriodLabel }).click()
        }
      }

      if (config.aligned !== undefined && config.budgets.length > 0) {
        const switchEl = this.page.getByTestId('provider-governance-calendar-aligned-switch')
        const isChecked = (await switchEl.getAttribute('data-state')) === 'checked'
        if (isChecked !== config.aligned) {
          await switchEl.click()
        }
      }
    }

    if (config.tokenLimit !== undefined) {
      const input = this.page.locator('#providerTokenMaxLimit')
      await input.clear()
      await input.fill(String(config.tokenLimit))
    }

    if (config.requestLimit !== undefined) {
      const input = this.page.locator('#providerRequestMaxLimit')
      await input.clear()
      await input.fill(String(config.requestLimit))
    }
  }

  /**
   * Check if governance tab is visible (depends on permissions)
   */
  async isGovernanceTabVisible(): Promise<boolean> {
    await this.openConfigSheet()
    const tab = this.page.getByTestId('provider-tab-governance')
    return await tab.isVisible().catch(() => false)
  }
}
