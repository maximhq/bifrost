import type { Page } from '@playwright/test'
import { expect, test } from '../../core/fixtures/base.fixture'

// The dock keeps the thread, the conversation id and any pending question
// across a close, but the in-flight answer used to live in the panel and die
// with it: closing the dock mid-answer aborted the request, and reopening
// showed the question with nothing after it. The answer now belongs to the
// provider and finishes whether or not the panel is on screen.

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
  semantic_search_threshold: 0.7,
  semantic_search_limit: 10,
  vector_store_connected: true,
}

function sse(answer: string): string {
  const frames = [
    { type: 'start' },
    { type: 'delta', delta: answer },
    { type: 'done', finish_reason: 'stop', conversation_id: '00000000-0000-4000-8000-000000000002' },
  ]
  return frames.map((frame) => `event: ${frame.type}\ndata: ${JSON.stringify(frame)}\n\n`).join('')
}

// The warp flag is shared server state that other suites toggle in parallel,
// and it only gates the UI. Turning it on in this page's view of the flags
// leaves the server's copy alone, so no suite races another over it.
async function enableWarpFlag(page: Page) {
  await page.route('**/api/feature-flags', async (route) => {
    if (route.request().method() !== 'GET') return route.continue()
    const response = await route.fetch()
    const body = (await response.json()) as { flags: { id: string; enabled: boolean }[] }
    body.flags = body.flags.map((flag) => (flag.id === 'warp' ? { ...flag, enabled: true } : flag))
    await route.fulfill({ response, json: body })
  })
}

async function mockWarpConfig(page: Page) {
  await page.route('**/api/warp/config', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(configuredWarp) }),
  )
  await page.route('**/api/warp/log-index/status', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ state: 'ready', vector_store_connected: true, embedding_configured: true }),
    }),
  )
}

test.describe('Warp dock and an in-flight answer', () => {
  test.beforeEach(async ({ dashboardPage }) => {
    await enableWarpFlag(dashboardPage.page)
    await dashboardPage.goto()
    await expect(dashboardPage.pageTitle).toBeVisible()
  })

  test('closing the dock mid-answer lets the answer finish and land in the thread', async ({ dashboardPage }) => {
    const page = dashboardPage.page
    await mockWarpConfig(page)
    // The chat request is held open until the test has closed the dock, so the
    // answer can only arrive while the panel is unmounted.
    let release!: () => void
    const dockClosed = new Promise<void>((resolve) => {
      release = resolve
    })
    await page.route('**/api/warp/chat', async (route) => {
      await dockClosed
      // Before the fix the close aborted this request; fulfilling an aborted
      // route throws, and the assertion below is what reports it.
      await route.fulfill({ status: 200, contentType: 'text/event-stream', body: sse('Two requests hit a 429.') }).catch(() => {})
    })
    await page.reload()

    await page.getByTestId('topbar-warp-btn').click()
    const composer = page.getByTestId('warp-composer-input')
    await expect(composer).toBeVisible()
    await composer.fill('why did the requests fail?')
    await composer.press('Enter')
    await expect(page.getByTestId('warp-stop-btn')).toBeVisible()

    await page.getByTestId('warp-close-btn').click()
    await expect(page.getByTestId('warp-panel')).toHaveCount(0)
    release()

    await page.getByTestId('topbar-warp-btn').click()
    await expect(page.getByTestId('warp-message-user')).toHaveText('why did the requests fail?')
    await expect(page.getByTestId('warp-message-assistant')).toContainText('Two requests hit a 429.')
    // The answer is filed, not still streaming, so the composer is ready again.
    await expect(page.getByTestId('warp-stop-btn')).toHaveCount(0)
  })
})
