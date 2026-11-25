import type { Page } from '@playwright/test'
import { expect, test } from '../../core/fixtures/base.fixture'

// BF-2990: with inference auth enforced, a caller with no user identity of their
// own - the local admin, or anyone on a deployment without an identity provider -
// could not use Warp, because the dashboard has no way to attach a virtual key.
// Warp's settings now name a virtual key its model calls present for that caller.
// /api/warp/config, the key list and the core config are answered in this page's
// view only and a save is captured, never stored, since they are shared server state.

const warpKey = { id: 'vk-warp-e2e', name: 'Warp e2e key', description: 'Used by Warp' }

const configuredWarp = {
  configured: true,
  enabled: true,
  provider: 'openai',
  model: 'gpt-5.6-luna',
  virtual_key_id: undefined as string | undefined,
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
  management_writes_available: false,
  bifrost_mcp_available: true,
  bifrost_mcp_enabled: false,
}

// Returns the bodies of the saves the page sent.
async function mockWarp(page: Page, overrides: Partial<typeof configuredWarp> = {}, enforceAuth = true): Promise<{ virtual_key_id?: string }[]> {
  const saved: { virtual_key_id?: string }[] = []
  await page.route('**/api/feature-flags', async (route) => {
    if (route.request().method() !== 'GET') return route.continue()
    const response = await route.fetch()
    const body = (await response.json()) as { flags: { id: string; enabled: boolean }[] }
    body.flags = body.flags.map((flag) => (flag.id === 'warp' ? { ...flag, enabled: true } : flag))
    await route.fulfill({ response, json: body })
  })
  await page.addInitScript(() => {
    localStorage.setItem('bifrost.warpBifrostMCPNoticeSeen', '1')
    localStorage.setItem('bifrost.warpBifrostMCPNoticeLaterCount', '5')
  })
  await page.route(/\/api\/config(\?.*)?$/, async (route) => {
    if (route.request().method() !== 'GET') return route.continue()
    const response = await route.fetch()
    const body = (await response.json()) as { client_config?: Record<string, unknown> }
    body.client_config = { ...(body.client_config ?? {}), enforce_auth_on_inference: enforceAuth }
    await route.fulfill({ response, json: body })
  })
  await page.route(/\/api\/governance\/virtual-keys(\?.*)?$/, async (route) => {
    if (route.request().method() !== 'GET') return route.continue()
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ virtual_keys: [warpKey], total_count: 1 }) })
  })
  await page.route(`**/api/governance/virtual-keys/${warpKey.id}`, async (route) => {
    if (route.request().method() !== 'GET') return route.continue()
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ virtual_key: warpKey }) })
  })
  let current = { ...configuredWarp, ...overrides }
  await page.route('**/api/warp/config', async (route) => {
    if (route.request().method() === 'PUT') {
      const body = route.request().postDataJSON() as { virtual_key_id?: string }
      saved.push(body)
      current = { ...current, virtual_key_id: body.virtual_key_id || undefined }
    }
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(current) })
  })
  return saved
}

test.describe("Warp's virtual key", () => {
  test('with inference auth enforced, choosing a key saves it and clearing it saves none', async ({ page }) => {
    const saved = await mockWarp(page)
    await page.goto('/workspace/config/warp')

    const note = page.getByTestId('warp-virtual-key-enforced-note')
    await expect(note).toBeVisible()

    // By its accessible name: the "Virtual key" label must name the combobox.
    const field = page.getByTestId('warp-virtual-key-select')
    await page.getByRole('combobox', { name: 'Virtual key' }).click()
    await page.getByRole('option', { name: new RegExp(warpKey.name) }).click()
    await expect(field).toContainText(warpKey.name)
    await expect(note).toBeHidden()

    await page.getByTestId('warp-save-btn').click()
    await expect.poll(() => saved.length).toBe(1)
    expect(saved[0].virtual_key_id).toBe(warpKey.id)

    // Saved: after a reload the key shows by name, not by id, and clearing it saves "".
    await page.reload()
    await expect(page.getByTestId('warp-virtual-key-select')).toContainText(warpKey.name)
    await page.getByTestId('warp-virtual-key-clear-btn').click()
    await expect(page.getByTestId('warp-virtual-key-enforced-note')).toBeVisible()
    await page.getByTestId('warp-save-btn').click()
    await expect.poll(() => saved.length).toBe(2)
    expect(saved[1].virtual_key_id).toBe('')
  })

  test('without enforced inference auth there is no note, and a save keeps an unset key unset', async ({ page }) => {
    const saved = await mockWarp(page, {}, false)
    // Awaited before the hidden check: until core config arrives the page reads inference auth
    // as off, so a note that appears only once it loads would otherwise slip past.
    const coreConfig = page.waitForResponse((response) => /\/api\/config(\?|$)/.test(response.url()) && response.request().method() === 'GET')
    await page.goto('/workspace/config/warp')
    await coreConfig
    await page.waitForLoadState('networkidle')

    await expect(page.getByTestId('warp-virtual-key-select')).toBeVisible()
    await expect(page.getByTestId('warp-virtual-key-enforced-note')).toBeHidden()
    await expect(page.getByTestId('warp-virtual-key-clear-btn')).toBeHidden()

    await page.getByTestId('warp-max-iterations-input').fill('6')
    await page.getByTestId('warp-save-btn').click()
    await expect.poll(() => saved.length).toBe(1)
    expect(saved[0].virtual_key_id).toBe('')
  })
})
