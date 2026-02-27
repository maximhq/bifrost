import { Page, Locator, expect } from '@playwright/test'
import { BasePage } from '../../../core/pages/base.page'
import { waitForNetworkIdle } from '../../../core/utils/test-helpers'

/**
 * Observability connector state
 */
export interface ObservabilityState {
  otelEnabled: boolean
  maximEnabled: boolean
  datadogEnabled: boolean
  newRelicEnabled: boolean
}

export class ObservabilityPage extends BasePage {
  // Connector tabs/buttons - using exact names from the UI
  readonly otelTab: Locator
  readonly maximTab: Locator
  readonly datadogTab: Locator
  readonly newRelicTab: Locator
  
  // Save button (within the active view)
  readonly saveBtn: Locator
  
  // OTel form elements
  readonly otelToggle: Locator
  readonly otelEndpointInput: Locator
  readonly otelHeadersTable: Locator
  
  // Maxim form elements
  readonly maximToggle: Locator
  readonly maximApiKeyInput: Locator

  constructor(page: Page) {
    super(page)
    
    // Connector tabs - using text content to find the exact button
    this.otelTab = page.locator('button:has-text("Open Telemetry")')
    this.maximTab = page.locator('button:has-text("Maxim"):not(:has-text("Save"))')
    this.datadogTab = page.locator('button:has-text("Datadog"):not(:has-text("Save"))')
    this.newRelicTab = page.locator('button:has-text("New Relic")')
    
    // Save button
    this.saveBtn = page.getByRole('button', { name: /Save/i })
    
    // OTel elements
    this.otelToggle = page.locator('button[role="switch"]').first()
    this.otelEndpointInput = page.getByPlaceholder(/otel-collector/i)
    this.otelHeadersTable = page.locator('[data-testid="headers-table"]')
    
    // Maxim elements
    this.maximToggle = page.locator('button[role="switch"]').first()
    this.maximApiKeyInput = page.getByPlaceholder(/API Key/i)
  }

  async goto(): Promise<void> {
    await this.page.goto('/workspace/observability')
    await waitForNetworkIdle(this.page)
  }

  /**
   * Select a connector tab
   */
  async selectConnector(connector: 'otel' | 'maxim' | 'datadog' | 'newrelic'): Promise<void> {
    const tabs = {
      'otel': this.otelTab,
      'maxim': this.maximTab,
      'datadog': this.datadogTab,
      'newrelic': this.newRelicTab,
    }
    
    const tab = tabs[connector]
    
    // Wait for tab to be visible first
    await tab.waitFor({ state: 'visible', timeout: 10000 })
    
    const isDisabled = await tab.getAttribute('aria-disabled') === 'true' || 
                       await tab.isDisabled()
    
    if (!isDisabled) {
      await tab.click()
      await waitForNetworkIdle(this.page)
    }
  }

  /**
   * Check if a connector tab is available (not disabled)
   */
  async isConnectorAvailable(connector: 'otel' | 'maxim' | 'datadog' | 'newrelic'): Promise<boolean> {
    const tabs = {
      'otel': this.otelTab,
      'maxim': this.maximTab,
      'datadog': this.datadogTab,
      'newrelic': this.newRelicTab,
    }
    
    const tab = tabs[connector]
    const isVisible = await tab.isVisible().catch(() => false)
    if (!isVisible) return false
    
    const isDisabled = await tab.getAttribute('aria-disabled') === 'true' ||
                       await tab.isDisabled()
    return !isDisabled
  }

  /**
   * Get the currently selected connector
   */
  async getSelectedConnector(): Promise<string | null> {
    // The selected connector has aria-current="page"
    const selected = this.page.locator('button[aria-current="page"]')
    const isVisible = await selected.isVisible().catch(() => false)
    if (!isVisible) return null
    return await selected.textContent()
  }

  /**
   * Check if a connector is enabled (toggle is checked)
   */
  async isConnectorEnabled(): Promise<boolean> {
    const toggle = this.page.locator('button[role="switch"]').first()
    const isVisible = await toggle.isVisible().catch(() => false)
    if (!isVisible) return false
    const state = await toggle.getAttribute('data-state')
    return state === 'checked'
  }

  /**
   * Check if the toggle is clickable (not disabled)
   */
  async isToggleEnabled(): Promise<boolean> {
    const toggle = this.page.locator('button[role="switch"]').first()
    const isVisible = await toggle.isVisible().catch(() => false)
    if (!isVisible) return false
    const isDisabled = await toggle.isDisabled()
    return !isDisabled
  }

  /**
   * Toggle the current connector enabled state (only if toggle is enabled)
   */
  async toggleConnector(): Promise<boolean> {
    const toggle = this.page.locator('button[role="switch"]').first()
    const isVisible = await toggle.isVisible().catch(() => false)
    if (!isVisible) return false
    
    const isDisabled = await toggle.isDisabled()
    if (isDisabled) return false
    
    await toggle.click()
    return true
  }

  /**
   * Enable a connector
   */
  async enableConnector(toggle: Locator): Promise<void> {
    const isChecked = await toggle.getAttribute('data-state') === 'checked'
    if (!isChecked) {
      const isDisabled = await toggle.isDisabled()
      if (!isDisabled) {
        await toggle.click()
      }
    }
  }

  /**
   * Disable a connector
   */
  async disableConnector(toggle: Locator): Promise<void> {
    const isChecked = await toggle.getAttribute('data-state') === 'checked'
    if (isChecked) {
      const isDisabled = await toggle.isDisabled()
      if (!isDisabled) {
        await toggle.click()
      }
    }
  }

  /**
   * Enable Metrics Export
   */
  async enableMetricsExport(): Promise<void> {
    await this.selectConnector('otel')
    const switch_ = this.page.getByTestId('otel-metrics-export-toggle')
    await switch_.waitFor({ state: 'visible', timeout: 5000 })
    const checked = await switch_.getAttribute('data-state') === 'checked'
    if (!checked) {
      await switch_.click()
      await this.page.waitForTimeout(400)
    }
  }

  /**
   * Configure OTel endpoint
   */
  async configureOtelEndpoint(endpoint: string): Promise<void> {
    await this.selectConnector('otel')
    const endpointInput = this.page.getByPlaceholder(/otel-collector/i)
    
    const isVisible = await endpointInput.isVisible().catch(() => false)
    if (isVisible) {
      await endpointInput.clear()
      await endpointInput.fill(endpoint)
    }
  }

  /**
   * Configure Maxim API key
   */
  async configureMaximApiKey(apiKey: string): Promise<void> {
    await this.selectConnector('maxim')
    const apiKeyInput = this.page.getByPlaceholder(/api-key/i)
    
    const isVisible = await apiKeyInput.isVisible().catch(() => false)
    if (isVisible) {
      await apiKeyInput.clear()
      await apiKeyInput.fill(apiKey)
    }
  }

  /**
   * Save the current connector configuration
   */
  async saveConfiguration(): Promise<void> {
    await this.saveBtn.click()
    await this.waitForSuccessToast()
  }

  /**
   * Get current state of all connectors (enabled/disabled)
   */
  async getCurrentState(): Promise<ObservabilityState> {
    const state: ObservabilityState = {
      otelEnabled: false,
      maximEnabled: false,
      datadogEnabled: false,
      newRelicEnabled: false,
    }

    // Check OTel
    if (await this.isConnectorAvailable('otel')) {
      await this.selectConnector('otel')
      state.otelEnabled = await this.isConnectorEnabled()
    }

    // Check Maxim
    if (await this.isConnectorAvailable('maxim')) {
      await this.selectConnector('maxim')
      state.maximEnabled = await this.isConnectorEnabled()
    }

    // Datadog might require enterprise
    if (await this.isConnectorAvailable('datadog')) {
      await this.selectConnector('datadog')
      state.datadogEnabled = await this.isConnectorEnabled()
    }

    return state
  }

  /**
   * Disable all connectors
   */
  async disableAllConnectors(): Promise<void> {
    // Disable OTel
    if (await this.isConnectorAvailable('otel')) {
      try {
        await this.selectConnector('otel')
        if (await this.isConnectorEnabled() && await this.isToggleEnabled()) {
          await this.toggleConnector()
          await this.saveConfiguration().catch(() => {})
        }
      } catch {
        // Ignore errors
      }
    }

    // Disable Maxim
    if (await this.isConnectorAvailable('maxim')) {
      try {
        await this.selectConnector('maxim')
        if (await this.isConnectorEnabled() && await this.isToggleEnabled()) {
          await this.toggleConnector()
          await this.saveConfiguration().catch(() => {})
        }
      } catch {
        // Ignore errors
      }
    }

    // Disable Datadog (enterprise)
    if (await this.isConnectorAvailable('datadog')) {
      try {
        await this.selectConnector('datadog')
        if (await this.isConnectorEnabled() && await this.isToggleEnabled()) {
          await this.toggleConnector()
          await this.saveConfiguration().catch(() => {})
        }
      } catch {
        // Ignore errors - might be enterprise only
      }
    }
  }

  /**
   * Check if OTel-specific content is visible (confirms we're on the OTel panel).
   * The metrics endpoint input is only in the DOM when "Enable Metrics Export" is on,
   * so we also treat the "Enable Metrics Export" section as OTel content.
   */
  async isMetricsEndpointVisible(): Promise<boolean> {
    // Metrics endpoint input (only visible when Enable Metrics Export is on)
    const metricsInputByValue = this.page.locator('input[value*="/metrics"]')
    const valueVisible = await metricsInputByValue.isVisible().catch(() => false)
    if (valueVisible) return true

    // "Enable Metrics Export" section is always visible on OTel tab (metrics subsection)
    const enableMetricsVisible = await this.page.getByText(/Enable Metrics Export/i).isVisible().catch(() => false)
    if (enableMetricsVisible) return true

    // Label "Metrics Endpoint" (when metrics export is enabled)
    const labelVisible = await this.page.getByText(/Metrics Endpoint/i).isVisible().catch(() => false)
    return labelVisible
  }

  /**
   * Get the metrics endpoint URL value
   */
  async getMetricsEndpointValue(): Promise<string | null> {
    const metricsInput = this.page.locator('input[value*="/metrics"]').first()
    const isVisible = await metricsInput.isVisible().catch(() => false)
    if (!isVisible) return null
    return await metricsInput.inputValue()
  }
}
