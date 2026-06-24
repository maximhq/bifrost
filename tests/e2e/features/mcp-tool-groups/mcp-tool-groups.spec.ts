import { expect, test } from '../../core/fixtures/base.fixture'

// MCP Tool Groups routes to @enterprise components not present in OSS.
// Tests only verify URL routing; do not add UI assertions for enterprise-only content.
test.describe('MCP Tool Groups', () => {
  test.beforeEach(async ({ mcpToolGroupsPage }) => {
    await mcpToolGroupsPage.goto()
  })

  test('should load MCP tool groups page', async ({ mcpToolGroupsPage }) => {
    await expect(mcpToolGroupsPage.page).toHaveURL(/mcp-tool-groups/)
  })
})
