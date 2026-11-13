import type { Page } from '@playwright/test'
import { expect, test } from '../../core/fixtures/base.fixture'

// Warp used to be read-only. It now reaches the gateway's own API through the
// Bifrost MCP: it reads configuration, and creates after the person confirms.
// A deployment that already runs Warp gets that on upgrade, so the settings
// page shows its admins a one-time notice with the settings that decide it:
// bifrost_url (without it the configuration tools never load), openapi_url,
// and bifrost_mcp_allow_writes, which the notice can turn off before anyone
// uses a create. /api/config is patched in this page's view only and its PUT is
// captured, never sent, since those settings are shared server state.

const NOTICE_KEY = 'bifrost.warpBifrostMCPNoticeSeen'

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

interface MCPSettings {
  bifrost_url?: { value: string }
  bifrost_mcp_allow_writes?: boolean
  openapi_url?: string
}

interface ConfigBody {
  client_config: { bifrost_url?: { value: string }; bifrost_mcp_allow_writes?: boolean }
  framework_config: { openapi_url?: string }
}

const isCoreConfig = (url: URL) => url.pathname.endsWith('/api/config')

// Returns the PUT bodies the notice sent. A PUT updates the settings the next
// GET reports, as the server would.
async function mockNotice(page: Page, settings: MCPSettings, warp: object = configuredWarp): Promise<ConfigBody[]> {
  const saved: ConfigBody[] = []
  const state = { ...settings }
  await page.route('**/api/feature-flags', async (route) => {
    if (route.request().method() !== 'GET') return route.continue()
    const response = await route.fetch()
    const body = (await response.json()) as { flags: { id: string; enabled: boolean }[] }
    body.flags = body.flags.map((flag) => (flag.id === 'warp' ? { ...flag, enabled: true } : flag))
    await route.fulfill({ response, json: body })
  })
  await page.route('**/api/warp/config', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(warp) }),
  )
  await page.route(isCoreConfig, async (route) => {
    const method = route.request().method()
    if (method === 'PUT') {
      const body = route.request().postDataJSON() as ConfigBody
      saved.push(body)
      state.bifrost_mcp_allow_writes = body.client_config.bifrost_mcp_allow_writes
      return route.fulfill({ status: 200, contentType: 'application/json', body: 'null' })
    }
    if (method !== 'GET') return route.continue()
    const response = await route.fetch()
    const body = (await response.json()) as ConfigBody
    const client = { ...body.client_config }
    delete client.bifrost_url
    delete client.bifrost_mcp_allow_writes
    if (state.bifrost_url) client.bifrost_url = state.bifrost_url
    if (state.bifrost_mcp_allow_writes !== undefined) client.bifrost_mcp_allow_writes = state.bifrost_mcp_allow_writes
    const framework = { ...body.framework_config, openapi_url: state.openapi_url ?? '' }
    await route.fulfill({ response, json: { ...body, client_config: client, framework_config: framework } })
  })
  return saved
}

// Answers the stored-config read (GET /api/config?from_db=true, the one the notice
// makes) with the status status() returns, or passes it on to mockNotice's handler
// when it returns undefined. The dashboard shell reads with from_db=false and is left
// alone, or the whole page would show its connection error. Registered after
// mockNotice, so it runs first.
async function failCoreConfigReads(page: Page, status: () => number | undefined) {
  await page.route(isCoreConfig, (route) => {
    const request = route.request()
    const fromDB = new URL(request.url()).searchParams.get('from_db') === 'true'
    const code = request.method() === 'GET' && fromDB ? status() : undefined
    if (code === undefined) return route.fallback()
    return route.fulfill({ status: code, contentType: 'application/json', body: JSON.stringify({ error: { message: `status ${code}` } }) })
  })
}

test.describe('Warp Bifrost MCP notice', () => {
  test('flags a missing Bifrost URL and stays dismissed once acknowledged', async ({ page }) => {
    await mockNotice(page, {})
    await page.goto('/workspace/config/warp')

    const dialog = page.getByTestId('warp-mcp-notice-dialog')
    await expect(dialog).toBeVisible()
    await expect(page.getByTestId('warp-mcp-notice-bifrost-url')).toHaveAttribute('data-ok', 'false')
    await expect(page.getByTestId('warp-mcp-notice-outcome')).toHaveAttribute('data-mode', 'traffic_only')
    await expect(page.getByTestId('warp-mcp-notice-client-settings')).toHaveAttribute('href', /\/workspace\/config\/client-settings$/)

    await page.getByTestId('warp-mcp-notice-acknowledge').click()
    await expect(dialog).toHaveCount(0)
    expect(await page.evaluate((key) => localStorage.getItem(key), NOTICE_KEY)).toBe('1')

    await page.reload()
    await expect(page.getByTestId('warp-config-view')).toBeVisible()
    await expect(page.getByTestId('warp-mcp-notice-dialog')).toHaveCount(0)
  })

  test('turning creates off saves bifrost_mcp_allow_writes and keeps the rest', async ({ page }) => {
    const saved = await mockNotice(page, {
      bifrost_url: { value: 'https://bifrost.example.com' },
      openapi_url: 'specs/openapi.json',
    })
    await page.goto('/workspace/config/warp')

    await expect(page.getByTestId('warp-mcp-notice-bifrost-url')).toHaveAttribute('data-ok', 'true')
    await expect(page.getByTestId('warp-mcp-notice-bifrost-url')).toContainText('https://bifrost.example.com')
    await expect(page.getByTestId('warp-mcp-notice-openapi-url')).toContainText('specs/openapi.json')
    const outcome = page.getByTestId('warp-mcp-notice-outcome')
    await expect(outcome).toHaveAttribute('data-mode', 'reads_and_creates')

    const writes = page.getByTestId('warp-mcp-notice-allow-writes')
    await expect(writes).toBeChecked()
    await writes.click()
    await expect(outcome).toHaveAttribute('data-mode', 'reads')

    await page.getByTestId('warp-mcp-notice-acknowledge').click()
    await expect(page.getByTestId('warp-mcp-notice-dialog')).toHaveCount(0)
    expect(saved).toHaveLength(1)
    expect(saved[0].client_config.bifrost_mcp_allow_writes).toBe(false)
    expect(saved[0].client_config.bifrost_url).toEqual({ value: 'https://bifrost.example.com' })
    expect(saved[0].framework_config.openapi_url).toBe('specs/openapi.json')
  })

  test('acknowledging without a change saves nothing', async ({ page }) => {
    const saved = await mockNotice(page, { bifrost_url: { value: 'https://bifrost.example.com' } })
    await page.goto('/workspace/config/warp')

    await expect(page.getByTestId('warp-mcp-notice-outcome')).toHaveAttribute('data-mode', 'reads_and_creates')
    await page.getByTestId('warp-mcp-notice-acknowledge').click()
    await expect(page.getByTestId('warp-mcp-notice-dialog')).toHaveCount(0)
    expect(saved).toHaveLength(0)
  })

  test('remind me later closes it for this visit only', async ({ page }) => {
    await mockNotice(page, {})
    await page.goto('/workspace/config/warp')

    await page.getByTestId('warp-mcp-notice-later').click()
    await expect(page.getByTestId('warp-mcp-notice-dialog')).toHaveCount(0)

    await page.reload()
    await expect(page.getByTestId('warp-mcp-notice-dialog')).toBeVisible()
  })

  test('says the settings are not readable only for a 401 or 403', async ({ page }) => {
    await mockNotice(page, {})
    await failCoreConfigReads(page, () => 403)
    await page.goto('/workspace/config/warp')

    await expect(page.getByTestId('warp-mcp-notice-settings-error')).toBeVisible()
    await expect(page.getByTestId('warp-mcp-notice-settings-load-error')).toHaveCount(0)
    await expect(page.getByTestId('warp-mcp-notice-acknowledge')).toBeEnabled()
  })

  test('offers a retry when the settings fail to load for another reason', async ({ page }) => {
    await mockNotice(page, { bifrost_url: { value: 'https://bifrost.example.com' } })
    let failing = true
    await failCoreConfigReads(page, () => (failing ? 500 : undefined))
    await page.goto('/workspace/config/warp')

    const loadError = page.getByTestId('warp-mcp-notice-settings-load-error')
    await expect(loadError).toBeVisible()
    await expect(page.getByTestId('warp-mcp-notice-settings-error')).toHaveCount(0)

    failing = false
    await page.getByTestId('warp-mcp-notice-settings-retry').click()
    await expect(loadError).toHaveCount(0)
    await expect(page.getByTestId('warp-mcp-notice-bifrost-url')).toHaveAttribute('data-ok', 'true')
  })

  test('is not shown while Warp is switched off', async ({ page }) => {
    await mockNotice(page, {}, { ...configuredWarp, enabled: false })
    await page.goto('/workspace/config/warp')

    await expect(page.getByTestId('warp-config-view')).toBeVisible()
    await expect(page.getByTestId('warp-mcp-notice-dialog')).toHaveCount(0)
  })
})
