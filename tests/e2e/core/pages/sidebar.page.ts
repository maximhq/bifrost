import { Page, Locator } from '@playwright/test'
import { BasePage } from './base.page'

/**
 * Sidebar navigation page object.
 *
 * Most pages are sub-items of a collapsible group (Models, Governance, ...),
 * which renders its links only while expanded, so navigation goes through
 * openSubItem: expand the group if the link is not there, then click it.
 * Selectors are the sidebar's data-testids (ui/components/sidebar.tsx).
 */
export class SidebarPage extends BasePage {
  // Navigation links
  readonly providersLink: Locator
  readonly virtualKeysLink: Locator
  readonly logsLink: Locator
  readonly mcpClientsLink: Locator
  readonly pluginsLink: Locator
  readonly configLink: Locator

  constructor(page: Page) {
    super(page)
    this.providersLink = this.subItemLink('model-providers')
    this.virtualKeysLink = this.subItemLink('virtual-keys')
    this.logsLink = this.subItemLink('llm-logs')
    this.mcpClientsLink = this.subItemLink('mcp-catalog')
    this.pluginsLink = page.getByRole('link', { name: 'Plugins', exact: true })
    this.configLink = this.subItemLink('client-settings')
  }

  /**
   * Link of a group's sub-item, by its slug (lowercased title, spaces as dashes)
   */
  subItemLink(slug: string): Locator {
    return this.page.getByTestId(`sidebar-subitem-link-${slug}`)
  }

  /**
   * Toggle button of a collapsible group, by its slug
   */
  groupButton(slug: string): Locator {
    return this.page.getByTestId(`sidebar-item-btn-${slug}`)
  }

  /**
   * Click a group's sub-item, expanding the group first when it is collapsed.
   * The group button toggles, so it is only clicked when the link is absent;
   * waiting for the button first means the sidebar has rendered, so an already
   * expanded group (e.g. the current page's) is not collapsed by mistake.
   */
  async openSubItem(group: string, subItem: string): Promise<void> {
    const link = this.subItemLink(subItem)
    await this.groupButton(group).waitFor()
    if (!(await link.isVisible())) {
      await this.groupButton(group).click()
    }
    await link.click()
    await this.waitForPageLoad()
  }

  /**
   * Navigate to Providers page (Models > Model Providers)
   */
  async goToProviders(): Promise<void> {
    await this.openSubItem('models', 'model-providers')
  }

  /**
   * Navigate to Virtual Keys page (Governance > Virtual Keys)
   */
  async goToVirtualKeys(): Promise<void> {
    await this.openSubItem('governance', 'virtual-keys')
  }

  /**
   * Navigate to LLM Logs page (Observability > LLM Logs)
   */
  async goToLogs(): Promise<void> {
    await this.openSubItem('observability', 'llm-logs')
  }

  /**
   * Navigate to MCP Catalog page (MCP Servers > MCP Catalog)
   */
  async goToMCPClients(): Promise<void> {
    await this.openSubItem('mcp-gateway', 'mcp-catalog')
  }

  /**
   * Navigate to Plugins page (top-level link)
   */
  async goToPlugins(): Promise<void> {
    await this.pluginsLink.click()
    await this.waitForPageLoad()
  }

  /**
   * Navigate to Config page (Settings > Client Settings)
   */
  async goToConfig(): Promise<void> {
    await this.openSubItem('settings', 'client-settings')
  }
}
