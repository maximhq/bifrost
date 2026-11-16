import { expect, test } from '../../core/fixtures/base.fixture'
import { PROXY_TEST_CA_PEM } from './config.data'

test.describe('Proxy Config', () => {
  // Run all config tests serially to avoid parallel writes to the same config/store
  test.describe.configure({ mode: 'serial' })

  // The global proxy applies to every outbound request the server makes, so each test
  // restores the config it found. GET returns the password as "<redacted>", which PUT
  // treats as "keep the stored password".
  type StoredProxyConfig = {
    enabled: boolean
    type: string
    url: string
    username?: string
    password?: string
    no_proxy?: string
    timeout: number
    skip_tls_verify?: boolean
    ca_cert_pem?: string
    enable_for_scim: boolean
    enable_for_inference: boolean
    enable_for_api: boolean
  }
  let original: StoredProxyConfig | null = null

  test.beforeEach(async ({ configSettingsPage }) => {
    const res = await configSettingsPage.page.request.get('/api/proxy-config')
    expect(res.status(), await res.text()).toBe(200)
    original = await res.json()
    // GET masks a password in the proxy URL, and once a test replaced the URL afterEach
    // could not put that password back: leave such a proxy alone.
    const masked = original?.url?.includes('<redacted>') ?? false
    if (masked) original = null
    test.skip(masked, 'the stored proxy URL carries a password GET masks, so afterEach could not restore it')
    await configSettingsPage.goto('proxy')
  })

  test.afterEach(async ({ configSettingsPage }) => {
    if (!original) return
    const res = await configSettingsPage.page.request.put('/api/proxy-config', {
      data: {
        enabled: original.enabled,
        type: original.type || 'http',
        url: original.url || '',
        username: original.username || '',
        password: original.password || '',
        no_proxy: original.no_proxy || '',
        timeout: original.timeout || 0,
        skip_tls_verify: original.skip_tls_verify || false,
        ca_cert_pem: original.ca_cert_pem || '',
        enable_for_scim: original.enable_for_scim,
        enable_for_inference: original.enable_for_inference,
        enable_for_api: original.enable_for_api,
      },
    })
    expect(res.status(), await res.text()).toBe(200)
  })

  test('should save a SOCKS5 proxy for inference and API traffic', async ({ configSettingsPage }) => {
    await configSettingsPage.ensureSwitchOn(configSettingsPage.proxyEnabledSwitch)
    await configSettingsPage.selectProxyType('socks5')
    await configSettingsPage.proxyUrlInput.fill('socks5://127.0.0.1:1080')
    await configSettingsPage.proxyUsernameInput.fill('e2e-user')
    // The password is left as loaded: GET cannot return a stored one, so a password typed
    // here could never be put back by afterEach.
    await configSettingsPage.ensureSwitchOn(configSettingsPage.proxyEnableInferenceSwitch)
    await configSettingsPage.ensureSwitchOn(configSettingsPage.proxyEnableApiSwitch)

    await expect(configSettingsPage.proxySaveBtn).toBeEnabled()
    await configSettingsPage.proxySaveBtn.click()
    await configSettingsPage.waitForSuccessToast()

    const res = await configSettingsPage.page.request.get('/api/proxy-config')
    expect(res.status(), await res.text()).toBe(200)
    const stored = await res.json()
    expect(stored.enabled).toBe(true)
    expect(stored.type).toBe('socks5')
    expect(stored.url).toBe('socks5://127.0.0.1:1080')
    expect(stored.username).toBe('e2e-user')
    expect(stored.password ?? '', 'the stored proxy password must be the one the test found').toBe(original?.password ?? '')
    expect(stored.enable_for_inference).toBe(true)
    expect(stored.enable_for_api).toBe(true)

    // The form reads the saved values back after a reload.
    await configSettingsPage.goto('proxy')
    await expect(configSettingsPage.proxyTypeSelect).toContainText('SOCKS5')
    await expect(configSettingsPage.proxyUrlInput).toHaveValue('socks5://127.0.0.1:1080')
    await expect(configSettingsPage.proxyEnableInferenceSwitch).toHaveAttribute('data-state', 'checked')
    await expect(configSettingsPage.proxyEnableApiSwitch).toHaveAttribute('data-state', 'checked')
  })

  test('should save the proxy CA certificate', async ({ configSettingsPage }) => {
    await configSettingsPage.ensureSwitchOn(configSettingsPage.proxyEnabledSwitch)
    await configSettingsPage.selectProxyType('http')
    await configSettingsPage.proxyUrlInput.fill('https://127.0.0.1:3129')
    await configSettingsPage.proxyCaCertInput.fill(PROXY_TEST_CA_PEM)

    await expect(configSettingsPage.proxySaveBtn).toBeEnabled()
    await configSettingsPage.proxySaveBtn.click()
    await configSettingsPage.waitForSuccessToast()

    const res = await configSettingsPage.page.request.get('/api/proxy-config')
    expect(res.status(), await res.text()).toBe(200)
    const stored = await res.json()
    expect(stored.url).toBe('https://127.0.0.1:3129')
    expect(stored.ca_cert_pem?.trim()).toBe(PROXY_TEST_CA_PEM.trim())

    await configSettingsPage.goto('proxy')
    await expect(configSettingsPage.proxyCaCertInput).toHaveValue(new RegExp('BEGIN CERTIFICATE'))
  })

  test('should reject a SOCKS5 proxy named by an http URL', async ({ configSettingsPage }) => {
    await configSettingsPage.ensureSwitchOn(configSettingsPage.proxyEnabledSwitch)
    await configSettingsPage.selectProxyType('socks5')
    await configSettingsPage.proxyUrlInput.fill('http://127.0.0.1:1080')

    await expect(configSettingsPage.page.getByText('A SOCKS5 proxy URL must start with socks5:// or socks5h://')).toBeVisible()
    await expect(configSettingsPage.proxySaveBtn).toBeDisabled()
  })

  test('should keep TCP unavailable', async ({ configSettingsPage }) => {
    await configSettingsPage.ensureSwitchOn(configSettingsPage.proxyEnabledSwitch)
    await configSettingsPage.proxyTypeSelect.click()
    await expect(configSettingsPage.page.getByTestId('proxy-type-option-tcp')).toHaveAttribute('data-disabled', '')
    await configSettingsPage.page.keyboard.press('Escape')
  })
})
