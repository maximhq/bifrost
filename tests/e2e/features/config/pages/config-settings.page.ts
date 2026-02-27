import { Locator, Page } from '@playwright/test'
import { BasePage } from '../../../core/pages/base.page'
import { waitForNetworkIdle } from '../../../core/utils/test-helpers'

/**
 * Config settings state interface
 */
export interface ConfigSettingsState {
  toggleStates: Record<string, boolean>
  inputValues: Record<string, string>
  configPath: string
}

export class ConfigSettingsPage extends BasePage {
  readonly saveBtn: Locator

  // Client Settings
  readonly dropExcessRequestsSwitch: Locator
  readonly enableLiteLLMFallbacksSwitch: Locator
  readonly disableDBPingsSwitch: Locator
  readonly asyncJobResultTtlInput: Locator

  // Logging Settings
  readonly enableLoggingSwitch: Locator
  readonly disableContentLoggingSwitch: Locator
  readonly logRetentionDaysInput: Locator
  readonly workspaceLoggingHeadersTextarea: Locator

  // Security Settings
  readonly rateLimitingSection: Locator
  readonly enforceAuthOnInferenceSwitch: Locator
  readonly requiredHeadersTextarea: Locator

  // Performance Tuning Settings
  readonly workerPoolSizeInput: Locator
  readonly maxRequestBodySizeInput: Locator

  // Observability Settings
  readonly observabilityToggles: Locator

  // Pricing Config
  readonly pricingConfigView: Locator
  readonly pricingDatasheetUrlInput: Locator
  readonly pricingForceSyncBtn: Locator
  readonly pricingSaveBtn: Locator

  constructor(page: Page) {
    super(page)
    this.saveBtn = page.getByRole('button', { name: /Save/i })

    // Client Settings locators
    this.dropExcessRequestsSwitch = page.locator('#drop-excess-requests')
    this.enableLiteLLMFallbacksSwitch = page.locator('#enable-litellm-fallbacks')
    this.disableDBPingsSwitch = page.locator('#disable-db-pings-in-health')
    this.asyncJobResultTtlInput = page.getByTestId('client-settings-async-job-result-ttl-input')

    // Logging Settings locators
    this.enableLoggingSwitch = page.locator('#enable-logging')
    this.disableContentLoggingSwitch = page.locator('#disable-content-logging')
    this.logRetentionDaysInput = page.getByLabel(/Log Retention Days/i).or(
      page.locator('#log-n-days')
    )
    this.workspaceLoggingHeadersTextarea = page.getByTestId('workspace-logging-headers-textarea')

    // Security Settings locators
    this.rateLimitingSection = page.locator('text=Rate Limiting').locator('..')
    this.enforceAuthOnInferenceSwitch = page.getByTestId('enforce-auth-on-inference-switch')
    this.requiredHeadersTextarea = page.getByTestId('required-headers-textarea')

    // Performance Tuning locators
    this.workerPoolSizeInput = page.getByLabel(/Worker Pool Size/i)
    this.maxRequestBodySizeInput = page.getByLabel(/Max Request Body Size/i)

    // Observability locators
    this.observabilityToggles = page.locator('button[role="switch"]')

    // Pricing Config locators
    this.pricingConfigView = page.getByTestId('pricing-config-view')
    this.pricingDatasheetUrlInput = page.getByTestId('pricing-datasheet-url-input')
    this.pricingForceSyncBtn = page.getByTestId('pricing-force-sync-btn')
    this.pricingSaveBtn = page.getByTestId('pricing-save-btn')
  }

  async goto(path: string): Promise<void> {
    await this.page.goto(`/workspace/config/${path}`)
    await waitForNetworkIdle(this.page)
  }

  async saveSettings(): Promise<void> {
    await this.saveBtn.click()
    await this.waitForSuccessToast()
  }

  /**
   * Check if save button is enabled (changes pending)
   */
  async hasPendingChanges(): Promise<boolean> {
    const isDisabled = await this.saveBtn.isDisabled()
    return !isDisabled
  }

  /**
   * Toggle a switch element
   */
  async toggleSwitch(switchLocator: Locator): Promise<void> {
    await switchLocator.click()
  }

  /**
   * Get the state of a switch
   */
  async getSwitchState(switchLocator: Locator): Promise<boolean> {
    const state = await switchLocator.getAttribute('data-state')
    return state === 'checked'
  }

  /**
   * Set input value
   */
  async setInputValue(inputLocator: Locator, value: string): Promise<void> {
    await inputLocator.clear()
    await inputLocator.fill(value)
  }

  /**
   * Get input value
   */
  async getInputValue(inputLocator: Locator): Promise<string> {
    return await inputLocator.inputValue()
  }

  /**
   * Capture current settings state for a config page
   */
  async getCurrentSettings(configPath: string): Promise<ConfigSettingsState> {
    const state: ConfigSettingsState = {
      toggleStates: {},
      inputValues: {},
      configPath,
    }

    // Get all switch states on the page
    const switches = this.page.locator('button[role="switch"]')
    const switchCount = await switches.count()

    for (let i = 0; i < switchCount; i++) {
      const switchEl = switches.nth(i)
      const elId = await switchEl.getAttribute('id')
      if (!elId) {
        console.warn(`Switch at index ${i} has no id attribute — using positional fallback "switch-${i}" which may mismatch on restore`)
      }
      const id = elId || `switch-${i}`
      const isChecked = await switchEl.getAttribute('data-state') === 'checked'
      state.toggleStates[id] = isChecked
    }

    // Get all number input values on the page
    const numberInputs = this.page.locator('input[type="number"]')
    const inputCount = await numberInputs.count()

    for (let i = 0; i < inputCount; i++) {
      const input = numberInputs.nth(i)
      const elId = await input.getAttribute('id')
      if (!elId) {
        console.warn(`Input at index ${i} has no id attribute — using positional fallback "input-${i}" which may mismatch on restore`)
      }
      const id = elId || `input-${i}`
      const value = await input.inputValue()
      state.inputValues[id] = value
    }

    return state
  }

  /**
   * Restore settings to a previous state
   */
  async restoreSettings(state: ConfigSettingsState): Promise<void> {
    // Navigate to the config page if not already there
    const currentUrl = this.page.url()
    if (!currentUrl.includes(state.configPath)) {
      await this.goto(state.configPath)
    }

    let hasChanges = false

    // Restore switch states
    const switches = this.page.locator('button[role="switch"]')
    const switchCount = await switches.count()

    for (let i = 0; i < switchCount; i++) {
      const switchEl = switches.nth(i)
      const elId = await switchEl.getAttribute('id')
      if (!elId) {
        console.warn(`Switch at index ${i} has no id attribute — using positional fallback "switch-${i}" which may mismatch on restore`)
      }
      const id = elId || `switch-${i}`

      if (state.toggleStates[id] !== undefined) {
        const currentState = await switchEl.getAttribute('data-state') === 'checked'
        if (currentState !== state.toggleStates[id]) {
          await switchEl.click()
          hasChanges = true
        }
      }
    }

    // Restore input values
    const numberInputs = this.page.locator('input[type="number"]')
    const inputCount = await numberInputs.count()

    for (let i = 0; i < inputCount; i++) {
      const input = numberInputs.nth(i)
      const elId = await input.getAttribute('id')
      if (!elId) {
        console.warn(`Input at index ${i} has no id attribute — using positional fallback "input-${i}" which may mismatch on restore`)
      }
      const id = elId || `input-${i}`

      if (state.inputValues[id] !== undefined) {
        const currentValue = await input.inputValue()
        if (currentValue !== state.inputValues[id]) {
          await input.clear()
          await input.fill(state.inputValues[id])
          hasChanges = true
        }
      }
    }

    // Save if changes were made
    if (hasChanges) {
      const canSave = await this.hasPendingChanges()
      if (canSave) {
        await this.saveSettings()
      }
    }
  }

  // === Client Settings Methods ===

  async toggleDropExcessRequests(): Promise<void> {
    await this.dropExcessRequestsSwitch.click()
  }

  async toggleLiteLLMFallbacks(): Promise<void> {
    await this.enableLiteLLMFallbacksSwitch.click()
  }

  async toggleDisableDBPings(): Promise<void> {
    await this.disableDBPingsSwitch.click()
  }

  // === Logging Settings Methods ===

  async toggleEnableLogging(): Promise<void> {
    await this.enableLoggingSwitch.click()
  }

  async toggleDisableContentLogging(): Promise<void> {
    await this.disableContentLoggingSwitch.click()
  }

  async setLogRetentionDays(days: number): Promise<void> {
    const input = this.page.locator('input[type="number"]').first()
    await input.clear()
    await input.fill(days.toString())
  }

  async getLogRetentionDays(): Promise<number> {
    const input = this.page.locator('input[type="number"]').first()
    const value = await input.inputValue()
    return parseInt(value, 10)
  }

  // === Security Settings Methods ===

  async isRateLimitingSectionVisible(): Promise<boolean> {
    return await this.page.getByText(/Rate Limiting/i).isVisible()
  }

  async toggleEnforceAuthOnInference(): Promise<void> {
    await this.enforceAuthOnInferenceSwitch.click()
  }

  async setRequiredHeaders(value: string): Promise<void> {
    await this.requiredHeadersTextarea.clear()
    await this.requiredHeadersTextarea.fill(value)
  }

  async setWorkspaceLoggingHeaders(value: string): Promise<void> {
    await this.workspaceLoggingHeadersTextarea.clear()
    await this.workspaceLoggingHeadersTextarea.fill(value)
  }

  async setAsyncJobResultTtl(value: string): Promise<void> {
    await this.asyncJobResultTtlInput.clear()
    await this.asyncJobResultTtlInput.fill(value)
  }

  // === Observability Settings Methods ===

  async getObservabilityConnectors(): Promise<string[]> {
    const connectorHeadings = this.page.locator('h3, h4').filter({ hasText: /Datadog|New Relic|OTel|OpenTelemetry|Maxim/i })
    const count = await connectorHeadings.count()
    const connectors: string[] = []

    for (let i = 0; i < count; i++) {
      const text = await connectorHeadings.nth(i).textContent()
      if (text) connectors.push(text)
    }

    return connectors
  }

  async toggleObservabilityConnector(connectorName: string): Promise<void> {
    const connectorSection = this.page.locator('div').filter({ hasText: new RegExp(connectorName, 'i') }).first()
    const toggleSwitch = connectorSection.locator('button[role="switch"]').first()
    await toggleSwitch.click()
  }

  // === Pricing Config Methods ===

  async setPricingDatasheetUrl(url: string): Promise<void> {
    await this.pricingDatasheetUrlInput.clear()
    await this.pricingDatasheetUrlInput.fill(url)
  }

  async triggerForceSync(): Promise<void> {
    await this.pricingForceSyncBtn.click()
    await this.waitForSuccessToast()
  }

  async savePricingConfig(): Promise<void> {
    await this.pricingSaveBtn.click()
    await this.waitForSuccessToast()
  }
}
