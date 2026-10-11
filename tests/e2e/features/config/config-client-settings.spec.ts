import { expect, test } from '../../core/fixtures/base.fixture'
import { ConfigSettingsState } from './pages/config-settings.page'
import { DefaultCoreConfig } from '../../../../ui/lib/types/config'

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

test.describe('Client Settings', () => {
  // Run all config tests serially to avoid parallel writes to the same config/store
  test.describe.configure({ mode: 'serial' })

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
