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
  semantic_search_threshold: 0.7,
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
