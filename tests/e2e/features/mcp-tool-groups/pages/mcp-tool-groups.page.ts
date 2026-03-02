import { Page } from '@playwright/test'
import { BasePage } from '../../../core/pages/base.page'
import { waitForNetworkIdle } from '../../../core/utils/test-helpers'

export class MCPToolGroupsPage extends BasePage {
  constructor(page: Page) {
    super(page)
  }

  async goto(): Promise<void> {
    await this.page.goto('/workspace/mcp-tool-groups')
    await waitForNetworkIdle(this.page)
  }
}
