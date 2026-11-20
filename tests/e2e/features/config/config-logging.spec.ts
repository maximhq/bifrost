import { expect, test } from '../../core/fixtures/base.fixture'
import { ConfigSettingsState } from './pages/config-settings.page'

test.describe('Logging Settings', () => {
  // Run all config tests serially to avoid parallel writes to the same config/store
  test.describe.configure({ mode: 'serial' })

  let originalState: ConfigSettingsState

  test.beforeEach(async ({ configSettingsPage }) => {
    await configSettingsPage.goto('logging')
    // Capture original state for restoration
    originalState = await configSettingsPage.getCurrentSettings('logging')
  })

  test.afterEach(async ({ configSettingsPage }) => {
    // Restore original settings. Reload first, so the restore compares the
    // snapshot against what is saved, not against edits a failed test left on
    // the page, and puts back whatever the test did persist.
    if (originalState) {
      await configSettingsPage.goto('logging')
      await configSettingsPage.restoreSettings(originalState)
    }
  })

  test('should display logging settings controls', async ({ configSettingsPage }) => {
    // Check for main logging controls
    await expect(configSettingsPage.page.getByText(/Enable Logs/i)).toBeVisible()
    await expect(configSettingsPage.page.getByText('Log Retention Days', { exact: true })).toBeVisible()
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

  test('should save, persist and clear vector retention days', async ({ configSettingsPage }) => {
    const vectorInput = configSettingsPage.vectorRetentionDaysInput
    await expect(vectorInput).toBeVisible()

    const newValue = (await vectorInput.inputValue()) === '7' ? '14' : '7'
    await vectorInput.fill(newValue)
    expect(await configSettingsPage.hasPendingChanges()).toBe(true)
    await configSettingsPage.saveSettings()

    await configSettingsPage.goto('logging')
    await expect(vectorInput).toHaveValue(newValue)

    // A fraction is not a day count: it is not read as its leading integer.
    await vectorInput.fill('7.5')
    await expect(vectorInput).toHaveValue('')

    // Longer than the server can hold: an inline error, and no save.
    await vectorInput.fill('106752')
    await expect(configSettingsPage.page.getByTestId('workspace-vector-retention-days-error')).toBeVisible()
    await expect(configSettingsPage.page.getByRole('button', { name: 'Save Changes' })).toBeDisabled()
    await vectorInput.fill('106751')
    await expect(configSettingsPage.page.getByTestId('workspace-vector-retention-days-error')).toHaveCount(0)

    // A blank field means "same as logs" and is stored as 0, which renders blank again.
    await vectorInput.fill('')
    await configSettingsPage.saveSettings()

    await configSettingsPage.goto('logging')
    await expect(vectorInput).toHaveValue('')
  })

  test('should save, persist and clear the vector storage limit', async ({ configSettingsPage }) => {
    const capInput = configSettingsPage.vectorMaxSizeInput
    await expect(capInput).toBeVisible()
    await expect(capInput).toHaveAttribute('placeholder', 'No limit')

    const newValue = (await capInput.inputValue()) === '2048' ? '4096' : '2048'
    await capInput.fill(newValue)
    expect(await configSettingsPage.hasPendingChanges()).toBe(true)
    await configSettingsPage.saveSettings()

    await configSettingsPage.goto('logging')
    await expect(capInput).toHaveValue(newValue)

    // A blank field means no limit and is stored as 0, which renders blank again.
    await capInput.fill('')
    await configSettingsPage.saveSettings()

    await configSettingsPage.goto('logging')
    await expect(capInput).toHaveValue('')
  })
})
