import { devices } from '@playwright/test'
import { expect, test } from '../../core/fixtures/base.fixture'

// defaultBrowserType is worker-scoped and cannot be set per file; the lane already runs Chromium.
const { defaultBrowserType: _, ...pixel7 } = devices['Pixel 7']
test.use(pixel7)

const mobileRoutes = [
  { name: 'dashboard', path: '/workspace/dashboard' },
  { name: 'logs', path: '/workspace/logs' },
  { name: 'mcp logs', path: '/workspace/mcp-logs' },
  { name: 'agent logs', path: '/workspace/agent-logs' },
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

  test('logs header buttons and date picker stay inside the viewport', async ({ page }) => {
    await page.goto('/workspace/logs')
    for (const id of ['logs-refresh-btn', 'filter-date-range']) {
      const el = page.getByTestId(id)
      await expect(el).toBeVisible()
      await expect
        .poll(() => el.evaluate((node) => node.getBoundingClientRect().right <= window.innerWidth + 1), { message: `${id} runs past the viewport` })
        .toBe(true)
    }
  })

  // Below the @4xl container breakpoint the header wraps: buttons, then search on its own row,
  // then the date picker.
  for (const scope of [
    { name: 'mcp logs', path: '/workspace/mcp-logs', prefix: 'mcp-logs-header' },
    { name: 'agent logs', path: '/workspace/agent-logs', prefix: 'agent-logs-header' },
  ]) {
    test(`${scope.name} header puts search on its own row`, async ({ page }) => {
      await page.goto(scope.path)
      const refresh = page.getByTestId(`${scope.prefix}-refresh-btn`)
      const live = page.getByTestId(`${scope.prefix}-live-btn`)
      const search = page.getByTestId(`${scope.prefix}-search-input`)
      const dateRange = page.getByTestId(`${scope.prefix}-date-range`)
      for (const el of [refresh, live, search, dateRange]) await expect(el).toBeVisible()

      const box = (l: typeof refresh) => l.evaluate((el) => el.getBoundingClientRect().toJSON())
      await expect
        .poll(
          async () => {
            const [r, lv, s, d] = await Promise.all([box(refresh), box(live), box(search), box(dateRange)])
            return s.top >= Math.max(r.bottom, lv.bottom) - 1 && d.top >= s.bottom - 1
          },
          { message: `${scope.name}: search does not sit on its own row between the buttons and the date picker` },
        )
        .toBe(true)
    })
  }
})

const PRESETS = ['1h', '6h', '24h', '7d', '30d', 'today', 'this_week', 'this_month']

test.describe('Date picker on a narrow phone with enlarged text', () => {
  // French carries the longest preset labels ("Dernières 24 heures"). A 125% root font is what a
  // browser text-size setting does to the rem-based button sizes.
  test.use({ viewport: { width: 320, height: 640 } })

  test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => localStorage.setItem('bifrost.locale', 'fr'))
  })

  test('preset labels fit their buttons and the popover fits the viewport', async ({ page }) => {
    await page.goto('/workspace/logs')
    await page.addStyleTag({ content: 'html { font-size: 125% !important; }' })
    await page.getByTestId('filter-date-range').click()
    const popover = page.getByTestId('datepicker-popover-content')
    await expect(popover).toBeVisible()
    await expect
      .poll(
        () =>
          popover.evaluate((el) => {
            const r = el.getBoundingClientRect()
            return r.left >= -1 && r.right <= window.innerWidth + 1
          }),
        { message: 'date picker popover runs past a viewport edge' },
      )
      .toBe(true)

    for (const value of PRESETS) {
      const preset = page.getByTestId(`datepicker-preset-btn-${value}`)
      await expect(preset).toBeVisible()
      await expect
        .poll(() => preset.evaluate((el) => el.scrollWidth <= el.clientWidth && el.scrollHeight <= el.clientHeight), {
          message: `preset ${value} label overflows its button`,
        })
        .toBe(true)
    }
  })
})

test.describe('Date picker on desktop', () => {
  test.use({ viewport: { width: 1280, height: 800 }, isMobile: false, hasTouch: false })

  test('presets sit in a single column beside the calendar', async ({ page }) => {
    await page.goto('/workspace/logs')
    await page.getByTestId('filter-date-range').click()
    const popover = page.getByTestId('datepicker-popover-content')
    await expect(popover).toBeVisible()
    const presets = await Promise.all(
      PRESETS.map((value) => page.getByTestId(`datepicker-preset-btn-${value}`).evaluate((el) => el.getBoundingClientRect().toJSON())),
    )
    const lefts = presets.map((r) => Math.round(r.left))
    expect(new Set(lefts).size, `presets are not in one column: ${lefts.join(', ')}`).toBe(1)

    // One column also passes when the presets fall below the calendar, so pin the side-by-side layout.
    const calendar = await popover.locator('[data-slot="calendar"]').first().evaluate((el) => el.getBoundingClientRect().toJSON())
    const top = Math.min(...presets.map((r) => r.top))
    expect(lefts[0], `presets start at ${lefts[0]}, left of the calendar's right edge ${calendar.right}`).toBeGreaterThanOrEqual(Math.floor(calendar.right))
    expect(top, `presets start at y=${top}, below the calendar (bottom ${calendar.bottom})`).toBeLessThan(calendar.bottom)
  })
})
