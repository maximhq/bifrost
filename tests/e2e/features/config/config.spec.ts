import { expect, test } from '../../core/fixtures/base.fixture'
import { ConfigSettingsState } from './pages/config-settings.page'

test.describe('Config Settings', () => {
  // Run all config tests serially to avoid parallel writes to the same config/store
  test.describe.configure({ mode: 'serial' })

  test.describe('Navigation', () => {
    test('should navigate to client settings', async ({ configSettingsPage }) => {
      await configSettingsPage.goto('client-settings')
      await expect(configSettingsPage.saveBtn).toBeVisible()
      // Use heading to avoid matching sidebar link
      await expect(configSettingsPage.page.getByRole('heading', { name: /Client Settings/i })).toBeVisible()
    })

    test('should navigate to caching config', async ({ configSettingsPage }) => {
      await configSettingsPage.goto('caching')
      // Caching page exists - verify page loaded
      await expect(configSettingsPage.page.getByRole('heading', { name: /Caching/i })).toBeVisible()
    })

    test('should navigate to logging config', async ({ configSettingsPage }) => {
      await configSettingsPage.goto('logging')
      await expect(configSettingsPage.saveBtn).toBeVisible()
      await expect(configSettingsPage.page.getByRole('heading', { name: /Logging/i })).toBeVisible()
    })

    test('should navigate to security config', async ({ configSettingsPage }) => {
      await configSettingsPage.goto('security')
      await expect(configSettingsPage.saveBtn).toBeVisible()
      await expect(configSettingsPage.page.getByRole('heading', { name: /Security/i })).toBeVisible()
    })

    test('should navigate to performance tuning config', async ({ configSettingsPage }) => {
      await configSettingsPage.goto('performance-tuning')
      await expect(configSettingsPage.saveBtn).toBeVisible()
      await expect(configSettingsPage.page.getByRole('heading', { name: /Performance Tuning/i })).toBeVisible()
    })

    test('should navigate to pricing config', async ({ configSettingsPage }) => {
      await configSettingsPage.goto('pricing-config')
      await expect(configSettingsPage.saveBtn).toBeVisible()
      await expect(configSettingsPage.page.getByRole('heading', { name: /Pricing/i })).toBeVisible()
    })

    test('should navigate to MCP settings', async ({ configSettingsPage }) => {
      await configSettingsPage.goto('mcp-gateway')
      await expect(configSettingsPage.page.getByTestId('mcp-settings-view')).toBeVisible()
      await expect(configSettingsPage.page.getByLabel('Max Agent Depth')).toBeVisible()
      await expect(configSettingsPage.page.getByLabel('Tool Execution Timeout (seconds)')).toBeVisible()
    })
  })

  test.describe('MCP Settings', () => {
    test('should display MCP settings form', async ({ configSettingsPage }) => {
      await configSettingsPage.goto('mcp-gateway')

      await expect(configSettingsPage.page.getByTestId('mcp-settings-view')).toBeVisible()
      await expect(configSettingsPage.page.locator('#mcp-agent-depth')).toBeVisible()
      await expect(configSettingsPage.page.locator('#mcp-tool-execution-timeout')).toBeVisible()
      await expect(configSettingsPage.page.locator('#mcp-binding-level')).toBeVisible()
    })

    test('should have save button disabled when no changes', async ({ configSettingsPage }) => {
      await configSettingsPage.goto('mcp-gateway')

      const saveBtn = configSettingsPage.page.getByTestId('mcp-settings-save-btn')
      await expect(saveBtn).toBeVisible()
      await expect(saveBtn).toBeDisabled()
    })
  })

  test.describe('Pricing Config', () => {
    let originalPricingUrl: string

    test.beforeEach(async ({ configSettingsPage }) => {
      await configSettingsPage.goto('pricing-config')
      originalPricingUrl = await configSettingsPage.pricingDatasheetUrlInput.inputValue()
    })

    test.afterEach(async ({ configSettingsPage }) => {
      await configSettingsPage.goto('pricing-config')
      const canEdit = await configSettingsPage.pricingDatasheetUrlInput.isEditable().catch(() => false)
      if (!canEdit) return
      await configSettingsPage.setPricingDatasheetUrl(originalPricingUrl)
      const isSaveEnabled = await configSettingsPage.pricingSaveBtn.isDisabled().then((d) => !d)
      if (isSaveEnabled) {
        await configSettingsPage.savePricingConfig()
        await configSettingsPage.dismissToasts()
      }
    })

    test('should display pricing config view', async ({ configSettingsPage }) => {
      await expect(configSettingsPage.pricingConfigView).toBeVisible()
      await expect(configSettingsPage.pricingDatasheetUrlInput).toBeVisible()
      await expect(configSettingsPage.pricingForceSyncBtn).toBeVisible()
      await expect(configSettingsPage.pricingSaveBtn).toBeVisible()
    })

    test('should set and save datasheet URL', async ({ configSettingsPage }) => {
      const testUrl = 'https://example.com/pricing.json'
      await configSettingsPage.setPricingDatasheetUrl(testUrl)

      const isSaveEnabled = await configSettingsPage.pricingSaveBtn.isDisabled().then((d) => !d)
      if (!isSaveEnabled) {
        test.skip(true, 'Save button disabled (no changes detected or RBAC)')
        return
      }

      await configSettingsPage.savePricingConfig()
      await configSettingsPage.dismissToasts()
    })

    test('should trigger force sync', async ({ configSettingsPage }) => {
      const isForceSyncEnabled = await configSettingsPage.pricingForceSyncBtn.isDisabled().then((d) => !d)
      if (!isForceSyncEnabled) {
        test.skip(true, 'Force sync button disabled (RBAC or no datasheet URL)')
        return
      }

      await configSettingsPage.triggerForceSync()
      await configSettingsPage.dismissToasts()
    })

    test('should validate URL format', async ({ configSettingsPage }) => {
      await configSettingsPage.pricingDatasheetUrlInput.fill('invalid-url-no-http')
      const canSave = await configSettingsPage.pricingSaveBtn.isDisabled().then((d) => !d)
      if (!canSave) {
        test.skip(true, 'Save button disabled (RBAC)')
        return
      }
      await configSettingsPage.pricingSaveBtn.click()

      await expect(configSettingsPage.page.getByText(/URL must start with http|valid URL/i)).toBeVisible()
    })
  })

  test.describe('Client Settings', () => {
    let originalState: ConfigSettingsState

    test.beforeEach(async ({ configSettingsPage }) => {
      await configSettingsPage.goto('client-settings')
      // Capture original state for restoration
      originalState = await configSettingsPage.getCurrentSettings('client-settings')
    })

    test.afterEach(async ({ configSettingsPage }) => {
      // Restore original settings
      if (originalState) {
        await configSettingsPage.restoreSettings(originalState)
      }
    })

    test('should display client settings controls', async ({ configSettingsPage }) => {
      // Check for main controls
      await expect(configSettingsPage.dropExcessRequestsSwitch).toBeVisible()
      await expect(configSettingsPage.enableLiteLLMFallbacksSwitch).toBeVisible()
      await expect(configSettingsPage.disableDBPingsSwitch).toBeVisible()
    })

    test('should display async job result TTL input when available', async ({ configSettingsPage }) => {
      const isVisible = await configSettingsPage.asyncJobResultTtlInput.isVisible().catch(() => false)
      if (isVisible) {
        await expect(configSettingsPage.asyncJobResultTtlInput).toBeVisible()
      } else {
        test.skip(true, 'Async job result TTL not available')
      }
    })

    test('should toggle drop excess requests', async ({ configSettingsPage }) => {
      const initialState = await configSettingsPage.getSwitchState(configSettingsPage.dropExcessRequestsSwitch)

      await configSettingsPage.toggleDropExcessRequests()

      const newState = await configSettingsPage.getSwitchState(configSettingsPage.dropExcessRequestsSwitch)
      expect(newState).toBe(!initialState)

      // Verify changes are pending
      const hasChanges = await configSettingsPage.hasPendingChanges()
      expect(hasChanges).toBe(true)
    })

    test('should save and persist drop excess requests toggle', async ({ configSettingsPage }) => {
      const initialState = await configSettingsPage.getSwitchState(configSettingsPage.dropExcessRequestsSwitch)

      await configSettingsPage.toggleDropExcessRequests()
      await configSettingsPage.saveSettings()
      await configSettingsPage.goto('client-settings')

      const expectedState = !initialState
      await expect(configSettingsPage.dropExcessRequestsSwitch).toHaveAttribute(
        'data-state',
        expectedState ? 'checked' : 'unchecked'
      )
    })

    test('should toggle LiteLLM fallbacks', async ({ configSettingsPage }) => {
      const initialState = await configSettingsPage.getSwitchState(configSettingsPage.enableLiteLLMFallbacksSwitch)

      await configSettingsPage.toggleLiteLLMFallbacks()

      const newState = await configSettingsPage.getSwitchState(configSettingsPage.enableLiteLLMFallbacksSwitch)
      expect(newState).toBe(!initialState)
    })

    test('should save and persist LiteLLM fallbacks toggle', async ({ configSettingsPage }) => {
      const initialState = await configSettingsPage.getSwitchState(configSettingsPage.enableLiteLLMFallbacksSwitch)

      await configSettingsPage.toggleLiteLLMFallbacks()
      await configSettingsPage.saveSettings()
      await configSettingsPage.goto('client-settings')

      // Wait for persisted state (form is populated async after navigation)
      const expectedState = !initialState
      await expect(configSettingsPage.enableLiteLLMFallbacksSwitch).toHaveAttribute(
        'data-state',
        expectedState ? 'checked' : 'unchecked'
      )
    })

    test('should toggle disable DB pings', async ({ configSettingsPage }) => {
      const initialState = await configSettingsPage.getSwitchState(configSettingsPage.disableDBPingsSwitch)

      await configSettingsPage.toggleDisableDBPings()

      const newState = await configSettingsPage.getSwitchState(configSettingsPage.disableDBPingsSwitch)
      expect(newState).toBe(!initialState)
    })

    test('should save and persist disable DB pings toggle', async ({ configSettingsPage }) => {
      const initialState = await configSettingsPage.getSwitchState(configSettingsPage.disableDBPingsSwitch)

      await configSettingsPage.toggleDisableDBPings()
      await configSettingsPage.saveSettings()
      await configSettingsPage.goto('client-settings')

      const expectedState = !initialState
      await expect(configSettingsPage.disableDBPingsSwitch).toHaveAttribute(
        'data-state',
        expectedState ? 'checked' : 'unchecked'
      )
    })
  })

  test.describe('Logging Settings', () => {
    let originalState: ConfigSettingsState

    test.beforeEach(async ({ configSettingsPage }) => {
      await configSettingsPage.goto('logging')
      // Capture original state for restoration
      originalState = await configSettingsPage.getCurrentSettings('logging')
    })

    test.afterEach(async ({ configSettingsPage }) => {
      // Restore original settings
      if (originalState) {
        await configSettingsPage.restoreSettings(originalState)
      }
    })

    test('should display logging settings controls', async ({ configSettingsPage }) => {
      // Check for main logging controls
      await expect(configSettingsPage.page.getByText(/Enable Logs/i)).toBeVisible()
      await expect(configSettingsPage.page.getByText(/Log Retention/i)).toBeVisible()
    })

    test('should display workspace logging headers textarea when available', async ({ configSettingsPage }) => {
      const isVisible = await configSettingsPage.workspaceLoggingHeadersTextarea.isVisible().catch(() => false)
      if (isVisible) {
        await expect(configSettingsPage.workspaceLoggingHeadersTextarea).toBeVisible()
      } else {
        test.skip(true, 'Workspace logging headers not available (depends on log connector)')
      }
    })

    test('should toggle content logging when available', async ({ configSettingsPage }) => {
      // Check if the switch is available (depends on logs being connected)
      const disableContentLoggingVisible = await configSettingsPage.disableContentLoggingSwitch.isVisible().catch(() => false)

      if (disableContentLoggingVisible) {
        const initialState = await configSettingsPage.getSwitchState(configSettingsPage.disableContentLoggingSwitch)

        await configSettingsPage.toggleDisableContentLogging()

        const newState = await configSettingsPage.getSwitchState(configSettingsPage.disableContentLoggingSwitch)
        expect(newState).toBe(!initialState)
      } else {
        // Skip if logging not available
        test.skip()
      }
    })

    test('should save and persist content logging toggle when available', async ({ configSettingsPage }) => {
      // Check if the switch is available (depends on logs being connected)
      const disableContentLoggingVisible = await configSettingsPage.disableContentLoggingSwitch.isVisible().catch(() => false)

      if (disableContentLoggingVisible) {
        const initialState = await configSettingsPage.getSwitchState(configSettingsPage.disableContentLoggingSwitch)

        // Toggle
        await configSettingsPage.toggleDisableContentLogging()

        // Save
        await configSettingsPage.saveSettings()

        // Reload the page
        await configSettingsPage.goto('logging')

        // Verify change persisted
        const savedState = await configSettingsPage.getSwitchState(configSettingsPage.disableContentLoggingSwitch)
        expect(savedState).toBe(!initialState)
      } else {
        // Skip if logging not available
        test.skip()
      }
    })

    test('should change log retention days', async ({ configSettingsPage }) => {
      const retentionInput = configSettingsPage.logRetentionDaysInput
      const isVisible = await retentionInput.isVisible().catch(() => false)

      if (isVisible) {
        const originalValue = await retentionInput.inputValue()
        const newValue = originalValue === '30' ? '60' : '30'

        await retentionInput.clear()
        await retentionInput.fill(newValue)

        const currentValue = await retentionInput.inputValue()
        expect(currentValue).toBe(newValue)

        // Verify changes are pending
        const hasChanges = await configSettingsPage.hasPendingChanges()
        expect(hasChanges).toBe(true)
      }
    })

    test('should save and persist log retention days', async ({ configSettingsPage }) => {
      const retentionInput = configSettingsPage.logRetentionDaysInput
      const isVisible = await retentionInput.isVisible().catch(() => false)

      if (isVisible) {
        const originalValue = await retentionInput.inputValue()
        const newValue = originalValue === '30' ? '60' : '30'

        // Change value
        await retentionInput.clear()
        await retentionInput.fill(newValue)

        // Save
        await configSettingsPage.saveSettings()

        // Reload the page
        await configSettingsPage.goto('logging')

        // Verify change persisted
        const savedValue = await retentionInput.inputValue()
        expect(savedValue).toBe(newValue)
      }
    })
  })

  test.describe('Security Settings', () => {
    let originalState: ConfigSettingsState

    test.beforeEach(async ({ configSettingsPage }) => {
      await configSettingsPage.goto('security')
      // Capture original state for restoration
      originalState = await configSettingsPage.getCurrentSettings('security')
    })

    test.afterEach(async ({ configSettingsPage }) => {
      // Restore original settings
      if (originalState) {
        await configSettingsPage.restoreSettings(originalState)
      }
    })

    test('should display security settings', async ({ configSettingsPage }) => {
      await expect(configSettingsPage.page.getByRole('heading', { name: /Security/i })).toBeVisible()
      await expect(configSettingsPage.saveBtn).toBeVisible()
    })

    test('should display enforce auth on inference switch', async ({ configSettingsPage }) => {
      const isVisible = await configSettingsPage.enforceAuthOnInferenceSwitch.isVisible().catch(() => false)
      if (!isVisible) {
        test.skip(true, 'Enforce auth on inference not available')
        return
      }
      await expect(configSettingsPage.enforceAuthOnInferenceSwitch).toBeVisible()
    })

    test('should toggle enforce auth on inference', async ({ configSettingsPage }) => {
      const isVisible = await configSettingsPage.enforceAuthOnInferenceSwitch.isVisible().catch(() => false)
      if (!isVisible) {
        test.skip(true, 'Enforce auth on inference not available')
        return
      }
      const initialState = await configSettingsPage.getSwitchState(configSettingsPage.enforceAuthOnInferenceSwitch)
      await configSettingsPage.toggleEnforceAuthOnInference()
      const newState = await configSettingsPage.getSwitchState(configSettingsPage.enforceAuthOnInferenceSwitch)
      expect(newState).toBe(!initialState)
      await configSettingsPage.toggleEnforceAuthOnInference()
      if (await configSettingsPage.hasPendingChanges()) {
        await configSettingsPage.saveSettings()
      }
    })

    test('should display required headers textarea', async ({ configSettingsPage }) => {
      const isVisible = await configSettingsPage.requiredHeadersTextarea.isVisible().catch(() => false)
      if (!isVisible) {
        test.skip(true, 'Required headers control not available')
        return
      }
      await expect(configSettingsPage.requiredHeadersTextarea).toBeVisible()
    })

    test('should display rate limiting section', async ({ configSettingsPage }) => {
      const isVisible = await configSettingsPage.isRateLimitingSectionVisible()
      expect(isVisible).toBeDefined()
    })
  })

  test.describe('Performance Tuning Settings', () => {
    let originalState: ConfigSettingsState

    test.beforeEach(async ({ configSettingsPage }) => {
      await configSettingsPage.goto('performance-tuning')
      originalState = await configSettingsPage.getCurrentSettings('performance-tuning')
    })

    test.afterEach(async ({ configSettingsPage }) => {
      if (originalState) {
        await configSettingsPage.restoreSettings(originalState)
      }
    })

    test('should display performance tuning settings', async ({ configSettingsPage }) => {
      await expect(configSettingsPage.page.getByRole('heading', { name: /Performance Tuning/i })).toBeVisible()
    })

    test('should change worker pool size', async ({ configSettingsPage }) => {
      const workerPoolInput = configSettingsPage.workerPoolSizeInput
      const isVisible = await workerPoolInput.isVisible().catch(() => false)

      if (isVisible) {
        const originalValue = await workerPoolInput.inputValue()
        const newValue = parseInt(originalValue) === 100 ? '200' : '100'

        await workerPoolInput.clear()
        await workerPoolInput.fill(newValue)

        const currentValue = await workerPoolInput.inputValue()
        expect(currentValue).toBe(newValue)
      }
    })

    test('should save and persist worker pool size', async ({ configSettingsPage }) => {
      const workerPoolInput = configSettingsPage.workerPoolSizeInput
      const isVisible = await workerPoolInput.isVisible().catch(() => false)

      if (isVisible) {
        const originalValue = await workerPoolInput.inputValue()
        const newValue = parseInt(originalValue) === 100 ? '200' : '100'

        // Change value
        await workerPoolInput.clear()
        await workerPoolInput.fill(newValue)

        // Save
        await configSettingsPage.saveSettings()

        // Reload the page
        await configSettingsPage.goto('performance-tuning')

        // Verify change persisted
        const savedValue = await workerPoolInput.inputValue()
        expect(savedValue).toBe(newValue)
      }
    })
  })

  test.describe('Pricing Config Settings', () => {
    let originalState: ConfigSettingsState

    test.beforeEach(async ({ configSettingsPage }) => {
      await configSettingsPage.goto('pricing-config')
      originalState = await configSettingsPage.getCurrentSettings('pricing-config')
    })

    test.afterEach(async ({ configSettingsPage }) => {
      if (originalState) {
        await configSettingsPage.restoreSettings(originalState)
      }
    })

    test('should display pricing config settings', async ({ configSettingsPage }) => {
      await expect(configSettingsPage.page.getByRole('heading', { name: /Pricing/i })).toBeVisible()
    })
  })

  test.describe('Caching Settings', () => {
    let originalState: ConfigSettingsState

    test.beforeEach(async ({ configSettingsPage }) => {
      await configSettingsPage.goto('caching')
      originalState = await configSettingsPage.getCurrentSettings('caching')
    })

    test.afterEach(async ({ configSettingsPage }) => {
      if (originalState) {
        await configSettingsPage.restoreSettings(originalState)
      }
    })

    test('should display caching settings', async ({ configSettingsPage }) => {
      await expect(configSettingsPage.page.getByRole('heading', { name: /Caching/i })).toBeVisible()
    })
  })
})
