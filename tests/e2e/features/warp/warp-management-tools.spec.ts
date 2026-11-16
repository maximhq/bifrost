import type { Page } from '@playwright/test'
import { expect, test } from '../../core/fixtures/base.fixture'

// Warp reads how Bifrost is configured through the Bifrost MCP, and makes only
// the kinds of change its settings allow: reads are always on, creates,
// updates, deletes and other actions are off until an admin turns them on.
// Each kind adds its tools to every Warp request, so the section shows what each
// costs and warns once any is on. bifrost_mcp_allow_writes is the ceiling: with
// it off the switches cannot be turned on. /api/warp/config is answered in this
// page's view only and a save is captured, never stored, since it is shared
// server state.

const sizes = [
  { kind: 'read', tools: 98, estimated_tokens: 29500 },
  { kind: 'create', tools: 20, estimated_tokens: 50700 },
  { kind: 'update', tools: 36, estimated_tokens: 86700 },
  { kind: 'delete', tools: 41, estimated_tokens: 5600 },
  { kind: 'action', tools: 44, estimated_tokens: 53700 },
]

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
  topic_discovery_min_requests: 100,
  vector_store_connected: true,
  management_writes: [] as string[],
  management_writes_available: true,
  management_tool_sizes: sizes as object[] | undefined,
  bifrost_mcp_available: true,
}

// Returns the bodies of the saves the page sent.
async function mockWarp(page: Page, overrides: Partial<typeof configuredWarp> = {}): Promise<{ management_writes?: string[] }[]> {
  const saved: { management_writes?: string[] }[] = []
  await page.route('**/api/feature-flags', async (route) => {
    if (route.request().method() !== 'GET') return route.continue()
    const response = await route.fetch()
    const body = (await response.json()) as { flags: { id: string; enabled: boolean }[] }
    body.flags = body.flags.map((flag) => (flag.id === 'warp' ? { ...flag, enabled: true } : flag))
    await route.fulfill({ response, json: body })
  })
  // The Bifrost MCP is loaded exactly when the tool sizes are known, as the server reports it.
  let current = { ...configuredWarp, ...overrides }
  current = { ...current, bifrost_mcp_available: !!current.management_tool_sizes }
  // The upgrade notice would open over the form: acknowledged, and past its reminder cap so a setup notice
  // (no Bifrost MCP) stays a banner. Its own behaviour is pinned in warp-mcp-notice.spec.ts.
  await page.addInitScript(() => {
    localStorage.setItem('bifrost.warpBifrostMCPNoticeSeen', '1')
    localStorage.setItem('bifrost.warpBifrostMCPNoticeLaterCount', '5')
  })
  await page.route('**/api/warp/config', async (route) => {
    if (route.request().method() === 'PUT') {
      const body = route.request().postDataJSON() as { management_writes?: string[] }
      saved.push(body)
      current = { ...current, management_writes: body.management_writes ?? current.management_writes }
    }
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(current) })
  })
  return saved
}

test.describe('Warp configuration tools', () => {
  test('reads only by default; turning kinds on warns about context and saves them', async ({ page }) => {
    const saved = await mockWarp(page)
    await page.goto('/workspace/config/warp')

    const section = page.getByTestId('warp-management-tools-section')
    await expect(section).toBeVisible()
    for (const kind of ['create', 'update', 'delete', 'action']) {
      await expect(page.getByTestId(`warp-management-write-${kind}-switch`)).not.toBeChecked()
    }
    const total = page.getByTestId('warp-management-tools-total')
    await expect(total).toHaveAttribute('data-writes', '0')

    await page.getByTestId('warp-management-write-update-switch').click()
    await page.getByTestId('warp-management-write-create-switch').click()
    await expect(total).toHaveAttribute('data-writes', '2')

    await page.getByTestId('warp-save-btn').click()
    await expect.poll(() => saved.length).toBe(1)
    expect(saved[0].management_writes).toEqual(['create', 'update'])

    // Saved: the switches reflect it after a reload, and turning one off saves the rest.
    await page.reload()
    await expect(page.getByTestId('warp-management-write-create-switch')).toBeChecked()
    await page.getByTestId('warp-management-write-create-switch').click()
    await page.getByTestId('warp-save-btn').click()
    await expect.poll(() => saved.length).toBe(2)
    expect(saved[1].management_writes).toEqual(['update'])
  })

  test('with Bifrost MCP writes off the kinds cannot be turned on, and the page says where to allow them', async ({ page }) => {
    await mockWarp(page, { management_writes_available: false, management_tool_sizes: sizes.filter((size) => size.kind === 'read') })
    await page.goto('/workspace/config/warp')

    for (const kind of ['create', 'update', 'delete', 'action']) {
      await expect(page.getByTestId(`warp-management-write-${kind}-switch`)).toBeDisabled()
    }
    const ceiling = page.getByTestId('warp-management-writes-ceiling')
    await expect(ceiling).toBeVisible()
    await expect(ceiling.getByRole('link')).toHaveAttribute('href', /\/workspace\/config\/security$/)
  })

  test('with Bifrost MCP writes off a kind that is on can still be turned off, but not back on', async ({ page }) => {
    const saved = await mockWarp(page, { management_writes_available: false, management_writes: ['update'] })
    await page.goto('/workspace/config/warp')

    const update = page.getByTestId('warp-management-write-update-switch')
    await expect(update).toBeChecked()
    await expect(update).toBeEnabled()
    for (const kind of ['create', 'delete', 'action']) {
      await expect(page.getByTestId(`warp-management-write-${kind}-switch`)).toBeDisabled()
    }

    await update.click()
    await expect(update).not.toBeChecked()
    await expect(update).toBeDisabled()
    await page.getByTestId('warp-save-btn').click()
    await expect.poll(() => saved.length).toBe(1)
    expect(saved[0].management_writes).toEqual([])
  })

  test('says the kinds can be chosen once the Bifrost MCP is loaded', async ({ page }) => {
    await mockWarp(page, { management_writes_available: false, management_tool_sizes: undefined })
    await page.goto('/workspace/config/warp')

    await expect(page.getByTestId('warp-management-tools-not-loaded')).toBeVisible()
    await expect(page.getByTestId('warp-management-tools-total')).toHaveCount(0)
    await expect(page.getByTestId('warp-management-write-create-switch')).toBeDisabled()
  })
})
