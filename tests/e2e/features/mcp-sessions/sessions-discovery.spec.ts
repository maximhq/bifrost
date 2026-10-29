import type { Page } from '@playwright/test'
import { expect, test } from '../../core/fixtures/base.fixture'

// What a per-user credential's binding holds on its MCP server: the sessions
// table's Discovery column, and the Callers tab of a Per-Caller Discovery
// server's view sheet. The sessions list is stubbed so every row state shows at
// once; the registry's client list is stubbed for the view sheet.

// Every timestamp is relative to NOW, and each test pins the browser clock to
// it, so relative times read the same however long the run takes.
const NOW = Date.now()
const minutesAgo = (n: number) => new Date(NOW - n * 60_000).toISOString()

const PER_CALLER = { client_id: 'client-notes', name: 'notes', per_user_discovery: true }
const SHARED = { client_id: 'client-wiki', name: 'wiki' }

const SESSIONS = [
  {
    id: 'tok-alice', kind: 'token', auth_kind: 'oauth', auth_mode: 'user', user_id: 'alice', mcp_client: PER_CALLER,
    status: 'active', created_at: minutesAgo(60), has_refresh_token: true, can_reauth: false,
    discovery: { discovered_at: minutesAgo(4), tool_count: 3, prompt_count: 1, resource_count: 2 },
  },
  {
    id: 'hdr-bot', kind: 'header', auth_kind: 'headers', auth_mode: 'vk', virtual_key: { id: 'vk-1', name: 'support-bot' }, mcp_client: PER_CALLER,
    status: 'active', created_at: minutesAgo(90), updated_at: minutesAgo(90), can_reauth: true,
    discovery: { discovered_at: minutesAgo(120), tool_count: 0, prompt_count: 0, resource_count: 0 },
  },
  {
    id: 'hdr-priya', kind: 'header', auth_kind: 'headers', auth_mode: 'user', user_id: 'priya', mcp_client: PER_CALLER,
    status: 'active', created_at: minutesAgo(20), updated_at: minutesAgo(20), can_reauth: false,
  },
  {
    id: 'flow-jo', kind: 'flow', auth_kind: 'oauth', auth_mode: 'user', user_id: 'jo', mcp_client: PER_CALLER,
    status: 'pending', expires_at: minutesAgo(-9), created_at: minutesAgo(1), can_reauth: false,
  },
  {
    id: 'tok-sam', kind: 'token', auth_kind: 'oauth', auth_mode: 'user', user_id: 'sam', mcp_client: PER_CALLER,
    status: 'needs_reauth', created_at: minutesAgo(3000), has_refresh_token: true, can_reauth: false,
  },
  {
    id: 'hdr-sess', kind: 'header', auth_kind: 'headers', auth_mode: 'session', session_id: 'sess_7f3c91a2', mcp_client: PER_CALLER,
    status: 'active', created_at: minutesAgo(300), updated_at: minutesAgo(300), can_reauth: true,
  },
  {
    id: 'tok-wiki', kind: 'token', auth_kind: 'oauth', auth_mode: 'user', user_id: 'alice', mcp_client: SHARED,
    status: 'active', created_at: minutesAgo(600), has_refresh_token: true, can_reauth: false,
  },
]

// stubSessions answers the sessions list from SESSIONS, applying the client,
// kind and status filters and the limit the way the gateway does.
async function stubSessions(page: Page) {
  await page.route(
    (url) => url.pathname === '/api/mcp/sessions',
    async (route) => {
      const params = new URL(route.request().url()).searchParams
      const list = (name: string) => params.get(name)?.split(',')
      const clients = list('mcp_client_id')
      const kinds = list('kind')
      const statuses = list('status')
      const rows = SESSIONS.filter(
        (row) =>
          (!clients || clients.includes(row.mcp_client.client_id)) &&
          (!kinds || kinds.includes(row.kind)) &&
          (!statuses || statuses.includes(row.status)),
      )
      const limit = Number(params.get('limit') ?? 25)
      const shown = rows.slice(0, limit)
      await route.fulfill({ json: { sessions: shown, count: shown.length, total_count: rows.length, limit, offset: 0 } })
    },
  )
}

test.describe('MCP per-caller discovery in sessions', () => {
  test.beforeEach(async ({ page }) => {
    await page.clock.setFixedTime(NOW)
  })

  test('the sessions table says what each credential holds on its server', async ({ page }) => {
    await stubSessions(page)
    await page.goto('/workspace/mcp-sessions')

    const cell = (id: string) => page.getByTestId(`mcp-session-discovery-${id}`)
    await expect(cell('tok-alice')).toContainText('3 tools · 1 prompt · 2 resources')
    await expect(cell('tok-alice')).toContainText('discovered 4 min ago')
    await expect(cell('hdr-bot')).toContainText('No tools')
    await expect(cell('hdr-bot')).toContainText('discovered 2h ago')
    await expect(cell('hdr-priya')).toContainText('Not yet')
    await expect(cell('hdr-priya')).toContainText('on the next connect')
    await expect(cell('flow-jo')).toHaveText('Not yet')
    await expect(cell('tok-sam')).toHaveText('-')
    await expect(cell('hdr-sess')).toHaveText('-')
    await expect(cell('tok-wiki')).toHaveText('Shared')
  })

  test("a Per-Caller Discovery server's Callers tab shows each caller's discovery", async ({ page, mcpRegistryPage }) => {
    await stubSessions(page)
    await page.route(
      (url) => url.pathname === '/api/mcp/clients',
      (route) =>
        route.fulfill({
          json: {
            clients: [
              {
                config: {
                  client_id: 'client-notes', name: 'notes', connection_type: 'http',
                  connection_string: { value: 'http://upstream.invalid/mcp', ref: '' },
                  auth_type: 'per_user_oauth', per_user_discovery: true, tools_to_execute: ['*'], endpoint_slug: 'notes',
                },
                tools: [],
                state: 'healthy',
              },
            ],
            count: 1, total_count: 1, limit: 25, offset: 0,
          },
        }),
    )
    await mcpRegistryPage.goto()
    await mcpRegistryPage.viewClientDetails('notes')

    const sheet = mcpRegistryPage.viewSheet
    await sheet.getByTestId('mcpclient-view-tab-callers').click()
    const cell = (id: string) => sheet.getByTestId(`mcpclient-view-caller-discovery-${id}`)
    await expect(cell('tok-alice')).toContainText('3 tools · 1 prompt · 2 resources')
    await expect(cell('flow-jo')).toHaveText('Not yet')
    await expect(cell('tok-sam')).toHaveText('-')
    await mcpRegistryPage.closeDetailSheet()
  })
})
