import { expect, test } from '../../core/fixtures/base.fixture'
import {
    createCodeModeClientData,
    createHTTPClientData,
    createHeadersAuthClientData,
    createOAuthClientData,
    createPerUserOAuthClientData,
    createSSEClientData,
    createSTDIOClientData
} from './mcp-registry.data'

const hasSSEHeaders = Boolean(process.env.MCP_SSE_HEADERS)

async function completeOAuthFlow(page: { context: () => any; request: any }, flow: {
  authorize_url: string
  oauth_config_id: string
  complete_url?: string
  status_url?: string
}) {
  const popup = await page.context().newPage()
  await popup.goto(flow.authorize_url)
  await popup.locator('#user').fill('demo-user')
  await popup.getByRole('button', { name: /Sign in/i }).click()
  await popup.waitForLoadState('networkidle').catch(() => {})
  await popup.close().catch(() => {})

  const statusUrl = flow.status_url ?? `/api/oauth/config/${flow.oauth_config_id}/status`
  let authorized = false
  for (let i = 0; i < 30; i++) {
    const statusResponse = await page.request.get(statusUrl)
    if (!statusResponse.ok()) {
      await new Promise((resolve) => setTimeout(resolve, 500))
      continue
    }
    const statusBody = await statusResponse.json().catch(() => null)
    if (statusBody?.status === 'authorized') {
      authorized = true
      break
    }
    await new Promise((resolve) => setTimeout(resolve, 500))
  }

  expect(authorized).toBe(true)

  const completeUrl = flow.complete_url ?? `/api/mcp/client/${flow.oauth_config_id}/complete-oauth`
  const completeResponse = await page.request.post(completeUrl)
  expect(completeResponse.ok()).toBe(true)
}

// Track created clients for cleanup
const createdClients: string[] = []

test.describe('MCP Registry', () => {
  // MCP client creation can be slow (backend connects to MCP server); give tests room to complete
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

  test.describe('MCP Client Display', () => {
    test('should display MCP clients table', async ({ mcpRegistryPage }) => {
      await expect(mcpRegistryPage.table).toBeVisible()
    })

    test('should display create button', async ({ mcpRegistryPage }) => {
      await expect(mcpRegistryPage.createBtn).toBeVisible()
    })

    test('should show empty state or client list', async ({ mcpRegistryPage }) => {
      const count = await mcpRegistryPage.getClientCount()
      const isEmptyStateVisible = await mcpRegistryPage.isEmptyStateVisible()

      if (count === 0) {
        expect(isEmptyStateVisible).toBe(true)
      } else {
        expect(count).toBeGreaterThan(0)
        expect(isEmptyStateVisible).toBe(false)
      }
    })
  })

  test.describe('MCP Client Creation', () => {
    test('should open client creation sheet', async ({ mcpRegistryPage }) => {
      await mcpRegistryPage.createBtn.click()
      await expect(mcpRegistryPage.sheet).toBeVisible()
      await expect(mcpRegistryPage.nameInput).toBeVisible()

      // Cancel to clean up
      await mcpRegistryPage.cancelCreation()
    })

    test('should create basic HTTP client', async ({ mcpRegistryPage }) => {
      const clientData = createHTTPClientData()

      const created = await mcpRegistryPage.createClient(clientData)
      expect(created).toBe(true) // Client creation must succeed

      createdClients.push(clientData.name)
      const exists = await mcpRegistryPage.clientExists(clientData.name)
      expect(exists).toBe(true)

      // Verify connection type displayed correctly
      const connectionType = await mcpRegistryPage.getClientConnectionType(clientData.name)
      expect(connectionType).toBe('HTTP')
    })

    test('should create SSE client', async ({ mcpRegistryPage }) => {
      test.skip(!hasSSEHeaders, 'Requires MCP_SSE_HEADERS for authenticated SSE MCP endpoint')
      const clientData = createSSEClientData({
        name: `sse_test_${Date.now()}`,
      })

      const created = await mcpRegistryPage.createClient(clientData)
      expect(created).toBe(true) // Client creation must succeed

      createdClients.push(clientData.name)
      const exists = await mcpRegistryPage.clientExists(clientData.name)
      expect(exists).toBe(true)

      // Verify connection type displayed correctly
      const connectionType = await mcpRegistryPage.getClientConnectionType(clientData.name)
      expect(connectionType).toBe('SSE')
    })

    test('should create STDIO client with command', async ({ mcpRegistryPage }) => {
      const clientData = createSTDIOClientData()

      const created = await mcpRegistryPage.createClient(clientData)
      expect(created).toBe(true) // Client creation must succeed

      createdClients.push(clientData.name)
      const exists = await mcpRegistryPage.clientExists(clientData.name)
      expect(exists).toBe(true)
    })

    test('should create client with code mode enabled', async ({ mcpRegistryPage }) => {
      const clientData = createCodeModeClientData({
        name: `codemode_test_${Date.now()}`,
      })

      const created = await mcpRegistryPage.createClient(clientData)
      expect(created).toBe(true) // Client creation must succeed

      createdClients.push(clientData.name)
      const exists = await mcpRegistryPage.clientExists(clientData.name)
      expect(exists).toBe(true)
    })

    test('should cancel client creation', async ({ mcpRegistryPage }) => {
      await mcpRegistryPage.createBtn.click()
      await expect(mcpRegistryPage.sheet).toBeVisible()

      const testName = `cancelled_client_${Date.now()}`
      await mcpRegistryPage.nameInput.fill(testName)

      await mcpRegistryPage.cancelCreation()

      // Sheet should be closed
      await expect(mcpRegistryPage.sheet).not.toBeVisible()

      // Client should not exist
      const exists = await mcpRegistryPage.clientExists(testName)
      expect(exists).toBe(false)
    })
  })

  test.describe('MCP Server Connection Validation', () => {
    test('should connect to HTTP server and list tools', async ({ mcpRegistryPage }) => {
      const clientData = createHTTPClientData({
        name: `http_validation_${Date.now()}`,
      })

      const created = await mcpRegistryPage.createClient(clientData)
      expect(created).toBe(true)
      createdClients.push(clientData.name)

      // Wait a moment for connection to establish
      await mcpRegistryPage.page.waitForTimeout(2000)

      // Verify client shows connection status
      const status = await mcpRegistryPage.getClientStatus(clientData.name)
      expect(status).toBeTruthy()
      // Status could be connecting, connected, or disconnected depending on timing
      expect(['connected', 'disconnected', 'connecting', 'error']).toContain(status.toLowerCase())

      // Verify tools are loaded (http-no-ping-server has: echo, add, greet)
      await mcpRegistryPage.viewClientDetails(clientData.name)
      const toolsCount = await mcpRegistryPage.getToolsCount()
      expect(toolsCount).toBeGreaterThanOrEqual(3)

      await mcpRegistryPage.closeDetailSheet()
    })

    test('should connect to SSE server and list tools', async ({ mcpRegistryPage }) => {
      test.skip(!hasSSEHeaders, 'Requires MCP_SSE_HEADERS for authenticated SSE MCP endpoint')
      const clientData = createSSEClientData({
        name: `sse_validation_${Date.now()}`,
      })

      const created = await mcpRegistryPage.createClient(clientData)
      expect(created).toBe(true)
      createdClients.push(clientData.name)

      // Wait a moment for connection to establish
      await mcpRegistryPage.page.waitForTimeout(2000)

      // Verify tools are loaded
      await mcpRegistryPage.viewClientDetails(clientData.name)
      const toolsCount = await mcpRegistryPage.getToolsCount()
      expect(toolsCount).toBeGreaterThanOrEqual(3)

      await mcpRegistryPage.closeDetailSheet()
    })

    test('should connect to STDIO server and list tools', async ({ mcpRegistryPage }) => {
      const clientData = createSTDIOClientData({
        name: `stdio_validation_${Date.now()}`,
      })

      const created = await mcpRegistryPage.createClient(clientData)
      expect(created).toBe(true)
      createdClients.push(clientData.name)

      // Wait a moment for connection to establish
      await mcpRegistryPage.page.waitForTimeout(2000)

      // Verify tools from test-tools-server (echo, calculator, get_weather, delay, throw_error)
      await mcpRegistryPage.viewClientDetails(clientData.name)
      const toolsCount = await mcpRegistryPage.getToolsCount()
      expect(toolsCount).toBeGreaterThanOrEqual(5)

      await mcpRegistryPage.closeDetailSheet()
    })
  })

  test.describe('MCP Client Management', () => {
    test('should delete MCP client', async ({ mcpRegistryPage }) => {
      // Create a client first using HTTP (most reliable)
      const clientData = createHTTPClientData({
        name: `delete_test_${Date.now()}`,
      })

      const created = await mcpRegistryPage.createClient(clientData)
      expect(created).toBe(true) // Client creation must succeed for this test

      // Verify it exists
      let exists = await mcpRegistryPage.clientExists(clientData.name)
      expect(exists).toBe(true)

      // Delete it
      await mcpRegistryPage.deleteClient(clientData.name)

      // Verify it's gone
      exists = await mcpRegistryPage.clientExists(clientData.name)
      expect(exists).toBe(false)
    })

    test('should view client details', async ({ mcpRegistryPage }) => {
      // Create a client first
      const clientData = createHTTPClientData({
        name: `view_test_${Date.now()}`,
      })

      const created = await mcpRegistryPage.createClient(clientData)
      expect(created).toBe(true) // Client creation must succeed for this test
      createdClients.push(clientData.name)

      // View details
      await mcpRegistryPage.viewClientDetails(clientData.name)

      // Detail sheet should be visible
      await expect(mcpRegistryPage.detailSheet).toBeVisible()

      // Close the sheet
      await mcpRegistryPage.closeDetailSheet()
    })

    test('should close client details sheet', async ({ mcpRegistryPage }) => {
      // Create a client first
      const clientData = createHTTPClientData({
        name: `close_sheet_test_${Date.now()}`,
      })

      const created = await mcpRegistryPage.createClient(clientData)
      expect(created).toBe(true) // Client creation must succeed for this test
      createdClients.push(clientData.name)

      // Open details
      await mcpRegistryPage.viewClientDetails(clientData.name)
      await expect(mcpRegistryPage.detailSheet).toBeVisible()

      // Close it
      await mcpRegistryPage.closeDetailSheet()

      // Should be closed
      await expect(mcpRegistryPage.detailSheet).not.toBeVisible()
    })

    test('should reconnect MCP client', async ({ mcpRegistryPage }) => {
      // Create a client first
      const clientData = createHTTPClientData({
        name: `reconnect_test_${Date.now()}`,
      })

      const created = await mcpRegistryPage.createClient(clientData)
      expect(created).toBe(true) // Client creation must succeed for this test
      createdClients.push(clientData.name)

      // Reconnect - method waits for success toast
      await mcpRegistryPage.reconnectClient(clientData.name)

      // Verify client still exists and has a status (reconnect completed)
      const exists = await mcpRegistryPage.clientExists(clientData.name)
      expect(exists).toBe(true)
      const status = await mcpRegistryPage.getClientStatus(clientData.name)
      expect(status).toBeTruthy()
      expect(['connected', 'disconnected', 'connecting']).toContain(status.toLowerCase())
    })
  })

  test.describe('Client Status Display', () => {
    test('should display client connection status', async ({ mcpRegistryPage }) => {
      // Create a client first
      const clientData = createHTTPClientData({
        name: `status_test_${Date.now()}`,
      })

      const created = await mcpRegistryPage.createClient(clientData)
      expect(created).toBe(true) // Client creation must succeed for this test
      createdClients.push(clientData.name)

      // Get status
      const status = await mcpRegistryPage.getClientStatus(clientData.name)

      // Status should be one of the expected values
      expect(status).toBeTruthy()
      expect(['connected', 'disconnected', 'connecting', 'error']).toContain(status?.toLowerCase())
    })
  })

  test.describe('Form Validation', () => {
    test('should validate name format', async ({ mcpRegistryPage }) => {
      await mcpRegistryPage.createBtn.click()
      await expect(mcpRegistryPage.sheet).toBeVisible()

      // Try invalid name with hyphens (not allowed)
      await mcpRegistryPage.nameInput.fill('invalid-name-with-hyphens')

      // Fill connection URL to satisfy other validation
      await mcpRegistryPage.connectionUrlInput.fill('http://localhost:3001')

      // Complex forms keep the action enabled and show exact inline errors on submit.
      await mcpRegistryPage.saveBtn.click()
      await expect(
        mcpRegistryPage.page.getByText('Server name can only contain letters, numbers, and underscores')
      ).toBeVisible()

      await mcpRegistryPage.cancelCreation()
    })

    test('should require connection URL for HTTP clients', async ({ mcpRegistryPage }) => {
      await mcpRegistryPage.createBtn.click()
      await expect(mcpRegistryPage.sheet).toBeVisible()

      // Fill valid name
      await mcpRegistryPage.nameInput.fill(`valid_name_${Date.now()}`)

      // Leave connection URL empty, submit, and assert the highlighted requirement.
      await mcpRegistryPage.saveBtn.click()
      await expect(mcpRegistryPage.page.getByText('Connection URL is required')).toBeVisible()

      await mcpRegistryPage.cancelCreation()
    })
  })

  test.describe('MCP Header Authentication', () => {
    test('should display header fields when headers auth type is selected', async ({ mcpRegistryPage }) => {
      await mcpRegistryPage.createBtn.click()
      await expect(mcpRegistryPage.sheet).toBeVisible()

      await mcpRegistryPage.selectAuthType('headers')

      const headersTable = mcpRegistryPage.page.locator('[data-testid="mcp-headers-table"]')
      await expect(headersTable).toBeVisible()

      await mcpRegistryPage.cancelCreation()
    })

    test('should create MCP client with header auth and connect to auth-demo-server', async ({ mcpRegistryPage }) => {
      const clientData = createHeadersAuthClientData()
      createdClients.push(clientData.name)

      const created = await mcpRegistryPage.createClient(clientData)
      expect(created).toBe(true)

      const exists = await mcpRegistryPage.clientExists(clientData.name)
      expect(exists).toBe(true)

      // Server requires X-API-Key — should connect and expose tools (public_info, secret_data)
      await mcpRegistryPage.viewClientDetails(clientData.name)
      const toolsCount = await mcpRegistryPage.getToolsCount()
      expect(toolsCount).toBeGreaterThanOrEqual(2)

      await mcpRegistryPage.closeDetailSheet()
    })
  })

  test.describe('MCP OAuth 2.0', () => {
    test('should display OAuth fields when OAuth 2.0 auth type is selected', async ({ mcpRegistryPage }) => {
      await mcpRegistryPage.createBtn.click()
      await expect(mcpRegistryPage.sheet).toBeVisible()

      await mcpRegistryPage.selectAuthType('oauth')
      await mcpRegistryPage.expandOAuthAdvancedIfCollapsed()

      // All fields are optional — auto-discovered from server metadata
      await expect(mcpRegistryPage.oauthClientIdInput).toBeVisible()
      await expect(mcpRegistryPage.oauthClientSecretInput).toBeVisible()
      await expect(mcpRegistryPage.oauthAuthorizeUrlInput).toBeVisible()
      await expect(mcpRegistryPage.oauthTokenUrlInput).toBeVisible()

      await mcpRegistryPage.cancelCreation()
    })

    test('should create OAuth 2.0 client and complete authorization flow', async ({ mcpRegistryPage }) => {
      const clientData = createOAuthClientData()
      createdClients.push(clientData.name)

      const flow = await mcpRegistryPage.createOAuthClient(clientData)
      await completeOAuthFlow(mcpRegistryPage.page, flow)
      await mcpRegistryPage.goto()

      // Client should now be connected and visible in table
      const exists = await mcpRegistryPage.clientExists(clientData.name)
      expect(exists).toBe(true)
    })
  })

  test.describe('MCP Per-User OAuth 2.0', () => {
    test('should display OAuth fields when Per-User OAuth 2.0 auth type is selected', async ({ mcpRegistryPage }) => {
      await mcpRegistryPage.createBtn.click()
      await expect(mcpRegistryPage.sheet).toBeVisible()

      await mcpRegistryPage.selectAuthType('per_user_oauth')
      await mcpRegistryPage.expandOAuthAdvancedIfCollapsed()

      await expect(mcpRegistryPage.oauthClientIdInput).toBeVisible()
      await expect(mcpRegistryPage.oauthClientSecretInput).toBeVisible()
      await expect(mcpRegistryPage.oauthAuthorizeUrlInput).toBeVisible()
      await expect(mcpRegistryPage.oauthTokenUrlInput).toBeVisible()

      await mcpRegistryPage.cancelCreation()
    })

    test('should create Per-User OAuth 2.0 client and complete authorization flow', async ({ mcpRegistryPage }) => {
      const clientData = createPerUserOAuthClientData()
      createdClients.push(clientData.name)

      const flow = await mcpRegistryPage.createOAuthClient(clientData)
      await completeOAuthFlow(mcpRegistryPage.page, flow)
      await mcpRegistryPage.goto()

      // Client should be visible in table
      const exists = await mcpRegistryPage.clientExists(clientData.name)
      expect(exists).toBe(true)
    })
  })
})