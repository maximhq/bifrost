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
