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

async function mockWarpConfig(page: Page, additionalModels: { provider: string; model: string }[] = []) {
  await page.route('**/api/warp/config', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ ...configuredWarp, additional_models: additionalModels }),
    }),
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

// Warp ran on exactly one model, and the composer's model chip was a link to
// the settings page for whoever was looking at it. An administrator can now
// expose several models: the chip switches between them, the request names the
// one picked, and the way into settings is a separate control.
test.describe('Warp model switcher', () => {
  test.beforeEach(async ({ dashboardPage }) => {
    await enableWarpFlag(dashboardPage.page)
    await dashboardPage.goto()
    await expect(dashboardPage.pageTitle).toBeVisible()
    // The first-run checklist is a fixed card in the bottom-right corner, over
    // the dock's composer, so a click on the model chip would time out against
    // it. Snoozed with its own cookie, the same way the config suite does; each
    // test reloads after setting up its routes, which is when it takes effect.
    await dashboardPage.page.context().addCookies([
      {
        name: 'bifrost_onboarding_remind_at',
        value: new Date(Date.now() + 24 * 60 * 60 * 1000).toISOString(),
        url: new URL(dashboardPage.page.url()).origin,
      },
    ])
  })

  test('asks on the model picked from the exposed list, and remembers the pick', async ({ dashboardPage }) => {
    const page = dashboardPage.page
    await mockWarpConfig(page, [{ provider: 'anthropic', model: 'claude-sonnet-5' }])
    const sent: { provider?: string; model?: string }[] = []
    await page.route('**/api/warp/chat', async (route) => {
      sent.push(route.request().postDataJSON() as { provider?: string; model?: string })
      await route.fulfill({ status: 200, contentType: 'text/event-stream', body: sse('Answered.') })
    })
    await page.reload()

    await page.getByTestId('topbar-warp-btn').click()
    const chip = page.getByTestId('warp-composer-model')
    await expect(chip).toContainText('gpt-5.6-luna')

    // With nothing picked the request names no model, so the server's default answers.
    const composer = page.getByTestId('warp-composer-input')
    await composer.fill('first question')
    await composer.press('Enter')
    await expect(page.getByTestId('warp-message-assistant')).toHaveCount(1)
    expect(sent).toHaveLength(1)
    expect(sent[0].provider).toBeUndefined()
    expect(sent[0].model).toBeUndefined()

    await chip.click()
    await expect(page.getByTestId('warp-model-option-openai-gpt-5.6-luna')).toContainText('Default')
    await page.getByTestId('warp-model-option-anthropic-claude-sonnet-5').click()
    await expect(chip).toContainText('claude-sonnet-5')

    await composer.fill('second question')
    await composer.press('Enter')
    await expect(page.getByTestId('warp-message-assistant')).toHaveCount(2)
    expect(sent).toHaveLength(2)
    expect(sent[1].provider).toBe('anthropic')
    expect(sent[1].model).toBe('claude-sonnet-5')

    // The pick is this browser's preference, so it outlives a reload.
    await page.reload()
    await page.getByTestId('topbar-warp-btn').click()
    await expect(page.getByTestId('warp-composer-model')).toContainText('claude-sonnet-5')
  })

  test('a single configured model is a label with settings beside it, not a link', async ({ dashboardPage }) => {
    const page = dashboardPage.page
    await mockWarpConfig(page)
    await page.reload()

    await page.getByTestId('topbar-warp-btn').click()
    const chip = page.getByTestId('warp-composer-model')
    await expect(chip).toContainText('gpt-5.6-luna')
    // Nothing to switch to, so the chip opens no menu and goes nowhere.
    await chip.click()
    await expect(page.getByTestId('warp-model-menu')).toHaveCount(0)
    await expect(page).not.toHaveURL(/\/workspace\/config\/warp/)

    // Settings is its own control. This suite runs as the local administrator;
    // the control is withheld from callers without Warp Update.
    await page.getByTestId('warp-composer-settings').click()
    await expect(page).toHaveURL(/\/workspace\/config\/warp/)
  })

  test('falls back to the default when the remembered model is no longer exposed', async ({ dashboardPage }) => {
    const page = dashboardPage.page
    await mockWarpConfig(page, [{ provider: 'anthropic', model: 'claude-sonnet-5' }])
    await page.reload()
    await page.getByTestId('topbar-warp-btn').click()
    await page.getByTestId('warp-composer-model').click()
    await page.getByTestId('warp-model-option-anthropic-claude-sonnet-5').click()
    await expect(page.getByTestId('warp-composer-model')).toContainText('claude-sonnet-5')

    // The administrator removes that model. A request still naming it would be
    // refused, so the panel must stop offering it and stop sending it.
    await page.unroute('**/api/warp/config')
    await mockWarpConfig(page)
    const sent: { provider?: string; model?: string }[] = []
    await page.route('**/api/warp/chat', async (route) => {
      sent.push(route.request().postDataJSON() as { provider?: string; model?: string })
      await route.fulfill({ status: 200, contentType: 'text/event-stream', body: sse('Answered.') })
    })
    await page.reload()

    await page.getByTestId('topbar-warp-btn').click()
    await expect(page.getByTestId('warp-composer-model')).toContainText('gpt-5.6-luna')
    const composer = page.getByTestId('warp-composer-input')
    await composer.fill('still works?')
    await composer.press('Enter')
    await expect(page.getByTestId('warp-message-assistant')).toHaveCount(1)
    expect(sent[0].provider).toBeUndefined()
    expect(sent[0].model).toBeUndefined()
  })
})
