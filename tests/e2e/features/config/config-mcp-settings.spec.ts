import { expect, test } from '../../core/fixtures/base.fixture'

test.describe('MCP Settings', () => {
  // Run all config tests serially to avoid parallel writes to the same config/store
  test.describe.configure({ mode: 'serial' })

  test('should display MCP settings form', async ({ configSettingsPage }) => {
    await configSettingsPage.goto('mcp-gateway')

    await expect(configSettingsPage.page.getByTestId('mcp-settings-view')).toBeVisible()
    await expect(configSettingsPage.page.getByTestId('mcp-agent-depth-input')).toBeVisible()
    await expect(configSettingsPage.page.getByTestId('mcp-tool-timeout-input')).toBeVisible()
    // Binding level sits inside the collapsed Code Mode Settings section.
    await configSettingsPage.page.getByTestId('mcp-settings-code-mode-trigger').click()
    await expect(configSettingsPage.page.getByTestId('mcp-binding-level')).toBeVisible()
  })

  test('should have save button disabled when no changes', async ({ configSettingsPage }) => {
    await configSettingsPage.goto('mcp-gateway')

    const saveBtn = configSettingsPage.page.getByTestId('mcp-settings-save-btn')
    await expect(saveBtn).toBeVisible()
    await expect(saveBtn).toBeDisabled()
  })

  test('should save the hosted client metadata document URL', async ({ configSettingsPage }) => {
    const page = configSettingsPage.page
    const hostedURL = 'https://public.example.com/bifrost/client.json'
    let saved: { value?: string } | undefined
    // Capture the save without writing it, so the shared store keeps its config.
    await page.route('**/api/config', async route => {
      if (route.request().method() !== 'PUT') {
        await route.continue()
        return
      }
      const payload = route.request().postDataJSON() as { client_config: { mcp_client_metadata_url?: { value?: string } } }
      saved = payload.client_config.mcp_client_metadata_url
      await route.fulfill({ json: { message: 'Configuration updated successfully' } })
    })
    await configSettingsPage.goto('mcp-gateway')
    await page.getByTestId('mcp-settings-advanced-trigger').click()

    const input = page.getByTestId('mcp-client-metadata-url-input')
    await expect(input).toBeVisible()
    await expect(page.getByTestId('mcp-client-metadata-document-link')).toHaveAttribute('href', /\/api\/oauth\/client-metadata\.json$/)
    await input.fill(hostedURL)
    const saveBtn = page.getByTestId('mcp-settings-save-btn')
    await expect(saveBtn).toBeEnabled()
    await saveBtn.click()
    await expect.poll(() => saved?.value).toBe(hostedURL)
  })

  test('should save the switch that stops advertising client metadata documents', async ({ configSettingsPage }) => {
    const page = configSettingsPage.page
    let saved: { disable_client_metadata_documents?: boolean } | undefined
    // Serve the stored config in OAuth mode (the switch is OAuth-only) and capture the save without writing it.
    // Match on pathname: the GET carries ?from_db=, which a glob would miss.
    await page.route(url => url.pathname === '/api/config', async route => {
      if (route.request().method() === 'PUT') {
        const payload = route.request().postDataJSON() as { client_config: { oauth2_server_config?: { disable_client_metadata_documents?: boolean } } }
        saved = payload.client_config.oauth2_server_config
        await route.fulfill({ json: { message: 'Configuration updated successfully' } })
        return
      }
      const response = await route.fetch()
      const body = await response.json()
      body.client_config = {
        ...body.client_config,
        mcp_server_auth_mode: 'both',
        oauth2_server_config: {
          ...body.client_config.oauth2_server_config,
          issuer_url: { value: 'https://bifrost.example.com' },
          disable_client_metadata_documents: false,
        },
      }
      await route.fulfill({ response, json: body })
    })
    await configSettingsPage.goto('mcp-gateway')
    await page.getByTestId('mcp-settings-advanced-trigger').click()

    const toggle = page.getByTestId('oauth2-disable-client-metadata-documents-switch')
    await expect(toggle).toBeVisible()
    await expect(toggle).not.toBeChecked()
    await toggle.click()
    const saveBtn = page.getByTestId('mcp-settings-save-btn')
    await expect(saveBtn).toBeEnabled()
    await saveBtn.click()
    await expect.poll(() => saved?.disable_client_metadata_documents).toBe(true)
  })
})
