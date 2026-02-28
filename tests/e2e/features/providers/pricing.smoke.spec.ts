/**
 * Smoke tests for Pricing Overrides functionality
 * These tests validate the critical paths for the pricing overrides feature
 */

import { expect, test } from '../../core/fixtures/base.fixture';

/**
 * Litmus test: Verify pricing tab appears in provider config
 * This is a quick sanity check that the pricing integration is working
 */
test.describe('Pricing Overrides Smoke Tests', () => {
  test.describe.configure({ mode: 'serial' })

  test.beforeEach(async ({ providersPage }) => {
    await providersPage.goto()
  })

  test('litmus: pricing tab is visible in provider config sheet', async ({ providersPage }) => {
    // Select OpenAI provider
    await providersPage.selectProvider('openai')

    // Open config sheet
    await providersPage.openConfigSheet()

    // Verify pricing tab exists and is visible
    const pricingTab = providersPage.page.getByRole('tab', { name: 'Pricing' })
    await expect(pricingTab).toBeVisible()

    // Click pricing tab
    await pricingTab.click()

    // Verify pricing JSON input is visible
    const jsonInput = providersPage.getPricingJsonInput()
    await expect(jsonInput).toBeVisible()
  })

  test('smoke: can save and reset pricing overrides', async ({ providersPage }) => {
    // Select a provider
    await providersPage.selectProvider('openai')

    // Set a simple pricing override
    const simplePricing = JSON.stringify([{
      model_pattern: 'test-*',
      match_type: 'wildcard',
      request_types: ['chat_completion'],
      input_cost_per_token: 0.000001,
      output_cost_per_token: 0.000002,
    }], null, 2)

    await providersPage.setPricingOverridesJson(simplePricing)

    // Save should trigger success toast
    await providersPage.savePricingConfig()

    // Reset to empty
    await providersPage.setPricingOverridesJson('[]')
    await providersPage.savePricingConfig()
  })

  test('smoke: validation prevents invalid JSON', async ({ providersPage }) => {
    await providersPage.selectProvider('openai')
    await providersPage.selectConfigTab('pricing')

    // Enter invalid JSON
    const jsonInput = providersPage.getPricingJsonInput()
    await jsonInput.clear()
    await jsonInput.fill('{ invalid json }')

    // Blur to trigger validation
    await jsonInput.blur()

    // Verify error message appears
    const errorMessage = providersPage.page.getByText('Invalid JSON format or pricing overrides structure')
    await expect(errorMessage).toBeVisible()

    // Verify save button is disabled
    const saveBtn = providersPage.getPricingSaveBtn()
    await expect(saveBtn).toBeDisabled()
  })

  test('smoke: pricing overrides persist across navigation', async ({ providersPage }) => {
    const uniquePattern = `persist-test-${Date.now()}-*`
    const pricingOverride = JSON.stringify([{
      model_pattern: uniquePattern,
      match_type: 'wildcard',
      request_types: ['chat_completion'],
      input_cost_per_token: 0.000001,
      output_cost_per_token: 0.000002,
    }], null, 2)

    // Set pricing on OpenAI
    await providersPage.selectProvider('openai')
    await providersPage.setPricingOverridesJson(pricingOverride)
    await providersPage.savePricingConfig()

    // Close the config sheet by pressing Escape
    await providersPage.page.keyboard.press('Escape')
    await providersPage.page.waitForTimeout(300)

    // Navigate away to another provider
    await providersPage.selectProvider('anthropic')
    await providersPage.page.waitForTimeout(500)

    // Navigate back to OpenAI
    await providersPage.selectProvider('openai')

    // Open pricing tab and verify the value persisted
    await providersPage.selectConfigTab('pricing')
    const jsonInput = providersPage.getPricingJsonInput()
    const savedValue = await jsonInput.inputValue()

    // Verify the pattern we saved is in the textarea
    expect(savedValue).toContain(uniquePattern)

    // Cleanup: reset to empty
    await providersPage.setPricingOverridesJson('[]')
    await providersPage.savePricingConfig()
  })
})
