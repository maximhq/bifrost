import { Locator, Page } from '@playwright/test'
import { BasePage } from '../../../core/pages/base.page'
import { waitForNetworkIdle } from '../../../core/utils/test-helpers'

export class ClaudeCodeGatewayPage extends BasePage {
  readonly view: Locator
  readonly saveBtn: Locator
  readonly enabledSwitch: Locator
  readonly issuerURLInput: Locator
  readonly accessTokenTTLInput: Locator
  readonly allowTempTokenSwitch: Locator
  readonly gatewayURL: Locator
  readonly connectInstructions: Locator
  readonly managedSettingsInput: Locator
  readonly deviceCodeInput: Locator
  readonly deviceContinueBtn: Locator
  readonly deviceError: Locator
  readonly deviceApproved: Locator
  readonly deviceDenied: Locator
  readonly telemetrySwitch: Locator
  readonly telemetryAddBtn: Locator
  readonly telemetryEndpointInput: Locator
  readonly telemetryHeadersInput: Locator
  readonly telemetryDropInput: Locator

  constructor(page: Page) {
    super(page)
    this.view = page.getByTestId('claude-code-view')
    this.saveBtn = page.getByTestId('claude-code-save-btn')
    this.enabledSwitch = page.getByTestId('claude-code-gateway-switch')
    this.issuerURLInput = page.getByTestId('gateways-issuer-url-input')
    this.accessTokenTTLInput = page.getByTestId('gateways-access-token-ttl-input')
    this.allowTempTokenSwitch = page.getByTestId('gateways-allow-temp-token-switch')
    this.gatewayURL = page.getByTestId('claude-code-gateway-url')
    this.connectInstructions = page.getByTestId('claude-code-connect-instructions')
    this.managedSettingsInput = page.getByTestId('claude-code-managed-settings-input')
    this.deviceCodeInput = page.getByTestId('claude-code-device-code-input')
    this.deviceContinueBtn = page.getByTestId('claude-code-device-continue-btn')
    this.deviceError = page.getByTestId('claude-code-device-error')
    this.deviceApproved = page.getByTestId('claude-code-device-approved')
    this.deviceDenied = page.getByTestId('claude-code-device-denied')
    this.telemetrySwitch = page.getByTestId('claude-code-telemetry-switch')
    this.telemetryAddBtn = page.getByTestId('claude-code-telemetry-add-btn')
    this.telemetryEndpointInput = page.getByTestId('claude-code-telemetry-endpoint-input')
    this.telemetryHeadersInput = page.getByTestId('claude-code-telemetry-headers-input')
    this.telemetryDropInput = page.getByTestId('claude-code-telemetry-drop-input')
  }

  async goto(): Promise<void> {
    await this.page.goto('/workspace/config/gateways')
    await waitForNetworkIdle(this.page)
  }

  async gotoDevicePage(query: string): Promise<void> {
    await this.page.goto(`/oauth/device${query}`)
  }
}
