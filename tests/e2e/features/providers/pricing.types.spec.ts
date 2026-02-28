/**
 * Type validation tests for Pricing Overrides
 * These tests ensure the type definitions match the expected schemas
 */

import { expect } from '@playwright/test'
import { test } from '../../core/fixtures/base.fixture'
import { ProviderPricingOverride } from '../../../../ui/lib/types/config'

/**
 * Validate that the ProviderPricingOverride type has all required fields
 * This test ensures type safety for the pricing overrides feature
 */
test.describe('Pricing Overrides Type Validation', () => {
  test('ProviderPricingOverride type has required fields', async () => {
    // Create a valid pricing override object
    const validOverride: ProviderPricingOverride = {
      model_pattern: 'gpt-4o*',
      match_type: 'wildcard',
      request_types: ['chat_completion'],
      input_cost_per_token: 0.000005,
      output_cost_per_token: 0.000015,
    }

    // Verify the object structure
    expect(validOverride.model_pattern).toBe('gpt-4o*')
    expect(validOverride.match_type).toBe('wildcard')
    expect(validOverride.request_types).toContain('chat_completion')
    expect(validOverride.input_cost_per_token).toBe(0.000005)
    expect(validOverride.output_cost_per_token).toBe(0.000015)
  })

  test('ProviderPricingOverride supports all match types', async () => {
    const exactMatch: ProviderPricingOverride = {
      model_pattern: 'gpt-4o',
      match_type: 'exact',
      request_types: ['chat_completion'],
    }

    const wildcardMatch: ProviderPricingOverride = {
      model_pattern: 'gpt-4o*',
      match_type: 'wildcard',
      request_types: ['chat_completion'],
    }

    const regexMatch: ProviderPricingOverride = {
      model_pattern: '^gpt-4.*$',
      match_type: 'regex',
      request_types: ['chat_completion'],
    }

    expect(exactMatch.match_type).toBe('exact')
    expect(wildcardMatch.match_type).toBe('wildcard')
    expect(regexMatch.match_type).toBe('regex')
  })

  test('ProviderPricingOverride supports all request types', async () => {
    const allRequestTypes: ProviderPricingOverride['request_types'] = [
      'text_completion',
      'text_completion_stream',
      'chat_completion',
      'chat_completion_stream',
      'responses',
      'responses_stream',
      'embedding',
      'rerank',
      'speech',
      'speech_stream',
      'transcription',
      'transcription_stream',
      'image_generation',
      'image_generation_stream',
    ]

    const override: ProviderPricingOverride = {
      model_pattern: 'test-*',
      match_type: 'wildcard',
      request_types: allRequestTypes,
    }

    expect(override.request_types).toHaveLength(14)
  })

  test('ProviderPricingOverride supports advanced pricing fields', async () => {
    const advancedOverride: ProviderPricingOverride = {
      model_pattern: 'claude-3-opus',
      match_type: 'exact',
      request_types: ['chat_completion'],
      input_cost_per_token: 0.000015,
      output_cost_per_token: 0.000075,
      input_cost_per_token_above_128k_tokens: 0.00003,
      output_cost_per_token_above_128k_tokens: 0.00015,
      cache_read_input_token_cost: 0.0000015,
      cache_creation_input_token_cost: 0.00001875,
    }

    expect(advancedOverride.input_cost_per_token_above_128k_tokens).toBe(0.00003)
    expect(advancedOverride.cache_read_input_token_cost).toBe(0.0000015)
  })
})
