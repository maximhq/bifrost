/**
 * Parametrization utilities for cross-provider testing.
 *
 * This module provides utilities for testing across multiple AI providers
 * with automatic scenario-based filtering.
 */

import { getConfig } from './config-loader'

export interface ProviderModelParam {
  provider: string
  model: string
}

export interface ProviderModelVkParam extends ProviderModelParam {
  vkEnabled: boolean
}

/**
 * Get cross-provider parameters for a specific scenario.
 *
 * @param scenario - Test scenario name
 * @param includeProviders - Optional list of providers to include
 * @param excludeProviders - Optional list of providers to exclude
 * @returns Array of [provider, model] tuples for test parametrization
 */
export function getCrossProviderParamsForScenario(
  scenario: string,
  includeProviders?: string[],
  excludeProviders?: string[]
): ProviderModelParam[] {
  const config = getConfig()

  // Get providers that support this scenario
  let providers = config.getProvidersForScenario(scenario)

  // Apply include filter
  if (includeProviders && includeProviders.length > 0) {
    providers = providers.filter((p) => includeProviders.includes(p))
  }

  // Apply exclude filter
  if (excludeProviders && excludeProviders.length > 0) {
    providers = providers.filter((p) => !excludeProviders.includes(p))
  }

  // Generate { provider, model } objects
  // Automatically maps: scenario → capability → model
  const params: ProviderModelParam[] = []

  for (const provider of providers.sort()) {
    // Map scenario to capability, then get model
    const capability = config.getScenarioCapability(scenario)
    const model = config.getProviderModel(provider, capability)

    // Only add if provider has a model for this scenario's capability
    if (model) {
      params.push({ provider, model })
    }
  }

  // If no providers available, return a dummy tuple to avoid test errors
  // The test will be skipped with appropriate message
  if (params.length === 0) {
    params.push({ provider: '_no_providers_', model: '_no_model_' })
  }

  return params
}

/**
 * Get cross-provider parameters with virtual key flag for test parametrization.
 *
 * When virtual key is configured, each provider/model combo is tested twice:
 * once without VK (vkEnabled=false) and once with VK (vkEnabled=true).
 *
 * @param scenario - Test scenario name
 * @param includeProviders - Optional list of providers to include
 * @param excludeProviders - Optional list of providers to exclude
 * @returns Array of { provider, model, vkEnabled } objects
 */
export function getCrossProviderParamsWithVkForScenario(
  scenario: string,
  includeProviders?: string[],
  excludeProviders?: string[]
): ProviderModelVkParam[] {
  const config = getConfig()

  // Get base params without VK
  const baseParams = getCrossProviderParamsForScenario(scenario, includeProviders, excludeProviders)

  // Handle the dummy tuple case
  if (baseParams.length === 1 && baseParams[0].provider === '_no_providers_') {
    return [{ provider: '_no_providers_', model: '_no_model_', vkEnabled: false }]
  }

  // Build params list with VK flag
  const params: ProviderModelVkParam[] = []
  const vkConfigured = config.isVirtualKeyConfigured()

  for (const { provider, model } of baseParams) {
    // Always add the non-VK variant
    params.push({ provider, model, vkEnabled: false })

    // Add VK variant only if VK is configured
    if (vkConfigured) {
      params.push({ provider, model, vkEnabled: true })
    }
  }

  return params
}

/**
 * Format test ID for virtual key parameterized tests.
 *
 * @param provider - Provider name
 * @param model - Model name
 * @param vkEnabled - Whether VK is enabled
 * @returns Formatted test ID string
 */
export function formatVkTestId(provider: string, model: string, vkEnabled: boolean): string {
  const vkSuffix = vkEnabled ? 'with_vk' : 'no_vk'
  return `${provider}-${model}-${vkSuffix}`
}

/**
 * Format provider and model into the standard "provider/model" format.
 *
 * @param provider - Provider name
 * @param model - Model name
 * @returns Formatted string "provider/model"
 */
export function formatProviderModel(provider: string, model: string): string {
  return `${provider}/${model}`
}

/**
 * Helper to check if test should be skipped due to no providers.
 */
export function shouldSkipNoProviders(params: ProviderModelParam | ProviderModelVkParam): boolean {
  return params.provider === '_no_providers_'
}

/**
 * Get test cases for Vitest's describe.each or it.each.
 *
 * Returns an array suitable for use with Vitest's parametrization.
 *
 * @example
 * ```typescript
 * const testCases = getTestCasesForScenario('simple_chat')
 * describe.each(testCases)('Simple Chat - $provider', ({ provider, model }) => {
 *   it('should complete a simple chat', async () => {
 *     // test implementation
 *   })
 * })
 * ```
 */
export function getTestCasesForScenario(
  scenario: string,
  includeProviders?: string[],
  excludeProviders?: string[]
): ProviderModelParam[] {
  return getCrossProviderParamsForScenario(scenario, includeProviders, excludeProviders)
}

/**
 * Get test cases with VK variants for Vitest's describe.each or it.each.
 *
 * @example
 * ```typescript
 * const testCases = getTestCasesWithVkForScenario('simple_chat')
 * describe.each(testCases)('Simple Chat - $provider (VK: $vkEnabled)', ({ provider, model, vkEnabled }) => {
 *   it('should complete a simple chat', async () => {
 *     // test implementation
 *   })
 * })
 * ```
 */
export function getTestCasesWithVkForScenario(
  scenario: string,
  includeProviders?: string[],
  excludeProviders?: string[]
): ProviderModelVkParam[] {
  return getCrossProviderParamsWithVkForScenario(scenario, includeProviders, excludeProviders)
}

/**
 * Create a test name with provider and model info.
 */
export function createTestName(baseName: string, provider: string, model: string): string {
  return `${baseName} [${provider}/${model}]`
}

/**
 * Create a test name with provider, model, and VK info.
 */
export function createTestNameWithVk(baseName: string, provider: string, model: string, vkEnabled: boolean): string {
  const vkSuffix = vkEnabled ? ' (with VK)' : ''
  return `${baseName} [${provider}/${model}]${vkSuffix}`
}