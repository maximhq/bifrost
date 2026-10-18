import { Locator, Page } from '@playwright/test'
import { BasePage } from '../../../core/pages/base.page'
import { waitForNetworkIdle } from '../../../core/utils/test-helpers'

export class MCPSettingsPage extends BasePage {
  readonly mcpSettingsView: Locator
  readonly saveBtn: Locator
  readonly maxAgentDepthInput: Locator
  readonly toolTimeoutInput: Locator
  readonly codeModeLimits: Locator
  readonly oauthPrivateAllowlistInput: Locator
  readonly advancedTrigger: Locator
  readonly allowedRedirectUrisInput: Locator
  readonly allowedRedirectUrisWildcardWarning: Locator
  readonly issuerUrlInput: Locator

  constructor(page: Page) {
    super(page)

    this.mcpSettingsView = page.getByTestId('mcp-settings-view')
    this.saveBtn = page.getByTestId('mcp-settings-save-btn')
    this.maxAgentDepthInput = page.getByTestId('mcp-agent-depth-input').or(page.locator('#mcp-agent-depth'))
    this.toolTimeoutInput = page.getByTestId('mcp-tool-timeout-input').or(page.locator('#mcp-tool-execution-timeout'))
    this.codeModeLimits = page.getByTestId('mcp-code-mode-limits')
    this.oauthPrivateAllowlistInput = page.getByTestId('mcp-oauth-private-allowlist-input')
    this.advancedTrigger = page.getByTestId('mcp-settings-advanced-trigger')
    this.allowedRedirectUrisInput = page.getByTestId('oauth2-allowed-redirect-uris-input')
    this.allowedRedirectUrisWildcardWarning = page.getByTestId('oauth2-allowed-redirect-uris-wildcard-warning')
    this.issuerUrlInput = page.getByTestId('oauth2-issuer-url-input')
  }

  /** The remove button of one OAuth private allowlist entry, present while the entry is listed. */
  oauthPrivateAllowlistEntry(entry: string): Locator {
    return this.page.getByRole('button', { name: `Remove ${entry}`, exact: true })
  }

  /** The remove button of one allowed OAuth redirect URI, present while the URI is listed. */
  allowedRedirectUriEntry(uri: string): Locator {
    return this.page.getByRole('button', { name: `Remove ${uri}`, exact: true })
  }

  /** Input for one code mode limit, keyed by its config field (e.g. 'max_tool_calls'). */
  codeModeLimitInput(field: string): Locator {
    return this.page.getByTestId(`mcp-code-mode-${field.replace(/_/g, '-')}-input`)
  }

  async goto(): Promise<void> {
    await this.page.goto('/workspace/mcp-settings')
    await waitForNetworkIdle(this.page)
  }
}
