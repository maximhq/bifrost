import { expect, test } from '../../core/fixtures/base.fixture'
import { ConfigSettingsState } from './pages/config-settings.page'

test.describe('Performance Tuning Settings', () => {
  // Run all config tests serially to avoid parallel writes to the same config/store
  test.describe.configure({ mode: 'serial' })

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
