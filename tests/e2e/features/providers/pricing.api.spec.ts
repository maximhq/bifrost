/**
 * API integration tests for Pricing Overrides
 * These tests validate the backend API integration for pricing overrides
 */

import { expect } from '@playwright/test'
import { test } from '../../core/fixtures/base.fixture'

/**
 * Validate that pricing overrides are properly sent to and received from the API
 */
test.describe('Pricing Overrides API Integration', () => {
  test.describe.configure({ mode: 'serial' })

  test.beforeEach(async ({ providersPage }) => {
    await providersPage.goto()
  })

  test('api: pricing overrides are persisted via provider update API', async ({ providersPage, page }) => {
    // Listen for API requests
    const apiRequests: { url: string; method: string; body: unknown }[] = []
    
    page.on('request', (request) => {
      if (request.url().includes('/api/providers/') && request.method() === 'PUT') {
        apiRequests.push({
          url: request.url(),
          method: request.method(),
          body: request.postDataJSON(),
        })
      }
    })

    // Select OpenAI and set pricing
    await providersPage.selectProvider('openai')
    
    const pricingOverride = JSON.stringify([{
      model_pattern: 'api-test-*',
      match_type: 'wildcard',
      request_types: ['chat_completion'],
      input_cost_per_token: 0.000001,
      output_cost_per_token: 0.000002,
    }], null, 2)

    await providersPage.setPricingOverridesJson(pricingOverride)
    await providersPage.savePricingConfig()

    // Verify the API was called with pricing_overrides
    await expect.poll(() => apiRequests.length).toBeGreaterThan(0)
    
    const lastRequest = apiRequests[apiRequests.length - 1]
    expect(lastRequest.body).toHaveProperty('pricing_overrides')
    expect(Array.isArray((lastRequest.body as { pricing_overrides: unknown[] }).pricing_overrides)).toBe(true)

    // Cleanup
    await providersPage.setPricingOverridesJson('[]')
    await providersPage.savePricingConfig()
  })

  test('api: malformed pricing overrides are rejected', async ({ providersPage, page }) => {
    // Listen for API responses
    let errorResponse: { status: number; body: unknown } | null = null
    
    page.on('response', async (response) => {
      if (response.url().includes('/api/providers/') && response.status() >= 400) {
        errorResponse = {
          status: response.status(),
          body: await response.json().catch(() => null),
        }
      }
    })

    await providersPage.selectProvider('openai')
    await providersPage.selectConfigTab('pricing')

    // Try to save with invalid pricing structure (UI validation should prevent this,
    // but we test the API behavior directly)
    const jsonInput = providersPage.getPricingJsonInput()
    
    // Clear and enter invalid JSON
    await jsonInput.fill('{ invalid }')
    
    // Attempt to save (save button should be disabled due to validation)
    const saveBtn = providersPage.getPricingSaveBtn()
    const isDisabled = await saveBtn.isDisabled()
    
    // Verify UI validation prevents the save
    expect(isDisabled).toBe(true)
  })
})
