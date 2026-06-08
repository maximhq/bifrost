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
  readonly alertingButton: Locator
  readonly alertingChannelsLink: Locator
  readonly alertingRulesLink: Locator
  readonly alertingHistoryLink: Locator
  readonly configLink: Locator

  constructor(page: Page) {
    super(page)
    this.providersLink = page.getByRole('link', { name: /providers/i })
    this.virtualKeysLink = page.getByRole('link', { name: /virtual keys/i })
    this.logsLink = page.getByRole('link', { name: /logs/i })
    this.mcpClientsLink = page.getByRole('link', { name: /mcp/i })
    this.userGroupsLink = page.getByRole('link', { name: /user groups/i })
    this.pluginsLink = page.getByRole('link', { name: /plugins/i })
    this.alertingButton = page.getByTestId('sidebar-item-btn-alerting')
    this.alertingChannelsLink = page.getByTestId('sidebar-subitem-link-channels')
    this.alertingRulesLink = page.getByTestId('sidebar-subitem-link-rules')
    this.alertingHistoryLink = page.getByTestId('sidebar-subitem-link-history')
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
   * Ensure the Alerting submenu is expanded before clicking a subitem.
   * The Alerting button toggles the submenu, so only click it when the
   * target link is not already visible to avoid collapsing an open menu.
   */
  private async expandAlertingIfNeeded(targetLink: Locator): Promise<void> {
    if (!(await targetLink.isVisible())) {
      await this.alertingButton.click()
    }
  }

  /**
   * Navigate to Alerting Channels page
   */
  async goToAlertingChannels(): Promise<void> {
    await this.expandAlertingIfNeeded(this.alertingChannelsLink)
    await this.alertingChannelsLink.click()
    await this.waitForPageLoad()
  }

  /**
   * Navigate to Alerting Rules page
   */
  async goToAlertingRules(): Promise<void> {
    await this.expandAlertingIfNeeded(this.alertingRulesLink)
    await this.alertingRulesLink.click()
    await this.waitForPageLoad()
  }

  /**
   * Navigate to Alerting History page
   */
  async goToAlertingHistory(): Promise<void> {
    await this.expandAlertingIfNeeded(this.alertingHistoryLink)
    await this.alertingHistoryLink.click()
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
