import { expect, test } from '../../core/fixtures/base.fixture'

test.describe('Navigation', () => {
  // Run all config tests serially to avoid parallel writes to the same config/store
  test.describe.configure({ mode: 'serial' })

  test('should navigate to client settings', async ({ configSettingsPage }) => {
    await configSettingsPage.goto('client-settings')
    await expect(configSettingsPage.saveBtn).toBeVisible()
    // Use heading to avoid matching sidebar link
    await expect(configSettingsPage.page.getByRole('heading', { name: /Client Settings/i })).toBeVisible()
  })

  test('should navigate to caching config', async ({ configSettingsPage }) => {
    await configSettingsPage.goto('caching')
    // Caching page exists - verify page loaded
    await expect(configSettingsPage.page.getByRole('heading', { name: /Local Cache/i })).toBeVisible()
    await expect(configSettingsPage.page.getByTestId('caching-enable-switch')).toBeVisible()
  })

  test('should navigate to logging config', async ({ configSettingsPage }) => {
    await configSettingsPage.goto('logging')
    await expect(configSettingsPage.saveBtn).toBeVisible()
    await expect(configSettingsPage.page.getByRole('heading', { name: /Logs Settings/i })).toBeVisible()
  })

  test('should navigate to security config', async ({ configSettingsPage }) => {
    await configSettingsPage.goto('security')
    await expect(configSettingsPage.saveBtn).toBeVisible()
    await expect(configSettingsPage.page.getByRole('heading', { name: /Security/i })).toBeVisible()
  })

  test('should navigate to performance tuning config', async ({ configSettingsPage }) => {
    await configSettingsPage.goto('performance-tuning')
    await expect(configSettingsPage.saveBtn).toBeVisible()
    await expect(configSettingsPage.page.getByRole('heading', { name: /Performance Tuning/i })).toBeVisible()
  })

  test('should navigate to pricing config', async ({ configSettingsPage }) => {
    await configSettingsPage.goto('pricing-config')
    await expect(configSettingsPage.saveBtn).toBeVisible()
    await expect(configSettingsPage.page.getByRole('heading', { name: /Model Settings/i })).toBeVisible()
  })

  test('should navigate to MCP settings', async ({ configSettingsPage }) => {
    await configSettingsPage.goto('mcp-gateway')
    await expect(configSettingsPage.page.getByTestId('mcp-settings-view')).toBeVisible()
    await expect(configSettingsPage.page.getByTestId('mcp-agent-depth-input')).toBeVisible()
    await expect(configSettingsPage.page.getByTestId('mcp-tool-timeout-input')).toBeVisible()
  })
})
