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
    await expect(configSettingsPage.page.getByTestId('mcp-binding-level')).toBeVisible()
  })

  test('should have save button disabled when no changes', async ({ configSettingsPage }) => {
    await configSettingsPage.goto('mcp-gateway')

    const saveBtn = configSettingsPage.page.getByTestId('mcp-settings-save-btn')
    await expect(saveBtn).toBeVisible()
    await expect(saveBtn).toBeDisabled()
  })

  test('should save the OpenAPI spec size limit', async ({ configSettingsPage, request }) => {
    const original = (await coreConfigApi.get(request)).client_config.mcp_openapi_max_spec_mb ?? 0
    try {
      await configSettingsPage.goto('mcp-gateway')
      const input = configSettingsPage.page.getByTestId('mcp-openapi-max-spec-mb-input')
      await expect(input).toHaveValue(String(original))

      await input.fill('64')
      const saveBtn = configSettingsPage.page.getByTestId('mcp-settings-save-btn')
      await expect(saveBtn).toBeEnabled()
      await saveBtn.click()
      await expect.poll(async () => (await coreConfigApi.get(request)).client_config.mcp_openapi_max_spec_mb).toBe(64)

      await configSettingsPage.page.reload()
      await expect(input).toHaveValue('64')
    } finally {
      await coreConfigApi.updateClientConfig(request, { mcp_openapi_max_spec_mb: original })
    }
  })
})
