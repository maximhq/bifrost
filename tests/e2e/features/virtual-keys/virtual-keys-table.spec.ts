import { expect, test } from '../../core/fixtures/base.fixture'
import type { VirtualKeysPage } from './pages/virtual-keys.page'
import { createVirtualKeyData } from './virtual-keys.data'

async function skipIfConfigStoreMissing(virtualKeysPage: VirtualKeysPage): Promise<void> {
  const missingConfigStore = await virtualKeysPage.page
    .getByText('Config store setup is missing.')
    .isVisible({ timeout: 1000 })
    .catch(() => false)

  test.skip(missingConfigStore, 'Config store setup is missing; virtual-key E2E tests require a configured config store.')
}

// Track VKs created in Virtual Keys Table tests for cleanup
const tableTestVKs: string[] = []

test.describe('Virtual Keys Table', () => {
  test.beforeEach(async ({ virtualKeysPage }) => {
    await virtualKeysPage.goto()
    await skipIfConfigStoreMissing(virtualKeysPage)
  })

  test.afterEach(async ({ virtualKeysPage }) => {
    await virtualKeysPage.closeSheet()
    if (tableTestVKs.length > 0) {
      await virtualKeysPage.cleanupVirtualKeys([...tableTestVKs])
      tableTestVKs.length = 0
    }
  })

  test('should display virtual keys table', async ({ virtualKeysPage }) => {
    await virtualKeysPage.page.getByRole('heading', { name: /Virtual Keys/i }).or(virtualKeysPage.emptyState).first().waitFor({ state: 'visible', timeout: 10000 })
    const hadTable = await virtualKeysPage.table.isVisible().catch(() => false)
    if (!hadTable) {
      await expect(virtualKeysPage.emptyState).toBeVisible({ timeout: 10000 })
    } else {
      await expect(virtualKeysPage.table).toBeVisible({ timeout: 10000 })
    }
    const vkData = createVirtualKeyData({ name: `Table test VK ${Date.now()}`, description: 'For table display test' })
    tableTestVKs.push(vkData.name)
    await virtualKeysPage.createVirtualKey(vkData)
    await expect(virtualKeysPage.table).toBeVisible({ timeout: 10000 })
    await expect(virtualKeysPage.table.locator('th', { hasText: 'Name' })).toBeVisible()
    await expect(virtualKeysPage.table.locator('th', { hasText: 'Key' })).toBeVisible()
  })

  test('should show empty state when no virtual keys', async ({ virtualKeysPage }) => {
    await virtualKeysPage.page.getByRole('heading', { name: /Virtual Keys/i }).or(virtualKeysPage.emptyState).first().waitFor({ state: 'visible', timeout: 10000 })
    const tableVisible = await virtualKeysPage.table.isVisible().catch(() => false)
    if (tableVisible) {
      test.skip(true, 'Pre-existing virtual keys found; empty-state assertion requires isolated data.')
      return
    }
    await expect(virtualKeysPage.emptyState).toBeVisible({ timeout: 10000 })
  })
})
