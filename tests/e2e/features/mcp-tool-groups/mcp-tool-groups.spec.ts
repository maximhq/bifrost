import { expect, test } from '../../core/fixtures/base.fixture'

// MCP Tool Groups is superseded by Virtual MCPs; the old path only redirects.
test.describe('MCP Tool Groups', () => {
  test.beforeEach(async ({ mcpToolGroupsPage }) => {
    await mcpToolGroupsPage.goto()
  })

  test('should redirect the old MCP tool groups path to Virtual MCPs', async ({ mcpToolGroupsPage }) => {
    await expect(mcpToolGroupsPage.page).toHaveURL(/\/workspace\/virtual-mcps/)
  })
})
