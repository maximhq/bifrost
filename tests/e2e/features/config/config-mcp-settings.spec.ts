import { coreConfigApi } from '../../core/actions/api'
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

  test('should save the OpenAPI spec size limit', async ({ configSettingsPage, request }) => {
    const original = (await coreConfigApi.get(request)).client_config.mcp_openapi_max_spec_mb ?? 0
    try {
      await configSettingsPage.goto('mcp-gateway')
      const input = configSettingsPage.page.getByTestId('mcp-openapi-max-spec-mb-input')
      await expect(input).toHaveValue(String(original))

      // Differs from the stored value, so the form is dirty and Save enables.
      const target = original === 64 ? 65 : 64
      await input.fill(String(target))
      const saveBtn = configSettingsPage.page.getByTestId('mcp-settings-save-btn')
      await expect(saveBtn).toBeEnabled()
      await saveBtn.click()
      await expect.poll(async () => (await coreConfigApi.get(request)).client_config.mcp_openapi_max_spec_mb).toBe(target)

      await configSettingsPage.page.reload()
      await expect(input).toHaveValue(String(target))
    } finally {
      await coreConfigApi.updateClientConfig(request, { mcp_openapi_max_spec_mb: original })
    }
  })

  test('should persist the free tool calls over budget switch', async ({ configSettingsPage, request }) => {
    const field = 'mcp_allow_free_calls_when_budget_exhausted'
    const initial = (await coreConfigApi.get(request)).client_config[field] === true
    try {
      await configSettingsPage.goto('mcp-gateway')
      await configSettingsPage.dismissOnboardingWidget()
      const freeCalls = configSettingsPage.mcpAllowFreeCallsWhenBudgetExhaustedSwitch
      await expect(freeCalls).toBeVisible()
      expect(await configSettingsPage.getSwitchState(freeCalls)).toBe(initial)

      await freeCalls.click()
      await configSettingsPage.mcpSettingsSaveBtn.click()
      await configSettingsPage.waitForSuccessToast()

      await configSettingsPage.goto('mcp-gateway')
      expect(await configSettingsPage.getSwitchState(configSettingsPage.mcpAllowFreeCallsWhenBudgetExhaustedSwitch)).toBe(!initial)
      expect((await coreConfigApi.get(request)).client_config[field]).toBe(!initial)
    } finally {
      await coreConfigApi.updateClientConfig(request, { [field]: initial })
    }
  })
})
