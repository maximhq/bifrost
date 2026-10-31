import { expect, test } from '../../core/fixtures/base.fixture'
import type { VirtualKeysPage } from './pages/virtual-keys.page'

async function skipIfConfigStoreMissing(virtualKeysPage: VirtualKeysPage): Promise<void> {
  const missingConfigStore = await virtualKeysPage.page
    .getByText('Config store setup is missing.')
    .isVisible({ timeout: 1000 })
    .catch(() => false)

  test.skip(missingConfigStore, 'Config store setup is missing; virtual-key E2E tests require a configured config store.')
}

test.describe('Form Validation', () => {
  test.beforeEach(async ({ virtualKeysPage }) => {
    await virtualKeysPage.goto()
    await skipIfConfigStoreMissing(virtualKeysPage)
  })

  test.afterEach(async ({ virtualKeysPage }) => {
    // Close any open sheets
    await virtualKeysPage.closeSheet()
  })

  test('should require name for virtual key', async ({ virtualKeysPage }) => {
    await virtualKeysPage.dismissToasts()
    await virtualKeysPage.createBtn.click()
    await expect(virtualKeysPage.sheet).toBeVisible()
    // Wait for sheet animation to complete
    await virtualKeysPage.waitForSheetAnimation()

    // Save button should be disabled when name is empty
    await expect(virtualKeysPage.saveBtn).toBeDisabled()
  })

  test('should accept valid budget values', async ({ virtualKeysPage }) => {
    await virtualKeysPage.dismissToasts()
    await virtualKeysPage.createBtn.click()
    await expect(virtualKeysPage.sheet).toBeVisible()
    // Wait for sheet animation to complete
    await virtualKeysPage.waitForSheetAnimation()

    // Fill name (required field)
    await virtualKeysPage.nameInput.fill(`Valid Budget Test ${Date.now()}`)

    // Add a budget line and fill amount
    await virtualKeysPage.page.getByTestId('vk-budget-lines-add-btn').click()
    const budgetInput = virtualKeysPage.page.getByTestId('vk-budget-lines-amount-0')
    await expect(budgetInput).toBeVisible({ timeout: 5000 })
    await budgetInput.fill('100')

    // Save button should be enabled if form is valid
    await expect(virtualKeysPage.saveBtn).toBeEnabled()
  })
})
