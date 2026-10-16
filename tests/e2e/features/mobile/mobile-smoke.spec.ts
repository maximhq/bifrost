import { devices } from '@playwright/test'
import { expect, test } from '../../core/fixtures/base.fixture'

// defaultBrowserType is worker-scoped and cannot be set per file; the lane already runs Chromium.
const { defaultBrowserType: _, ...pixel7 } = devices['Pixel 7']
test.use(pixel7)

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

  test('provider keys table headers keep their own space', async ({ page }) => {
    await page.goto('/workspace/providers?provider=openai')
    const headers = page.getByTestId('keys-table').getByRole('columnheader')
    await expect(headers.first()).toBeVisible()
    for (const name of ['Weight', 'Enabled']) {
      const header = page.getByTestId('keys-table').getByRole('columnheader', { name, exact: true })
      await expect
        .poll(() => header.evaluate((el) => el.scrollWidth <= el.clientWidth), { message: `${name} header overflows its column` })
        .toBe(true)
    }
  })

  test('model catalog traffic and cost headers keep their own space', async ({ page }) => {
    await page.goto('/workspace/model-catalog')
    for (const name of ['Total Traffic (24h)', 'Total Cost (24h)']) {
      const header = page.getByRole('columnheader', { name, exact: true })
      await expect(header).toBeVisible()
      await expect
        .poll(() => header.evaluate((el) => el.scrollWidth <= el.clientWidth), { message: `${name} header overflows its column` })
        .toBe(true)
    }
  })

  test('async job result TTL input is not squeezed', async ({ page }) => {
    await page.goto('/workspace/config/client-settings')
    const input = page.getByTestId('client-settings-async-job-result-ttl-input')
    await expect(input).toBeVisible()
    // w-32 is 128px; a flex row that shrinks it clips the value.
    await expect.poll(() => input.evaluate((el) => el.getBoundingClientRect().width)).toBeGreaterThanOrEqual(127)
  })

  test('observability connector names stay on one line', async ({ page }) => {
    await page.goto('/workspace/observability')
    const newRelic = page.getByTestId('observability-provider-btn-newrelic')
    await expect(newRelic).toBeVisible()
    await expect.poll(() => newRelic.evaluate((el) => el.scrollHeight <= el.clientHeight)).toBe(true)
  })

  test('routing tree shows the mobile fallback', async ({ page }) => {
    await page.goto('/workspace/routing-rules/tree')
    await expect(page.getByTestId('routing-tree-mobile-list-btn')).toBeVisible()
  })
})
