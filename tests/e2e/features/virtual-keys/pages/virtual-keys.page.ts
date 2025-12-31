import { Locator, Page, expect } from '@playwright/test'
import { BasePage } from '../../../core/pages/base.page'
import { fillSelect, waitForNetworkIdle } from '../../../core/utils/test-helpers'

/**
 * Provider display names mapping - matches the UI's ProviderLabels
 * Used for exact matching when selecting providers in dropdowns
 */
const PROVIDER_DISPLAY_NAMES: Record<string, string> = {
  openai: 'OpenAI',
  anthropic: 'Anthropic',
  azure: 'Azure',
  bedrock: 'AWS Bedrock',
  cohere: 'Cohere',
  vertex: 'Vertex AI',
  mistral: 'Mistral AI',
  ollama: 'Ollama',
  groq: 'Groq',
  gemini: 'Gemini',
  openrouter: 'OpenRouter',
  huggingface: 'HuggingFace',
  cerebras: 'Cerebras',
  perplexity: 'Perplexity',
  elevenlabs: 'Elevenlabs',
  parasail: 'Parasail',
  sgl: 'SGLang',
  nebius: 'Nebius Token Factory',
}

/**
 * Budget configuration
 */
export interface BudgetConfig {
  maxLimit: number
  resetDuration?: string
}

/**
 * Rate limit configuration
 */
export interface RateLimitConfig {
  tokenMaxLimit?: number
  tokenResetDuration?: string
  requestMaxLimit?: number
  requestResetDuration?: string
}

/**
 * Provider configuration for virtual key
 */
export interface ProviderConfig {
  provider: string
  weight?: number
  allowedModels?: string[]
  budget?: BudgetConfig
  rateLimit?: RateLimitConfig
}

/**
 * Virtual key configuration
 */
export interface VirtualKeyConfig {
  name: string
  description?: string
  isActive?: boolean
  providerConfigs?: ProviderConfig[]
  budget?: BudgetConfig
  rateLimit?: RateLimitConfig
  entityType?: 'none' | 'team' | 'customer'
  teamId?: string
  customerId?: string
}

/**
 * Page object for the Virtual Keys page
 */
export class VirtualKeysPage extends BasePage {
  // Main page elements
  readonly createBtn: Locator
  readonly table: Locator

  // Virtual key sheet elements
  readonly sheet: Locator
  readonly nameInput: Locator
  readonly descriptionInput: Locator
  readonly isActiveToggle: Locator
  readonly providerSelect: Locator
  readonly saveBtn: Locator
  readonly cancelBtn: Locator

  constructor(page: Page) {
    super(page)

    // Main page elements
    this.createBtn = page.getByTestId('create-vk-btn')
    this.table = page.getByTestId('vk-table')

    // Virtual key sheet elements
    this.sheet = page.getByTestId('vk-sheet')
    this.nameInput = page.getByTestId('vk-name-input')
    this.descriptionInput = page.getByTestId('vk-description-input')
    this.isActiveToggle = page.getByTestId('vk-is-active-toggle')
    this.providerSelect = page.getByTestId('vk-provider-select')
    this.saveBtn = page.getByTestId('vk-save-btn')
    this.cancelBtn = page.getByTestId('vk-cancel-btn')
  }

  /**
   * Navigate to the virtual keys page
   */
  async goto(): Promise<void> {
    await this.page.goto('/workspace/virtual-keys')
    await waitForNetworkIdle(this.page)
  }

  /**
   * Get virtual key row locator by name
   */
  getVirtualKeyRow(name: string): Locator {
    return this.page.getByTestId(`vk-row-${name}`)
  }

  /**
   * Check if a virtual key exists in the table
   */
  async virtualKeyExists(name: string): Promise<boolean> {
    const row = this.getVirtualKeyRow(name)
    // Use count() to check if element exists in DOM (doesn't require visibility)
    const count = await row.count()
    return count > 0
  }

  /**
   * Create a new virtual key
   */
  async createVirtualKey(config: VirtualKeyConfig): Promise<void> {
    // Click create button
    await this.createBtn.click()

    // Wait for sheet to appear and animation to complete
    await expect(this.sheet).toBeVisible()
    await this.page.waitForTimeout(200) // Wait for sheet animation

    // Fill basic information using keyboard navigation
    await this.nameInput.focus()
    await this.page.keyboard.type(config.name)

    if (config.description) {
      await this.page.keyboard.press('Tab') // Move to description
      await this.page.keyboard.type(config.description)
    }

    // Set active state if specified (default is true, so only toggle if we want inactive)
    if (config.isActive === false) {
      await this.isActiveToggle.focus()
      await this.page.keyboard.press('Space') // Toggle the switch
    }

    // Add provider configurations
    if (config.providerConfigs && config.providerConfigs.length > 0) {
      for (const providerConfig of config.providerConfigs) {
        await this.addProviderConfig(providerConfig)
      }
    }

    // Set budget if specified
    if (config.budget) {
      await this.setBudget(config.budget)
    }

    // Set rate limits if specified
    if (config.rateLimit) {
      await this.setRateLimit(config.rateLimit)
    }

    // Set entity assignment if specified
    if (config.entityType && config.entityType !== 'none') {
      await this.setEntityAssignment(config.entityType, config.teamId, config.customerId)
    }

    // Save the virtual key by clicking the save button
    await this.saveBtn.click()

    // Wait for success toast
    await this.waitForSuccessToast()

    // Wait for sheet to close
    await expect(this.sheet).not.toBeVisible({ timeout: 5000 })

    // Wait for the new row to appear in the table (ensures table has refreshed)
    const row = this.getVirtualKeyRow(config.name)
    await row.waitFor({ state: 'attached', timeout: 10000 })
    await row.scrollIntoViewIfNeeded()
  }

  /**
   * Add a provider configuration to the virtual key form
   */
  private async addProviderConfig(config: ProviderConfig): Promise<void> {
    // Click the provider select dropdown
    await this.providerSelect.click()

    // Wait for dropdown content
    await this.page.waitForSelector('[role="listbox"]', { timeout: 5000 })

    // Get display name - use mapping for known providers, otherwise use exact name
    const displayName = PROVIDER_DISPLAY_NAMES[config.provider.toLowerCase()] || config.provider

    // First try exact match for base providers (e.g., "OpenAI", "Anthropic")
    let option = this.page.getByRole('option', { name: displayName, exact: true })

    if (await option.count() === 0) {
      // Fallback: try partial match for custom providers (contains provider name)
      // This handles custom providers like "test-anthropic-1234567890"
      option = this.page.getByRole('option').filter({
        hasText: new RegExp(config.provider, 'i')
      }).first()
    }

    // Verify we found a matching option
    const optionCount = await option.count()
    if (optionCount === 0) {
      throw new Error(`No provider option found matching "${config.provider}" (display name: "${displayName}")`)
    }

    await option.click()

    // Wait for dropdown to close after selection
    await this.page.waitForSelector('[role="listbox"]', { state: 'hidden', timeout: 5000 })

    // Small delay to allow UI to update with the new provider config
    await this.page.waitForTimeout(200)
  }

  /**
   * Set budget configuration in the form
   */
  private async setBudget(budget: BudgetConfig): Promise<void> {
    // Find budget max limit input and fill using focus + keyboard
    const budgetInput = this.page.locator('#budgetMaxLimit')
    await budgetInput.focus()
    await this.page.keyboard.type(String(budget.maxLimit))

    // Set reset duration if specified - skip for now as default is fine
    // The reset duration select is complex and default "Monthly" is usually correct
  }

  /**
   * Set rate limit configuration in the form
   */
  private async setRateLimit(rateLimit: RateLimitConfig): Promise<void> {
    // Set token limits using focus + keyboard
    if (rateLimit.tokenMaxLimit !== undefined) {
      const tokenInput = this.page.locator('#tokenMaxLimit')
      await tokenInput.focus()
      await this.page.keyboard.type(String(rateLimit.tokenMaxLimit))
    }

    // Set request limits using focus + keyboard
    if (rateLimit.requestMaxLimit !== undefined) {
      const requestInput = this.page.locator('#requestMaxLimit')
      await requestInput.focus()
      await this.page.keyboard.type(String(rateLimit.requestMaxLimit))
    }
  }

  /**
   * Set entity assignment (team or customer)
   */
  private async setEntityAssignment(
    entityType: 'team' | 'customer',
    teamId?: string,
    customerId?: string
  ): Promise<void> {
    // Find and click entity type select
    const entityTypeSelect = this.page.locator('[data-testid="vk-entity-type-select"]')
    if (await entityTypeSelect.isVisible()) {
      await fillSelect(
        this.page,
        '[data-testid="vk-entity-type-select"]',
        entityType === 'team' ? 'Assign to Team' : 'Assign to Customer'
      )

      // Select team or customer
      if (entityType === 'team' && teamId) {
        const teamSelect = this.page.locator('[data-testid="vk-team-select"]')
        if (await teamSelect.isVisible()) {
          await fillSelect(this.page, '[data-testid="vk-team-select"]', teamId)
        }
      } else if (entityType === 'customer' && customerId) {
        const customerSelect = this.page.locator('[data-testid="vk-customer-select"]')
        if (await customerSelect.isVisible()) {
          await fillSelect(this.page, '[data-testid="vk-customer-select"]', customerId)
        }
      }
    }
  }

  /**
   * Edit an existing virtual key
   */
  async editVirtualKey(name: string, updates: Partial<VirtualKeyConfig>): Promise<void> {
    // Find and click the edit button using data-testid
    const editBtn = this.page.getByTestId(`vk-edit-btn-${name}`)
    await editBtn.waitFor({ state: 'attached', timeout: 10000 })
    await editBtn.scrollIntoViewIfNeeded()
    await editBtn.click()

    // Wait for sheet to appear and animation to complete
    await expect(this.sheet).toBeVisible()
    await this.page.waitForTimeout(200) // Wait for sheet animation

    // Update name using clear() and fill() for cross-platform compatibility
    if (updates.name) {
      await this.nameInput.clear()
      await this.nameInput.fill(updates.name)
    }

    // Update description using clear() and fill() for cross-platform compatibility
    if (updates.description !== undefined) {
      await this.descriptionInput.clear()
      if (updates.description) {
        await this.descriptionInput.fill(updates.description)
      }
    }

    // Update toggle using click() and data-state attribute for reliability
    if (updates.isActive !== undefined) {
      // Check current state using data-state attribute (Radix Switch)
      const isCurrentlyChecked = await this.isActiveToggle.getAttribute('data-state') === 'checked'
      if (isCurrentlyChecked !== updates.isActive) {
        await this.isActiveToggle.click()
      }
    }

    if (updates.budget) {
      await this.setBudget(updates.budget)
    }

    if (updates.rateLimit) {
      await this.setRateLimit(updates.rateLimit)
    }

    // Save changes by clicking the save button
    await this.saveBtn.click()

    // Wait for success toast
    await this.waitForSuccessToast()

    // Wait for sheet to close with increased timeout
    await expect(this.sheet).not.toBeVisible({ timeout: 10000 })
  }

  /**
   * Delete a virtual key
   */
  async deleteVirtualKey(name: string): Promise<void> {
    // Find and click the delete button using data-testid
    const deleteBtn = this.page.getByTestId(`vk-delete-btn-${name}`)
    await deleteBtn.waitFor({ state: 'attached', timeout: 10000 })
    await deleteBtn.scrollIntoViewIfNeeded()
    await deleteBtn.click()

    // Confirm deletion in dialog
    await this.page.getByRole('button', { name: 'Delete' }).click()

    // Wait for success toast
    await this.waitForSuccessToast('deleted')
  }

  /**
   * Click on a virtual key row to view details
   */
  async viewVirtualKey(name: string): Promise<void> {
    // Wait for the row and scroll to it
    const row = this.getVirtualKeyRow(name)
    await row.waitFor({ state: 'attached', timeout: 10000 })
    await row.scrollIntoViewIfNeeded()

    await row.click()

    // Wait for detail sheet to appear
    await this.page.waitForSelector('[role="dialog"]', { timeout: 5000 })
  }

  /**
   * Get the count of virtual keys in the table
   */
  async getVirtualKeyCount(): Promise<number> {
    const rows = this.table.locator('tbody tr')
    const count = await rows.count()

    // Check if it's the empty state row
    const firstRowText = await rows.first().textContent()
    if (firstRowText?.includes('No virtual keys found')) {
      return 0
    }

    return count
  }

  /**
   * Copy virtual key value to clipboard
   */
  async copyVirtualKeyValue(name: string): Promise<void> {
    // Find and click the copy button using data-testid
    const copyBtn = this.page.getByTestId(`vk-copy-btn-${name}`)
    await copyBtn.waitFor({ state: 'attached', timeout: 10000 })
    await copyBtn.scrollIntoViewIfNeeded()
    await copyBtn.click()

    await this.waitForSuccessToast('Copied')
  }

  /**
   * Toggle key visibility (show/hide)
   */
  async toggleKeyVisibility(name: string): Promise<void> {
    // Find and click the visibility toggle button using data-testid
    const toggleBtn = this.page.getByTestId(`vk-visibility-btn-${name}`)
    await toggleBtn.waitFor({ state: 'attached', timeout: 10000 })
    await toggleBtn.scrollIntoViewIfNeeded()
    await toggleBtn.click()
  }
}
