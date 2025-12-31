import { VirtualKeyConfig, ProviderConfig, BudgetConfig, RateLimitConfig } from './pages/virtual-keys.page'

/**
 * Factory function to create virtual key test data
 */
export function createVirtualKeyData(overrides: Partial<VirtualKeyConfig> = {}): VirtualKeyConfig {
  const timestamp = Date.now()
  return {
    name: `Test VK ${timestamp}`,
    description: 'E2E test virtual key',
    isActive: true,
    providerConfigs: [],
    ...overrides,
  }
}

/**
 * Factory function to create virtual key with single provider
 */
export function createVirtualKeyWithProvider(
  provider: string,
  vkOverrides: Partial<VirtualKeyConfig> = {}
): VirtualKeyConfig {
  const timestamp = Date.now()
  return {
    name: `Test VK ${provider} ${timestamp}`,
    description: `Virtual key for ${provider}`,
    isActive: true,
    providerConfigs: [
      {
        provider,
        weight: 1.0,
      },
    ],
    ...vkOverrides,
  }
}

/**
 * Factory function to create virtual key with budget
 */
export function createVirtualKeyWithBudget(
  budget: BudgetConfig,
  vkOverrides: Partial<VirtualKeyConfig> = {}
): VirtualKeyConfig {
  const timestamp = Date.now()
  return {
    name: `Test VK Budget ${timestamp}`,
    description: 'Virtual key with budget configuration',
    isActive: true,
    budget,
    ...vkOverrides,
  }
}

/**
 * Factory function to create virtual key with rate limits
 */
export function createVirtualKeyWithRateLimit(
  rateLimit: RateLimitConfig,
  vkOverrides: Partial<VirtualKeyConfig> = {}
): VirtualKeyConfig {
  const timestamp = Date.now()
  return {
    name: `Test VK RateLimit ${timestamp}`,
    description: 'Virtual key with rate limit configuration',
    isActive: true,
    rateLimit,
    ...vkOverrides,
  }
}

/**
 * Factory function to create virtual key with multiple providers
 */
export function createVirtualKeyWithMultipleProviders(
  providers: string[],
  vkOverrides: Partial<VirtualKeyConfig> = {}
): VirtualKeyConfig {
  const timestamp = Date.now()
  const weight = 1.0 / providers.length
  
  return {
    name: `Test VK Multi ${timestamp}`,
    description: `Virtual key with ${providers.length} providers`,
    isActive: true,
    providerConfigs: providers.map((provider) => ({
      provider,
      weight,
    })),
    ...vkOverrides,
  }
}

/**
 * Factory function to create provider config
 */
export function createProviderConfig(overrides: Partial<ProviderConfig> = {}): ProviderConfig {
  return {
    provider: 'openai',
    weight: 1.0,
    ...overrides,
  }
}

/**
 * Sample budget configurations
 */
export const SAMPLE_BUDGETS: Record<string, BudgetConfig> = {
  small: {
    maxLimit: 10,
    resetDuration: '1M',
  },
  medium: {
    maxLimit: 100,
    resetDuration: '1M',
  },
  large: {
    maxLimit: 1000,
    resetDuration: '1M',
  },
  daily: {
    maxLimit: 50,
    resetDuration: '1d',
  },
  weekly: {
    maxLimit: 200,
    resetDuration: '1w',
  },
}

/**
 * Sample rate limit configurations
 */
export const SAMPLE_RATE_LIMITS: Record<string, RateLimitConfig> = {
  conservative: {
    tokenMaxLimit: 10000,
    tokenResetDuration: '1h',
    requestMaxLimit: 100,
    requestResetDuration: '1h',
  },
  moderate: {
    tokenMaxLimit: 100000,
    tokenResetDuration: '1h',
    requestMaxLimit: 1000,
    requestResetDuration: '1h',
  },
  aggressive: {
    tokenMaxLimit: 1000000,
    tokenResetDuration: '1h',
    requestMaxLimit: 10000,
    requestResetDuration: '1h',
  },
  tokenOnly: {
    tokenMaxLimit: 50000,
    tokenResetDuration: '1h',
  },
  requestOnly: {
    requestMaxLimit: 500,
    requestResetDuration: '1h',
  },
}

/**
 * Reset duration options
 */
export const RESET_DURATIONS = [
  { label: '1 Hour', value: '1h' },
  { label: '1 Day', value: '1d' },
  { label: '1 Week', value: '1w' },
  { label: '1 Month', value: '1M' },
] as const
