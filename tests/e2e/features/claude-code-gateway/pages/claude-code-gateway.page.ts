import { Locator, Page } from '@playwright/test'
import { BasePage } from '../../../core/pages/base.page'
import { waitForNetworkIdle } from '../../../core/utils/test-helpers'

export class ClaudeCodeGatewayPage extends BasePage {
  readonly view: Locator
  readonly saveBtn: Locator
  readonly enabledSwitch: Locator
  readonly issuerURLInput: Locator
  readonly gatewayURL: Locator
  readonly connectInstructions: Locator
  readonly managedSettingsInput: Locator
  readonly deviceCodeInput: Locator
  readonly deviceContinueBtn: Locator
  readonly deviceError: Locator
  readonly deviceApproved: Locator

  constructor(page: Page) {
    super(page)
    this.view = page.getByTestId('claude-code-view')
    this.saveBtn = page.getByTestId('claude-code-save-btn')
    this.enabledSwitch = page.getByTestId('claude-code-gateway-switch')
    this.issuerURLInput = page.getByTestId('claude-code-issuer-url-input')
    this.gatewayURL = page.getByTestId('claude-code-gateway-url')
    this.connectInstructions = page.getByTestId('claude-code-connect-instructions')
    this.managedSettingsInput = page.getByTestId('claude-code-managed-settings-input')
    this.deviceCodeInput = page.getByTestId('claude-code-device-code-input')
    this.deviceContinueBtn = page.getByTestId('claude-code-device-continue-btn')
    this.deviceError = page.getByTestId('claude-code-device-error')
    this.deviceApproved = page.getByTestId('claude-code-device-approved')
  }

  async goto(): Promise<void> {
    await this.page.goto('/workspace/config/claude-code')
    await waitForNetworkIdle(this.page)
  }

  async gotoDevicePage(query: string): Promise<void> {
    await this.page.goto(`/oauth/device${query}`)
  }
}
