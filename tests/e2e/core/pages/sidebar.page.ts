import { Page, Locator } from '@playwright/test'
import { BasePage } from './base.page'

/**
 * Sidebar navigation page object
 */
export class SidebarPage extends BasePage {
  // Navigation links
  readonly providersLink: Locator
  readonly virtualKeysLink: Locator
  readonly logsLink: Locator
  readonly mcpClientsLink: Locator
  readonly userGroupsLink: Locator
  readonly pluginsLink: Locator
  readonly configLink: Locator

  constructor(page: Page) {
    super(page)
    this.providersLink = page.getByRole('link', { name: /providers/i })
    this.virtualKeysLink = page.getByRole('link', { name: /virtual keys/i })
    this.logsLink = page.getByRole('link', { name: /logs/i })
    this.mcpClientsLink = page.getByRole('link', { name: /mcp/i })
    this.userGroupsLink = page.getByRole('link', { name: /user groups/i })
    this.pluginsLink = page.getByRole('link', { name: /plugins/i })
    this.configLink = page.getByRole('link', { name: /config/i })
  }

  /**
   * Navigate to Providers page
   */
  async goToProviders(): Promise<void> {
    await this.providersLink.click()
    await this.waitForPageLoad()
  }

  /**
   * Navigate to Virtual Keys page
   */
  async goToVirtualKeys(): Promise<void> {
    await this.virtualKeysLink.click()
    await this.waitForPageLoad()
  }

  /**
   * Navigate to Logs page
   */
  async goToLogs(): Promise<void> {
    await this.logsLink.click()
    await this.waitForPageLoad()
  }

  /**
   * Navigate to MCP Clients page
   */
  async goToMCPClients(): Promise<void> {
    await this.mcpClientsLink.click()
    await this.waitForPageLoad()
  }

  /**
   * Navigate to User Groups page
   */
  async goToUserGroups(): Promise<void> {
    await this.userGroupsLink.click()
    await this.waitForPageLoad()
  }

  /**
   * Navigate to Plugins page
   */
  async goToPlugins(): Promise<void> {
    await this.pluginsLink.click()
    await this.waitForPageLoad()
  }

  /**
   * Navigate to Config page
   */
  async goToConfig(): Promise<void> {
    await this.configLink.click()
    await this.waitForPageLoad()
  }
}
