import { expect, test } from '../../core/fixtures/base.fixture'

// Each SidebarPage helper must reach its page by clicking through the sidebar,
// including links that sit inside a collapsed group.
test.describe('Sidebar navigation', () => {
  const cases = [
    { name: 'providers', go: 'goToProviders', path: '/workspace/providers' },
    { name: 'virtual keys', go: 'goToVirtualKeys', path: '/workspace/governance/virtual-keys' },
    { name: 'logs', go: 'goToLogs', path: '/workspace/logs' },
    { name: 'MCP catalog', go: 'goToMCPClients', path: '/workspace/mcp-registry' },
    { name: 'plugins', go: 'goToPlugins', path: '/workspace/plugins' },
    { name: 'config', go: 'goToConfig', path: '/workspace/config/client-settings' },
  ] as const

  for (const c of cases) {
    test(`reaches ${c.name} from the dashboard`, async ({ page, sidebarPage }) => {
      await page.goto('/workspace/dashboard')
      await sidebarPage[c.go]()
      // Compare the whole pathname: a suffix match would accept a redirect such as /login?goto=<path>.
      await expect.poll(() => new URL(page.url()).pathname).toBe(c.path)
    })
  }
})
