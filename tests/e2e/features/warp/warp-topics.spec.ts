import type { Page, Request } from '@playwright/test'
import { expect, test } from '../../core/fixtures/base.fixture'

// Topic clustering is a background job started from Warp's settings. The job
// itself is covered by the Go tests and the Warp API suite; what only the
// browser can show is whether an operator can start one, stop one, and read
// how the last one ended. Every endpoint is mocked, so this pins the block's
// behaviour, not the job.

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

const idle = { status: 'idle' }

const completedRun = {
  id: 'warp-topics-1',
  status: 'completed',
  start_time: '2026-08-30T00:00:00Z',
  end_time: '2026-09-29T00:00:00Z',
  total: 7015,
  scanned: 7015,
  indexed: 95,
  skipped: 0,
  failed: 0,
  clustered: 7015,
  merged: 2,
  merged_by_name: 3,
  merge_threshold: 0.95,
  message: 'Clustered 7015 request(s) into 95 topic(s). Merged 2 near-duplicate cluster(s) at similarity 0.95 or above. 3 more joined for sharing a name.',
}

const runningRun = {
  id: 'warp-topics-2',
  status: 'running',
  start_time: '2026-08-30T00:00:00Z',
  end_time: '2026-09-29T00:00:00Z',
  total: 1200,
  scanned: 1200,
  indexed: 0,
  skipped: 0,
  failed: 0,
  message: 'Read 1200 request(s); clustering.',
}

interface TopicsMock {
  config?: object
  // status answers GET /topics/status. A function, so a test can change what
  // the next poll sees.
  status: () => object
  onStart?: (request: Request) => object
  onCancel?: (request: Request) => object
}

// The warp flag is shared server state that other suites toggle in parallel,
// and it only gates the UI. Turning it on in this page's view of the flags
// leaves the server's copy alone, so no suite races another over it.
async function mockWarpTopics(page: Page, mock: TopicsMock) {
  await page.route('**/api/feature-flags', async (route) => {
    if (route.request().method() !== 'GET') return route.continue()
    const response = await route.fetch()
    const body = (await response.json()) as { flags: { id: string; enabled: boolean }[] }
    body.flags = body.flags.map((flag) => (flag.id === 'warp' ? { ...flag, enabled: true } : flag))
    await route.fulfill({ response, json: body })
  })
  await page.route('**/api/warp/config', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(mock.config ?? configuredWarp) }),
  )
  await page.route('**/api/warp/log-index/backfill/status**', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(idle) }),
  )
  await page.route('**/api/warp/log-index/topics/status**', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(mock.status()) }),
  )
  await page.route('**/api/warp/log-index/topics/cancel', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(mock.onCancel ? mock.onCancel(route.request()) : { ...runningRun, status: 'cancelled' }),
    }),
  )
  await page.route('**/api/warp/log-index/topics', (route) => {
    if (route.request().method() !== 'POST') return route.continue()
    return route.fulfill({
      status: 202,
      contentType: 'application/json',
      body: JSON.stringify(mock.onStart ? mock.onStart(route.request()) : runningRun),
    })
  })
}

test.describe('Warp topic clusters', () => {
  test('shows how the last run ended', async ({ page }) => {
    await mockWarpTopics(page, { status: () => completedRun })
    await page.goto('/workspace/config/warp')

    const status = page.getByTestId('warp-topics-status')
    await expect(status).toBeVisible()
    await expect(status).toContainText('completed')
    // Counted in requests: every model call is one, so "conversations" would overstate it.
    await expect(status).toContainText('7015 requests read')
    await expect(status).toContainText('95 topics written')
    await expect(status).toContainText('Merged 2 near-duplicate cluster(s)')
    await expect(status).not.toContainText('fell back')
    await expect(status).not.toContainText('Latest error')
    await expect(page.getByTestId('warp-topics-start-btn')).toBeEnabled()
    await expect(page.getByTestId('warp-topics-cancel-btn')).toHaveCount(0)
  })

  test('shows how many topics the model could not name, and why', async ({ page }) => {
    await mockWarpTopics(page, {
      status: () => ({ ...completedRun, failed: 25, last_error: 'label topic: no keys found that support model: openai/gpt-5.6-luna' }),
    })
    await page.goto('/workspace/config/warp')

    const status = page.getByTestId('warp-topics-status')
    await expect(status).toContainText('95 topics written · 25 labels fell back')
    await expect(status).toContainText('Latest error: label topic: no keys found')
  })

  test('shows nothing of a run before one has been started', async ({ page }) => {
    await mockWarpTopics(page, { status: () => idle })
    await page.goto('/workspace/config/warp')

    await expect(page.getByTestId('warp-topics-section')).toBeVisible()
    await expect(page.getByTestId('warp-topics-status')).toHaveCount(0)
    await expect(page.getByTestId('warp-topics-start-btn')).toBeEnabled()
  })

  test('starts a run over the chosen window and follows it', async ({ page }) => {
    let current: object = idle
    let started: { start_time?: string; end_time?: string } = {}
    await mockWarpTopics(page, {
      status: () => current,
      onStart: (request) => {
        started = request.postDataJSON()
        current = runningRun
        return { id: runningRun.id, status: 'pending', total: 0, scanned: 0, indexed: 0, skipped: 0, failed: 0 }
      },
    })
    await page.goto('/workspace/config/warp')

    await page.getByTestId('warp-topics-start-btn').click()

    await expect(page.getByTestId('warp-topics-cancel-btn')).toBeVisible()
    await expect(page.getByTestId('warp-topics-start-btn')).toHaveCount(0)
    const status = page.getByTestId('warp-topics-status')
    await expect(status).toContainText('running')
    await expect(status).toContainText('1200 requests read')

    // The default window is the last 30 days, sent as the instants it covers.
    expect(started.start_time).toBeTruthy()
    expect(started.end_time).toBeTruthy()
    const days = (new Date(started.end_time!).getTime() - new Date(started.start_time!).getTime()) / 86_400_000
    expect(days).toBeGreaterThan(27)
    expect(days).toBeLessThan(32)
  })

  test('cancels the run that is in flight', async ({ page }) => {
    let current: object = runningRun
    let cancelled: { id?: string } = {}
    await mockWarpTopics(page, {
      status: () => current,
      onCancel: (request) => {
        cancelled = request.postDataJSON()
        current = { ...runningRun, status: 'cancelled', message: 'Stopped while clustering.' }
        return current
      },
    })
    await page.goto('/workspace/config/warp')

    await page.getByTestId('warp-topics-cancel-btn').click()

    const status = page.getByTestId('warp-topics-status')
    await expect(status).toContainText('cancelled')
    await expect(status).toContainText('Stopped while clustering.')
    await expect(page.getByTestId('warp-topics-start-btn')).toBeVisible()
    expect(cancelled.id).toBe(runningRun.id)
  })

  test('cannot start a run with no vector store to read from', async ({ page }) => {
    await mockWarpTopics(page, { config: { ...configuredWarp, vector_store_connected: false }, status: () => idle })
    await page.goto('/workspace/config/warp')

    await expect(page.getByTestId('warp-topics-start-btn')).toBeDisabled()
  })

  test('says so when the status cannot be read, and lets it be asked for again', async ({ page }) => {
    let healthy = false
    await mockWarpTopics(page, { status: () => completedRun })
    // Registered last, so it is consulted first.
    await page.route('**/api/warp/log-index/topics/status**', (route) =>
      healthy
        ? route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(completedRun) })
        : route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: { message: 'boom' } }) }),
    )
    await page.goto('/workspace/config/warp')

    await expect(page.getByTestId('warp-topics-status-error')).toBeVisible()
    // Without a status, "no run is in flight" is a guess, and starting on it is a 409.
    await expect(page.getByTestId('warp-topics-start-btn')).toBeDisabled()

    healthy = true
    await page.getByTestId('warp-topics-status-retry').click()

    await expect(page.getByTestId('warp-topics-status-error')).toHaveCount(0)
    await expect(page.getByTestId('warp-topics-status')).toContainText('95 topics written')
    await expect(page.getByTestId('warp-topics-start-btn')).toBeEnabled()
  })
})
