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
})
