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

// A shared or hand-edited link can carry a metadata filter key the server refuses. The page must not
// send it (every list and export request would fail with 400) and must offer to clear it. The API is
// mocked so the shared test gateway is untouched.
test.describe('Virtual key metadata filter from the URL', () => {
  test.use({ skipAutoLogin: true })

  test('an invalid metadata_key in the link is not sent and can be cleared', async ({ page }) => {
    const listQueries: string[] = []
    await page.route('**/api/**', async route => {
      const url = new URL(route.request().url())
      if (url.pathname === '/api/governance/virtual-keys') {
        listQueries.push(url.search)
        await route.fulfill({ json: { virtual_keys: [], count: 0, total_count: 0, limit: 25, offset: 0 } })
      } else if (url.pathname === '/api/session/is-auth-enabled') {
        await route.fulfill({ json: { is_auth_enabled: false, has_valid_token: false, auth_type: 'none', inference_auth_enforced: false } })
      } else if (url.pathname === '/api/version') {
        await route.fulfill({ json: '1.0.0' })
      } else if (url.pathname === '/api/config') {
        await route.fulfill({ json: { client_config: {}, auth_config: null, framework_config: {}, is_db_connected: true, metadata: { onboarding_dismissed: true } } })
      } else {
        await route.fulfill({ json: {} })
      }
    })

    await page.goto('/workspace/governance/virtual-keys?metadata_key=cost%20center&metadata_value=cc-42')
    await expect(page.getByTestId('vk-metadata-filter-invalid-alert')).toBeVisible()
    await expect.poll(() => listQueries.length).toBeGreaterThan(0)
    expect(listQueries.every(q => !q.includes('metadata_'))).toBe(true)

    await page.getByTestId('vk-metadata-filter-invalid-clear-btn').click()
    await expect(page.getByTestId('vk-metadata-filter-invalid-alert')).toHaveCount(0)
    await expect(page).not.toHaveURL(/metadata_key=/)
  })
})
