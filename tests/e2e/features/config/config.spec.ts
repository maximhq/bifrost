import { coreConfigApi } from '../../core/actions/api'
import { expect, test } from '../../core/fixtures/base.fixture'
import { ConfigSettingsState } from './pages/config-settings.page'
import { DefaultCoreConfig } from '../../../../ui/lib/types/config'

test.describe('Inference auth setup defaults', () => {
  test.use({ skipAutoLogin: true })

  for (const existing of [false, true]) {
    test(`inference setup preserves explicit opt-out (existing admin: ${existing})`, async ({ page }) => {
      // Exercise the real form without changing credentials on the shared test gateway.
      await page.route('**/api/**', async route => {
        const path = new URL(route.request().url()).pathname
        if (path === '/api/config') {
          if (route.request().method() === 'PUT') {
            await route.fulfill({ json: { status: 'success', message: 'configuration updated successfully' } })
          } else {
            await route.fulfill({ json: {
              client_config: { ...DefaultCoreConfig, enforce_auth_on_inference: false, allowed_origins: ['http://localhost:3000'] },
              auth_config: existing ? { is_enabled: true, admin_username: { value: 'admin', ref: '' }, admin_password: { value: '', ref: '' } } : null,
              framework_config: {}, is_db_connected: true, metadata: { onboarding_dismissed: true },
            } })
          }
        } else if (path === '/api/version') {
          await route.fulfill({ json: '1.0.0' })
        } else if (path === '/api/session/is-auth-enabled') {
          await route.fulfill({ json: { is_auth_enabled: false, has_valid_token: false, auth_type: 'none', inference_auth_enforced: false } })
        } else {
          await route.fulfill({ json: {} })
        }
      })
      await page.goto('/workspace/config/security')
      const inference = page.getByTestId('enforce-auth-on-inference-switch')
      const dashboard = page.locator('#auth-enabled')
      await expect(inference).not.toBeChecked()
      if (!existing) {
        await dashboard.click()
        await expect(inference).toBeChecked()
        await inference.click()
      }
      await expect(page.getByTestId('inference-auth-off-warning')).toBeVisible()
      // Toggling dashboard auth again must not undo the operator's explicit choice.
      await dashboard.click()
      await dashboard.click()
      await expect(inference).not.toBeChecked()
      await page.locator('#admin-username').fill('admin')
      await page.locator('#admin-password').fill('StrongPassword1!')
      if (!existing) await page.locator('#setup-token').fill('test-setup-token')
      const submitted = page.waitForRequest(r => new URL(r.url()).pathname === '/api/config' && r.method() === 'PUT')
      await page.getByRole('button', { name: /Save/i }).click()
      expect((await submitted).postDataJSON().client_config.enforce_auth_on_inference).toBe(false)
    })
  }

  test('inference setup preserves a choice made before enabling dashboard auth', async ({ page }) => {
    await page.route('**/api/**', async route => {
      const path = new URL(route.request().url()).pathname
      await route.fulfill({ json: path === '/api/config' ? {
        client_config: { ...DefaultCoreConfig, enforce_auth_on_inference: false }, auth_config: null,
        framework_config: {}, is_db_connected: true, metadata: { onboarding_dismissed: true },
      } : path === '/api/version' ? '1.0.0' : path === '/api/session/is-auth-enabled' ? { is_auth_enabled: false, auth_type: 'none' } : {} })
    })
    await page.goto('/workspace/config/security')
    const inference = page.getByTestId('enforce-auth-on-inference-switch')
    await inference.click()
    await inference.click()
    await page.locator('#auth-enabled').click()
    await expect(inference).not.toBeChecked()
    await expect(page.getByTestId('inference-auth-off-warning')).toBeVisible()
  })

  test('canceling first-time dashboard auth restores the stored inference setting', async ({ page }) => {
    await page.route('**/api/**', async route => {
      const path = new URL(route.request().url()).pathname
      await route.fulfill({ json: path === '/api/config' ? {
        client_config: { ...DefaultCoreConfig, enforce_auth_on_inference: false }, auth_config: null,
        framework_config: {}, is_db_connected: true, metadata: { onboarding_dismissed: true },
      } : path === '/api/version' ? '1.0.0' : path === '/api/session/is-auth-enabled' ? { is_auth_enabled: false, auth_type: 'none' } : {} })
    })
    await page.goto('/workspace/config/security')
    const inference = page.getByTestId('enforce-auth-on-inference-switch')
    const dashboard = page.locator('#auth-enabled')
    await dashboard.click()
    await expect(inference).toBeChecked()
    // The switch mirrors the client_config value Save sends, so an unchecked switch means cancel did not persist inference auth.
    await dashboard.click()
    await expect(inference).not.toBeChecked()
  })

  test('setup toggles stay disabled when the stored config failed to load', async ({ page }) => {
    // The dashboard shell loads GET /api/config?from_db=false, while the config layout gates the from_db=true copy
    // on loading only, not on error. With just that copy failing the form rendered with no config, and a toggle made
    // then was overwritten by a later successful refetch, saving dashboard auth on and inference auth off.
    await page.route('**/api/**', async route => {
      const url = new URL(route.request().url())
      if (url.pathname === '/api/config' && url.searchParams.get('from_db') === 'true') {
        await route.fulfill({ status: 500, json: { error: { message: 'config store unavailable' } } })
        return
      }
      await route.fulfill({ json: url.pathname === '/api/config' ? {
        client_config: { ...DefaultCoreConfig, enforce_auth_on_inference: false }, auth_config: null,
        framework_config: {}, is_db_connected: true, metadata: { onboarding_dismissed: true },
      } : url.pathname === '/api/version' ? '1.0.0' : url.pathname === '/api/session/is-auth-enabled' ? { is_auth_enabled: false, auth_type: 'none' } : {} })
    })
    await page.goto('/workspace/config/security')
    await expect(page.locator('#auth-enabled')).toBeDisabled()
    await expect(page.getByTestId('enforce-auth-on-inference-switch')).toBeDisabled()
  })
})

test.describe('Onboarding widget snooze', () => {
  test.use({ skipAutoLogin: true })

  test('remind me later opens a picker above the widget and snoozes it', async ({ page }) => {
    // Mock an unconfigured instance so the checklist is incomplete and visible.
    await page.route('**/api/**', async route => {
      const path = new URL(route.request().url()).pathname
      if (path === '/api/config') {
        await route.fulfill({ json: {
          client_config: { ...DefaultCoreConfig, enforce_auth_on_inference: false, allowed_origins: [] },
          auth_config: null,
          framework_config: {}, is_db_connected: true, metadata: {},
        } })
      } else if (path === '/api/version') {
        await route.fulfill({ json: '1.0.0' })
      } else if (path === '/api/session/is-auth-enabled') {
        await route.fulfill({ json: { is_auth_enabled: false, has_valid_token: false, auth_type: 'none', inference_auth_enforced: false } })
      } else if (path === '/api/keys') {
        await route.fulfill({ json: [] })
      } else {
        await route.fulfill({ json: {} })
      }
    })
    await page.goto('/workspace/config/client-settings')

    await page.getByTestId('onboarding-later').click()
    // The popover is portaled to body, so it must stack above the widget card.
    // A non-forced click fails if the card covers the option.
    await page.getByTestId('onboarding-remind-tomorrow').click({ timeout: 5000 })
    await expect(page.getByTestId('onboarding-later')).not.toBeVisible()
    const cookies = await page.context().cookies()
    expect(cookies.some(c => c.name === 'bifrost_onboarding_remind_at')).toBe(true)
  })
})

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
      await expect(configSettingsPage.page.getByTestId('mcp-agent-depth-input')).toBeVisible()
      await expect(configSettingsPage.page.getByTestId('mcp-tool-timeout-input')).toBeVisible()
    })
  })

  test.describe('MCP Settings', () => {
    test('should display MCP settings form', async ({ configSettingsPage }) => {
      await configSettingsPage.goto('mcp-gateway')

      await expect(configSettingsPage.page.getByTestId('mcp-settings-view')).toBeVisible()
      await expect(configSettingsPage.page.getByTestId('mcp-agent-depth-input')).toBeVisible()
      await expect(configSettingsPage.page.getByTestId('mcp-tool-timeout-input')).toBeVisible()
      await expect(configSettingsPage.page.getByTestId('mcp-binding-level')).toBeVisible()
    })

    test('should have save button disabled when no changes', async ({ configSettingsPage }) => {
      await configSettingsPage.goto('mcp-gateway')

      const saveBtn = configSettingsPage.page.getByTestId('mcp-settings-save-btn')
      await expect(saveBtn).toBeVisible()
      await expect(saveBtn).toBeDisabled()
    })
  })

  test.describe('Pricing Config', () => {
    let originalPricingUrl: string | null = null

    test.beforeEach(async ({ configSettingsPage }) => {
      await configSettingsPage.goto('pricing-config')
      originalPricingUrl = await configSettingsPage.pricingDatasheetUrlInput.inputValue()
    })

    test.afterEach(async ({ configSettingsPage }) => {
      await configSettingsPage.goto('pricing-config')
      const canEdit = await configSettingsPage.pricingDatasheetUrlInput.isEditable().catch(() => false)
      if (!canEdit || originalPricingUrl === null) return
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
      await expect(configSettingsPage.hideDeletedVirtualKeysInFiltersSwitch).toBeVisible()
    })

    test('should toggle hide deleted virtual keys in filters', async ({ configSettingsPage }) => {
      const initialState = await configSettingsPage.getSwitchState(configSettingsPage.hideDeletedVirtualKeysInFiltersSwitch)

      await configSettingsPage.toggleHideDeletedVirtualKeysInFilters()

      const newState = await configSettingsPage.getSwitchState(configSettingsPage.hideDeletedVirtualKeysInFiltersSwitch)
      expect(newState).toBe(!initialState)

      const hasChanges = await configSettingsPage.hasPendingChanges()
      expect(hasChanges).toBe(true)
    })

    test('should save and persist hide deleted virtual keys in filters toggle', async ({ configSettingsPage }) => {
      const initialState = await configSettingsPage.getSwitchState(configSettingsPage.hideDeletedVirtualKeysInFiltersSwitch)

      await configSettingsPage.toggleHideDeletedVirtualKeysInFilters()
      await configSettingsPage.saveSettings()
      await configSettingsPage.goto('logging')

      const expectedState = !initialState
      await expect(configSettingsPage.hideDeletedVirtualKeysInFiltersSwitch).toHaveAttribute(
        'data-state',
        expectedState ? 'checked' : 'unchecked'
      )
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

    test.describe('Dashboard Auth Confirmation', () => {
      // Once an admin account exists, switching dashboard protection off lets
      // anyone reach the settings API without signing in, so switching it back
      // on from that state must prove control of the instance with the stored
      // admin password (or the operator's setup token). The fixture signs the
      // browser back in with BIFROST_ADMIN_USERNAME/PASSWORD when the server
      // flushes sessions, so that password is also what this test confirms with.
      const adminPassword = process.env.BIFROST_ADMIN_PASSWORD

      test.beforeEach(async ({ configSettingsPage, request }) => {
        test.skip(!adminPassword, 'BIFROST_ADMIN_PASSWORD is not set, so there is no stored admin password to confirm with')
        const current = await coreConfigApi.get(request)
        test.skip(
          current.auth_config?.is_enabled !== true,
          'Dashboard auth is not enabled with stored credentials on this instance'
        )
        // The onboarding checklist covers the Save button in the bottom-right
        // corner on a fresh instance, so dismiss it before touching settings.
        await configSettingsPage.dismissOnboardingWidget()
      })

      test.afterEach(async ({ configSettingsPage, request }) => {
        // If the test stopped while protection was off, turn it back on with the
        // proof the server now requires. GET fails with 401 once auth is already
        // on and the session was flushed, which means there is nothing to undo.
        const current = await coreConfigApi.get(request).catch(() => null)
        if (current?.auth_config && !current.auth_config.is_enabled) {
          await coreConfigApi.setDashboardAuthEnabled(request, true, { currentPassword: adminPassword })
        }
        // Refresh the page state so the outer restore sees the switch already on
        // rather than clicking it and saving without proof.
        await configSettingsPage.goto('security')
        await expect(configSettingsPage.dashboardAuthSwitch).toHaveAttribute('data-state', 'checked', { timeout: 20000 })
      })

      test('should require the current admin password to turn protection back on after disabling it', async ({
        configSettingsPage,
        request,
      }) => {
        // Signed-in session: no confirmation field while protection is on, and
        // switching it off needs no proof.
        await expect(configSettingsPage.dashboardAuthSwitch).toHaveAttribute('data-state', 'checked')
        await expect(configSettingsPage.currentPasswordInput).not.toBeVisible()
        await configSettingsPage.toggleDashboardAuth()
        await configSettingsPage.saveSettings()

        await configSettingsPage.goto('security')
        await expect(configSettingsPage.dashboardAuthSwitch).toHaveAttribute('data-state', 'unchecked')
        expect((await coreConfigApi.get(request)).auth_config?.is_enabled).toBe(false)

        // Protection off, stored account: turning it on reveals the confirmation
        // field, and saving without filling it is refused in the form itself.
        await configSettingsPage.toggleDashboardAuth()
        await expect(configSettingsPage.currentPasswordInput).toBeVisible()
        await configSettingsPage.saveBtn.click()
        await expect(configSettingsPage.currentPasswordError).toBeVisible()
        await expect(configSettingsPage.currentPasswordError).toContainText(/current admin password/i)
        expect((await coreConfigApi.get(request)).auth_config?.is_enabled).toBe(false)

        // A wrong password is refused by the server with 403, shown inline on the
        // same field rather than as a toast, and nothing is saved.
        await configSettingsPage.setCurrentPassword(`${adminPassword}-wrong`)
        await configSettingsPage.saveBtn.click()
        await expect(configSettingsPage.currentPasswordError).toContainText(/current_password|setup_token/i)
        await expect(configSettingsPage.getToast('error')).not.toBeVisible()
        expect((await coreConfigApi.get(request)).auth_config?.is_enabled).toBe(false)

        // The stored password turns protection back on. The server flushes every
        // session on that change, so the next page load goes through the login
        // redirect, which the base fixture completes.
        await configSettingsPage.setCurrentPassword(adminPassword!)
        await configSettingsPage.saveBtn.click()
        await configSettingsPage.goto('security')
        await expect(configSettingsPage.dashboardAuthSwitch).toHaveAttribute('data-state', 'checked', { timeout: 20000 })
        await expect(configSettingsPage.currentPasswordInput).not.toBeVisible()
        expect((await coreConfigApi.get(request)).auth_config?.is_enabled).toBe(true)
      })
    })

    test.describe('Virtual Key Rotation Cooldown', () => {
      // The cooldown is a text input, which the generic settings capture and
      // restore (number inputs and switches only) does not cover, so each test
      // restores the operator's original value itself.
      let originalCooldown: string

      test.beforeEach(async ({ configSettingsPage }) => {
        // The onboarding checklist covers the Save button in the bottom-right
        // corner on a fresh instance, so dismiss it before touching settings.
        await configSettingsPage.dismissOnboardingWidget()
        originalCooldown = await configSettingsPage.getVkRotationCooldown()
      })

      test.afterEach(async ({ configSettingsPage }) => {
        await configSettingsPage.goto('security')
        await configSettingsPage.dismissOnboardingWidget()
        const current = await configSettingsPage.getVkRotationCooldown()
        if (current !== originalCooldown) {
          await configSettingsPage.setVkRotationCooldown(originalCooldown)
          if (await configSettingsPage.hasPendingChanges()) {
            await configSettingsPage.saveSettings()
          }
        }
      })

      test('should display the rotation cooldown input', async ({ configSettingsPage }) => {
        await expect(configSettingsPage.vkRotationCooldownInput).toBeVisible()
        await expect(configSettingsPage.vkRotationCooldownInput).toHaveAttribute('placeholder', '5m')
      })

      test('should persist a duration across a reload', async ({ configSettingsPage }) => {
        await configSettingsPage.setVkRotationCooldown('5m')
        await configSettingsPage.saveSettings()

        await configSettingsPage.goto('security')
        // The API stores and returns the cooldown as nanoseconds, so this also
        // pins that the UI formats it back into the duration string that was
        // typed rather than showing 300000000000.
        await expect(configSettingsPage.vkRotationCooldownInput).toHaveValue('5m')
      })

      test('should reject an invalid duration without saving it', async ({ configSettingsPage }) => {
        await configSettingsPage.setVkRotationCooldown('not-a-duration')
        await configSettingsPage.saveBtn.click()
        await configSettingsPage.waitForErrorToast()

        await configSettingsPage.goto('security')
        await expect(configSettingsPage.vkRotationCooldownInput).not.toHaveValue('not-a-duration')
      })

      test('should disable the grace period when cleared', async ({ configSettingsPage }) => {
        await configSettingsPage.setVkRotationCooldown('30s')
        await configSettingsPage.saveSettings()
        await configSettingsPage.goto('security')
        await expect(configSettingsPage.vkRotationCooldownInput).toHaveValue('30s')

        // Clearing the field is how an operator turns the grace period off: the
        // previous key value must stop working the moment a key is rotated.
        await configSettingsPage.setVkRotationCooldown('')
        await configSettingsPage.saveSettings()
        await configSettingsPage.goto('security')
        await expect(configSettingsPage.vkRotationCooldownInput).toHaveValue('')
      })
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
