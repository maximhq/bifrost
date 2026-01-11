import { Page, Locator, expect } from '@playwright/test'
import { BasePage } from '../../../core/pages/base.page'
import { Selectors } from '../../../core/utils/selectors'
import { fillSelect, waitForNetworkIdle } from '../../../core/utils/test-helpers'

/**
 * Provider key configuration
 */
export interface ProviderKeyConfig {
  name: string
  value: string
  models?: string[]
  weight?: number
}

/**
 * Custom provider configuration
 */
export interface CustomProviderConfig {
  name: string
  baseProviderType: 'openai' | 'anthropic' | 'gemini' | 'cohere' | 'bedrock'
  baseUrl: string
  isKeyless?: boolean
}

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
    const providerItem = this.page.getByTestId(`provider-${name}`)
    await providerItem.click()
    await waitForNetworkIdle(this.page)
  }

  /**
   * Get provider item locator
   */
  getProviderItem(name: string): Locator {
    return this.page.getByTestId(`provider-${name}`)
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
    await this.baseUrlInput.fill(config.baseUrl)
    
    // Save the provider
    await this.customProviderSaveBtn.click()
    
    // Wait for sheet to close (indicates success)
    await expect(this.customProviderSheet).not.toBeVisible({ timeout: 10000 })
    
    // Wait for network to settle
    await waitForNetworkIdle(this.page)
  }

  /**
   * Delete a custom provider
   */
  async deleteProvider(name: string): Promise<void> {
    // First select the provider
    await this.selectProvider(name)
    
    // Find and click the delete button
    const providerItem = this.getProviderItem(name)
    await providerItem.hover()
    
    const deleteBtn = providerItem.locator('svg.lucide-trash')
    await deleteBtn.click()
    
    // Confirm deletion in dialog
    await this.page.getByRole('button', { name: 'Delete' }).click()
    
    // Wait for success toast
    await this.waitForSuccessToast('deleted')
  }

  /**
   * Get key row locator
   */
  getKeyRow(name: string): Locator {
    return this.page.getByTestId(`key-row-${name}`)
  }

  /**
   * Check if a key exists in the table
   */
  async keyExists(name: string): Promise<boolean> {
    const keyRow = this.getKeyRow(name)
    return await keyRow.isVisible()
  }

  /**
   * Edit an existing key
   */
  async editKey(keyName: string, updates: Partial<ProviderKeyConfig>): Promise<void> {
    // Find the key row and open the dropdown menu
    const keyRow = this.getKeyRow(keyName)
    // The dropdown trigger is the last button in the row (with ellipsis icon)
    const menuBtn = keyRow.locator('button').last()
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
    // Find the key row and open the dropdown menu
    const keyRow = this.getKeyRow(keyName)
    // The dropdown trigger is the last button in the row (with ellipsis icon)
    const menuBtn = keyRow.locator('button').last()
    await menuBtn.click()
    
    // Click Delete in the dropdown
    await this.page.getByRole('menuitem', { name: /Delete/i }).click()
    
    // Confirm deletion in the alert dialog
    await this.page.getByRole('button', { name: 'Delete' }).click()
    
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
}
