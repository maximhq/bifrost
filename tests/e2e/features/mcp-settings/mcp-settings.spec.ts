import { expect, test } from '../../core/fixtures/base.fixture'

test.describe('MCP Settings', () => {
  test.beforeEach(async ({ mcpSettingsPage }) => {
    await mcpSettingsPage.goto()
  })

  test('should display MCP settings page', async ({ mcpSettingsPage }) => {
    await expect(mcpSettingsPage.mcpSettingsView).toBeVisible()
  })

  test('should display MCP settings form fields', async ({ mcpSettingsPage }) => {
    await expect(mcpSettingsPage.page.getByLabel('Max Agent Depth')).toBeVisible()
    await expect(mcpSettingsPage.page.getByLabel('Tool Execution Timeout (seconds)')).toBeVisible()
  })

  test('should have save button disabled when no changes', async ({ mcpSettingsPage }) => {
    await expect(mcpSettingsPage.saveBtn).toBeVisible()
    await expect(mcpSettingsPage.saveBtn).toBeDisabled()
  })
})
