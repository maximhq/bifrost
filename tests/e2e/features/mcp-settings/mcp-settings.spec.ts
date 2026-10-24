import { coreConfigApi } from '../../core/actions/api'
import { expect, test } from '../../core/fixtures/base.fixture'

test.describe('MCP Settings', () => {
  test.beforeEach(async ({ mcpSettingsPage }) => {
    await mcpSettingsPage.goto()
  })

  test('should display MCP settings page', async ({ mcpSettingsPage }) => {
    await expect(mcpSettingsPage.mcpSettingsView).toBeVisible()
  })

  test('should display MCP settings form fields', async ({ mcpSettingsPage }) => {
    await expect(mcpSettingsPage.page.getByLabel('Max Agent Depth')).toBeVisible()
    await expect(mcpSettingsPage.page.getByLabel('Tool Execution Timeout (seconds)')).toBeVisible()
  })

  test('should have save button disabled when no changes', async ({ mcpSettingsPage }) => {
    await expect(mcpSettingsPage.saveBtn).toBeVisible()
    await expect(mcpSettingsPage.saveBtn).toBeDisabled()
  })

  test.describe('Code Mode Limits', () => {
    // Every test here rewrites the shared mcp_code_mode_limits, so they must not interleave.
    test.describe.configure({ mode: 'serial' })
    let originalLimits: unknown

    test.beforeEach(async ({ request }) => {
      originalLimits = (await coreConfigApi.get(request)).client_config.mcp_code_mode_limits
    })

    test.afterEach(async ({ request }) => {
      await coreConfigApi.updateClientConfig(request, { mcp_code_mode_limits: originalLimits ?? {} })
    })

    test('should show every limit with its default as the placeholder', async ({ mcpSettingsPage }) => {
      await expect(mcpSettingsPage.codeModeLimits).toBeVisible()
      const defaults: [string, string][] = [
        ['max_source_bytes', '65536'],
        ['max_steps', '1000000'],
        ['max_memory_bytes', '67108864'],
        ['max_log_bytes', '65536'],
        ['max_tool_calls', '64'],
        ['max_value_bytes', '1048576'],
        ['max_nesting_depth', '64'],
      ]
      for (const [field, placeholder] of defaults) {
        await expect(mcpSettingsPage.codeModeLimitInput(field)).toHaveAttribute('placeholder', placeholder)
      }
    })

    test('should save a limit and keep empty fields on their defaults', async ({ mcpSettingsPage, request }) => {
      await coreConfigApi.updateClientConfig(request, { mcp_code_mode_limits: {} })
      await mcpSettingsPage.goto()

      await mcpSettingsPage.codeModeLimitInput('max_tool_calls').fill('200')
      await expect(mcpSettingsPage.saveBtn).toBeEnabled()
      await mcpSettingsPage.saveBtn.click()
      await mcpSettingsPage.waitForSuccessToast()

      const saved = (await coreConfigApi.get(request)).client_config.mcp_code_mode_limits as Record<string, number>
      expect(saved.max_tool_calls).toBe(200)
      expect(saved.max_steps ?? 0).toBe(0)

      await mcpSettingsPage.goto()
      await expect(mcpSettingsPage.codeModeLimitInput('max_tool_calls')).toHaveValue('200')
      await expect(mcpSettingsPage.codeModeLimitInput('max_steps')).toHaveValue('')
      await expect(mcpSettingsPage.saveBtn).toBeDisabled()
    })

    test('should reject a value size below the minimum without saving', async ({ mcpSettingsPage, request }) => {
      await coreConfigApi.updateClientConfig(request, { mcp_code_mode_limits: {} })
      await mcpSettingsPage.goto()

      await mcpSettingsPage.codeModeLimitInput('max_value_bytes').fill('10')
      await mcpSettingsPage.saveBtn.click()
      await expect(mcpSettingsPage.getToast('error')).toContainText('at least 1024')

      const saved = (await coreConfigApi.get(request)).client_config.mcp_code_mode_limits as Record<string, number> | undefined
      expect(saved?.max_value_bytes ?? 0).toBe(0)
    })
  })

  test.describe('OAuth Private Network Allowlist', () => {
    // Every test here rewrites the shared mcp_oauth_private_allowlist, so they must not interleave.
    test.describe.configure({ mode: 'serial' })
    let originalAllowlist: string[]

    test.beforeEach(async ({ request }) => {
      originalAllowlist = ((await coreConfigApi.get(request)).client_config.mcp_oauth_private_allowlist as string[] | undefined) ?? []
    })

    test.afterEach(async ({ request }) => {
      await coreConfigApi.updateClientConfig(request, { mcp_oauth_private_allowlist: originalAllowlist })
    })

    test('should add an allowlist entry and persist it', async ({ mcpSettingsPage, request }) => {
      const entry = '10.20.0.0/16'
      await coreConfigApi.updateClientConfig(request, { mcp_oauth_private_allowlist: originalAllowlist.filter((e) => e !== entry) })
      await mcpSettingsPage.goto()

      await mcpSettingsPage.oauthPrivateAllowlistInput.fill(entry)
      await mcpSettingsPage.oauthPrivateAllowlistInput.press('Enter')
      await expect(mcpSettingsPage.oauthPrivateAllowlistEntry(entry)).toBeVisible()
      await expect(mcpSettingsPage.saveBtn).toBeEnabled()
      await mcpSettingsPage.saveBtn.click()
      await mcpSettingsPage.waitForSuccessToast()

      const saved = (await coreConfigApi.get(request)).client_config.mcp_oauth_private_allowlist as string[] | undefined
      expect(saved).toContain(entry)

      await mcpSettingsPage.goto()
      await expect(mcpSettingsPage.oauthPrivateAllowlistEntry(entry)).toBeVisible()
      await expect(mcpSettingsPage.saveBtn).toBeDisabled()
    })

    test('should reject a link-local entry without saving', async ({ mcpSettingsPage, request }) => {
      await mcpSettingsPage.goto()

      await mcpSettingsPage.oauthPrivateAllowlistInput.fill('169.254.169.254')
      await mcpSettingsPage.oauthPrivateAllowlistInput.press('Enter')
      await mcpSettingsPage.saveBtn.click()
      await expect(mcpSettingsPage.getToast('error')).toContainText(/mcp_oauth_private_allowlist: invalid allowlist entry/i)

      const saved = (await coreConfigApi.get(request)).client_config.mcp_oauth_private_allowlist as string[] | undefined
      expect(saved ?? []).toEqual(originalAllowlist)
    })
  })

  test.describe('OAuth Allowed Redirect URIs', () => {
    // Every test here rewrites the shared auth mode and oauth2_server_config, so they must not interleave.
    test.describe.configure({ mode: 'serial' })
    let originalAuthMode: unknown
    let originalOAuth2Config: Record<string, unknown> | undefined

    test.beforeEach(async ({ request }) => {
      const client = (await coreConfigApi.get(request)).client_config
      originalAuthMode = client.mcp_server_auth_mode
      originalOAuth2Config = client.oauth2_server_config as Record<string, unknown> | undefined
      // The field is only shown when OAuth discovery is on, which requires an issuer URL.
      await coreConfigApi.updateClientConfig(request, {
        mcp_server_auth_mode: 'both',
        oauth2_server_config: {
          issuer_url: 'https://bifrost.e2e.test',
          auth_code_ttl: 300,
          access_token_ttl: 600,
          allowed_redirect_uris: [],
        },
      })
    })

    test.afterEach(async ({ request }) => {
      await coreConfigApi.updateClientConfig(request, {
        mcp_server_auth_mode: originalAuthMode ?? 'headers',
        oauth2_server_config: originalOAuth2Config,
      })
    })

    test('should add an allowed redirect URI and persist it', async ({ mcpSettingsPage, request }) => {
      const uri = 'https://www.cursor.com/agents/mcp/oauth/callback'
      await mcpSettingsPage.goto()
      await mcpSettingsPage.advancedTrigger.click()

      await mcpSettingsPage.allowedRedirectUrisInput.fill(uri)
      await mcpSettingsPage.allowedRedirectUrisInput.press('Enter')
      await expect(mcpSettingsPage.allowedRedirectUriEntry(uri)).toBeVisible()
      await expect(mcpSettingsPage.saveBtn).toBeEnabled()
      await mcpSettingsPage.saveBtn.click()
      await mcpSettingsPage.waitForSuccessToast()

      const saved = (await coreConfigApi.get(request)).client_config.oauth2_server_config as { allowed_redirect_uris?: string[] } | undefined
      expect(saved?.allowed_redirect_uris).toEqual([uri])

      await mcpSettingsPage.goto()
      await mcpSettingsPage.advancedTrigger.click()
      await expect(mcpSettingsPage.allowedRedirectUriEntry(uri)).toBeVisible()
      await expect(mcpSettingsPage.saveBtn).toBeDisabled()
    })

    test('should save a bare wildcard and warn about it', async ({ mcpSettingsPage, request }) => {
      await mcpSettingsPage.goto()
      await mcpSettingsPage.advancedTrigger.click()
      await expect(mcpSettingsPage.allowedRedirectUrisWildcardWarning).toBeHidden()

      await mcpSettingsPage.allowedRedirectUrisInput.fill('*')
      await mcpSettingsPage.allowedRedirectUrisInput.press('Enter')
      await expect(mcpSettingsPage.allowedRedirectUrisWildcardWarning).toBeVisible()
      await mcpSettingsPage.saveBtn.click()
      await mcpSettingsPage.waitForSuccessToast()

      const saved = (await coreConfigApi.get(request)).client_config.oauth2_server_config as { allowed_redirect_uris?: string[] } | undefined
      expect(saved?.allowed_redirect_uris).toEqual(['*'])
    })

    test('should reject a partial wildcard without saving', async ({ mcpSettingsPage, request }) => {
      await mcpSettingsPage.goto()
      await mcpSettingsPage.advancedTrigger.click()

      await mcpSettingsPage.allowedRedirectUrisInput.fill('https://*.e2e.test/callback')
      await mcpSettingsPage.allowedRedirectUrisInput.press('Enter')
      await mcpSettingsPage.saveBtn.click()
      await expect(mcpSettingsPage.getToast('error')).toContainText(/allowed_redirect_uris supports only a bare "\*" wildcard/i)

      const saved = (await coreConfigApi.get(request)).client_config.oauth2_server_config as { allowed_redirect_uris?: string[] } | undefined
      expect(saved?.allowed_redirect_uris ?? []).toEqual([])
    })

    test('should show the issuer read-only with a link to Security', async ({ mcpSettingsPage, page }) => {
      await mcpSettingsPage.goto()
      await mcpSettingsPage.advancedTrigger.click()

      await expect(mcpSettingsPage.issuerUrlValue).toHaveText('https://bifrost.e2e.test')
      await mcpSettingsPage.issuerUrlSecurityLink.click()
      await expect(page).toHaveURL(/\/workspace\/config\/security/)
      await expect(mcpSettingsPage.securityIssuerUrlInput).toBeVisible()
    })

    test('should show an env issuer with its reference beside the resolved URL', async ({ mcpSettingsPage }) => {
      await mcpSettingsPage.serveIssuer({ value: 'https://bifrost.e2e.test', ref: 'env.BIFROST_ISSUER', type: 'env' })
      await mcpSettingsPage.goto()
      await mcpSettingsPage.advancedTrigger.click()

      await expect(mcpSettingsPage.issuerUrlValue).toHaveText('https://bifrost.e2e.test')
      await expect(mcpSettingsPage.issuerUrlRef).toHaveText('env.BIFROST_ISSUER')
      await expect(mcpSettingsPage.issuerUrlUnresolved).toBeHidden()
    })

    test('should show an unresolved env issuer as not resolved rather than unset', async ({ mcpSettingsPage }) => {
      await mcpSettingsPage.serveIssuer({ value: '', ref: 'env.BIFROST_ISSUER', type: 'env' })
      await mcpSettingsPage.goto()
      await mcpSettingsPage.advancedTrigger.click()

      await expect(mcpSettingsPage.issuerUrlUnresolved).toBeVisible()
      await expect(mcpSettingsPage.issuerUrlRef).toHaveText('env.BIFROST_ISSUER')
      await expect(mcpSettingsPage.issuerUrlValue).toBeHidden()
    })

    test('should show a plain issuer without a reference', async ({ mcpSettingsPage }) => {
      await mcpSettingsPage.goto()
      await mcpSettingsPage.advancedTrigger.click()

      await expect(mcpSettingsPage.issuerUrlValue).toHaveText('https://bifrost.e2e.test')
      await expect(mcpSettingsPage.issuerUrlRef).toBeHidden()
    })

    test('should refuse to clear the issuer under Security while OAuth mode is on', async ({ mcpSettingsPage, request }) => {
      await mcpSettingsPage.gotoSecurity()

      await mcpSettingsPage.securityIssuerUrlInput.fill('')
      await expect(mcpSettingsPage.securitySaveBtn).toBeEnabled()
      await mcpSettingsPage.securitySaveBtn.click()
      await expect(mcpSettingsPage.getToast('error')).toContainText(/Issuer URL is required while MCP auth mode is OAuth or Both/i)

      const saved = (await coreConfigApi.get(request)).client_config.oauth2_server_config as { issuer_url?: unknown } | undefined
      expect(JSON.stringify(saved?.issuer_url)).toContain('https://bifrost.e2e.test')
    })

    test('should warn and save a changed issuer under Security', async ({ mcpSettingsPage, request }) => {
      await mcpSettingsPage.gotoSecurity()

      await mcpSettingsPage.securityIssuerUrlInput.fill('https://gateway.e2e.test')
      await expect(mcpSettingsPage.securityIssuerUrlChangeWarning).toBeVisible()
      await mcpSettingsPage.securitySaveBtn.click()
      await expect(mcpSettingsPage.getToast('success')).toBeVisible()

      const saved = (await coreConfigApi.get(request)).client_config.oauth2_server_config as { issuer_url?: unknown } | undefined
      expect(JSON.stringify(saved?.issuer_url)).toContain('https://gateway.e2e.test')
    })

    test('should reject a non-https remote redirect URI without saving', async ({ mcpSettingsPage, request }) => {
      await mcpSettingsPage.goto()
      await mcpSettingsPage.advancedTrigger.click()

      await mcpSettingsPage.allowedRedirectUrisInput.fill('http://app.e2e.test/callback')
      await mcpSettingsPage.allowedRedirectUrisInput.press('Enter')
      await mcpSettingsPage.saveBtn.click()
      await expect(mcpSettingsPage.getToast('error')).toContainText(/allowed_redirect_uris contains an invalid callback URI/i)

      const saved = (await coreConfigApi.get(request)).client_config.oauth2_server_config as { allowed_redirect_uris?: string[] } | undefined
      expect(saved?.allowed_redirect_uris ?? []).toEqual([])
    })
  })
})
