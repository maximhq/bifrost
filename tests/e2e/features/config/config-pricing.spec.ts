import { expect, test } from '../../core/fixtures/base.fixture'
import { ConfigSettingsState } from './pages/config-settings.page'

test.describe('Pricing Config', () => {
  // Run all config tests serially to avoid parallel writes to the same config/store
  test.describe.configure({ mode: 'serial' })

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
    // Saving checks the URL is reachable, so it has to be a live datasheet.
    const testUrl = 'https://getbifrost.ai/datasheet?source=e2e'
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

test.describe('Pricing Config Settings', () => {
  test.describe.configure({ mode: 'serial' })

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
    await expect(configSettingsPage.page.getByRole('heading', { name: /Model Settings/i })).toBeVisible()
  })
})
