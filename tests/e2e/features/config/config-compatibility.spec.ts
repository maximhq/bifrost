import { expect, test } from '../../core/fixtures/base.fixture'
import { ConfigSettingsState } from './pages/config-settings.page'
import { coreConfigApi } from '../../core/actions/api'

test.describe('Compatibility Settings', () => {
  // Run all config tests serially to avoid parallel writes to the same config/store
  test.describe.configure({ mode: 'serial' })

  let originalState: ConfigSettingsState

  test.beforeEach(async ({ configSettingsPage }) => {
    await configSettingsPage.goto('compatibility')
    await configSettingsPage.dismissOnboardingWidget()
    originalState = await configSettingsPage.getCurrentSettings('compatibility')
  })

  test.afterEach(async ({ configSettingsPage }) => {
    if (originalState) {
      await configSettingsPage.restoreSettings(originalState)
    }
  })

  test('should display the decision emulation toggle', async ({ configSettingsPage }) => {
    await expect(configSettingsPage.compatConvertDecisionToResponsesSwitch).toBeVisible()
    await expect(configSettingsPage.page.getByText(/Emulate Decisions API on non-decision models/i)).toBeVisible()
  })

  test('should save and persist the decision emulation toggle', async ({ configSettingsPage, request }) => {
    const toggle = configSettingsPage.compatConvertDecisionToResponsesSwitch
    const state = (on: boolean) => (on ? 'checked' : 'unchecked')

    // Read the saved value from the server: the view copies the loaded config into
    // local state after render, so a one-shot UI read can still see the default.
    const { client_config } = await coreConfigApi.get(request)
    const original = (client_config.compat as { convert_decision_to_responses?: boolean } | undefined)?.convert_decision_to_responses ?? false
    await expect(toggle).toHaveAttribute('data-state', state(original))

    // Flip, save, reload: the new state must come back from the server
    await configSettingsPage.toggleSwitch(toggle)
    await expect(configSettingsPage.saveBtn).toBeEnabled()
    await configSettingsPage.saveSettings()

    await configSettingsPage.goto('compatibility')
    await expect(toggle).toHaveAttribute('data-state', state(!original))

    // Flip back, save, reload: the original state persists too
    await configSettingsPage.toggleSwitch(toggle)
    await expect(configSettingsPage.saveBtn).toBeEnabled()
    await configSettingsPage.saveSettings()

    await configSettingsPage.goto('compatibility')
    await expect(toggle).toHaveAttribute('data-state', state(original))
  })
})
