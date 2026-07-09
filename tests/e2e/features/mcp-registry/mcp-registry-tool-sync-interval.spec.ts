import { expect, test } from '../../core/fixtures/base.fixture'
import { createHTTPClientData } from './mcp-registry.data'

/**
 * Regression coverage for the enable/disable Switch corrupting `tool_sync_interval`.
 *
 * Original bug: GET returns `tool_sync_interval` in nanoseconds, but the table's
 * enable/disable toggle resent that raw nanoseconds value into a PUT field the
 * backend parsed as minutes, overflowing int64 into a garbage negative duration.
 * The fix makes the toggle send only `{ disabled }` (the PUT handler has PATCH
 * semantics, so every other field is preserved) and guards the backend's
 * minutes/seconds-to-Duration conversions against overflow.
 */

interface MCPClientListItem {
  config: { client_id: string; name: string; tool_sync_interval?: number; disabled?: boolean }
}

async function getClientConfig(
  request: { get: (url: string) => Promise<{ json: () => Promise<unknown> }> },
  name: string
): Promise<MCPClientListItem['config']> {
  const res = await request.get('/api/mcp/clients')
  const body = (await res.json()) as { clients?: MCPClientListItem[] }
  const client = (body.clients ?? []).find((c) => c.config.name === name)
  if (!client) throw new Error(`MCP client "${name}" not found in GET /api/mcp/clients`)
  return client.config
}

const createdClients: string[] = []

test.describe('MCP Registry - tool_sync_interval unit corruption regression', () => {
  test.setTimeout(120000)

  test.beforeEach(async ({ mcpRegistryPage }) => {
    await mcpRegistryPage.goto()
  })

  test.afterEach(async ({ mcpRegistryPage }) => {
    const toClean = [...createdClients]
    createdClients.length = 0
    if (toClean.length > 0) {
      await mcpRegistryPage.cleanupMCPClients(toClean)
    }
  })

  test('enable/disable toggle preserves a non-zero tool_sync_interval override', async ({ mcpRegistryPage, page }) => {
    const clientData = createHTTPClientData({ name: `tool_sync_toggle_${Date.now()}` })

    const created = await mcpRegistryPage.createClient(clientData)
    expect(created).toBe(true)
    createdClients.push(clientData.name)

    const exists = await mcpRegistryPage.clientExists(clientData.name)
    expect(exists).toBe(true)

    // Set a per-client tool_sync_interval override the way the edit sheet would:
    // the update endpoint's request contract is minutes. 10 minutes = 600000000000ns,
    // the exact value that overflowed int64 when misread as minutes.
    const initialConfig = await getClientConfig(page.request, clientData.name)
    const putRes = await page.request.put(`/api/mcp/client/${initialConfig.client_id}`, {
      data: { tool_sync_interval: 10 },
    })
    expect(putRes.ok()).toBe(true)

    const afterOverride = await getClientConfig(page.request, clientData.name)
    const tenMinutesInNs = 10 * 60 * 1_000_000_000
    expect(afterOverride.tool_sync_interval).toBe(tenMinutesInNs)
    expect(afterOverride.disabled).toBe(false)

    // Toggle disabled via the actual UI switch — this is the code path that used to
    // resend the raw nanoseconds value and corrupt it.
    const row = mcpRegistryPage.getClientRow(clientData.name)
    const enabledSwitch = row.locator('button[role="switch"]').first()
    await expect(enabledSwitch).toBeVisible({ timeout: 10000 })

    const toggleResponsePromise = page.waitForResponse(
      (response) => response.url().includes(`/api/mcp/client/${initialConfig.client_id}`) && response.request().method() === 'PUT',
      { timeout: 15000 }
    )
    await enabledSwitch.click()
    const toggleResponse = await toggleResponsePromise
    expect(toggleResponse.ok()).toBe(true)

    const afterDisable = await getClientConfig(page.request, clientData.name)
    expect(afterDisable.disabled).toBe(true)
    // The regression: this used to become a large negative number after the toggle.
    expect(afterDisable.tool_sync_interval).toBe(tenMinutesInNs)

    // Toggle back on and confirm the value survives a second round trip too.
    const secondToggleResponsePromise = page.waitForResponse(
      (response) => response.url().includes(`/api/mcp/client/${initialConfig.client_id}`) && response.request().method() === 'PUT',
      { timeout: 15000 }
    )
    await enabledSwitch.click()
    const secondToggleResponse = await secondToggleResponsePromise
    expect(secondToggleResponse.ok()).toBe(true)

    const afterReEnable = await getClientConfig(page.request, clientData.name)
    expect(afterReEnable.disabled).toBe(false)
    expect(afterReEnable.tool_sync_interval).toBe(tenMinutesInNs)
  })
})
