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
    await expect(claudeCodeGatewayPage.gatewayURL).toHaveText('https://bifrost.example.com/gateway/claude-code')
    await expect(claudeCodeGatewayPage.connectInstructions).toContainText('"forceLoginGatewayUrl": "https://bifrost.example.com/gateway/claude-code"')

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
    expect(clientConfig.oauth2_server_config.gateways.claude_code).toEqual({
      enabled: true,
      managed_settings: { env: { CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY: '1' } },
    })
  })

  test('forwards telemetry to a collector with reference-only headers', async ({ page, claudeCodeGatewayPage }) => {
    await mockConfig(page, 'https://bifrost.example.com')
    await claudeCodeGatewayPage.goto()
    await claudeCodeGatewayPage.enabledSwitch.click()
    await claudeCodeGatewayPage.telemetrySwitch.click()
    await claudeCodeGatewayPage.telemetryAddBtn.click()
    await claudeCodeGatewayPage.telemetryEndpointInput.fill('https://otel-collector.example.com')

    // A plain-text credential is refused before anything is sent.
    let puts = 0
    page.on('request', r => { if (new URL(r.url()).pathname === '/api/config' && r.method() === 'PUT') puts++ })
    await claudeCodeGatewayPage.telemetryHeaderNameInput.fill('authorization')
    await claudeCodeGatewayPage.telemetryHeaderValueInput.fill('Bearer abc')
    await claudeCodeGatewayPage.saveBtn.click()
    const refusal = page.getByText('The authorization header must be an env. or vault. reference.')
    await expect(refusal).toBeVisible()
    expect(puts).toBe(0)
    await expect(refusal).toBeHidden({ timeout: 15000 })

    await claudeCodeGatewayPage.telemetryHeaderValueInput.fill('env.OTEL_COLLECTOR_TOKEN')
    await claudeCodeGatewayPage.telemetryDropInput.fill('prompt, tool_input')
    const submitted = page.waitForRequest(r => new URL(r.url()).pathname === '/api/config' && r.method() === 'PUT')
    await claudeCodeGatewayPage.saveBtn.click()
    const clientConfig = (await submitted).postDataJSON().client_config
    expect(clientConfig.oauth2_server_config.gateways.claude_code.telemetry).toEqual({
      enabled: true,
      destinations: [{ endpoint: 'https://otel-collector.example.com', headers: { authorization: 'env.OTEL_COLLECTOR_TOKEN' } }],
      drop_attributes: ['prompt', 'tool_input'],
    })
  })

  test('edits the shared access token lifetime in minutes and refuses one under a minute', async ({ page, claudeCodeGatewayPage }) => {
    await mockConfig(page, 'https://bifrost.example.com')
    await claudeCodeGatewayPage.goto()
    await expect(claudeCodeGatewayPage.accessTokenTTLInput).toHaveValue('10')

    let puts = 0
    page.on('request', r => { if (new URL(r.url()).pathname === '/api/config' && r.method() === 'PUT') puts++ })
    await claudeCodeGatewayPage.accessTokenTTLInput.fill('0')
    await claudeCodeGatewayPage.saveBtn.click()
    const invalidToast = page.getByText('The access token lifetime must be at least 1 minute.')
    await expect(invalidToast).toBeVisible()
    expect(puts).toBe(0)
    await expect(invalidToast).toBeHidden({ timeout: 15000 })

    await claudeCodeGatewayPage.accessTokenTTLInput.fill('5')
    const submitted = page.waitForRequest(r => new URL(r.url()).pathname === '/api/config' && r.method() === 'PUT')
    await claudeCodeGatewayPage.saveBtn.click()
    const clientConfig = (await submitted).postDataJSON().client_config
    expect(clientConfig.oauth2_server_config.access_token_ttl).toBe(300)
    expect(clientConfig.mcp_server_auth_mode).toBe('headers')
  })

  test('turns on approval without a Bifrost account, shared with MCP', async ({ page, claudeCodeGatewayPage }) => {
    await mockConfig(page, 'https://bifrost.example.com')
    await claudeCodeGatewayPage.goto()
    await expect(claudeCodeGatewayPage.allowTempTokenSwitch).not.toBeChecked()
    await claudeCodeGatewayPage.allowTempTokenSwitch.click()
    const submitted = page.waitForRequest(r => new URL(r.url()).pathname === '/api/config' && r.method() === 'PUT')
    await claudeCodeGatewayPage.saveBtn.click()
    const clientConfig = (await submitted).postDataJSON().client_config
    expect(clientConfig.mcp_enable_temp_token_auth).toBe(true)
    expect(clientConfig.mcp_server_auth_mode).toBe('headers')
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
    await page.route('**/oauth2/device/verify', async route => {
      verifyCalls++
      expect(route.request().postDataJSON()).toEqual({ user_code: 'BCDF-GHJK' })
      // The shared handler's own answer for an unknown or expired code.
      await route.fulfill({ status: 404, json: { error: { message: 'this code is not valid or has expired; start the sign-in again from your terminal' } } })
    })
    await claudeCodeGatewayPage.gotoDevicePage('?user_code=bcdfghjk')

    // The page serves every client gateway, so it never names one.
    await expect(page.getByRole('heading', { name: 'Sign in to Bifrost' })).toBeVisible()
    await expect(page.getByText('Confirm the code shown in your terminal matches the one below.')).toBeVisible()
    await expect(page.getByText('Claude Code')).toHaveCount(0)
    await expect(claudeCodeGatewayPage.deviceCodeInput).toHaveValue('BCDF-GHJK')
    expect(verifyCalls).toBe(0)

    await claudeCodeGatewayPage.deviceContinueBtn.click()
    await expect(claudeCodeGatewayPage.deviceError).toHaveText('this code is not valid or has expired; start the sign-in again from your terminal')
    expect(verifyCalls).toBe(1)
  })

  test('falls back to client-neutral guidance when verify gives no reason', async ({ page, claudeCodeGatewayPage }) => {
    await page.route('**/oauth2/device/verify', route => route.fulfill({ status: 500, body: '' }))
    await claudeCodeGatewayPage.gotoDevicePage('?user_code=bcdfghjk')
    await claudeCodeGatewayPage.deviceContinueBtn.click()
    await expect(claudeCodeGatewayPage.deviceError).toHaveText('This code could not be verified. Start the sign-in again from your terminal.')
  })

  test('stays on the code entry for a signed-out visitor while dashboard auth is on', async ({ page, claudeCodeGatewayPage }) => {
    // A developer signing in with a virtual key has no dashboard session: every
    // protected API call 401s, which the dashboard answers with a /login redirect.
    await page.route('**/api/**', async route => {
      const path = new URL(route.request().url()).pathname
      if (path === '/api/session/is-auth-enabled') {
        await route.fulfill({ json: { is_auth_enabled: true, has_valid_token: false, auth_type: 'password', inference_auth_enforced: true } })
      } else if (path === '/api/version') {
        await route.fulfill({ json: '1.0.0' })
      } else {
        await route.fulfill({ status: 401, json: { error: { message: 'Unauthorized' } } })
      }
    })
    await claudeCodeGatewayPage.gotoDevicePage('?user_code=bcdfghjk')

    await expect(claudeCodeGatewayPage.deviceCodeInput).toHaveValue('BCDF-GHJK')
    // Let any 401 and its redirect play out before checking where the page is.
    await page.waitForLoadState('networkidle')
    await expect(page).toHaveURL(/\/oauth\/device\?user_code=bcdfghjk$/)
    await expect(claudeCodeGatewayPage.deviceCodeInput).toBeVisible()
  })

  test('confirms an approved sign-in', async ({ claudeCodeGatewayPage }) => {
    await claudeCodeGatewayPage.gotoDevicePage('?approved=1')
    await expect(claudeCodeGatewayPage.deviceApproved).toContainText("You're signed in")
    await expect(claudeCodeGatewayPage.deviceApproved).toContainText('Return to your terminal to finish.')
    await expect(claudeCodeGatewayPage.deviceApproved).not.toContainText('Claude Code')
  })

  test('confirms a denied sign-in', async ({ claudeCodeGatewayPage }) => {
    await claudeCodeGatewayPage.gotoDevicePage('?denied=1')
    await expect(claudeCodeGatewayPage.deviceDenied).toContainText('Sign-in denied')
    await expect(claudeCodeGatewayPage.deviceApproved).toHaveCount(0)
  })
})
