import { expect, test } from '../../core/fixtures/base.fixture'
import type { Page } from '@playwright/test'
import { ConfigSettingsState } from './pages/config-settings.page'
import { DefaultCoreConfig } from '../../../../ui/lib/types/config'

test.describe('Skills marketplace URL settings', () => {
  test.use({ skipAutoLogin: true })

  for (const state of [
    { name: 'missing URL', git: true, baseUrl: '' },
    { name: 'missing Git', git: false, baseUrl: 'https://skills.example/prefix' },
    { name: 'configured URL', git: true, baseUrl: 'https://skills.example/prefix' },
  ]) {
    test(`marketplace registration follows ${state.name}`, async ({ page }) => {
      let savedURL = state.baseUrl
      await page.route('https://getbifrost.ai/latest-release', route => route.fulfill({ json: { name: '1.0.0' } }))
      await page.route('**/api/**', async route => {
        const path = new URL(route.request().url()).pathname
        if (path === '/api/config') {
          if (route.request().method() === 'PUT') {
            const payload = route.request().postDataJSON() as { client_config: { skills_external_client_url: { value: string } } }
            savedURL = payload.client_config.skills_external_client_url.value
            await route.fulfill({ json: { message: 'Configuration updated successfully' } })
            return
          }
          await route.fulfill({ json: {
            client_config: { ...DefaultCoreConfig, enforce_auth_on_inference: false, skills_external_client_url: { value: savedURL } },
            auth_config: null, framework_config: {}, is_db_connected: true,
            is_git_available: state.git, skills_marketplace_base_url: savedURL, metadata: {},
          } })
        } else if (path === '/api/version') {
          await route.fulfill({ json: '1.0.0' })
        } else if (path === '/api/session/is-auth-enabled') {
          await route.fulfill({ json: { is_auth_enabled: false, has_valid_token: false, auth_type: 'none', inference_auth_enforced: false } })
        } else if (path === '/api/skills') {
          await route.fulfill({ json: { skills: [{
            id: 'url-fixture', name: 'url-fixture', description: 'Marketplace URL fixture',
            latest_version: '1.0.0', highest_version: '1.0.0', file_count: 0,
            created_at: '2026-10-14T00:00:00Z', updated_at: '2026-10-14T00:00:00Z',
          }], total: 1, limit: 20, offset: 0 } })
        } else if (path === '/api/skills/all/version') {
          await route.fulfill({ json: { version: '0.0.0' } })
        } else {
          await route.fulfill({ json: {} })
        }
      })
      await page.goto('/workspace/skills-repo')
      const register = page.getByTestId('skill-marketplace-register-btn')
      await expect(page.getByTestId('skill-download-all-btn')).toBeEnabled()
      if (state.git && state.baseUrl) {
        await expect(register).toBeEnabled()
        await register.click()
        await expect(page.getByTestId('skill-copy-marketplace-claude-desktop')).toContainText(`${state.baseUrl}/api/skills/serve/claude-code.git`)
        await expect(page.getByTestId('skill-copy-marketplace-codex')).toContainText(`${state.baseUrl}/api/skills/serve/codex`)
      } else {
        await expect(register).toBeDisabled()
        if (state.git) {
          await page.getByTestId('skill-marketplace-disabled-trigger').hover()
          await page.getByTestId('skill-marketplace-settings-link').filter({ visible: true }).click()
          await expect(page).toHaveURL(/\/workspace\/config\/skills$/)
          await expect(page.getByTestId('skills-external-client-url-input')).toBeVisible()
          await page.goto('/workspace/skills-repo')
        }
      }
      const settingsLink = page.getByTestId('sidebar-subitem-link-skills-settings')
      await expect(settingsLink).toBeVisible()
      await page.getByTestId('sidebar-item-btn-skills').click()
      await expect(settingsLink).not.toBeVisible()
      await page.getByTestId('sidebar-item-btn-skills').click()
      await page.getByTestId('sidebar-subitem-link-skills-settings').click()
      await expect(page).toHaveURL(/\/workspace\/config\/skills$/)
      await expect(page.getByTestId('sidebar-item-btn-skills')).toHaveClass(/text-primary/)
      await expect(page.getByTestId('sidebar-item-btn-settings')).not.toHaveClass(/text-primary/)
      await expect(page.getByTestId('skills-external-client-url-input')).toHaveValue(state.baseUrl)
      await expect(page.getByTestId('skills-settings-save-btn')).toBeDisabled()
      await page.getByTestId('skills-external-client-url-input').fill('https://updated-skills.example')
      await page.getByTestId('skills-settings-save-btn').click()
      await expect.poll(() => savedURL).toBe('https://updated-skills.example')
      await page.reload()
      await expect(page.getByTestId('skills-external-client-url-input')).toHaveValue('https://updated-skills.example')
    })
  }
})

// Mock an unconfigured instance so the checklist is incomplete and would be visible.
// disable_onboarding is read per request, so a test can flip it between loads.
async function mockUnconfiguredInstance(page: Page, state = { disableOnboarding: false }) {
  await page.route('**/api/**', async route => {
    const path = new URL(route.request().url()).pathname
    if (path === '/api/config') {
      await route.fulfill({ json: {
        client_config: { ...DefaultCoreConfig, enforce_auth_on_inference: false, allowed_origins: [] },
        auth_config: null,
        framework_config: {}, is_db_connected: true, metadata: {},
        disable_onboarding: state.disableOnboarding,
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
}

test.describe('Onboarding widget snooze', () => {
  test.use({ skipAutoLogin: true })

  test('remind me later opens a picker above the widget and snoozes it', async ({ page }) => {
    await mockUnconfiguredInstance(page)
    await page.goto('/workspace/config/client-settings')

    await page.getByTestId('onboarding-later').click()
    // The popover is portaled to body, so it must stack above the widget card.
    // A non-forced click fails if the card covers the option.
    await page.getByTestId('onboarding-remind-tomorrow').click({ timeout: 5000 })
    await expect(page.getByTestId('onboarding-later')).not.toBeVisible()
    const cookies = await page.context().cookies()
    expect(cookies.some(c => c.name === 'bifrost_onboarding_remind_at')).toBe(true)
  })

  test('disable_onboarding from config.json hides the checklist on an incomplete instance', async ({ page }) => {
    const state = { disableOnboarding: false }
    await mockUnconfiguredInstance(page, state)
    // Positive control on the same page and mock: the widget renders while the flag is off.
    await page.goto('/workspace/config/client-settings')
    await expect(page.getByTestId('onboarding-later')).toBeVisible()

    state.disableOnboarding = true
    const configLoaded = page.waitForResponse(res => new URL(res.url()).pathname === '/api/config')
    await page.reload()
    await configLoaded
    await expect(page.getByTestId('onboarding-later')).toHaveCount(0)
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

  test('should save and persist extra fields placement', async ({ configSettingsPage, page }) => {
    await expect(configSettingsPage.extraFieldsPlacementSelect).toBeVisible()

    // Start from whatever the gateway stores, switch to the other value, and put the original back.
    const stored = await page.request.get('/api/config?from_db=true')
    expect(stored.status(), await stored.text()).toBe(200)
    const original: 'body' | 'headers' = (await stored.json()).client_config.extra_fields_placement === 'headers' ? 'headers' : 'body'
    const target: 'body' | 'headers' = original === 'body' ? 'headers' : 'body'
    const label = (value: 'body' | 'headers') => (value === 'body' ? /Body/ : /Headers/)

    await expect(configSettingsPage.extraFieldsPlacementSelect).toHaveText(label(original))
    try {
      await configSettingsPage.selectExtraFieldsPlacement(target)
      // Headers mode spells out which fields are dropped; Body mode hides the note.
      if (target === 'headers') {
        await expect(configSettingsPage.extraFieldsPlacementDroppedNote).toBeVisible()
      } else {
        await expect(configSettingsPage.extraFieldsPlacementDroppedNote).not.toBeVisible()
      }
      expect(await configSettingsPage.hasPendingChanges()).toBe(true)
      await configSettingsPage.saveSettings()

      const saved = await page.request.get('/api/config?from_db=true')
      expect(saved.status(), await saved.text()).toBe(200)
      expect((await saved.json()).client_config.extra_fields_placement).toBe(target)

      await configSettingsPage.goto('client-settings')
      await expect(configSettingsPage.extraFieldsPlacementSelect).toHaveText(label(target))
    } finally {
      // restoreSettings only covers switches and number inputs, so put the original select value back here.
      await configSettingsPage.goto('client-settings')
      if (!label(original).test((await configSettingsPage.extraFieldsPlacementSelect.textContent()) ?? '')) {
        await configSettingsPage.selectExtraFieldsPlacement(original)
        await configSettingsPage.saveSettings()
      }
    }
  })
})
