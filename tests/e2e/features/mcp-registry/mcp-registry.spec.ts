import { expect, test } from '../../core/fixtures/base.fixture'
import {
    createCodeModeClientData,
    createHTTPClientData,
    createSSEClientData,
    createSTDIOClientData
} from './mcp-registry.data'

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
      expect(['connected', 'disconnected', 'connecting', 'error']).toContain(status?.toLowerCase())
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
    test('should require name for client', async ({ mcpRegistryPage }) => {
      await mcpRegistryPage.createBtn.click()
      await expect(mcpRegistryPage.sheet).toBeVisible()

      // Clear name field (should be empty by default)
      await mcpRegistryPage.nameInput.clear()

      // Save button should be disabled when name is empty
      const saveBtn = mcpRegistryPage.saveBtn
      await expect(saveBtn).toBeDisabled()

      await mcpRegistryPage.cancelCreation()
    })

    test('should validate name format', async ({ mcpRegistryPage }) => {
      await mcpRegistryPage.createBtn.click()
      await expect(mcpRegistryPage.sheet).toBeVisible()

      // Try invalid name with hyphens (not allowed)
      await mcpRegistryPage.nameInput.fill('invalid-name-with-hyphens')

      // Fill connection URL to satisfy other validation
      await mcpRegistryPage.connectionUrlInput.fill('http://localhost:3001')

      // Save button should be disabled due to validation error
      const saveBtn = mcpRegistryPage.saveBtn
      await expect(saveBtn).toBeDisabled()

      await mcpRegistryPage.cancelCreation()
    })

    test('should require connection URL for HTTP clients', async ({ mcpRegistryPage }) => {
      await mcpRegistryPage.createBtn.click()
      await expect(mcpRegistryPage.sheet).toBeVisible()

      // Fill valid name
      await mcpRegistryPage.nameInput.fill(`valid_name_${Date.now()}`)

      // Leave connection URL empty - save should be disabled
      const saveBtn = mcpRegistryPage.saveBtn
      await expect(saveBtn).toBeDisabled()

      await mcpRegistryPage.cancelCreation()
    })
  })
})
