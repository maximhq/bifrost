import { expect, test } from '../../core/fixtures/base.fixture'

const mobileRoutes = [
  { name: 'dashboard', path: '/workspace/dashboard' },
  { name: 'logs', path: '/workspace/logs' },
  { name: 'virtual keys', path: '/workspace/virtual-keys' },
  { name: 'providers', path: '/workspace/providers' },
  { name: 'client settings', path: '/workspace/config/client-settings' },
]

test.describe('Mobile reachability', () => {
  for (const route of mobileRoutes) {
    test(`${route.name} fits the viewport and exposes navigation`, async ({ page }) => {
      await page.goto(route.path)
      await page.waitForLoadState('domcontentloaded')

      await expect(page.locator('[data-sidebar="trigger"]')).toBeVisible()
      await expect
        .poll(() => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1))
        .toBe(true)
    })
  }

  test('mobile navigation opens and exposes workspace links', async ({ page }) => {
    await page.goto('/workspace/dashboard')
    await page.locator('[data-sidebar="trigger"]').click()
    const sidebar = page.getByRole('dialog')
    await expect(sidebar).toBeVisible()

    await page.getByTestId('sidebar-item-btn-models').click()

    await expect(sidebar).toBeVisible()
    await expect(page.getByTestId('sidebar-subitem-link-model-catalog')).toBeVisible()
  })

  test('routing tree shows the mobile fallback', async ({ page }) => {
    await page.goto('/workspace/routing-rules/tree')
    await expect(page.getByTestId('routing-tree-mobile-list-btn')).toBeVisible()
  })
})
