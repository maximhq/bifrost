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
  readonly issuerUrlValue: Locator
  readonly issuerUrlRef: Locator
  readonly issuerUrlUnresolved: Locator
  readonly issuerUrlSecurityLink: Locator
  readonly securityIssuerUrlInput: Locator
  readonly securityIssuerUrlChangeWarning: Locator
  readonly securitySaveBtn: Locator
  readonly maxProtocolVersionSelect: Locator

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
    this.issuerUrlValue = page.getByTestId('oauth2-issuer-url-value')
    this.issuerUrlRef = page.getByTestId('oauth2-issuer-url-ref')
    this.issuerUrlUnresolved = page.getByTestId('oauth2-issuer-url-unresolved')
    this.issuerUrlSecurityLink = page.getByTestId('oauth2-issuer-url-security-link')
    // The issuer is edited under Settings > Security, since Cross App Access uses it too.
    this.securityIssuerUrlInput = page.getByTestId('oauth2-issuer-url-input')
    this.securityIssuerUrlChangeWarning = page.getByTestId('oauth2-issuer-url-change-warning')
    this.securitySaveBtn = page.getByTestId('security-save-btn')
    this.maxProtocolVersionSelect = page.getByTestId('mcp-max-protocol-version-select')
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

  /** Picks a global max protocol version ('auto' or a version) from the select. */
  async selectMaxProtocolVersion(value: string): Promise<void> {
    await this.maxProtocolVersionSelect.click()
    await this.page.getByTestId(`mcp-max-protocol-version-${value}`).click()
  }

  async goto(): Promise<void> {
    await this.page.goto('/workspace/mcp-settings')
    await waitForNetworkIdle(this.page)
  }

  /**
   * Serves the real config with the OAuth issuer replaced, for issuer states the gateway cannot be put
   * in from a test: an env reference, resolved or not, depends on the gateway's own environment.
   */
  async serveIssuer(issuer: { value: string; ref: string; type: 'env' | 'vault' }): Promise<void> {
    await this.page.route('**/api/config**', async (route) => {
      if (route.request().method() !== 'GET') {
        await route.fallback()
        return
      }
      const response = await route.fetch()
      const body = await response.json()
      body.client_config.oauth2_server_config = { ...body.client_config.oauth2_server_config, issuer_url: issuer }
      await route.fulfill({ response, json: body })
    })
  }

  async gotoSecurity(): Promise<void> {
    await this.page.goto('/workspace/config/security')
    await waitForNetworkIdle(this.page)
  }
}
