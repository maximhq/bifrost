import { test as base } from '@playwright/test'
import { randomUUID } from 'crypto'

/**
 * Test data types
 */
export interface ProviderKeyConfig {
  name: string
  value: string
  models?: string[]
  weight?: number
}

export interface CustomProviderConfig {
  name: string
  baseProviderType: 'openai' | 'anthropic' | 'gemini' | 'cohere' | 'bedrock' | string
  baseUrl?: string
  authType?: 'api_key' | 'bearer' | 'basic' | 'none'
  isKeyless?: boolean
}

export interface VirtualKeyConfig {
  name: string
  description?: string
  isActive?: boolean
  providerConfigs?: ProviderConfigItem[]
  budget?: BudgetConfig
  rateLimit?: RateLimitConfig
  teamId?: string
  customerId?: string
}

export interface ProviderConfigItem {
  provider: string
  weight?: number
  allowedModels?: string[]
  keyIds?: string[]
  budget?: BudgetConfig
  rateLimit?: RateLimitConfig
}

export interface BudgetConfig {
  maxLimit: number
  resetDuration: string
}

export interface RateLimitConfig {
  tokenMaxLimit?: number
  tokenResetDuration?: string
  requestMaxLimit?: number
  requestResetDuration?: string
}

/**
 * Test data fixture type
 */
type TestDataFixtures = {
  testData: TestDataFactory
}

/**
 * Factory for creating test data with unique identifiers
 */
export class TestDataFactory {
  private counter = 0
  private runId = randomUUID()

  /**
   * Generate a unique ID for test data
   */
  uniqueId(prefix = 'test'): string {
    this.counter++
    return `${prefix}-${this.runId}-${this.counter}`
  }

  /**
   * Create provider key test data
   */
  createProviderKey(overrides: Partial<ProviderKeyConfig> = {}): ProviderKeyConfig {
    return {
      name: this.uniqueId('key'),
      value: `sk-test-${this.uniqueId()}`,
      models: [],
      weight: 1.0,
      ...overrides,
    }
  }

  /**
   * Create custom provider test data
   */
  createCustomProvider(overrides: Partial<CustomProviderConfig> = {}): CustomProviderConfig {
    return {
      name: this.uniqueId('provider'),
      baseProviderType: 'openai',
      baseUrl: 'https://api.example.com',
      authType: 'api_key',
      ...overrides,
    }
  }

  /**
   * Create virtual key test data
   */
  createVirtualKey(overrides: Partial<VirtualKeyConfig> = {}): VirtualKeyConfig {
    return {
      name: this.uniqueId('vk'),
      description: 'Test virtual key',
      isActive: true,
      providerConfigs: [],
      ...overrides,
    }
  }

  /**
   * Create virtual key with budget
   */
  createVirtualKeyWithBudget(
    budgetOverrides: Partial<BudgetConfig> = {},
    vkOverrides: Partial<VirtualKeyConfig> = {}
  ): VirtualKeyConfig {
    return this.createVirtualKey({
      budget: {
        maxLimit: 100,
        resetDuration: '1M',
        ...budgetOverrides,
      },
      ...vkOverrides,
    })
  }

  /**
   * Create virtual key with rate limits
   */
  createVirtualKeyWithRateLimit(
    rateLimitOverrides: Partial<RateLimitConfig> = {},
    vkOverrides: Partial<VirtualKeyConfig> = {}
  ): VirtualKeyConfig {
    return this.createVirtualKey({
      rateLimit: {
        tokenMaxLimit: 10000,
        tokenResetDuration: '1h',
        requestMaxLimit: 1000,
        requestResetDuration: '1h',
        ...rateLimitOverrides,
      },
      ...vkOverrides,
    })
  }

  /**
   * Create provider config item for virtual key
   */
  createProviderConfigItem(overrides: Partial<ProviderConfigItem> = {}): ProviderConfigItem {
    return {
      provider: 'openai',
      weight: 1.0,
      allowedModels: [],
      keyIds: [],
      ...overrides,
    }
  }
}

/**
 * Extended test with test data fixture
 */
export const testWithData = base.extend<TestDataFixtures>({
  testData: async (_, use) => {
    await use(new TestDataFactory())
  },
})
