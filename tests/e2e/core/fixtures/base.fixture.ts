import { test as base, expect } from '@playwright/test'
import { SidebarPage } from '../pages/sidebar.page'
import { ProvidersPage } from '../../features/providers/pages/providers.page'
import { VirtualKeysPage } from '../../features/virtual-keys/pages/virtual-keys.page'
import { DashboardPage } from '../../features/dashboard/pages/dashboard.page'
import { LogsPage } from '../../features/logs/pages/logs.page'
import { MCPLogsPage } from '../../features/mcp-logs/pages/mcp-logs.page'
import { RoutingRulesPage } from '../../features/routing-rules/pages/routing-rules.page'
import { MCPRegistryPage } from '../../features/mcp-registry/pages/mcp-registry.page'
import { PluginsPage } from '../../features/plugins/pages/plugins.page'
import { ObservabilityPage } from '../../features/observability/pages/observability.page'
import { ConfigSettingsPage } from '../../features/config/pages/config-settings.page'
import { GovernancePage } from '../../features/governance/pages/governance.page'
import { MCPAuthConfigPage } from '../../features/mcp-auth-config/pages/mcp-auth-config.page'
import { MCPSettingsPage } from '../../features/mcp-settings/pages/mcp-settings.page'
import { MCPToolGroupsPage } from '../../features/mcp-tool-groups/pages/mcp-tool-groups.page'
import { ModelLimitsPage } from '../../features/model-limits/pages/model-limits.page'

/**
 * Custom test fixtures type
 */
type BifrostFixtures = {
  closeDevProfiler: void
  sidebarPage: SidebarPage
  providersPage: ProvidersPage
  virtualKeysPage: VirtualKeysPage
  dashboardPage: DashboardPage
  logsPage: LogsPage
  mcpLogsPage: MCPLogsPage
  routingRulesPage: RoutingRulesPage
  mcpRegistryPage: MCPRegistryPage
  pluginsPage: PluginsPage
  observabilityPage: ObservabilityPage
  configSettingsPage: ConfigSettingsPage
  governancePage: GovernancePage
  modelLimitsPage: ModelLimitsPage
  mcpSettingsPage: MCPSettingsPage
  mcpToolGroupsPage: MCPToolGroupsPage
  mcpAuthConfigPage: MCPAuthConfigPage
}

/**
 * Extended test with Bifrost-specific fixtures
 */
export const test = base.extend<BifrostFixtures>({
  closeDevProfiler: [async ({ page }, use) => {
    // Automatically dismiss the Dev Profiler overlay whenever it appears.
    // Uses addLocatorHandler so it triggers before any test action if the profiler is visible.
    await page.addLocatorHandler(
      page.getByText('Dev Profiler', { exact: true }),
      async () => {
        await page.locator('button[title="Dismiss"]').click({ force: true })
      }
    )
    await use()
  }, { auto: true }],

  sidebarPage: async ({ page }, use) => {
    await use(new SidebarPage(page))
  },

  providersPage: async ({ page }, use) => {
    await use(new ProvidersPage(page))
  },

  virtualKeysPage: async ({ page }, use) => {
    await use(new VirtualKeysPage(page))
  },

  dashboardPage: async ({ page }, use) => {
    await use(new DashboardPage(page))
  },

  logsPage: async ({ page }, use) => {
    await use(new LogsPage(page))
  },

  mcpLogsPage: async ({ page }, use) => {
    await use(new MCPLogsPage(page))
  },

  routingRulesPage: async ({ page }, use) => {
    await use(new RoutingRulesPage(page))
  },

  mcpRegistryPage: async ({ page }, use) => {
    await use(new MCPRegistryPage(page))
  },

  pluginsPage: async ({ page }, use) => {
    await use(new PluginsPage(page))
  },

  observabilityPage: async ({ page }, use) => {
    await use(new ObservabilityPage(page))
  },

  configSettingsPage: async ({ page }, use) => {
    await use(new ConfigSettingsPage(page))
  },

  governancePage: async ({ page }, use) => {
    await use(new GovernancePage(page))
  },

  modelLimitsPage: async ({ page }, use) => {
    await use(new ModelLimitsPage(page))
  },

  mcpSettingsPage: async ({ page }, use) => {
    await use(new MCPSettingsPage(page))
  },

  mcpToolGroupsPage: async ({ page }, use) => {
    await use(new MCPToolGroupsPage(page))
  },

  mcpAuthConfigPage: async ({ page }, use) => {
    await use(new MCPAuthConfigPage(page))
  },
})

export { expect }
