import { coreConfigApi, managedClientsApi, virtualKeysApi } from '../../core/actions/api'
import { IdJagIssuer } from '../../core/utils/idjagIssuer'
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

    test('should refuse to save OAuth mode with an empty issuer URL', async ({ mcpSettingsPage, request }) => {
      await mcpSettingsPage.goto()
      await mcpSettingsPage.advancedTrigger.click()

      await mcpSettingsPage.issuerUrlInput.fill('')
      await expect(mcpSettingsPage.saveBtn).toBeEnabled()
      await mcpSettingsPage.saveBtn.click()
      await expect(mcpSettingsPage.getToast('error')).toContainText(/Issuer URL is required when MCP auth mode is OAuth or Both/i)

      const saved = (await coreConfigApi.get(request)).client_config.oauth2_server_config as { issuer_url?: unknown } | undefined
      expect(JSON.stringify(saved?.issuer_url)).toContain('https://bifrost.e2e.test')
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

// Enterprise-managed authorization: a client presents an IdP-signed ID-JAG at
// /oauth2/token (jwt-bearer grant) and receives an access-only /mcp token.
test.describe('Enterprise-managed authorization', () => {
  // Rewrites the shared auth mode and oauth2_server_config, so tests must not interleave.
  test.describe.configure({ mode: 'serial' })
  const issuerURL = 'https://bifrost.e2e.test'
  const idp = new IdJagIssuer()
  let originalAuthMode: unknown
  let originalOAuth2Config: Record<string, unknown> | undefined
  let originalAllowlist: unknown
  let vkID = ''
  const createdClients: string[] = []
  // The UI-created client has no id here, so it is swept by its run-scoped name.
  const uiClientName = `e2e ui client ${Date.now()}`

  test.beforeAll(async ({ request }) => {
    await idp.start()
    const client = (await coreConfigApi.get(request)).client_config
    originalAuthMode = client.mcp_server_auth_mode
    originalOAuth2Config = client.oauth2_server_config as Record<string, unknown> | undefined
    originalAllowlist = client.mcp_oauth_private_allowlist
    await coreConfigApi.updateClientConfig(request, {
      mcp_server_auth_mode: 'both',
      // The fake IdP listens on loopback, which the guarded key fetch refuses unless allowlisted.
      mcp_oauth_private_allowlist: [...((originalAllowlist as string[] | undefined) ?? []), '127.0.0.1'],
      oauth2_server_config: {
        issuer_url: issuerURL,
        auth_code_ttl: 300,
        access_token_ttl: 600,
        managed_auth: { enabled: true, trusted_issuer: idp.issuer },
      },
    })
    const vk = (await virtualKeysApi.create(request, { name: `e2e-managed-auth-${Date.now()}`, is_active: true })) as {
      virtual_key: { id: string }
    }
    vkID = vk.virtual_key.id
  })

  test.afterAll(async ({ request }) => {
    const { clients } = await managedClientsApi.list(request)
    for (const c of clients) if (createdClients.includes(c.id) || c.name === uiClientName) await managedClientsApi.delete(request, c.id)
    if (vkID) await virtualKeysApi.delete(request, vkID)
    // Managed auth must be off before the mode can leave both/oauth, and an undefined
    // original is dropped from the body (which keeps the current block), so restore
    // in two steps: the original block, else the current one without managed_auth.
    const current = (await coreConfigApi.get(request)).client_config.oauth2_server_config as Record<string, unknown> | undefined
    const { managed_auth: _, ...withoutManagedAuth } = current ?? {}
    await coreConfigApi.updateClientConfig(request, { oauth2_server_config: originalOAuth2Config ?? withoutManagedAuth })
    await coreConfigApi.updateClientConfig(request, {
      mcp_server_auth_mode: originalAuthMode ?? 'headers',
      mcp_oauth_private_allowlist: originalAllowlist ?? [],
    })
    await idp.stop()
  })

  test('advertises the jwt-bearer grant and ID-JAG profile', async ({ request }) => {
    const meta = await (await request.get('/.well-known/oauth-authorization-server')).json()
    expect(meta.grant_types_supported).toContain('urn:ietf:params:oauth:grant-type:jwt-bearer')
    expect(meta.authorization_grant_profiles_supported).toEqual(['urn:ietf:params:oauth:grant-profile:id-jag'])
    expect(meta.token_endpoint_auth_methods_supported[0]).toBe('none')
  })

  test('exchanges an ID-JAG for an access-only token that /mcp accepts', async ({ request }) => {
    const client = await managedClientsApi.create(request, { name: 'e2e confidential', identity: 'vk', virtual_key_id: vkID })
    createdClients.push(client.id)
    expect(client.client_secret).toBeTruthy()
    const basic = Buffer.from(`${encodeURIComponent(client.client_id)}:${encodeURIComponent(client.client_secret!)}`).toString('base64')

    const tokenRes = await request.post('/oauth2/token', {
      headers: { Authorization: `Basic ${basic}` },
      form: {
        grant_type: 'urn:ietf:params:oauth:grant-type:jwt-bearer',
        assertion: idp.mint(issuerURL, client.client_id, { resource: `${issuerURL}/mcp` }),
        resource: `${issuerURL}/mcp`,
      },
    })
    expect(tokenRes.status()).toBe(200)
    const token = await tokenRes.json()
    expect(token.refresh_token).toBeUndefined()
    expect(token.expires_in).toBe(86400)
    expect(token.token_type).toBe('Bearer')

    const mcpRes = await request.post('/mcp', {
      headers: { Authorization: `Bearer ${token.access_token}`, Accept: 'application/json, text/event-stream' },
      data: { jsonrpc: '2.0', id: 1, method: 'initialize', params: { protocolVersion: '2025-06-18', capabilities: {}, clientInfo: { name: 'e2e', version: '1' } } },
    })
    expect(mcpRes.status()).toBe(200)
  })

  test('rejects bad client credentials and foreign assertions', async ({ request }) => {
    const publicClient = await managedClientsApi.create(request, { name: 'e2e public', public: true, identity: 'vk', virtual_key_id: vkID })
    createdClients.push(publicClient.id)
    const exchange = (form: Record<string, string>) =>
      request.post('/oauth2/token', { form: { grant_type: 'urn:ietf:params:oauth:grant-type:jwt-bearer', ...form } })

    const wrongAudience = await exchange({ client_id: publicClient.client_id, assertion: idp.mint('https://other.example.com', publicClient.client_id) })
    expect(wrongAudience.status()).toBe(400)
    expect((await wrongAudience.json()).error).toBe('invalid_grant')

    const otherClient = await exchange({ client_id: publicClient.client_id, assertion: idp.mint(issuerURL, 'someone-else') })
    expect((await otherClient.json()).error).toBe('invalid_grant')

    const secretOnPublic = await exchange({ client_id: publicClient.client_id, client_secret: 'x', assertion: idp.mint(issuerURL, publicClient.client_id) })
    expect(secretOnPublic.status()).toBe(401)
    expect((await secretOnPublic.json()).error).toBe('invalid_client')

    const unknown = await exchange({ client_id: 'not-registered', assertion: idp.mint(issuerURL, 'not-registered') })
    expect((await unknown.json()).error).toBe('invalid_client')
  })

  test('creates a managed client from the settings page and shows its secret once', async ({ mcpSettingsPage }) => {
    await mcpSettingsPage.goto()
    await mcpSettingsPage.advancedTrigger.click()
    const page = mcpSettingsPage.page
    await expect(page.getByTestId('managed-auth-enabled-switch')).toBeChecked()
    await expect(page.getByTestId('managed-auth-trusted-issuer-input')).toHaveValue(idp.issuer)

    await page.getByTestId('managed-client-add-btn').click()
    await page.locator('#managed-client-name').fill(uiClientName)
    await page.getByTestId('managed-client-identity-select').click()
    await page.getByRole('option', { name: 'A virtual key' }).click()
    await page.getByTestId('managed-client-create-dialog').getByRole('combobox').last().click()
    await page.getByRole('option').first().click()
    await page.getByTestId('managed-client-create-submit').click()

    const secret = page.getByTestId('managed-client-secret')
    await expect(secret).not.toHaveValue('')
    await page.getByRole('button', { name: 'Done' }).click()
    await expect(page.getByTestId('managed-clients-table')).toBeVisible()
  })
})
