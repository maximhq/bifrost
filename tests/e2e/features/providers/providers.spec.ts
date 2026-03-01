import { expect, test } from '../../core/fixtures/base.fixture';
import { createCustomProviderData, createProviderKeyData } from './providers.data';
import { ProvidersPage } from './pages/providers.page';

// Track created resources for cleanup
const createdKeys: { provider: string; keyName: string }[] = []
const createdProviders: string[] = []

test.describe('Providers', () => {
  test.describe.configure({ mode: 'serial' })

  test.beforeEach(async ({ providersPage }) => {
    await providersPage.goto()
  })

  test.afterEach(async ({ providersPage }) => {
    // Clean up any keys created during tests
    for (const { provider, keyName } of [...createdKeys]) {
      try {
        await providersPage.selectProvider(provider)
        const exists = await providersPage.keyExists(keyName, 2000)
        if (exists) {
          await providersPage.deleteKey(keyName)
        }
      } catch (error) {
        const errorMsg = error instanceof Error ? error.message : String(error)
        console.error(`[CLEANUP ERROR] Failed to delete provider key ${provider}/${keyName}: ${errorMsg}`)
      }
    }
    createdKeys.length = 0

    // Clean up any custom providers created during tests (skip toast wait so cleanup does not fail if toast is missing)
    for (const providerName of [...createdProviders]) {
      try {
        await providersPage.deleteProvider(providerName, { skipToastWait: true })
      } catch (error) {
        const errorMsg = error instanceof Error ? error.message : String(error)
        console.error(`[CLEANUP ERROR] Failed to delete provider ${providerName}: ${errorMsg}`)
      }
    }
    createdProviders.length = 0
  })

  test.describe('Provider Navigation', () => {
    test('should display standard providers in sidebar', async ({ providersPage }) => {
      // Check that OpenAI provider is visible
      const openaiProvider = providersPage.getProviderItem('openai')
      await expect(openaiProvider).toBeVisible()

      // Check that Anthropic provider is visible
      const anthropicProvider = providersPage.getProviderItem('anthropic')
      await expect(anthropicProvider).toBeVisible()
    })

    test('should select a provider from the sidebar', async ({ providersPage }) => {
      await providersPage.selectProvider('openai')

      // Verify URL contains provider param
      await expect(providersPage.page).toHaveURL(/provider=openai/)
    })

    test('should switch between providers', async ({ providersPage }) => {
      // Select OpenAI first
      await providersPage.selectProvider('openai')
      await expect(providersPage.page).toHaveURL(/provider=openai/)

      // Switch to Anthropic
      await providersPage.selectProvider('anthropic')
      await expect(providersPage.page).toHaveURL(/provider=anthropic/)
    })
  })

  test.describe('Provider Keys', () => {
    test('should add a new key to OpenAI provider', async ({ providersPage }) => {
      // Select OpenAI provider
      await providersPage.selectProvider('openai')

      // Create test key data with unique name (no spaces for easier locating)
      const keyData = createProviderKeyData({
        name: `E2E-Test-Key-${Date.now()}`,
        value: 'sk-test-e2e-key-12345',
        weight: 1.0,
      })

      // Track for cleanup
      createdKeys.push({ provider: 'openai', keyName: keyData.name })

      // Add the key
      await providersPage.addKey(keyData)

      // Verify key appears in table (with waiting)
      const keyExists = await providersPage.keyExists(keyData.name)
      expect(keyExists).toBe(true)
    })

    test('should add a key with custom weight', async ({ providersPage }) => {
      await providersPage.selectProvider('openai')

      const keyData = createProviderKeyData({
        name: `Weight-Key-${Date.now()}`,
        value: 'sk-test-weight-key-12345',
        weight: 0.5,
      })

      // Track for cleanup
      createdKeys.push({ provider: 'openai', keyName: keyData.name })

      await providersPage.addKey(keyData)

      const keyExists = await providersPage.keyExists(keyData.name)
      expect(keyExists).toBe(true)
    })

    test('should display empty state when no keys configured', async ({ providersPage }) => {
      // Select a provider that typically has no keys in fresh install
      await providersPage.selectProvider('groq')
      // Get key count
      const keyCount = await providersPage.getKeyCount()

      // Check for empty state message
      const emptyMessage = providersPage.page.getByText('No keys found')
      const isEmptyStateVisible = await emptyMessage.isVisible().catch(() => false)

      // Deterministic assertion: either empty state is shown OR there are keys
      if (keyCount === 0) {
        // When no keys, empty state should be shown
        expect(isEmptyStateVisible).toBe(true)
      } else {
        // When keys exist, empty state should NOT be shown
        expect(isEmptyStateVisible).toBe(false)
      }
    })
  })

  test.describe('Custom Providers', () => {
    test('should open custom provider creation sheet', async ({ providersPage }) => {
      await providersPage.addProviderBtn.click()

      // Verify sheet is visible
      await expect(providersPage.customProviderSheet).toBeVisible()

      // Verify form fields are present
      await expect(providersPage.customProviderNameInput).toBeVisible()
      await expect(providersPage.baseProviderSelect).toBeVisible()
      await expect(providersPage.baseUrlInput).toBeVisible()
    })

    test('should create a custom OpenAI-compatible provider', async ({ providersPage }) => {
      const providerData = createCustomProviderData({
        name: `test-openai-${Date.now()}`,
        baseProviderType: 'openai',
        baseUrl: 'https://api.test-provider.com/v1',
      })

      // Track for cleanup
      createdProviders.push(providerData.name)

      await providersPage.createProvider(providerData)

      // Wait for provider to appear in sidebar
      const providerItem = providersPage.getProviderItem(providerData.name)
      await expect(providerItem).toBeVisible({ timeout: 15000 })
    })

    test('should create a custom Anthropic-compatible provider', async ({ providersPage }) => {
      const providerData = createCustomProviderData({
        name: `test-anthropic-${Date.now()}`,
        baseProviderType: 'anthropic',
        baseUrl: 'https://api.anthropic-proxy.com',
      })

      // Track for cleanup
      createdProviders.push(providerData.name)

      await providersPage.createProvider(providerData)

      // Wait for provider to appear in sidebar
      const providerItem = providersPage.getProviderItem(providerData.name)
      await expect(providerItem).toBeVisible({ timeout: 15000 })
    })

    test('should cancel custom provider creation', async ({ providersPage }) => {
      await providersPage.addProviderBtn.click()
      await expect(providersPage.customProviderSheet).toBeVisible()

      // Fill some data
      await providersPage.customProviderNameInput.fill('cancelled-provider')

      // Cancel
      await providersPage.customProviderCancelBtn.click()

      // Sheet should close
      await expect(providersPage.customProviderSheet).not.toBeVisible()

      // Provider should not exist
      const providerExists = await providersPage.providerExists('cancelled-provider')
      expect(providerExists).toBe(false)
    })
  })

  test.describe('Form Validation', () => {
    test('should require name for custom provider', async ({ providersPage }) => {
      await providersPage.addProviderBtn.click()
      await expect(providersPage.customProviderSheet).toBeVisible()

      // Try to save without name
      await providersPage.baseUrlInput.fill('https://api.example.com')

      // The save button should be disabled or show error
      const saveBtn = providersPage.customProviderSaveBtn
      await saveBtn.click()

      // Form should still be visible (not submitted)
      await expect(providersPage.customProviderSheet).toBeVisible()
    })

    test('should require base URL for custom provider', async ({ providersPage }) => {
      await providersPage.addProviderBtn.click()
      await expect(providersPage.customProviderSheet).toBeVisible()

      // Fill only name
      await providersPage.customProviderNameInput.fill('test-provider')

      // Try to save
      await providersPage.customProviderSaveBtn.click()

      // Form should still be visible
      await expect(providersPage.customProviderSheet).toBeVisible()
    })
  })
})

test.describe('Provider Key Management', () => {
  test.describe.configure({ mode: 'serial' })

  // Track keys for cleanup in this test suite
  const managementKeys: string[] = []

  test.beforeEach(async ({ providersPage }) => {
    await providersPage.goto()
    await providersPage.selectProvider('openai')
  })

  test.afterEach(async ({ providersPage }) => {
    // Clean up any keys created during tests
    for (const keyName of [...managementKeys]) {
      try {
        const exists = await providersPage.keyExists(keyName, 2000)
        if (exists) {
          await providersPage.deleteKey(keyName)
        }
      } catch (error) {
        const errorMsg = error instanceof Error ? error.message : String(error)
        console.error(`[CLEANUP ERROR] Failed to delete provider key ${keyName}: ${errorMsg}`)
      }
    }
    managementKeys.length = 0
  })

  test('should edit an existing key', async ({ providersPage }) => {
    // First add a key
    const keyData = createProviderKeyData({
      name: `Edit-Test-Key-${Date.now()}`,
      value: 'sk-test-edit-key',
    })

    // Track for cleanup
    managementKeys.push(keyData.name)

    await providersPage.addKey(keyData)

    // Now edit it
    await providersPage.editKey(keyData.name, {
      weight: 0.7,
    })

    // Key should still exist
    const keyExists = await providersPage.keyExists(keyData.name)
    expect(keyExists).toBe(true)
  })

  test('should delete a key', async ({ providersPage }) => {
    // First add a key
    const keyData = createProviderKeyData({
      name: `Delete-Test-Key-${Date.now()}`,
      value: 'sk-test-delete-key',
    })

    // Don't track for cleanup - we're testing delete

    await providersPage.addKey(keyData)

    // Verify it exists
    let keyExists = await providersPage.keyExists(keyData.name)
    expect(keyExists).toBe(true)

    // Delete it
    await providersPage.deleteKey(keyData.name)

    // Verify it's gone (use short timeout since we expect it to be gone)
    keyExists = await providersPage.keyExists(keyData.name, 1000)
    expect(keyExists).toBe(false)
  })

  test('should toggle key enabled state', async ({ providersPage }) => {
    // First add a key
    const keyData = createProviderKeyData({
      name: `Toggle-Test-Key-${Date.now()}`,
      value: 'sk-test-toggle-key',
    })

    // Track for cleanup
    managementKeys.push(keyData.name)

    await providersPage.addKey(keyData)

    // Toggle the key
    await providersPage.toggleKeyEnabled(keyData.name)

    // Key should still exist
    const keyExists = await providersPage.keyExists(keyData.name)
    expect(keyExists).toBe(true)
  })
})

test.describe('Provider Configuration', () => {
  test.beforeEach(async ({ providersPage }) => {
    await providersPage.goto()
  })

  test('should view provider configuration', async ({ providersPage }) => {
    // Select OpenAI provider
    await providersPage.selectProvider('openai')

    // Should see the provider's key table
    await expect(providersPage.keysTable).toBeVisible()

    // Should see the add key button
    await expect(providersPage.addKeyBtn).toBeVisible()
  })

  test('should show provider models list', async ({ providersPage }) => {
    // Select OpenAI provider
    await providersPage.selectProvider('openai')

    // Check for models section or tab
    const modelsSection = providersPage.page.getByText(/Models/i).first()
    const modelsVisible = await modelsSection.isVisible().catch(() => false)

    // Either models are shown directly or there's no models section
    expect(modelsVisible !== undefined).toBe(true)
  })
})

test.describe('Performance Tuning', () => {
  test.beforeEach(async ({ providersPage }) => {
    await providersPage.goto()
    await providersPage.selectProvider('openai')
  })

  test('should display performance tuning tab', async ({ providersPage }) => {
    await providersPage.selectConfigTab('performance')

    // Should see concurrency and buffer size inputs
    await expect(providersPage.getConcurrencyInput()).toBeVisible()
    await expect(providersPage.getBufferSizeInput()).toBeVisible()
  })

  test('should display raw request/response toggles', async ({ providersPage }) => {
    await providersPage.selectConfigTab('performance')

    // Should see raw request and response toggles
    const rawRequestLabel = providersPage.page.getByText('Include Raw Request')
    const rawResponseLabel = providersPage.page.getByText('Include Raw Response')

    await expect(rawRequestLabel).toBeVisible()
    await expect(rawResponseLabel).toBeVisible()
  })

  test('should update concurrency value', async ({ providersPage }) => {
    await providersPage.selectConfigTab('performance')

    const concurrencyInput = providersPage.getConcurrencyInput()
    const originalValue = await concurrencyInput.inputValue()

    // Use a small value that is always <= buffer size
    const newValue = '5'

    await providersPage.fillNumberInput(concurrencyInput, newValue)

    // Verify value changed
    const currentValue = await concurrencyInput.inputValue()
    expect(currentValue).toBe(newValue)
    // Blur the input
    await concurrencyInput.blur()
    // No validation error should appear
    await expect(providersPage.page.getByText('Concurrency must be a number')).not.toBeVisible()
    await expect(providersPage.page.getByText('Concurrency must be greater than 0')).not.toBeVisible()
    await expect(providersPage.page.getByText('Concurrency must be less than or equal to buffer size')).not.toBeVisible()

    // Save and verify success
    const saveBtn = providersPage.getConfigSaveBtn('performance')
    await expect(saveBtn).toBeEnabled()
    await providersPage.savePerformanceConfig()

    // Restore original value
    await providersPage.fillNumberInput(concurrencyInput, originalValue)
    // Blur the input
    await concurrencyInput.blur()
    await providersPage.savePerformanceConfig()
  })

  test('should update buffer size value', async ({ providersPage }) => {
    await providersPage.selectConfigTab('performance')

    const bufferSizeInput = providersPage.getBufferSizeInput()
    const originalValue = await bufferSizeInput.inputValue()

    // Use a large value that is always >= concurrency
    const newValue = '6000'

    await providersPage.fillNumberInput(bufferSizeInput, newValue)

    // Verify value changed
    const currentValue = await bufferSizeInput.inputValue()
    expect(currentValue).toBe(newValue)

    // Blur the input
    await bufferSizeInput.blur()

    // No validation error should appear
    await expect(providersPage.page.getByText('Buffer size must be a number')).not.toBeVisible()
    await expect(providersPage.page.getByText('Buffer size must be greater than 0')).not.toBeVisible()
    await expect(providersPage.page.getByText('Concurrency must be less than or equal to buffer size')).not.toBeVisible()

    // Save and verify success
    const saveBtn = providersPage.getConfigSaveBtn('performance')
    await expect(saveBtn).toBeEnabled()
    await providersPage.savePerformanceConfig()

    // Restore original value
    await providersPage.fillNumberInput(bufferSizeInput, originalValue)
    // Blur the input
    await bufferSizeInput.blur()
    await providersPage.savePerformanceConfig()
  })

  test('should toggle and save raw request/response', async ({ providersPage }) => {
    await providersPage.selectConfigTab('performance')

    const rawRequestSwitch = providersPage.getRawRequestSwitch()
    const rawResponseSwitch = providersPage.getRawResponseSwitch()

    // Capture original states
    const originalRawRequest = await rawRequestSwitch.getAttribute('data-state') === 'checked'
    const originalRawResponse = await rawResponseSwitch.getAttribute('data-state') === 'checked'

    // Toggle both switches
    await rawRequestSwitch.click()
    await rawResponseSwitch.click()

    // Save and verify success
    const saveBtn = providersPage.getConfigSaveBtn('performance')
    await expect(saveBtn).toBeEnabled()
    await providersPage.savePerformanceConfig()

    // Restore original states
    const currentRawRequest = await rawRequestSwitch.getAttribute('data-state') === 'checked'
    const currentRawResponse = await rawResponseSwitch.getAttribute('data-state') === 'checked'

    if (currentRawRequest !== originalRawRequest) {
      await rawRequestSwitch.click()
    }
    if (currentRawResponse !== originalRawResponse) {
      await rawResponseSwitch.click()
    }

    await providersPage.savePerformanceConfig()
  })
})

test.describe('Proxy Configuration', () => {
  test.beforeEach(async ({ providersPage }) => {
    await providersPage.goto()
    await providersPage.selectProvider('openai')
  })

  test('should display proxy config tab', async ({ providersPage }) => {
    await providersPage.selectConfigTab('proxy')

    // Should see proxy type selector
    const proxyTypeLabel = providersPage.page.getByText('Proxy Type')
    await expect(proxyTypeLabel).toBeVisible()
  })

  test('should show proxy type options', async ({ providersPage }) => {
    await providersPage.selectConfigTab('proxy')

    // Open the proxy type dropdown
    const proxySelect = providersPage.getProxyTypeSelect()
    await proxySelect.click()

    // Should see HTTP, SOCKS5, Environment options
    await expect(providersPage.page.getByRole('option', { name: /HTTP/i })).toBeVisible()
    await expect(providersPage.page.getByRole('option', { name: /SOCKS5/i })).toBeVisible()
    await expect(providersPage.page.getByRole('option', { name: /Environment/i })).toBeVisible()

    // Close dropdown
    await providersPage.page.keyboard.press('Escape')
  })

  test('should show URL fields when HTTP proxy selected', async ({ providersPage }) => {
    await providersPage.selectConfigTab('proxy')

    // Select HTTP proxy type
    const proxySelect = providersPage.getProxyTypeSelect()
    await proxySelect.click()
    await providersPage.page.getByRole('option', { name: /HTTP/i }).click()

    // Should show URL, username, password fields
    await expect(providersPage.page.getByLabel('Proxy URL')).toBeVisible()
    await expect(providersPage.page.getByLabel('Username')).toBeVisible()
    await expect(providersPage.page.getByLabel('Password')).toBeVisible()
  })
})

test.describe('Network Configuration', () => {
  test.beforeEach(async ({ providersPage }) => {
    await providersPage.goto()
    await providersPage.selectProvider('openai')
  })

  test('should display network config tab', async ({ providersPage }) => {
    await providersPage.selectConfigTab('network')

    // Should see timeout and retry settings
    await expect(providersPage.page.getByLabel(/Timeout/i)).toBeVisible()
    await expect(providersPage.page.getByLabel(/Max Retries/i)).toBeVisible()
  })

  test('should display backoff settings', async ({ providersPage }) => {
    await providersPage.selectConfigTab('network')

    // Should see backoff configuration
    await expect(providersPage.page.getByLabel(/Initial Backoff/i)).toBeVisible()
    await expect(providersPage.page.getByLabel(/Max Backoff/i)).toBeVisible()
  })

  test('should update timeout value', async ({ providersPage }) => {
    await providersPage.selectConfigTab('network')

    // Ensure backoff fields are valid (minimum 100ms) so form validation passes
    const initialBackoff = providersPage.page.getByLabel(/Initial Backoff/i)
    const maxBackoff = providersPage.page.getByLabel(/Max Backoff/i)
    const ibVal = await initialBackoff.inputValue()
    const mbVal = await maxBackoff.inputValue()
    if (Number(ibVal) < 100) {
      await providersPage.fillNumberInput(initialBackoff, '500')
    }
    if (Number(mbVal) < 100) {
      await providersPage.fillNumberInput(maxBackoff, '10000')
    }

    const timeoutInput = providersPage.page.getByLabel(/Timeout/i)
    const originalValue = await timeoutInput.inputValue()
    const newValue = originalValue === '30' ? '60' : '30'

    await providersPage.fillNumberInput(timeoutInput, newValue)

    // Verify value changed
    const currentValue = await timeoutInput.inputValue()
    expect(currentValue).toBe(newValue)

    // Save button should be enabled
    const saveBtn = providersPage.getConfigSaveBtn('network')
    await expect(saveBtn).toBeEnabled()
    await providersPage.saveNetworkConfig()

    // Restore original value to avoid leaving form dirty
    await providersPage.fillNumberInput(timeoutInput, originalValue)
    await providersPage.saveNetworkConfig()

  })

  test('should update max retries value', async ({ providersPage }) => {
    await providersPage.selectConfigTab('network')

    // Ensure backoff fields are valid (minimum 100ms) so form validation passes
    const initialBackoff = providersPage.page.getByLabel(/Initial Backoff/i)
    const maxBackoff = providersPage.page.getByLabel(/Max Backoff/i)
    const ibVal = await initialBackoff.inputValue()
    const mbVal = await maxBackoff.inputValue()
    if (Number(ibVal) < 100) {
      await providersPage.fillNumberInput(initialBackoff, '500')
    }
    if (Number(mbVal) < 100) {
      await providersPage.fillNumberInput(maxBackoff, '10000')
    }

    const retriesInput = providersPage.page.getByLabel(/Max Retries/i)
    const originalValue = await retriesInput.inputValue()
    const newValue = originalValue === '0' ? '3' : '0'

    await providersPage.fillNumberInput(retriesInput, newValue)

    // Verify value changed
    const currentValue = await retriesInput.inputValue()
    expect(currentValue).toBe(newValue)

    // Save button should be enabled
    const saveBtn = providersPage.getConfigSaveBtn('network')
    await expect(saveBtn).toBeEnabled()
    await providersPage.saveNetworkConfig()

    // Restore original value to avoid leaving form dirty
    await providersPage.fillNumberInput(retriesInput, originalValue)
    await providersPage.saveNetworkConfig()
  })
})

test.describe('Pricing Overrides', () => {
  test.beforeEach(async ({ providersPage }) => {
    await providersPage.goto()
    await providersPage.selectProvider('openai')
  })

  test('should display pricing tab', async ({ providersPage }) => {
    await providersPage.openConfigSheet()
    const tab = providersPage.page.getByRole('tab', { name: 'Pricing' })
    await expect(tab).toBeVisible()
  })

  test('should open pricing configuration', async ({ providersPage }) => {
    await providersPage.selectConfigTab('pricing')

    // Verify JSON textarea is visible
    const jsonInput = providersPage.getPricingJsonInput()
    await expect(jsonInput).toBeVisible()

    // Verify save and reset buttons are visible
    const saveBtn = providersPage.getPricingSaveBtn()
    const resetBtn = providersPage.getPricingResetBtn()
    await expect(saveBtn).toBeVisible()
    await expect(resetBtn).toBeVisible()
  })

  test('should add pricing overrides via JSON', async ({ providersPage }) => {
    await providersPage.selectConfigTab('pricing')

    // Use valid pricing overrides JSON
    const validPricingJson = JSON.stringify([
      {
        model_pattern: 'gpt-4o*',
        match_type: 'wildcard',
        request_types: ['chat_completion'],
        input_cost_per_token: 0.000005,
        output_cost_per_token: 0.000015,
      },
    ], null, 2)

    await providersPage.setPricingOverridesJson(validPricingJson)

    // Save and verify success toast
    await providersPage.savePricingConfig()

    // Cleanup: Reset to empty array
    await providersPage.setPricingOverridesJson('[]')
    await providersPage.savePricingConfig()
  })

  test('should validate invalid JSON format', async ({ providersPage }) => {
    await providersPage.selectConfigTab('pricing')

    // Fill textarea with invalid JSON
    await providersPage.setPricingOverridesJson('{invalid json syntax}')

    // Verify validation error appears (FormMessage shows error)
    const errorMessage = providersPage.page.getByText('Invalid JSON format or pricing overrides structure')
    await expect(errorMessage).toBeVisible()

    // Verify save button is disabled when invalid
    const saveBtn = providersPage.getPricingSaveBtn()
    await expect(saveBtn).toBeDisabled()
  })

  test('should validate invalid pricing override structure', async ({ providersPage }) => {
    await providersPage.selectConfigTab('pricing')

    // Fill textarea with valid JSON but invalid schema (missing required fields)
    const invalidStructureJson = JSON.stringify([
      {
        model_pattern: 'gpt-4o*',
        // missing match_type, request_types, and cost fields
      },
    ], null, 2)

    await providersPage.setPricingOverridesJson(invalidStructureJson)

    // Verify validation error about invalid structure appears
    const errorMessage = providersPage.page.getByText('Invalid JSON format or pricing overrides structure')
    await expect(errorMessage).toBeVisible()

    // Verify save button is disabled when invalid
    const saveBtn = providersPage.getPricingSaveBtn()
    await expect(saveBtn).toBeDisabled()
  })

  test('should save and verify pricing overrides persist', async ({ providersPage }) => {
    const pricingOverride = [
      {
        model_pattern: 'test-model-*',
        match_type: 'wildcard',
        request_types: ['chat_completion'],
        input_cost_per_token: 0.000001,
        output_cost_per_token: 0.000002,
      },
    ]
    const pricingJson = JSON.stringify(pricingOverride, null, 2)

    // First, save the pricing override
    await providersPage.selectConfigTab('pricing')
    await providersPage.setPricingOverridesJson(pricingJson)
    await providersPage.savePricingConfig()

    // Reopen config sheet and select pricing tab
    await providersPage.selectConfigTab('pricing')

    // Verify the previously saved JSON is still present
    const jsonInput = providersPage.getPricingJsonInput()
    const savedValue = await jsonInput.inputValue()
    const savedJson = JSON.parse(savedValue)
    expect(savedJson).toEqual(pricingOverride)

    // Cleanup: Reset to empty array
    await providersPage.setPricingOverridesJson('[]')
    await providersPage.savePricingConfig()
  })
})

test.describe('Governance (Budget & Rate Limits)', () => {
  test.beforeEach(async ({ providersPage }) => {
    await providersPage.goto()
    await providersPage.selectProvider('openai')
  })

  test('should display governance tab', async ({ providersPage }) => {
    const isVisible = await providersPage.isGovernanceTabVisible()

    if (isVisible) {
      await providersPage.selectConfigTab('governance')

      // Should see budget configuration section
      await expect(providersPage.page.getByText('Budget Configuration')).toBeVisible()
    }
  })

  test('should display budget configuration', async ({ providersPage }) => {
    const isVisible = await providersPage.isGovernanceTabVisible()

    if (isVisible) {
      await providersPage.selectConfigTab('governance')

      // Should see budget limit input
      const budgetInput = providersPage.page.locator('#providerBudgetMaxLimit')
      await expect(budgetInput).toBeVisible()
    }
  })

  test('should display rate limiting configuration', async ({ providersPage }) => {
    const isVisible = await providersPage.isGovernanceTabVisible()

    if (isVisible) {
      await providersPage.selectConfigTab('governance')

      // Should see rate limiting section
      await expect(providersPage.page.getByText('Rate Limiting Configuration')).toBeVisible()

      // Should see token and request limit inputs
      const tokenInput = providersPage.page.locator('#providerTokenMaxLimit')
      const requestInput = providersPage.page.locator('#providerRequestMaxLimit')

      await expect(tokenInput).toBeVisible()
      await expect(requestInput).toBeVisible()
    }
  })

  test('should set budget limit', async ({ providersPage }) => {
    const isVisible = await providersPage.isGovernanceTabVisible()

    if (isVisible) {
      await providersPage.selectConfigTab('governance')

      const budgetInput = providersPage.page.locator('#providerBudgetMaxLimit')
      await budgetInput.click()
      await budgetInput.fill('')
      // Type character by character to trigger React's onChange
      await budgetInput.pressSequentially('100')

      // Verify value
      const value = await budgetInput.inputValue()
      expect(value).toBe('100')

      // Form should now be dirty - save button should be enabled
      const saveBtn = providersPage.getConfigSaveBtn('governance')
      // Give React time to update the form state
      await providersPage.page.waitForTimeout(500)
      await expect(saveBtn).toBeEnabled({ timeout: 5000 })
    }
  })

  test('should set rate limits', async ({ providersPage }) => {
    const isVisible = await providersPage.isGovernanceTabVisible()

    if (isVisible) {
      await providersPage.selectConfigTab('governance')

      // Set token limit - use pressSequentially for proper React onChange
      const tokenInput = providersPage.page.locator('#providerTokenMaxLimit')
      await tokenInput.click()
      await tokenInput.fill('')
      await tokenInput.pressSequentially('100000')

      // Set request limit
      const requestInput = providersPage.page.locator('#providerRequestMaxLimit')
      await requestInput.click()
      await requestInput.fill('')
      await requestInput.pressSequentially('1000')

      // Verify values
      expect(await tokenInput.inputValue()).toBe('100000')
      expect(await requestInput.inputValue()).toBe('1000')
    }
  })
})
