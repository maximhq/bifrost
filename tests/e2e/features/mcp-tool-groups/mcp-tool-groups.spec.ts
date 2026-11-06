import type { APIRequestContext, Page } from '@playwright/test'
import { expect, test } from '../../core/fixtures/base.fixture'

// MCP Tool Groups is superseded by Virtual MCPs; the old path only redirects.
test.describe('MCP Tool Groups', () => {
  test.beforeEach(async ({ mcpToolGroupsPage }) => {
    await mcpToolGroupsPage.goto()
  })

  test('should redirect the old MCP tool groups path to Virtual MCPs', async ({ mcpToolGroupsPage }) => {
    await expect(mcpToolGroupsPage.page).toHaveURL(/\/workspace\/virtual-mcps/)
  })
})

const OPENAPI_SPEC = JSON.stringify({
  openapi: '3.0.3',
  info: { title: 'E2E Virtual MCP lookups', version: '1.0.0' },
  servers: [{ url: 'https://api.example.com' }],
  paths: {
    '/a': { get: { operationId: 'listA', responses: { '200': { description: 'ok' } } } },
    '/b': { get: { operationId: 'getB', responses: { '200': { description: 'ok' } } } },
  },
})

/**
 * The servers and keys a Virtual MCP names can sit past the first page of their lists. Seeding over
 * 100 of each would be slow, so the seeded rows are real and the unfiltered lists answer without them.
 */
test.describe('Virtual MCP lookups past the first page', () => {
  type Seeded = { clientId: string; clientName: string; vkId: string; vkName: string; vmcpId: number; vmcpName: string; toolCount: number }
  const seeded: Seeded[] = []

  async function seed(request: APIRequestContext): Promise<Seeded> {
    const stamp = `${Date.now()}_${Math.random().toString(36).slice(2, 8)}`
    const clientId = `e2e-vmcp-lookup-${stamp.replace('_', '-')}`
    const clientName = `e2e_vmcp_lookup_${stamp}`
    const client = await request.post('/api/mcp/client', {
      data: {
        client_id: clientId,
        name: clientName,
        connection_type: 'openapi',
        auth_type: 'none',
        tools_to_execute: ['listA', 'getB'],
        openapi_config: { spec: OPENAPI_SPEC, base_url: 'https://api.example.com' },
      },
    })
    expect(client.status(), await client.text()).toBe(200)

    const vkName = `e2e-vmcp-lookup-key-${stamp}`
    const vk = await request.post('/api/governance/virtual-keys', { data: { name: vkName } })
    expect(vk.ok(), await vk.text()).toBeTruthy()
    const vkId: string = (await vk.json()).virtual_key.id

    const vmcpName = `e2e_vmcp_lookup_vmcp_${stamp}`
    const vmcp = await request.post('/api/mcp/virtual-mcps', {
      data: { name: vmcpName, tools: [{ mcp_client_id: clientId, tool_names: ['*'] }] },
    })
    expect(vmcp.status(), await vmcp.text()).toBe(201)
    const vmcpId: number = (await vmcp.json()).virtual_mcp.id

    const attached = await request.post(`/api/mcp/virtual-mcps/${vmcpId}/virtual-keys/${vkId}`)
    expect(attached.ok(), await attached.text()).toBeTruthy()

    const read = await request.get(`/api/mcp/clients?server=${clientId}&limit=1`)
    expect(read.status(), await read.text()).toBe(200)
    const toolCount: number = (await read.json()).clients[0].tools.length
    expect(toolCount).toBeGreaterThan(0)

    const row = { clientId, clientName, vkId, vkName, vmcpId, vmcpName, toolCount }
    seeded.push(row)
    return row
  }

  /** The first pages of the lists, as a deployment with more than a page of each returns them. */
  async function capLists(page: Page) {
    await page.route(
      (url) => url.pathname.endsWith('/api/mcp/clients') && !url.searchParams.has('server') && !url.searchParams.has('search'),
      (route) => route.fulfill({ json: { clients: [], count: 0, total_count: 0, limit: 100, offset: 0 } }),
    )
    await page.route(
      (url) => url.pathname.endsWith('/api/governance/virtual-keys'),
      (route) => route.fulfill({ json: { virtual_keys: [], count: 0, total_count: 0, limit: 100, offset: 0 } }),
    )
  }

  test.afterEach(async ({ request }) => {
    for (const row of seeded.splice(0)) {
      await request.delete(`/api/mcp/virtual-mcps/${row.vmcpId}`).catch(() => undefined)
      await request.delete(`/api/governance/virtual-keys/${row.vkId}`).catch(() => undefined)
      await request.delete(`/api/mcp/client/${row.clientId}`).catch(() => undefined)
    }
  })

  test('the table names the key and counts the tools of a server past the first page', async ({ page, request }) => {
    const row = await seed(request)
    await capLists(page)
    await page.goto(`/workspace/virtual-mcps?q=${row.vmcpName}`)
    await expect(page.getByTestId(`virtual-mcp-included-tools-${row.vmcpId}`)).toHaveText(String(row.toolCount))
    await expect(page.getByTestId(`virtual-mcp-assigned-${row.vmcpId}`)).toContainText(row.vkName)
  })

  test('the table holds the tool count while it loads and dashes it when the read fails', async ({ page, request }) => {
    const row = await seed(request)
    await capLists(page)
    let release = () => {}
    const held = new Promise<void>((resolve) => {
      release = resolve
    })
    await page.route(
      (url) => url.pathname.endsWith('/api/mcp/clients') && url.searchParams.has('server'),
      async (route) => {
        await held
        await route.fulfill({ status: 500, json: { error: { message: 'clients unavailable' } } })
      },
    )
    await page.goto(`/workspace/virtual-mcps?q=${row.vmcpName}`)
    const cell = page.getByTestId(`virtual-mcp-included-tools-${row.vmcpId}`)
    await expect(cell).toHaveText('…')
    release()
    await expect(cell).toHaveText('—')
  })

  test('the table counts a deleted server as zero and shows a deleted key by id', async ({ page, request }) => {
    const row = await seed(request)
    await capLists(page)
    await page.route(
      (url) => url.pathname.endsWith('/api/mcp/clients') && url.searchParams.has('server'),
      (route) => route.fulfill({ json: { clients: [], count: 0, total_count: 0, limit: 1, offset: 0 } }),
    )
    await page.route(
      (url) => url.pathname.endsWith(`/api/governance/virtual-keys/${row.vkId}`),
      (route) => route.fulfill({ status: 404, json: { error: { message: 'not found' } } }),
    )
    await page.goto(`/workspace/virtual-mcps?q=${row.vmcpName}`)
    await expect(page.getByTestId(`virtual-mcp-included-tools-${row.vmcpId}`)).toHaveText('0')
    await expect(page.getByTestId(`virtual-mcp-assigned-${row.vmcpId}`)).toContainText(row.vkId)
  })

  test('the access tab names an assigned key past the first page and can remove it', async ({ page, request }) => {
    const row = await seed(request)
    await capLists(page)
    await page.goto(`/workspace/virtual-mcps?q=${row.vmcpName}`)
    await page.getByRole('row').filter({ hasText: row.vmcpName }).getByRole('button', { name: 'Virtual MCP actions' }).click()
    await page.getByRole('menuitem', { name: 'Edit' }).click()
    await page.getByTestId('virtual-mcp-tab-access').click()
    await expect(page.getByRole('dialog').getByText(row.vkName, { exact: true })).toBeVisible()
    const remove = page.getByTestId(`virtual-mcp-detach-vk-${row.vkId}`)
    await remove.click()
    await expect(remove).toHaveCount(0)
  })

  test('the wizard review names a server past the first page', async ({ page, request }) => {
    const row = await seed(request)
    await capLists(page)
    await page.goto('/workspace/virtual-mcps')
    await page.getByTestId('virtual-mcp-create-btn').click()
    await page.getByTestId('virtual-mcp-name-input').fill(`e2e_vmcp_lookup_review_${Date.now()}`)
    const next = page.getByTestId('virtual-mcp-wizard-next')
    await next.click()
    // A server past the first page is found by searching for it, which the capped list still answers.
    await page.getByText('Select an MCP server to add').click()
    await page.keyboard.type(row.clientName)
    await page.getByRole('option', { name: row.clientName }).click()
    await next.click()
    await next.click()
    await expect(page.getByTestId('virtual-mcp-review-endpoint')).toBeVisible()
    await expect(page.getByText(row.clientName, { exact: true })).toBeVisible()
  })
})
