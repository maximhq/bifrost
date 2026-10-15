import type { Page } from '@playwright/test'
import { expect, test } from '../../core/fixtures/base.fixture'

// A backfill's embedding calls skip the plugin pipeline, so they never reach
// the logs - the grey spend line next to the backfill counters is the only
// place an operator sees what indexing a window cost. The status endpoint is
// mocked, so this pins the rendering, not the job.

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
}

function completedJob(spend: { embedding_tokens?: number; embedding_cost?: number }) {
  return {
    id: 'warp-backfill-1',
    status: 'completed',
    start_time: '2026-09-17T00:00:00Z',
    end_time: '2026-09-24T00:00:00Z',
    total: 120,
    scanned: 120,
    indexed: 118,
    skipped: 2,
    failed: 0,
    ...spend,
    message: 'Scanned 120 log(s): 118 indexed, 2 skipped, 0 failed.',
  }
}

// The warp flag is shared server state that other suites toggle in parallel,
// and it only gates the UI. Turning it on in this page's view of the flags
// leaves the server's copy alone, so no suite races another over it.
async function mockWarpBackfill(page: Page, job: object) {
  await page.route('**/api/feature-flags', async (route) => {
    if (route.request().method() !== 'GET') return route.continue()
    const response = await route.fetch()
    const body = (await response.json()) as { flags: { id: string; enabled: boolean }[] }
    body.flags = body.flags.map((flag) => (flag.id === 'warp' ? { ...flag, enabled: true } : flag))
    await route.fulfill({ response, json: body })
  })
  await page.route('**/api/warp/config', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(configuredWarp) }),
  )
  await page.route('**/api/warp/log-index/backfill/status**', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(job) }),
  )
}

test.describe('Warp backfill spend', () => {
  test('shows embedding tokens and cost next to the backfill counters', async ({ page }) => {
    await mockWarpBackfill(page, completedJob({ embedding_tokens: 48210, embedding_cost: 0.00096 }))
    await page.goto('/workspace/config/warp')

    const status = page.getByTestId('warp-backfill-status')
    await expect(status).toBeVisible()
    await expect(status).toContainText('118 indexed · 2 skipped · 0 failed')
    // Sub-cent spend keeps four places rather than rounding down to "$0.00".
    await expect(page.getByTestId('warp-backfill-spend')).toHaveText('48,210 tokens · $0.0010')
  })

  test('shows tokens alone when the deployment cannot price them', async ({ page }) => {
    await mockWarpBackfill(page, completedJob({ embedding_tokens: 48210 }))
    await page.goto('/workspace/config/warp')

    await expect(page.getByTestId('warp-backfill-status')).toBeVisible()
    await expect(page.getByTestId('warp-backfill-spend')).toHaveText('48,210 tokens')
  })

  test('shows no spend line before any embedding call was made', async ({ page }) => {
    await mockWarpBackfill(page, completedJob({}))
    await page.goto('/workspace/config/warp')

    await expect(page.getByTestId('warp-backfill-status')).toBeVisible()
    await expect(page.getByTestId('warp-backfill-spend')).toHaveCount(0)
  })
})

// The status endpoint answers for the space the deployment is configured with,
// but the form can be edited off that space before anything is saved. The old
// run's "Completed" then sat under a model nothing has been indexed for, so
// the card follows the form: gone while the embedding space is edited, back
// when the fields return to the saved values.
test.describe('Warp backfill status and the embedding space', () => {
  test('hides a finished backfill while the embedding space is edited', async ({ page }) => {
    await mockWarpBackfill(page, completedJob({ embedding_tokens: 48210 }))
    await page.goto('/workspace/config/warp')

    const status = page.getByTestId('warp-backfill-status')
    await expect(status).toBeVisible()

    const dimension = page.getByTestId('warp-embedding-dimension-input')
    await dimension.fill('3072')
    await expect(status).toHaveCount(0)

    await dimension.fill(String(configuredWarp.embedding_dimension))
    await expect(status).toBeVisible()
  })
})

// The server refuses to change the embedding space while a backfill is in
// flight, but the moment the job finishes a save goes through - and the
// id-pinned poll the page uses during a run still answers for that job. So the
// old space's "Completed" could arrive after the new space was saved and sit
// on screen as a finished backfill of rows the new space never indexed.
test.describe('Warp backfill status after the embedding space is saved over', () => {
  test('drops a run whose terminal status lands after its space was replaced', async ({ page }) => {
    const runningJob = {
      id: 'warp-backfill-2',
      status: 'running',
      start_time: '2026-09-17T00:00:00Z',
      end_time: '2026-09-24T00:00:00Z',
      total: 120,
      scanned: 60,
      indexed: 60,
      skipped: 0,
      failed: 0,
    }
    const newSpace = { ...configuredWarp, embedding_dimension: 3072, log_vector_store_namespace: 'BifrostWarpLogsV2' }
    let savedConfig = configuredWarp
    await page.route('**/api/feature-flags', async (route) => {
      if (route.request().method() !== 'GET') return route.continue()
      const response = await route.fetch()
      const body = (await response.json()) as { flags: { id: string; enabled: boolean }[] }
      body.flags = body.flags.map((flag) => (flag.id === 'warp' ? { ...flag, enabled: true } : flag))
      await route.fulfill({ response, json: body })
    })
    await page.route('**/api/warp/config', async (route) => {
      if (route.request().method() === 'PUT') savedConfig = newSpace
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(savedConfig) })
    })
    await page.route('**/api/warp/log-index/backfill/status**', (route) => {
      const pinned = new URL(route.request().url()).searchParams.get('id') === runningJob.id
      // Once the new space is saved, the job is over: the pinned read still
      // reports it (by id, no space check), while the id-less read hides a run
      // of a space that is no longer configured.
      const job =
        savedConfig === configuredWarp
          ? runningJob
          : pinned
            ? { ...runningJob, status: 'completed', scanned: 120, indexed: 120 }
            : { status: 'idle' }
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(job) })
    })
    await page.goto('/workspace/config/warp')

    const status = page.getByTestId('warp-backfill-status')
    await expect(status).toContainText(/running/i)
    await expect(page.getByTestId('warp-backfill-cancel-btn')).toBeVisible()

    await page.getByTestId('warp-embedding-dimension-input').fill(String(newSpace.embedding_dimension))
    await page.getByTestId('warp-vector-namespace-input').fill(newSpace.log_vector_store_namespace)
    await page.getByTestId('warp-save-btn').click()

    // The pinned poll has delivered the terminal status once Cancel gives way
    // to Start. The old run must not have been kept as this space's result.
    await expect(page.getByTestId('warp-backfill-start-btn')).toBeVisible()
    await expect(status).toHaveCount(0)
  })
})

// The threshold is a 0-1 similarity on every vector store, and real topic
// matches score 0.575 to 0.735 on it, so a deployment that never set one
// starts at 0.5. A saved value is the operator's and is shown as saved.
test.describe('Warp semantic search threshold', () => {
  test('shows 0.5 when none is saved, and a saved value unchanged', async ({ page }) => {
    await page.route('**/api/feature-flags', async (route) => {
      if (route.request().method() !== 'GET') return route.continue()
      const response = await route.fetch()
      const body = (await response.json()) as { flags: { id: string; enabled: boolean }[] }
      body.flags = body.flags.map((flag) => (flag.id === 'warp' ? { ...flag, enabled: true } : flag))
      await route.fulfill({ response, json: body })
    })
    let threshold = 0
    await page.route('**/api/warp/config', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ ...configuredWarp, semantic_search_threshold: threshold }),
      }),
    )

    await page.goto('/workspace/config/warp')
    await expect(page.getByTestId('warp-search-threshold-input')).toHaveValue('0.5')

    threshold = 0.62
    await page.reload()
    await expect(page.getByTestId('warp-search-threshold-input')).toHaveValue('0.62')
  })
})
