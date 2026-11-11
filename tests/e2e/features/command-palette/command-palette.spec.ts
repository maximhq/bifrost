import type { Page } from '@playwright/test'
import { expect, test } from '../../core/fixtures/base.fixture'

// The ⌘K palette: opening it, jumping to a page, keeping the selected time
// range on the move, copying the link on a host without the Clipboard API, and
// handing a question to the Warp dock.

const configuredWarp = {
  configured: true,
  enabled: true,
  provider: 'openai',
  model: 'gpt-5.6-luna',
  max_iterations: 8,
  request_timeout_seconds: 120,
  history_retention_days: 30,
  embedding_provider: 'openai',
  embedding_model: 'text-embedding-3-small',
  embedding_dimension: 1536,
  log_vector_store_namespace: 'BifrostWarpLogs',
  semantic_search_threshold: 0.5,
  semantic_search_limit: 10,
  vector_store_connected: true,
  additional_models: [],
}

async function openPalette(page: Page) {
  await page.keyboard.press('ControlOrMeta+k')
  await expect(page.getByTestId('command-palette')).toBeVisible()
  return page.getByTestId('command-palette-input')
}

test.describe('Command palette', () => {
  test.beforeEach(async ({ dashboardPage }) => {
    await dashboardPage.goto()
    await expect(dashboardPage.pageTitle).toBeVisible()
  })

  test('opens on the shortcut and closes on Escape', async ({ dashboardPage }) => {
    const page = dashboardPage.page
    await openPalette(page)
    await page.keyboard.press('Escape')
    await expect(page.getByTestId('command-palette')).toHaveCount(0)
  })

  test('jumps to a page picked from the results', async ({ dashboardPage }) => {
    const page = dashboardPage.page
    const input = await openPalette(page)
    await input.fill('logs')
    await page.getByTestId('command-palette-item-/workspace/logs').click()
    await expect(page).toHaveURL(/\/workspace\/logs/)
    await expect(page.getByTestId('command-palette')).toHaveCount(0)
  })

  test('keeps the selected time range when moving between time-filtered pages', async ({ dashboardPage }) => {
    const page = dashboardPage.page
    await page.goto('/workspace/dashboard?period=24h')
    const input = await openPalette(page)
    await input.fill('logs')
    await page.getByTestId('command-palette-item-/workspace/logs').click()
    await expect(page).toHaveURL(/\/workspace\/logs\?.*period=24h/)
  })

  test('copy link toasts the failure when the Clipboard API is missing', async ({ dashboardPage }) => {
    const page = dashboardPage.page
    // Plain-HTTP hosts are not secure contexts, so the browser leaves navigator.clipboard undefined.
    await page.addInitScript(() => Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true }))
    await page.reload()
    const input = await openPalette(page)
    await input.fill('copy link')
    await page.getByTestId('command-palette-item-page-link:copy').click()
    await expect(page.getByText('Could not copy the link')).toBeVisible()
  })

  test('hands a typed question to the Warp dock', async ({ dashboardPage }) => {
    const page = dashboardPage.page
    await page.route('**/api/feature-flags', async (route) => {
      if (route.request().method() !== 'GET') return route.continue()
      const response = await route.fetch()
      const body = (await response.json()) as { flags: { id: string; enabled: boolean }[] }
      body.flags = body.flags.map((flag) => (flag.id === 'warp' ? { ...flag, enabled: true } : flag))
      await route.fulfill({ response, json: body })
    })
    await page.route('**/api/warp/config', (route) => route.fulfill({ json: configuredWarp }))
    await page.route('**/api/warp/log-index/status', (route) =>
      route.fulfill({ json: { state: 'ready', vector_store_connected: true, embedding_configured: true } }),
    )
    const asked: string[] = []
    await page.route('**/api/warp/chat', async (route) => {
      asked.push(JSON.stringify(route.request().postDataJSON()))
      const done = { type: 'done', finish_reason: 'stop', conversation_id: '00000000-0000-4000-8000-000000000003' }
      await route.fulfill({
        status: 200,
        contentType: 'text/event-stream',
        body: `event: delta\ndata: ${JSON.stringify({ type: 'delta', delta: 'Answered.' })}\n\nevent: done\ndata: ${JSON.stringify(done)}\n\n`,
      })
    })
    await page.reload()

    const input = await openPalette(page)
    await input.fill('why did requests fail?')
    await page.getByTestId('command-palette-item-warp:ask').click()
    await expect(page.getByTestId('warp-panel')).toBeVisible()
    await expect(page.getByTestId('warp-message-user')).toHaveText('why did requests fail?')
    await expect(page.getByTestId('warp-message-assistant')).toContainText('Answered.')
    expect(asked).toHaveLength(1)
  })
})
