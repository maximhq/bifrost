import { coreConfigApi } from '../../core/actions/api'
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

  test.describe('Code Mode Limits', () => {
    // Every test here rewrites the shared mcp_code_mode_limits, so they must not interleave.
    test.describe.configure({ mode: 'serial' })
    let originalLimits: unknown

    test.beforeEach(async ({ request }) => {
      originalLimits = (await coreConfigApi.get(request)).client_config.mcp_code_mode_limits
    })

    test.afterEach(async ({ request }) => {
      await coreConfigApi.updateClientConfig(request, { mcp_code_mode_limits: originalLimits ?? {} })
    })

    test('should show every limit with its default as the placeholder', async ({ mcpSettingsPage }) => {
      await expect(mcpSettingsPage.codeModeLimits).toBeVisible()
      const defaults: [string, string][] = [
        ['max_source_bytes', '65536'],
        ['max_steps', '1000000'],
        ['max_memory_bytes', '67108864'],
        ['max_log_bytes', '65536'],
        ['max_tool_calls', '64'],
        ['max_value_bytes', '1048576'],
        ['max_nesting_depth', '64'],
      ]
      for (const [field, placeholder] of defaults) {
        await expect(mcpSettingsPage.codeModeLimitInput(field)).toHaveAttribute('placeholder', placeholder)
      }
    })

    test('should save a limit and keep empty fields on their defaults', async ({ mcpSettingsPage, request }) => {
      await coreConfigApi.updateClientConfig(request, { mcp_code_mode_limits: {} })
      await mcpSettingsPage.goto()

      await mcpSettingsPage.codeModeLimitInput('max_tool_calls').fill('200')
      await expect(mcpSettingsPage.saveBtn).toBeEnabled()
      await mcpSettingsPage.saveBtn.click()
      await mcpSettingsPage.waitForSuccessToast()

      const saved = (await coreConfigApi.get(request)).client_config.mcp_code_mode_limits as Record<string, number>
      expect(saved.max_tool_calls).toBe(200)
      expect(saved.max_steps ?? 0).toBe(0)

      await mcpSettingsPage.goto()
      await expect(mcpSettingsPage.codeModeLimitInput('max_tool_calls')).toHaveValue('200')
      await expect(mcpSettingsPage.codeModeLimitInput('max_steps')).toHaveValue('')
      await expect(mcpSettingsPage.saveBtn).toBeDisabled()
    })

    test('should reject a value size below the minimum without saving', async ({ mcpSettingsPage, request }) => {
      await coreConfigApi.updateClientConfig(request, { mcp_code_mode_limits: {} })
      await mcpSettingsPage.goto()

      await mcpSettingsPage.codeModeLimitInput('max_value_bytes').fill('10')
      await mcpSettingsPage.saveBtn.click()
      await expect(mcpSettingsPage.getToast('error')).toContainText('at least 1024')

      const saved = (await coreConfigApi.get(request)).client_config.mcp_code_mode_limits as Record<string, number> | undefined
      expect(saved?.max_value_bytes ?? 0).toBe(0)
    })
  })
})
