import type { Page } from '@playwright/test'
import { expect, test } from '../../core/fixtures/base.fixture'

// Warp draws charts with its render_chart tool: the server swaps the id the
// model pasted for a spec of data a tool read, and the panel renders that spec
// with recharts instead of showing the JSON. The model is mocked here - the
// chat stream is a fixed SSE body - so this pins the rendering, not the model.

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

const lineChart = {
  id: 'chart-1',
  kind: 'line',
  title: 'Errors per day, last 7 days',
  metric: 'errors',
  unit: 'count',
  interval: 'day',
  points: [
    { x: '2026-09-21T00:00:00Z', y: 3 },
    { x: '2026-09-22T00:00:00Z', y: 5 },
    { x: '2026-09-23T00:00:00Z', y: 30 },
  ],
  window: { start: '2026-09-17T00:00:00Z', end: '2026-09-24T00:00:00Z' },
  link: '/workspace/logs?status=error',
}

function sse(answer: string): string {
  const frames = [
    { type: 'start' },
    { type: 'tool_call_start', tool_id: 'c-1', tool_name: 'render_chart', arguments: '{}' },
    { type: 'tool_call_end', tool_id: 'c-1', tool_name: 'render_chart', duration_ms: 12 },
    { type: 'delta', delta: answer },
    { type: 'done', finish_reason: 'stop', conversation_id: '00000000-0000-4000-8000-000000000001' },
  ]
  return frames.map((frame) => `event: ${frame.type}\ndata: ${JSON.stringify(frame)}\n\n`).join('')
}

async function mockWarp(page: Page, answer: string) {
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
  await page.route('**/api/warp/chat', (route) =>
    route.fulfill({ status: 200, contentType: 'text/event-stream', body: sse(answer) }),
  )
}

async function ask(page: Page, question: string) {
  await page.getByTestId('topbar-warp-btn').click()
  const composer = page.getByTestId('warp-composer-input')
  await expect(composer).toBeVisible()
  await composer.fill(question)
  await composer.press('Enter')
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

test.describe('Warp charts', () => {
  test.beforeEach(async ({ dashboardPage }) => {
    await enableWarpFlag(dashboardPage.page)
    await dashboardPage.goto()
    await expect(dashboardPage.pageTitle).toBeVisible()
  })

  test('renders a chart block as a chart, not as JSON', async ({ dashboardPage }) => {
    const page = dashboardPage.page
    await mockWarp(page, 'Errors spiked on the 23rd.\n\n```warp-chart\n' + JSON.stringify(lineChart) + '\n```\n\nMost were overloads.')
    await page.reload()
    await ask(page, 'Plot errors per day over the last 7 days.')

    const chart = page.getByTestId('warp-chart')
    await expect(chart).toBeVisible()
    await expect(chart).toHaveAttribute('data-chart-kind', 'line')
    await expect(page.getByTestId('warp-chart-title')).toHaveText('Errors per day, last 7 days')
    await expect(chart.locator('svg').first()).toBeVisible()
    // The surrounding prose still renders, and the spec never shows as text.
    await expect(page.getByText('Errors spiked on the 23rd.')).toBeVisible()
    await expect(page.getByText('Most were overloads.')).toBeVisible()
    await expect(page.getByText('"points"')).toHaveCount(0)
  })

  test('shows a malformed chart block as unavailable instead of breaking the answer', async ({ dashboardPage }) => {
    const page = dashboardPage.page
    await mockWarp(page, 'Here it is:\n\n```warp-chart\nnot a spec\n```\n\nThe rest of the answer.')
    await page.reload()
    await ask(page, 'Plot spend by provider.')

    await expect(page.getByTestId('warp-chart-invalid')).toBeVisible()
    await expect(page.getByText('The rest of the answer.')).toBeVisible()
    await expect(page.getByTestId('warp-chart')).toHaveCount(0)
  })
})
