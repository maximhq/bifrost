import type { Page } from '@playwright/test'
import { expect, test } from '../../core/fixtures/base.fixture'

// Warp used to be read-only. It now reaches the gateway's own API through the
// Bifrost MCP: it reads configuration, makes the kinds of change an admin allows
// in Warp's settings (none by default) after the person confirms, and does not
// answer at all without it. A deployment that already runs Warp gets that on
// upgrade, so its admins see a notice with what decides it: bifrost_url (without
// it the Bifrost MCP never loads), openapi_url, and the kinds of change Warp may
// make, with a link to Warp's settings where they are chosen; Security's
// bifrost_mcp_allow_writes is the ceiling. The notice saves no setting itself.
// Without the Bifrost MCP it is a setup step that opens on every visit, on the
// settings page and in the panel. /api/config is patched in this
// page's view only and its PUT is captured, never sent, since those settings are
// shared server state; /api/warp/config reports bifrost_mcp_available the way
// the server does, from whether bifrost_url is set, unless a test overrides it.

const NOTICE_KEY = 'bifrost.warpBifrostMCPNoticeSeen'
const LATER_COUNT_KEY = 'bifrost.warpBifrostMCPNoticeLaterCount'
const BIFROST_URL = { value: 'https://bifrost.example.com' }

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
async function mockNotice(page: Page, settings: MCPSettings, warp: object = {}): Promise<ConfigBody[]> {
  const saved: ConfigBody[] = []
  const state = { ...settings }
  await page.route('**/api/feature-flags', async (route) => {
    if (route.request().method() !== 'GET') return route.continue()
    const response = await route.fetch()
    const body = (await response.json()) as { flags: { id: string; enabled: boolean }[] }
    body.flags = body.flags.map((flag) => (flag.id === 'warp' ? { ...flag, enabled: true } : flag))
    await route.fulfill({ response, json: body })
  })
  const warpConfig = { ...configuredWarp, bifrost_mcp_available: !!settings.bifrost_url, ...warp }
  await page.route('**/api/warp/config', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(warpConfig) }),
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
  test('without a Bifrost URL it is a setup step that keeps the reminder cap, then stays as a banner', async ({ page }) => {
    await mockNotice(page, {})
    // Acknowledged while the Bifrost MCP was loaded, then put off four times: the
    // acknowledgement does not hide setup, the cap still counts. Set once, so
    // reloads keep what the page writes.
    await page.addInitScript(
      ([ack, later]) => {
        if (localStorage.getItem(later) !== null) return
        localStorage.setItem(ack, '1')
        localStorage.setItem(later, '4')
      },
      [NOTICE_KEY, LATER_COUNT_KEY],
    )
    await page.goto('/workspace/config/warp')

    const dialog = page.getByTestId('warp-mcp-notice-dialog')
    const banner = page.getByTestId('warp-mcp-notice-banner')
    await expect(dialog).toBeVisible()
    await expect(dialog).toHaveAttribute('data-setup', 'true')
    await expect(banner).toHaveCount(0)
    await expect(page.getByTestId('warp-mcp-notice-bifrost-url')).toHaveAttribute('data-ok', 'false')
    await expect(page.getByTestId('warp-mcp-notice-outcome')).toHaveAttribute('data-mode', 'off')
    await expect(page.getByTestId('warp-mcp-notice-client-settings')).toHaveAttribute('href', /\/workspace\/config\/client-settings$/)
    // Nothing to acknowledge: Warp does not answer until it is set up.
    await expect(page.getByTestId('warp-mcp-notice-acknowledge')).toHaveCount(0)
    // Setup links to the settings that load the Bifrost MCP, not the upgrade notes.
    await expect(page.getByTestId('warp-mcp-notice-docs-link')).toHaveAttribute('href', /#configuration-tools$/)

    await page.getByTestId('warp-mcp-notice-later').click()
    await expect(dialog).toHaveCount(0)
    await expect(banner).toHaveAttribute('data-setup', 'true')
    expect(await page.evaluate((key) => localStorage.getItem(key), LATER_COUNT_KEY)).toBe('5')

    await page.reload()
    await expect(banner).toBeVisible()
    await expect(dialog).toHaveCount(0)

    await page.getByTestId('warp-mcp-notice-banner-review').click()
    await expect(dialog).toHaveAttribute('data-setup', 'true')
  })

  test('says so when the Bifrost URL is set but the Bifrost MCP did not load', async ({ page }) => {
    await mockNotice(page, { bifrost_url: BIFROST_URL }, { bifrost_mcp_available: false })
    await page.goto('/workspace/config/warp')

    await expect(page.getByTestId('warp-mcp-notice-dialog')).toHaveAttribute('data-setup', 'true')
    await expect(page.getByTestId('warp-mcp-notice-bifrost-url')).toHaveAttribute('data-ok', 'true')
    await expect(page.getByTestId('warp-mcp-notice-openapi-url')).toHaveAttribute('data-ok', 'false')
    await expect(page.getByTestId('warp-mcp-notice-outcome')).toHaveAttribute('data-mode', 'not_loaded')
  })

  test('with the Bifrost MCP loaded it stays dismissed once acknowledged', async ({ page }) => {
    await mockNotice(page, { bifrost_url: BIFROST_URL })
    await page.goto('/workspace/config/warp')

    const dialog = page.getByTestId('warp-mcp-notice-dialog')
    await expect(dialog).toBeVisible()
    await expect(dialog).toHaveAttribute('data-setup', 'false')
    await expect(page.getByTestId('warp-mcp-notice-docs-link')).toHaveAttribute('href', /#upgrading-from-read-only-warp$/)
    await page.getByTestId('warp-mcp-notice-acknowledge').click()
    await expect(dialog).toHaveCount(0)
    expect(await page.evaluate((key) => localStorage.getItem(key), NOTICE_KEY)).toBe('1')

    await page.reload()
    await expect(page.getByTestId('warp-config-view')).toBeVisible()
    await expect(page.getByTestId('warp-mcp-notice-dialog')).toHaveCount(0)
  })

  test('shows the kinds of change Warp may make, links to where they are chosen, and saves nothing', async ({ page }) => {
    const saved = await mockNotice(
      page,
      { bifrost_url: { value: 'https://bifrost.example.com' }, openapi_url: 'specs/openapi.json' },
      { management_writes: ['create', 'update'] },
    )
    await page.goto('/workspace/config/warp')

    await expect(page.getByTestId('warp-mcp-notice-bifrost-url')).toHaveAttribute('data-ok', 'true')
    await expect(page.getByTestId('warp-mcp-notice-bifrost-url')).toContainText('https://bifrost.example.com')
    await expect(page.getByTestId('warp-mcp-notice-openapi-url')).toContainText('specs/openapi.json')
    const changes = page.getByTestId('warp-mcp-notice-changes-row')
    await expect(changes).toHaveAttribute('data-writes', 'create,update')
    await expect(changes).toHaveAttribute('data-ok', 'true')
    await expect(page.getByTestId('warp-mcp-notice-warp-settings-link')).toHaveAttribute('href', /\/workspace\/config\/warp$/)
    await expect(page.getByTestId('warp-mcp-notice-outcome')).toHaveAttribute('data-mode', 'reads_and_writes')
    await expect(page.getByTestId('warp-mcp-notice-allow-writes')).toHaveCount(0)

    await page.getByTestId('warp-mcp-notice-acknowledge').click()
    await expect(page.getByTestId('warp-mcp-notice-dialog')).toHaveCount(0)
    expect(saved).toHaveLength(0)
  })

  test('reads only by default, and with Bifrost MCP writes off the chosen kinds do not apply', async ({ page }) => {
    await mockNotice(page, { bifrost_url: { value: 'https://bifrost.example.com' } })
    await page.goto('/workspace/config/warp')
    const changes = page.getByTestId('warp-mcp-notice-changes-row')
    await expect(changes).toHaveAttribute('data-writes', '')
    await expect(page.getByTestId('warp-mcp-notice-outcome')).toHaveAttribute('data-mode', 'reads')

    await page.unrouteAll({ behavior: 'ignoreErrors' })
    await mockNotice(page, { bifrost_url: { value: 'https://bifrost.example.com' }, bifrost_mcp_allow_writes: false }, { management_writes: ['delete'] })
    await page.reload()
    await expect(changes).toHaveAttribute('data-writes', 'delete')
    await expect(changes).toHaveAttribute('data-ok', 'false')
    await expect(page.getByTestId('warp-mcp-notice-outcome')).toHaveAttribute('data-mode', 'reads')
  })

  test('remind me later closes it for this visit only', async ({ page }) => {
    await mockNotice(page, { bifrost_url: BIFROST_URL })
    await page.goto('/workspace/config/warp')

    await page.getByTestId('warp-mcp-notice-later').click()
    await expect(page.getByTestId('warp-mcp-notice-dialog')).toHaveCount(0)

    await page.reload()
    await expect(page.getByTestId('warp-mcp-notice-dialog')).toBeVisible()
  })

  test('after the fifth remind me later it stops opening and stays as a banner until acknowledged', async ({ page }) => {
    await mockNotice(page, { bifrost_url: BIFROST_URL })
    // Four earlier put-offs; set once, so reloads keep what the page writes.
    await page.addInitScript((key) => {
      if (localStorage.getItem(key) === null) localStorage.setItem(key, '4')
    }, LATER_COUNT_KEY)
    await page.goto('/workspace/config/warp')

    const dialog = page.getByTestId('warp-mcp-notice-dialog')
    const banner = page.getByTestId('warp-mcp-notice-banner')
    await expect(dialog).toBeVisible()
    await expect(banner).toHaveCount(0)

    await page.getByTestId('warp-mcp-notice-later').click()
    await expect(dialog).toHaveCount(0)
    await expect(banner).toBeVisible()
    expect(await page.evaluate((key) => localStorage.getItem(key), LATER_COUNT_KEY)).toBe('5')

    await page.reload()
    await expect(banner).toBeVisible()
    await expect(dialog).toHaveCount(0)

    // Closing a dialog opened from the banner keeps the banner and adds no reminder.
    await page.getByTestId('warp-mcp-notice-banner-review').click()
    await expect(dialog).toBeVisible()
    await page.getByTestId('warp-mcp-notice-later').click()
    await expect(dialog).toHaveCount(0)
    await expect(banner).toBeVisible()
    expect(await page.evaluate((key) => localStorage.getItem(key), LATER_COUNT_KEY)).toBe('5')

    await page.getByTestId('warp-mcp-notice-banner-review').click()
    await page.getByTestId('warp-mcp-notice-acknowledge').click()
    await expect(dialog).toHaveCount(0)
    await expect(banner).toHaveCount(0)

    await page.reload()
    await expect(page.getByTestId('warp-config-view')).toBeVisible()
    await expect(banner).toHaveCount(0)
    await expect(dialog).toHaveCount(0)
  })

  test('says the settings are not readable only for a 401 or 403', async ({ page }) => {
    await mockNotice(page, { bifrost_url: BIFROST_URL })
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
    await mockNotice(page, {}, { enabled: false, configured: false })
    await page.goto('/workspace/config/warp')

    await expect(page.getByTestId('warp-config-view')).toBeVisible()
    await expect(page.getByTestId('warp-mcp-notice-dialog')).toHaveCount(0)
  })
})

// Warp does not answer without the Bifrost MCP. Opening the panel shows the
// setup state in place of the transcript and composer, and an admin gets the
// setup popup straight away. The ask-an-admin variant needs a role without
// Update on Warp, which OSS cannot make; the panel picks it on that one check.
test.describe('Warp panel without the Bifrost MCP', () => {
  test.beforeEach(async ({ page }) => {
    await page.context().addCookies([
      {
        name: 'bifrost_onboarding_remind_at',
        value: new Date(Date.now() + 24 * 60 * 60 * 1000).toISOString(),
        url: test.info().project.use.baseURL ?? 'http://localhost:3000',
      },
    ])
  })

  test('opens the setup popup and offers no composer', async ({ page }) => {
    await mockNotice(page, {})
    await page.goto('/workspace/config/client-settings')

    await page.getByTestId('topbar-warp-btn').click()
    const dialog = page.getByTestId('warp-mcp-notice-dialog')
    await expect(dialog).toBeVisible()
    await expect(dialog).toHaveAttribute('data-setup', 'true')
    await expect(page.getByTestId('warp-needs-bifrost-mcp')).toBeVisible()
    await expect(page.getByTestId('warp-composer-input')).toHaveCount(0)

    await page.getByTestId('warp-mcp-notice-later').click()
    await expect(dialog).toHaveCount(0)
    await expect(page.getByTestId('warp-needs-bifrost-mcp')).toBeVisible()

    await page.getByTestId('warp-bifrost-mcp-set-up').click()
    await expect(dialog).toBeVisible()
  })

  test('past the reminder cap the panel shows setup without the popup, which Set up still opens', async ({ page }) => {
    await mockNotice(page, {})
    await page.addInitScript((key) => localStorage.setItem(key, '5'), LATER_COUNT_KEY)
    await page.goto('/workspace/config/client-settings')

    await page.getByTestId('topbar-warp-btn').click()
    await expect(page.getByTestId('warp-needs-bifrost-mcp')).toBeVisible()
    await expect(page.getByTestId('warp-composer-input')).toHaveCount(0)
    await expect(page.getByTestId('warp-mcp-notice-dialog')).toHaveCount(0)

    await page.getByTestId('warp-bifrost-mcp-set-up').click()
    await expect(page.getByTestId('warp-mcp-notice-dialog')).toHaveAttribute('data-setup', 'true')
  })

  test('a refusal for a missing Bifrost MCP switches an open panel to setup', async ({ page }) => {
    await mockNotice(page, { bifrost_url: BIFROST_URL })
    // Acknowledged, so the informational notice stays out of the way until the MCP goes.
    await page.addInitScript((key) => localStorage.setItem(key, '1'), NOTICE_KEY)
    let available = true
    await page.route('**/api/warp/config', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ ...configuredWarp, bifrost_mcp_available: available }),
      }),
    )
    await page.route('**/api/warp/chat', (route) => {
      available = false
      return route.fulfill({
        status: 503,
        contentType: 'application/json',
        body: JSON.stringify({ reason: 'no_bifrost_mcp', message: 'Warp needs the Bifrost MCP, which is not loaded.' }),
      })
    })
    await page.goto('/workspace/config/client-settings')

    await page.getByTestId('topbar-warp-btn').click()
    const composer = page.getByTestId('warp-composer-input')
    await expect(composer).toBeVisible()
    await composer.fill('what routing rules exist?')
    await composer.press('Enter')

    await expect(page.getByTestId('warp-needs-bifrost-mcp')).toBeVisible()
    await expect(page.getByTestId('warp-mcp-notice-dialog')).toHaveAttribute('data-setup', 'true')
    await expect(composer).toHaveCount(0)
  })
})
