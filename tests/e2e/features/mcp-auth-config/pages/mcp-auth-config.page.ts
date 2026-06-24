import { Page } from '@playwright/test'
import { BasePage } from '../../../core/pages/base.page'
import { waitForNetworkIdle } from '../../../core/utils/test-helpers'

export class MCPAuthConfigPage extends BasePage {
  constructor(page: Page) {
    super(page)
  }

  async goto(): Promise<void> {
    await this.page.goto('/workspace/mcp-auth-config')
    await waitForNetworkIdle(this.page)
  }
}
