import { Locator, Page, expect } from '@playwright/test'
import { CustomProviderConfig, ProviderKeyConfig } from '../../../core/fixtures/test-data.fixture'
import { BasePage } from '../../../core/pages/base.page'
import { Selectors } from '../../../core/utils/selectors'
import { fillSelect, waitForNetworkIdle } from '../../../core/utils/test-helpers'

export type { CustomProviderConfig, ProviderKeyConfig }

/**
 * Page object for the Providers page
 */
export class ProvidersPage extends BasePage {
  // Locators
  readonly providerList: Locator
  readonly addProviderBtn: Locator
  readonly addKeyBtn: Locator
  readonly keysTable: Locator

  // Custom provider sheet
  readonly customProviderSheet: Locator
  readonly customProviderNameInput: Locator
  readonly baseProviderSelect: Locator
  readonly baseUrlInput: Locator
  readonly customProviderSaveBtn: Locator
  readonly customProviderCancelBtn: Locator

  // Key form
  readonly keyForm: Locator
  readonly keySaveBtn: Locator
  readonly keyCancelBtn: Locator

  constructor(page: Page) {
    super(page)

    // Provider list
    this.providerList = page.locator(Selectors.providers.providerList)
    this.addProviderBtn = page.getByTestId('add-provider-btn')

    // Keys table
    this.addKeyBtn = page.getByTestId('add-key-btn')
    this.keysTable = page.getByTestId('keys-table')

    // Custom provider sheet
    this.customProviderSheet = page.getByTestId('custom-provider-sheet')
    this.customProviderNameInput = page.getByTestId('custom-provider-name')
    this.baseProviderSelect = page.getByTestId('base-provider-select')
    this.baseUrlInput = page.getByTestId('base-url-input')
    this.customProviderSaveBtn = page.getByTestId('custom-provider-save-btn')
    this.customProviderCancelBtn = page.getByTestId('custom-provider-cancel-btn')

    // Key form
    this.keyForm = page.getByTestId('key-form')
    this.keySaveBtn = page.getByTestId('key-save-btn')
    this.keyCancelBtn = page.getByTestId('key-cancel-btn')
  }

  /**
   * Navigate to the providers page
   */
  async goto(): Promise<void> {
    await this.page.goto('/workspace/providers')
    await waitForNetworkIdle(this.page)
  }

  /**
   * Select a provider from the sidebar list
   */
  async selectProvider(name: string): Promise<void> {
    const providerItem = this.page.getByTestId(`provider-item-${name.replace(/[^a-z0-9]+/gi, "-").toLowerCase()}`)
    await providerItem.click()
    await waitForNetworkIdle(this.page)
  }

  /**
   * Get provider item locator
   */
  getProviderItem(name: string): Locator {
    return this.page.getByTestId(`provider-item-${name.replace(/[^a-z0-9]+/gi, "-").toLowerCase()}`)
  }

  /**
   * Check if a provider exists in the list
   */
  async providerExists(name: string): Promise<boolean> {
    const providerItem = this.getProviderItem(name)
    return await providerItem.isVisible()
  }

  /**
   * Add a new key to the currently selected provider
   */
  async addKey(config: ProviderKeyConfig): Promise<void> {
    await this.dismissToasts()

    // Click add key button
    await this.addKeyBtn.click()

    // Wait for key form to appear
    await expect(this.keyForm).toBeVisible()

    // Fill in key details
    await this.page.getByLabel('Name').fill(config.name)
    await this.page.getByLabel('API Key').fill(config.value)

    // Fill weight if provided
    if (config.weight !== undefined) {
      const weightInput = this.page.getByLabel('Weight')
      if (await weightInput.isVisible()) {
        await weightInput.fill(String(config.weight))
      }
    }

    // Note: Model selection is skipped for now as it requires specific UI interaction
    // that may vary based on the provider type

    // Save the key
    await this.keySaveBtn.click()

    // Wait for success toast
    await this.waitForSuccessToast()

    // Wait for form to close and table to refresh
    await expect(this.keyForm).not.toBeVisible({ timeout: 5000 })
    await waitForNetworkIdle(this.page)
  }

  /**
   * Create a custom provider
   */
  async createProvider(config: CustomProviderConfig): Promise<void> {
    // Click add provider button
    await this.addProviderBtn.click()

    // Wait for custom provider sheet to appear
    await expect(this.customProviderSheet).toBeVisible()

    // Fill in provider name
    await this.customProviderNameInput.fill(config.name)

    // Select base provider type
    await fillSelect(
      this.page,
      '[data-testid="base-provider-select"]',
      this.getBaseProviderLabel(config.baseProviderType)
    )

    // Fill in base URL
    if (config.baseUrl) {
      await this.baseUrlInput.fill(config.baseUrl)
    }

    // Save the provider
    await this.customProviderSaveBtn.click()

    // Wait for sheet to close (indicates success)
    await expect(this.customProviderSheet).not.toBeVisible({ timeout: 10000 })

    // Wait for network to settle
    await waitForNetworkIdle(this.page)
  }

  /**
   * Delete a custom provider.
   * @param options.skipToastWait - If true, do not wait for success toast (e.g. for cleanup); avoids cleanup failures when toast is missing or already gone.
   */
  async deleteProvider(name: string, options?: { skipToastWait?: boolean }): Promise<void> {
    // First select the provider
    await this.selectProvider(name)

    // Find and click the delete button
    const providerItem = this.getProviderItem(name)
    await providerItem.hover()

    const deleteBtn = providerItem.locator('svg.lucide-trash')
    await deleteBtn.click()

    // Confirm deletion in dialog
    await this.page.getByRole('button', { name: 'Delete' }).click()

    if (options?.skipToastWait) {
      // Wait for dialog to close; do not require toast so cleanup does not fail
      await this.page.locator('[role="alertdialog"]').waitFor({ state: 'hidden', timeout: 5000 }).catch(() => {})
      await waitForNetworkIdle(this.page)
      return
    }
    // Wait for success toast
    await this.waitForSuccessToast('deleted')
  }

  /**
   * Get key row locator
   */
  getKeyRow(name: string): Locator {
    // Try data-testid first, fall back to finding row by text content
    return this.page.getByTestId(`key-row-${name}`).or(
      this.page.locator('tr, [role="row"]').filter({ hasText: name })
    )
  }

  /**
   * Check if a key exists in the table (waits for it to appear)
   */
  async keyExists(name: string, timeout: number = 5000): Promise<boolean> {
    // Wait for network to settle first
    await waitForNetworkIdle(this.page)

    // Try to find the key with waiting
    const keyRow = this.getKeyRow(name)
    try {
      await keyRow.waitFor({ state: 'visible', timeout })
      return true
    } catch {
      return false
    }
  }

  /**
   * Edit an existing key
   */
  async editKey(keyName: string, updates: Partial<ProviderKeyConfig>): Promise<void> {
    // Find the key row and open the dropdown menu
    const keyRow = this.getKeyRow(keyName)
    await keyRow.scrollIntoViewIfNeeded()

    // The dropdown trigger - look for ellipsis/more button
    const menuBtn = keyRow.locator('button').filter({ has: this.page.locator('svg') }).last()
    await menuBtn.waitFor({ state: 'visible', timeout: 5000 })
    await menuBtn.click()

    // Wait for dropdown to appear and click Edit
    await this.page.getByRole('menuitem', { name: /Edit/i }).click()

    // Wait for form
    await expect(this.keyForm).toBeVisible()

    // Update fields
    if (updates.name) {
      await this.page.getByLabel('Name').clear()
      await this.page.getByLabel('Name').fill(updates.name)
    }

    if (updates.value) {
      await this.page.getByLabel('API Key').clear()
      await this.page.getByLabel('API Key').fill(updates.value)
    }

    if (updates.weight !== undefined) {
      const weightInput = this.page.getByLabel('Weight')
      if (await weightInput.isVisible()) {
        await weightInput.clear()
        await weightInput.fill(String(updates.weight))
      }
    }

    // Save
    await this.keySaveBtn.click()
    await this.waitForSuccessToast()
  }

  /**
   * Delete a key
   */
  async deleteKey(keyName: string): Promise<void> {
    await this.dismissToasts()

    // Find the key row
    const keyRow = this.getKeyRow(keyName)
    await keyRow.scrollIntoViewIfNeeded()

    // The dropdown trigger - look for ellipsis/more button (last button with svg icon)
    const menuBtn = keyRow.locator('button').filter({ has: this.page.locator('svg') }).last()
    await menuBtn.waitFor({ state: 'visible', timeout: 5000 })
    await menuBtn.click()

    // Click Delete in the dropdown
    await this.page.getByRole('menuitem', { name: /Delete/i }).click()

    // Confirm deletion in the alert dialog
    const confirmBtn = this.page.locator('[role="alertdialog"]').getByRole('button', { name: /Delete/i })
    await confirmBtn.waitFor({ state: 'visible', timeout: 5000 })
    await confirmBtn.click()

    // Wait for success toast
    await this.waitForSuccessToast('deleted')
  }

  /**
   * Toggle key enabled/disabled
   */
  async toggleKeyEnabled(keyName: string): Promise<void> {
    const keyRow = this.getKeyRow(keyName)
    const switchEl = keyRow.locator('button[role="switch"]')
    await switchEl.click()
    await this.waitForSuccessToast()
  }

  /**
   * Get the count of keys in the table
   */
  async getKeyCount(): Promise<number> {
    const rows = this.keysTable.locator('tbody tr')
    const count = await rows.count()

    if (count === 0) {
      return 0
    }

    // Check if it's the "No keys found" row
    const firstRowText = await rows.first().textContent()
    if (firstRowText?.includes('No keys found')) {
      return 0
    }

    return count
  }

  /**
   * Helper to get base provider label for select
   */
  private getBaseProviderLabel(type: string): string {
    const labels: Record<string, string> = {
      openai: 'OpenAI',
      anthropic: 'Anthropic',
      gemini: 'Gemini',
      cohere: 'Cohere',
      bedrock: 'AWS Bedrock',
    }
    return labels[type] || type
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
  async selectConfigTab(tabName: 'network' | 'proxy' | 'performance' | 'governance' | 'pricing'): Promise<void> {
    await this.openConfigSheet()

    const tabLabels: Record<string, string> = {
      network: 'Network config',
      proxy: 'Proxy config',
      performance: 'Performance tuning',
      governance: 'Governance',
      pricing: 'Pricing',
    }

    const tab = this.page.getByRole('tab', { name: tabLabels[tabName] })
    await tab.click()
    await this.page.waitForTimeout(300)
  }

  /**
   * Get the save button for the current config tab
   */
  getConfigSaveBtn(configType: 'network' | 'proxy' | 'performance' | 'governance' | 'pricing'): Locator {
    const buttonNames: Record<string, string> = {
      network: 'Save Network Configuration',
      proxy: 'Save Proxy Configuration',
      performance: 'Save Performance Configuration',
      governance: 'Save Governance Configuration',
      pricing: 'Save Pricing Overrides',
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
   * Get raw request switch
   */
  getRawRequestSwitch(): Locator {
    return this.page.getByLabel('Include Raw Request').locator('..').locator('button[role="switch"]')
  }

  /**
   * Get raw response switch
   */
  getRawResponseSwitch(): Locator {
    return this.page.getByLabel('Include Raw Response').locator('..').locator('button[role="switch"]')
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
   * Set performance configuration
   */
  async setPerformanceConfig(config: {
    concurrency?: number
    bufferSize?: number
    rawRequest?: boolean
    rawResponse?: boolean
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

    if (config.rawRequest !== undefined) {
      const switchEl = this.getRawRequestSwitch()
      const isChecked = await switchEl.getAttribute('data-state') === 'checked'
      if (isChecked !== config.rawRequest) {
        await switchEl.click()
      }
    }

    if (config.rawResponse !== undefined) {
      const switchEl = this.getRawResponseSwitch()
      const isChecked = await switchEl.getAttribute('data-state') === 'checked'
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
    budgetLimit?: number
    tokenLimit?: number
    requestLimit?: number
  }): Promise<void> {
    await this.selectConfigTab('governance')

    if (config.budgetLimit !== undefined) {
      const input = this.page.locator('#providerBudgetMaxLimit')
      await input.clear()
      await input.fill(String(config.budgetLimit))
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
    const tab = this.page.getByRole('tab', { name: 'Governance' })
    return await tab.isVisible().catch(() => false)
  }

  // ============================================
  // Pricing Configuration (Pricing Overrides)
  // ============================================

  /**
   * Get pricing overrides JSON textarea input
   */
  getPricingJsonInput(): Locator {
    return this.page.getByTestId('provider-pricing-overrides-json-input')
  }

  /**
   * Get pricing overrides reset button
   */
  getPricingResetBtn(): Locator {
    return this.page.getByTestId('provider-pricing-overrides-reset-button')
  }

  /**
   * Get pricing overrides save button
   */
  getPricingSaveBtn(): Locator {
    return this.page.getByTestId('provider-pricing-overrides-save-button')
  }

  /**
   * Save pricing configuration and wait for success toast
   */
  async savePricingConfig(): Promise<void> {
    const saveBtn = this.getConfigSaveBtn('pricing')
    await saveBtn.click()
    await this.waitForSuccessToast()
  }

  /**
   * Check if pricing tab is visible
   */
  async isPricingTabVisible(): Promise<boolean> {
    await this.openConfigSheet()
    const tab = this.page.getByRole('tab', { name: 'Pricing' })
    return await tab.isVisible().catch(() => false)
  }

  /**
   * Set pricing overrides JSON value
   */
  async setPricingOverridesJson(json: string): Promise<void> {
    await this.selectConfigTab('pricing')
    const input = this.getPricingJsonInput()
    await input.clear()
    await input.fill(json)
  }
}
