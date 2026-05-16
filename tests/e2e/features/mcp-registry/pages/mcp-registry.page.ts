import { Page, Locator, expect } from '@playwright/test'
import { BasePage } from '../../../core/pages/base.page'
import { waitForNetworkIdle } from '../../../core/utils/test-helpers'

/**
 * Connection types supported by MCP clients
 */
export type MCPConnectionType = 'http' | 'sse' | 'stdio'

/**
 * Authentication types for HTTP/SSE connections
 */
export type MCPAuthType = 'none' | 'headers' | 'oauth'

/** Header value shape used by API (value / env_var / from_env) */
export type EnvVarLike = { value: string; env_var?: string; from_env?: boolean }

/**
 * MCP Client configuration
 */
export interface MCPClientConfig {
  name: string
  connectionType?: MCPConnectionType
  connectionUrl?: string
  authType?: MCPAuthType
  /** Headers for auth_type 'headers'. API shape: Record<string, EnvVarLike> */
  headers?: Record<string, EnvVarLike | string>
  isCodeMode?: boolean
  isPingAvailable?: boolean
  // STDIO specific
  command?: string
  args?: string
  envs?: string
  // OAuth specific
  oauthClientId?: string
  oauthClientSecret?: string
  oauthAuthorizeUrl?: string
  oauthTokenUrl?: string
  oauthScopes?: string
}

/**
 * Page object for the MCP Registry page
 */
export class MCPRegistryPage extends BasePage {
  readonly table: Locator
  readonly createBtn: Locator
  readonly sheet: Locator
  readonly detailSheet: Locator
  readonly nameInput: Locator
  readonly saveBtn: Locator
  readonly cancelBtn: Locator
  readonly connectionTypeSelect: Locator
  readonly authTypeSelect: Locator
  readonly connectionUrlInput: Locator
  readonly codeModeSwitch: Locator
  readonly pingAvailableSwitch: Locator
  // STDIO inputs
  readonly commandInput: Locator
  readonly argsInput: Locator
  readonly envsInput: Locator
  // OAuth inputs
  readonly oauthClientIdInput: Locator
  readonly oauthClientSecretInput: Locator
  readonly oauthAuthorizeUrlInput: Locator
  readonly oauthTokenUrlInput: Locator
  readonly oauthScopesInput: Locator

  constructor(page: Page) {
    super(page)
    this.table = page.locator('[data-testid="mcp-clients-table"]').or(page.locator('table'))
    this.createBtn = page.locator('[data-testid="create-mcp-client-btn"]').or(
      page.getByRole('button', { name: /New MCP Server/i }).or(page.getByRole('button', { name: /Add/i }))
    )
    this.sheet = page.locator('[role="dialog"]')
    this.detailSheet = page.locator('[role="dialog"]')
    this.nameInput = page.locator('[data-testid="client-name-input"]').or(
      this.sheet.getByLabel(/Name/i).first()
    )
    this.saveBtn = page.locator('[data-testid="save-client-btn"]').or(
      this.sheet.getByRole('button', { name: /Create/i }).or(
        this.sheet.getByRole('button', { name: /Save/i })
      )
    )
    this.cancelBtn = page.locator('[data-testid="cancel-client-btn"]').or(
      this.sheet.getByRole('button', { name: /Cancel/i })
    )

    // Connection type and auth
    this.connectionTypeSelect = page.locator('[data-testid="connection-type-select"]')
    this.authTypeSelect = page.locator('[data-testid="auth-type-select"]')
    // Use placeholder as primary selector for EnvVarInput (more reliable)
    this.connectionUrlInput = this.sheet.getByPlaceholder(/http:\/\/your-mcp-server/i).or(
      page.locator('[data-testid="connection-url-input"]')
    )

    // Switches (Radix UI switches)
    this.codeModeSwitch = page.locator('[data-testid="code-mode-switch"]')
    this.pingAvailableSwitch = this.sheet.locator('#ping-available')

    // STDIO inputs
    this.commandInput = page.locator('[data-testid="stdio-command-input"]')
    this.argsInput = page.locator('[data-testid="stdio-args-input"]')
    this.envsInput = page.locator('[data-testid="stdio-envs-input"]')

    // OAuth inputs
    this.oauthClientIdInput = this.sheet.getByPlaceholder(/your-client-id/i)
    this.oauthClientSecretInput = this.sheet.getByPlaceholder(/your-client-secret/i)
    this.oauthAuthorizeUrlInput = this.sheet.getByPlaceholder(/oauth\/authorize/i)
    this.oauthTokenUrlInput = this.sheet.getByPlaceholder(/oauth\/token/i)
    this.oauthScopesInput = this.sheet.getByPlaceholder(/read, write, admin/i)
  }

  async goto(): Promise<void> {
    await this.page.goto('/workspace/mcp-registry')
    await waitForNetworkIdle(this.page)
    // Wait for table to be visible
    await this.table.waitFor({ state: 'visible', timeout: 10000 }).catch(() => {})
  }

  /** Get the table row for a client by name. Scoped to tbody so the header row is never matched; first() for stable single-row target. */
  getClientRow(name: string): Locator {
    return this.table.locator('tbody tr').filter({ hasText: name }).first()
  }

  async clientExists(name: string): Promise<boolean> {
    await this.page.waitForTimeout(500) // Brief wait for UI update
    return (await this.getClientRow(name).count()) > 0
  }

  /**
   * Poll until the client row appears in the table or timeout.
   * Used as a fallback success signal when the create form doesn't close (e.g. SSE/stdio).
   */
  async waitForClientInTable(name: string, timeoutMs: number): Promise<boolean> {
    const deadline = Date.now() + timeoutMs
    while (Date.now() < deadline) {
      if ((await this.getClientRow(name).count()) > 0) return true
      await this.page.waitForTimeout(500)
    }
    return false
  }

  async getClientCount(): Promise<number> {
    // Exclude header row
    const rows = this.table.locator('tbody tr')
    return await rows.count()
  }

  /**
   * Select connection type from dropdown
   */
  async selectConnectionType(type: MCPConnectionType): Promise<void> {
    // Click the connection type select trigger
    const selectTrigger = this.page.locator('[data-testid="connection-type-select"]')
    await expect(selectTrigger).toBeVisible({ timeout: 5000 })
    await selectTrigger.click()

    // Wait for dropdown to open and select the option by data-testid
    const optionTestId = `connection-type-${type}`
    const option = this.page.locator(`[data-testid="${optionTestId}"]`)
    await expect(option).toBeVisible({ timeout: 5000 })
    await option.click()

    // Wait for dropdown to close
    await expect(option).not.toBeVisible({ timeout: 3000 }).catch(() => {})
  }

  /**
   * Select authentication type from dropdown
   */
  async selectAuthType(type: MCPAuthType): Promise<void> {
    const selectTrigger = this.page.locator('[data-testid="auth-type-select"]')
    await expect(selectTrigger).toBeVisible({ timeout: 5000 })
    await selectTrigger.click()

    // Select the option by data-testid
    const optionTestId = `auth-type-${type}`
    const option = this.page.locator(`[data-testid="${optionTestId}"]`)
    await expect(option).toBeVisible({ timeout: 5000 })
    await option.click()

    // Wait for dropdown to close
    await expect(option).not.toBeVisible({ timeout: 3000 }).catch(() => {})
  }

  /**
   * Fill the MCP client form with configuration (doesn't submit)
   */
  async fillClientForm(config: MCPClientConfig): Promise<void> {
    // Fill name
    await this.nameInput.fill(config.name)

    // Select connection type if specified
    if (config.connectionType) {
      await this.selectConnectionType(config.connectionType)
      // Wait for the form to update after connection type change
      await this.page.waitForTimeout(500)
    }

    // Toggle code mode if specified (Radix Switch uses data-state="checked"/"unchecked")
    if (config.isCodeMode !== undefined) {
      await expect(this.codeModeSwitch).toBeVisible({ timeout: 5000 })
      const dataState = await this.codeModeSwitch.getAttribute('data-state')
      const currentState = dataState === 'checked'
      if (currentState !== config.isCodeMode) {
        await this.codeModeSwitch.click()
        // Wait for state to change
        const expectedState = config.isCodeMode ? 'checked' : 'unchecked'
        await expect(this.codeModeSwitch).toHaveAttribute('data-state', expectedState, { timeout: 3000 })
      }
    }

    // Toggle ping available if specified (Radix Switch)
    if (config.isPingAvailable !== undefined) {
      const dataState = await this.pingAvailableSwitch.getAttribute('data-state')
      const currentState = dataState === 'checked'
      if (currentState !== config.isPingAvailable) {
        await this.pingAvailableSwitch.click()
      }
    }

    // Handle connection-type specific fields
    if (config.connectionType === 'http' || config.connectionType === 'sse' || !config.connectionType) {
      // Wait for auth type field to be visible (only shows for HTTP/SSE)
      await expect(this.authTypeSelect).toBeVisible({ timeout: 5000 })

      // Select auth type if specified
      if (config.authType) {
        await this.selectAuthType(config.authType)
        await this.page.waitForTimeout(500)
      }

      // Fill connection URL
      if (config.connectionUrl) {
        await expect(this.connectionUrlInput).toBeVisible({ timeout: 5000 })
        await this.connectionUrlInput.fill(config.connectionUrl)
        // Wait for React to process the input
        await this.page.waitForTimeout(500)
      }

      // Fill headers when auth_type is 'headers' (required for SSE test; export MCP_SSE_HEADERS in your environment)
      if (config.authType === 'headers' && config.headers && Object.keys(config.headers).length > 0) {
        const headersTable = this.sheet.locator('[data-testid="mcp-headers-table"]')
        await expect(headersTable).toBeVisible({ timeout: 5000 })
        const entries = Object.entries(config.headers)
        for (let i = 0; i < entries.length; i++) {
          const [key, val] = entries[i]
          const valueStr = typeof val === 'object' && val !== null && 'value' in val ? (val as EnvVarLike).value : String(val)
          const keyInput = headersTable.locator(`input[data-row="${i}"][data-column="key"]`)
          const valueInput = headersTable.locator(`input[data-row="${i}"][data-column="value"]`).or(
            headersTable.locator(`[data-row="${i}"][data-column="value"] input`)
          )
          await keyInput.waitFor({ state: 'visible', timeout: 8000 })
          await keyInput.scrollIntoViewIfNeeded()
          await keyInput.click()
          await keyInput.fill(key)
          await this.page.waitForTimeout(400)
          const valueEl = valueInput.first()
          await valueEl.waitFor({ state: 'visible', timeout: 3000 })
          await valueEl.scrollIntoViewIfNeeded()
          await valueEl.click()
          await valueEl.fill(valueStr)
          await this.page.waitForTimeout(500)
        }
      }

      // Handle OAuth config
      if (config.authType === 'oauth') {
        if (config.oauthClientId) {
          await this.oauthClientIdInput.fill(config.oauthClientId)
        }
        if (config.oauthClientSecret) {
          await this.oauthClientSecretInput.fill(config.oauthClientSecret)
        }
        if (config.oauthAuthorizeUrl) {
          await this.oauthAuthorizeUrlInput.fill(config.oauthAuthorizeUrl)
        }
        if (config.oauthTokenUrl) {
          await this.oauthTokenUrlInput.fill(config.oauthTokenUrl)
        }
        if (config.oauthScopes) {
          await this.oauthScopesInput.fill(config.oauthScopes)
        }
      }
    } else if (config.connectionType === 'stdio') {
      // Fill STDIO specific fields - wait for them to be visible after type change
      if (config.command) {
        await expect(this.commandInput).toBeVisible({ timeout: 5000 })
        await this.commandInput.fill(config.command)
        // Wait for React to process the input
        await this.page.waitForTimeout(500)
      }
      if (config.args) {
        await expect(this.argsInput).toBeVisible({ timeout: 5000 })
        await this.argsInput.fill(config.args)
      }
      if (config.envs) {
        await expect(this.envsInput).toBeVisible({ timeout: 5000 })
        await this.envsInput.fill(config.envs)
      }
    }
  }

  /**
   * Create an MCP client with full configuration
   */
  async createClient(config: MCPClientConfig): Promise<boolean> {
    await this.dismissToasts()
    await this.createBtn.click()
    await expect(this.sheet).toBeVisible({ timeout: 5000 })

    // Fill the form
    await this.fillClientForm(config)

    // Wait for form validation to complete
    await this.page.waitForTimeout(1500)

    // Wait for save button to be enabled (validation passed)
    await expect(this.saveBtn).toBeEnabled({ timeout: 10000 })

    // Verify button is visible and contains expected text
    await expect(this.saveBtn).toBeVisible()
    await expect(this.saveBtn).toContainText(/Create|Save/i)

    // Wait for create-client API response then click save (backend may be slow connecting to MCP server)
    // Create is POST to /mcp/client (singular); do not match GET /mcp/clients
    const responsePromise = this.page.waitForResponse(
      (response) => {
        const url = response.url()
        const method = response.request().method()
        return (
          (url.includes('/mcp/client') && !url.endsWith('/mcp/clients')) &&
          (method === 'POST' || method === 'PUT')
        )
      },
      { timeout: 60000 }
    )
    await this.saveBtn.click({ force: true })
    const response = await responsePromise.catch(() => null)
    const ok = response && response.ok()
    if (response && !ok) {
      const body = await response.text().catch(() => '')
      await this.page.keyboard.press('Escape')
      await this.sheet.waitFor({ state: 'hidden', timeout: 5000 }).catch(() => {})
      throw new Error(`Create MCP client failed: ${response.status()} ${body}`)
    }

    // Success: backend returned 2xx. Wait for create form to close (short timeout; UI usually updates quickly).
    const createFormHeading = this.page.getByRole('heading', { name: 'New MCP Server' })
    await createFormHeading.waitFor({ state: 'hidden', timeout: 15000 }).catch(() => null)
    const createFormClosed = !(await createFormHeading.isVisible().catch(() => false))
    if (createFormClosed) {
      return true
    }

    // Backend succeeded but form may not close quickly (e.g. SSE/stdio). If client appears in table, treat as success.
    const inTable = await this.waitForClientInTable(config.name, 10000)
    if (inTable) {
      await this.page.keyboard.press('Escape')
      await this.sheet.waitFor({ state: 'hidden', timeout: 5000 }).catch(() => {})
      return true
    }

    // Fallback: wait for toast or heading (e.g. slow UI)
    const toast = this.getToast()
    await Promise.race([
      createFormHeading.waitFor({ state: 'hidden', timeout: 10000 }).catch(() => null),
      toast.waitFor({ state: 'visible', timeout: 10000 }).catch(() => null),
    ])
    if (!(await createFormHeading.isVisible().catch(() => true))) {
      return true
    }

    // Sheet still open - check for toast (success or error)
    let toastText = ''
    let toastVisible = false
    try {
      toastVisible = await toast.isVisible()
      if (toastVisible) toastText = (await toast.textContent()) || ''
    } catch {
      // ignore
    }
    if (toastVisible && toastText) {
      const isSuccess =
        toastText.toLowerCase().includes('success') ||
        toastText.toLowerCase().includes('created') ||
        toastText.toLowerCase().includes('server created')
      if (isSuccess) {
        await createFormHeading.waitFor({ state: 'hidden', timeout: 10000 }).catch(() => null)
        return true
      }
      await this.page.keyboard.press('Escape')
      await this.sheet.waitFor({ state: 'hidden', timeout: 5000 }).catch(() => {})
      throw new Error(`Client creation failed with error: ${toastText}`)
    }

    // No toast, sheet still open - validation or unknown failure
    const errorMessages = (await this.page.locator('[role="alert"]').allTextContents().catch(() => []))
      .map((t) => t.trim())
      .filter(Boolean)
    if (errorMessages.length > 0) {
      throw new Error(`Form validation errors: ${errorMessages.join(', ')}`)
    }
    const isDisabled = await this.saveBtn.isDisabled().catch(() => false)
    if (isDisabled) {
      throw new Error('Save button is disabled - form validation failed')
    }
    throw new Error('No toast appeared and sheet did not close - form submission may have failed')
  }

  /**
   * View client details by clicking on the row
   */
  async viewClientDetails(name: string): Promise<void> {
    const row = this.getClientRow(name)
    await row.click()
    await expect(this.detailSheet).toBeVisible({ timeout: 5000 })
  }

  /**
   * Close the detail sheet
   */
  async closeDetailSheet(): Promise<void> {
    // Press Escape or click the X button
    await this.page.keyboard.press('Escape')
    await expect(this.detailSheet).not.toBeVisible({ timeout: 5000 }).catch(async () => {
      // If still visible, try clicking X button
      const closeBtn = this.detailSheet.locator('button').filter({ has: this.page.locator('svg.lucide-x') })
      if (await closeBtn.isVisible()) {
        await closeBtn.click()
      }
    })
  }

  /**
   * Close any open sheet/dialog (create or detail) so the table is visible
   */
  async closeSheet(): Promise<void> {
    const isVisible = await this.sheet.isVisible().catch(() => false)
    if (isVisible) {
      await this.page.keyboard.press('Escape')
      await expect(this.sheet).not.toBeVisible({ timeout: 5000 }).catch(() => {})
    }
  }

  /**
   * Clean up MCP clients by name. Ensures we're on the page and any sheet is closed before deleting.
   */
  async cleanupMCPClients(names: string[]): Promise<void> {
    if (names.length === 0) return

    await this.goto()
    await this.closeSheet()
    await this.dismissToasts()
    await this.table.waitFor({ state: 'visible', timeout: 10000 }).catch(() => {})
    await this.page.waitForTimeout(500)

    for (const name of names) {
      const tryDelete = async (): Promise<void> => {
        const exists = await this.clientExists(name)
        if (!exists) return
        await this.closeSheet()
        await this.deleteClient(name, { requireToast: false })
      }

      try {
        await tryDelete()
      } catch (error) {
        const errorMsg = error instanceof Error ? error.message : String(error)
        console.error(`[CLEANUP ERROR] Failed to delete MCP client: ${name} - ${errorMsg}`)
        await this.closeSheet()
        await this.page.waitForTimeout(1000)
        try {
          await tryDelete()
        } catch (retryErr) {
          const retryMsg = retryErr instanceof Error ? retryErr.message : String(retryErr)
          console.error(`[CLEANUP ERROR] Retry failed for MCP client: ${name} - ${retryMsg}`)
        }
      }
    }
  }

  /**
   * Edit an existing client
   */
  async editClient(name: string, updates: Partial<MCPClientConfig>): Promise<void> {
    await this.viewClientDetails(name)

    // Update name if provided
    if (updates.name) {
      const nameInput = this.detailSheet.getByLabel(/Name/i).first()
      await nameInput.clear()
      await nameInput.fill(updates.name)
    }

    // Toggle code mode if specified
    if (updates.isCodeMode !== undefined) {
      const codeModeSwitch = this.detailSheet
        .locator('input[type="checkbox"]')
        .filter({ has: this.page.locator('#code-mode') })
        .or(this.detailSheet.getByRole('switch', { name: /Code Mode/i }))
      const isVisible = await codeModeSwitch.isVisible().catch(() => false)
      if (isVisible) {
        const currentState = await codeModeSwitch.isChecked()
        if (currentState !== updates.isCodeMode) {
          await codeModeSwitch.click()
        }
      }
    }

    // Save changes
    const saveBtn = this.detailSheet.getByRole('button', { name: /Save/i })
    await saveBtn.click()
    await this.waitForSuccessToast()
    await expect(this.detailSheet).not.toBeVisible({ timeout: 10000 })
  }

  /**
   * Reconnect an MCP client
   */
  async reconnectClient(name: string): Promise<void> {
    const row = this.getClientRow(name)
    // Stop propagation by clicking the reconnect button directly
    const reconnectBtn = row.locator('button').filter({ has: this.page.locator('svg.lucide-refresh-ccw') })
    await reconnectBtn.click()
    await this.waitForSuccessToast('Reconnected')
  }

  /**
   * Toggle tool enabled state in the detail sheet
   */
  async toggleToolEnabled(clientName: string, toolName: string): Promise<void> {
    await this.viewClientDetails(clientName)

    // Find the tool row and toggle its enabled switch
    const toolRow = this.detailSheet.locator('tr').filter({ hasText: toolName })
    const enabledSwitch = toolRow.locator('button[role="switch"]').first()
    await enabledSwitch.click()

    // Save
    const saveBtn = this.detailSheet.getByRole('button', { name: /Save/i })
    await saveBtn.click()
    await this.waitForSuccessToast()
    await expect(this.detailSheet).not.toBeVisible({ timeout: 10000 })
  }

  /**
   * Toggle auto-execute for a tool in the detail sheet
   */
  async toggleAutoExecute(clientName: string, toolName: string): Promise<void> {
    await this.viewClientDetails(clientName)

    // Find the tool row and toggle its auto-execute switch (second switch)
    const toolRow = this.detailSheet.locator('tr').filter({ hasText: toolName })
    const autoExecuteSwitch = toolRow.locator('button[role="switch"]').nth(1)
    await autoExecuteSwitch.click()

    // Save
    const saveBtn = this.detailSheet.getByRole('button', { name: /Save/i })
    await saveBtn.click()
    await this.waitForSuccessToast()
    await expect(this.detailSheet).not.toBeVisible({ timeout: 10000 })
  }

  /**
   * Toggle code mode for a client
   */
  async toggleCodeMode(clientName: string): Promise<void> {
    await this.viewClientDetails(clientName)

    // Find and toggle the code mode switch
    const codeModeSwitch = this.detailSheet
      .getByRole('switch', { name: /Code Mode/i })
      .or(this.detailSheet.locator('#code-mode'))
    await codeModeSwitch.click()

    // Save
    const saveBtn = this.detailSheet.getByRole('button', { name: /Save/i })
    await saveBtn.click()
    await this.waitForSuccessToast()
    await expect(this.detailSheet).not.toBeVisible({ timeout: 10000 })
  }

  /**
   * Get client status from the table
   */
  async getClientStatus(name: string): Promise<string> {
    const row = this.getClientRow(name)
    const statusBadge = row
      .locator('[class*="badge"]')
      .or(row.locator('span').filter({ hasText: /connected|disconnected|connecting|error/i }))
      .last()
    const statusText = await statusBadge.textContent()
    return statusText?.toLowerCase().trim() || ''
  }

  /**
   * Get connection type displayed in the table (HTTP, SSE, STDIO)
   */
  async getClientConnectionType(name: string): Promise<string> {
    const row = this.getClientRow(name)
    const typeCell = row.getByTestId('mcp-client-connection-type')
    if ((await typeCell.count()) > 0) {
      return (await typeCell.first().textContent())?.trim() ?? ''
    }
    const cells = row.locator('td')
    if ((await cells.count()) >= 2) {
      return (await cells.nth(1).textContent())?.trim() ?? ''
    }
    return ''
  }

  /**
   * Get tools count from the client details sheet
   * Assumes the detail sheet is already open
   */
  async getToolsCount(): Promise<number> {
    // Tools are displayed in a table in the detail sheet
    const toolRows = this.detailSheet.locator('table tbody tr')
    const count = await toolRows.count()
    return count
  }

  /**
   * Get enabled tools count from table
   */
  async getEnabledToolsCount(name: string): Promise<string | null> {
    const row = this.getClientRow(name)
    // Enabled tools is typically shown as "X/Y" format
    const cells = row.locator('td')
    const count = await cells.count()
    if (count >= 5) {
      return await cells.nth(4).textContent()
    }
    return null
  }

  /**
   * Cancel client creation
   */
  async cancelCreation(): Promise<void> {
    await this.cancelBtn.click()
    await expect(this.sheet).not.toBeVisible({ timeout: 5000 })
  }

  /**
   * Wait for the client row to disappear from the table (e.g. after delete or refetch).
   * Polls so we don't rely on a stale locator.
   */
  async waitForClientGone(name: string, timeoutMs: number): Promise<boolean> {
    const deadline = Date.now() + timeoutMs
    while (Date.now() < deadline) {
      if ((await this.getClientRow(name).count()) === 0) return true
      await this.page.waitForTimeout(500)
    }
    return false
  }

  /**
   * Delete an MCP client. Success is determined by the DELETE API completing and the row
   * disappearing from the table after the list refetches.
   */
  async deleteClient(name: string, options?: { requireToast?: boolean }): Promise<void> {
    const row = this.getClientRow(name)
    const deleteBtn = row
      .locator('button')
      .filter({ has: this.page.locator('svg.lucide-trash-2') })
      .or(row.locator('button').filter({ has: this.page.locator('svg.lucide-trash') }))
    await deleteBtn.click()

    const confirmDialog = this.page.locator('[role="alertdialog"]')
    await expect(confirmDialog).toBeVisible({ timeout: 5000 })
    const deleteResponsePromise = this.page.waitForResponse(
      (response) => {
        const url = response.url()
        return url.includes('/mcp/client/') && response.request().method() === 'DELETE'
      },
      { timeout: 15000 }
    )
    await confirmDialog.getByRole('button', { name: /Delete/i }).click()

    await deleteResponsePromise.catch(() => null)

    // Wait for table to refetch and row to disappear (poll fresh locator; avoid stale row reference)
    const gone = await this.waitForClientGone(name, 20000)
    if (!gone) {
      throw new Error(`Client "${name}" still visible after delete`)
    }

    if (options?.requireToast !== false) {
      await this.getToast().waitFor({ state: 'visible', timeout: 5000 }).catch(() => {})
    }
  }

  /**
   * Check if empty state is visible
   */
  async isEmptyStateVisible(): Promise<boolean> {
    const emptyMessage = this.page.getByText(/No clients found/i)
    return await emptyMessage.isVisible().catch(() => false)
  }
}
