import type { Page } from '@playwright/test'
import { DefaultCoreConfig } from '../../../../ui/lib/types/config'
import { expect, test } from '../../core/fixtures/base.fixture'

// The gateway's configuration is mocked so the shared test gateway is never
// reconfigured; the assertions read the PUT the settings form submits.
const mockConfig = async (page: Page, issuer?: string) => {
  await page.route('**/api/**', async route => {
    const path = new URL(route.request().url()).pathname
    if (path === '/api/config') {
      if (route.request().method() === 'PUT') {
        await route.fulfill({ json: { status: 'success', message: 'configuration updated successfully' } })
        return
      }
      await route.fulfill({ json: {
        client_config: {
          ...DefaultCoreConfig,
          mcp_server_auth_mode: 'headers',
          oauth2_server_config: issuer ? { issuer_url: { value: issuer, ref: '', type: 'plain_text' }, auth_code_ttl: 300, access_token_ttl: 600 } : undefined,
        },
        auth_config: null, framework_config: {}, is_db_connected: true, metadata: { onboarding_dismissed: true },
      } })
    } else if (path === '/api/version') {
      await route.fulfill({ json: '1.0.0' })
    } else if (path === '/api/session/is-auth-enabled') {
      await route.fulfill({ json: { is_auth_enabled: false, has_valid_token: false, auth_type: 'none', inference_auth_enforced: false } })
    } else {
      await route.fulfill({ json: {} })
    }
  })
}

test.describe('Claude Code gateway settings', () => {
  test.use({ skipAutoLogin: true })

  test('enables sign-in with MCP on header auth and saves validated managed settings', async ({ page, claudeCodeGatewayPage }) => {
    await mockConfig(page, 'https://bifrost.example.com')
    await claudeCodeGatewayPage.goto()

    await expect(claudeCodeGatewayPage.enabledSwitch).not.toBeChecked()
    await expect(claudeCodeGatewayPage.connectInstructions).toHaveCount(0)
    await claudeCodeGatewayPage.enabledSwitch.click()
    await expect(claudeCodeGatewayPage.gatewayURL).toHaveText('https://bifrost.example.com/claude-code')
    await expect(claudeCodeGatewayPage.connectInstructions).toContainText('"forceLoginGatewayUrl": "https://bifrost.example.com/claude-code"')

    // Malformed JSON is refused before anything is sent.
    let puts = 0
    page.on('request', r => { if (new URL(r.url()).pathname === '/api/config' && r.method() === 'PUT') puts++ })
    await claudeCodeGatewayPage.managedSettingsInput.fill('{"env": ')
    await claudeCodeGatewayPage.saveBtn.click()
    const invalidToast = page.getByText('Managed settings must be valid JSON.')
    await expect(invalidToast).toBeVisible()
    expect(puts).toBe(0)
    // The toast overlaps the save button; let it clear before saving again.
    await expect(invalidToast).toBeHidden({ timeout: 15000 })

    await claudeCodeGatewayPage.managedSettingsInput.fill('{"env": {"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY": "1"}}')
    const submitted = page.waitForRequest(r => new URL(r.url()).pathname === '/api/config' && r.method() === 'PUT')
    await claudeCodeGatewayPage.saveBtn.click()
    const clientConfig = (await submitted).postDataJSON().client_config
    // /mcp keeps its auth mode: the gateway does not depend on it.
    expect(clientConfig.mcp_server_auth_mode).toBe('headers')
    expect(clientConfig.oauth2_server_config.issuer_url.value).toBe('https://bifrost.example.com')
    expect(clientConfig.oauth2_server_config.claude_code_gateway).toEqual({
      enabled: true,
      managed_settings: { env: { CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY: '1' } },
    })
  })

  test('requires an issuer URL before enabling', async ({ page, claudeCodeGatewayPage }) => {
    await mockConfig(page)
    await claudeCodeGatewayPage.goto()
    let puts = 0
    page.on('request', r => { if (new URL(r.url()).pathname === '/api/config' && r.method() === 'PUT') puts++ })
    await claudeCodeGatewayPage.enabledSwitch.click()
    await claudeCodeGatewayPage.saveBtn.click()
    await expect(page.getByText('Set the issuer URL before enabling Claude Code sign-in.')).toBeVisible()
    expect(puts).toBe(0)
  })
})

test.describe('Claude Code device sign-in page', () => {
  test.use({ skipAutoLogin: true })

  test('pre-fills the code without submitting it', async ({ page, claudeCodeGatewayPage }) => {
    let verifyCalls = 0
    await page.route('**/claude-code/device/verify', async route => {
      verifyCalls++
      expect(route.request().postDataJSON()).toEqual({ user_code: 'BCDF-GHJK' })
      await route.fulfill({ status: 404, json: { error: { message: 'this code is not valid or has expired; run /login in Claude Code again' } } })
    })
    await claudeCodeGatewayPage.gotoDevicePage('?user_code=bcdfghjk')

    await expect(claudeCodeGatewayPage.deviceCodeInput).toHaveValue('BCDF-GHJK')
    expect(verifyCalls).toBe(0)

    await claudeCodeGatewayPage.deviceContinueBtn.click()
    await expect(claudeCodeGatewayPage.deviceError).toContainText('not valid or has expired')
    expect(verifyCalls).toBe(1)
  })

  test('confirms an approved sign-in', async ({ claudeCodeGatewayPage }) => {
    await claudeCodeGatewayPage.gotoDevicePage('?approved=1')
    await expect(claudeCodeGatewayPage.deviceApproved).toContainText("You're signed in")
  })
})
