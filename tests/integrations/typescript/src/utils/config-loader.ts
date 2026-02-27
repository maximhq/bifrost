/**
 * Configuration loader for Bifrost integration tests.
 *
 * This module loads configuration from config.yml and provides utilities
 * for constructing integration URLs through the Bifrost gateway.
 */

import { readFileSync, existsSync } from 'fs'
import { resolve, dirname } from 'path'
import { fileURLToPath } from 'url'
import { parse as parseYaml } from 'yaml'

// Get __dirname equivalent for ES modules
const __filename = fileURLToPath(import.meta.url)
const __dirname = dirname(__filename)

// Integration to provider mapping
// Maps integration names to their underlying provider configurations
export const INTEGRATION_TO_PROVIDER_MAP: Record<string, string> = {
  openai: 'openai',
  anthropic: 'anthropic',
  google: 'gemini', // Google integration uses Gemini provider
  litellm: 'openai', // LiteLLM defaults to OpenAI
  langchain: 'openai', // LangChain defaults to OpenAI
  pydanticai: 'openai', // Pydantic AI defaults to OpenAI
  bedrock: 'bedrock', // Bedrock defaults to Amazon provider
  azure: 'azure',
}

export interface BifrostConfig {
  base_url: string
  endpoints: Record<string, string>
}

export interface ApiConfig {
  timeout: number
  max_retries: number
  retry_delay: number
}

export interface TestSettings {
  max_tokens: Record<string, number | null>
  timeouts: Record<string, number>
  retries: {
    max_attempts: number
    delay: number
  }
}

export interface ProviderScenarios {
  [scenario: string]: boolean
}

export interface RawConfig {
  bifrost: BifrostConfig
  api: ApiConfig
  providers: Record<string, Record<string, string | string[]>>
  provider_api_keys: Record<string, string>
  provider_scenarios: Record<string, ProviderScenarios>
  scenario_capabilities: Record<string, string>
  model_capabilities: Record<string, Record<string, unknown>>
  test_settings: TestSettings
  integration_settings: Record<string, Record<string, unknown>>
  environments: Record<string, Record<string, unknown>>
  logging: Record<string, unknown>
  virtual_key?: {
    enabled: boolean
    value: string
  }
}

class ConfigLoader {
  private config: RawConfig | null = null
  private configPath: string

  constructor(configPath?: string) {
    if (configPath) {
      this.configPath = configPath
    } else {
      // Look for config.yml in project root (symlinked from python)
      this.configPath = resolve(__dirname, '../../config.yml')
    }
    this.loadConfig()
  }

  private loadConfig(): void {
    if (!existsSync(this.configPath)) {
      throw new Error(`Configuration file not found: ${this.configPath}`)
    }

    const rawContent = readFileSync(this.configPath, 'utf-8')
    let rawConfig: unknown
    try {
      rawConfig = parseYaml(rawContent)
    } catch (e) {
      throw new Error(`Failed to parse YAML config at ${this.configPath}: ${String(e)}`)
    }
    if (rawConfig == null || typeof rawConfig !== 'object') {
      throw new Error(`Invalid YAML config at ${this.configPath}: expected a top-level object`)
    }

    // Expand environment variables
    this.config = this.expandEnvVars(rawConfig) as RawConfig
  }

  private expandEnvVars(obj: unknown): unknown {
    if (typeof obj === 'object' && obj !== null) {
      if (Array.isArray(obj)) {
        return obj.map((item) => this.expandEnvVars(item))
      }
      const result: Record<string, unknown> = {}
      for (const [key, value] of Object.entries(obj)) {
        result[key] = this.expandEnvVars(value)
      }
      return result
    }

    if (typeof obj === 'string') {
      // Handle ${VAR:-default} syntax
      return obj.replace(/\$\{([^}]+)\}/g, (_, varExpr: string) => {
        if (varExpr.includes(':-')) {
          const [varName, defaultValue] = varExpr.split(':-')
          return process.env[varName] || defaultValue
        }
        return process.env[varExpr] || ''
      })
    }

    return obj
  }

  getIntegrationUrl(integration: string): string {
    if (!this.config) throw new Error('Config not loaded')

    const bifrostConfig = this.config.bifrost
    const baseUrl = bifrostConfig.base_url
    const endpoint = bifrostConfig.endpoints[integration]

    if (!endpoint) {
      throw new Error(`No endpoint configured for integration: ${integration}`)
    }

    // Normalize URL to avoid double slashes
    const base = baseUrl.replace(/\/+$/, '')
    const ep = String(endpoint).replace(/^\/+/, '')
    return `${base}/${ep}`
  }

  getBifrostConfig(): BifrostConfig {
    if (!this.config) throw new Error('Config not loaded')
    return this.config.bifrost
  }

  getModel(integration: string, modelType: string = 'chat'): string {
    // Map integration to provider
    const provider = INTEGRATION_TO_PROVIDER_MAP[integration]
    if (!provider) {
      throw new Error(
        `Unknown integration: ${integration}. Valid integrations: ${Object.keys(INTEGRATION_TO_PROVIDER_MAP).join(', ')}`
      )
    }

    // Get model from provider configuration
    return this.getProviderModel(provider, modelType)
  }

  getModelAlternatives(integration: string): string[] {
    const provider = INTEGRATION_TO_PROVIDER_MAP[integration]
    if (!provider || !this.config?.providers?.[provider]) {
      return []
    }

    const alternatives = this.config.providers[provider].alternatives
    return Array.isArray(alternatives) ? alternatives : []
  }

  getModelCapabilities(model: string): Record<string, unknown> {
    if (!this.config) throw new Error('Config not loaded')

    return (
      this.config.model_capabilities[model] || {
        chat: true,
        tools: false,
        vision: false,
        max_tokens: 4096,
        context_window: 4096,
      }
    )
  }

  supportsCapability(model: string, capability: string): boolean {
    const caps = this.getModelCapabilities(model)
    return caps[capability] === true
  }

  getApiConfig(): ApiConfig {
    if (!this.config) throw new Error('Config not loaded')
    return this.config.api
  }

  getTestSettings(): TestSettings {
    if (!this.config) throw new Error('Config not loaded')
    return this.config.test_settings
  }

  getIntegrationSettings(integration: string): Record<string, unknown> {
    if (!this.config) throw new Error('Config not loaded')
    return this.config.integration_settings[integration] || {}
  }

  getEnvironmentConfig(environment?: string): Record<string, unknown> {
    if (!this.config) throw new Error('Config not loaded')
    const env = environment || process.env.TEST_ENV || 'development'
    return this.config.environments[env] || {}
  }

  getLoggingConfig(): Record<string, unknown> {
    if (!this.config) throw new Error('Config not loaded')
    return this.config.logging
  }

  listIntegrations(): string[] {
    return Object.keys(INTEGRATION_TO_PROVIDER_MAP)
  }

  listModels(integration?: string): Record<string, unknown> {
    if (!this.config) throw new Error('Config not loaded')

    if (integration) {
      const provider = INTEGRATION_TO_PROVIDER_MAP[integration]
      if (!provider) {
        throw new Error(`Unknown integration: ${integration}`)
      }

      if (!this.config.providers?.[provider]) {
        throw new Error(`No provider configuration for: ${provider}`)
      }

      return { [integration]: this.config.providers[provider] }
    }

    // Return all providers mapped to their integration names
    const result: Record<string, unknown> = {}
    for (const [integ, provider] of Object.entries(INTEGRATION_TO_PROVIDER_MAP)) {
      if (this.config.providers?.[provider]) {
        result[integ] = this.config.providers[provider]
      }
    }

    return result
  }

  validateConfig(): boolean {
    if (!this.config) throw new Error('Config not loaded')

    const requiredSections = ['bifrost', 'providers', 'api', 'test_settings']

    for (const section of requiredSections) {
      if (!(section in this.config)) {
        throw new Error(`Missing required configuration section: ${section}`)
      }
    }

    // Validate Bifrost configuration
    const bifrost = this.config.bifrost
    if (!bifrost.base_url || !bifrost.endpoints) {
      throw new Error('Bifrost configuration missing base_url or endpoints')
    }

    // Validate that all integrations map to valid providers
    for (const [integration, provider] of Object.entries(INTEGRATION_TO_PROVIDER_MAP)) {
      if (!this.config.providers[provider]) {
        throw new Error(
          `Integration '${integration}' maps to provider '${provider}' which is not configured in providers section`
        )
      }
    }

    return true
  }

  printConfigSummary(): void {
    if (!this.config) throw new Error('Config not loaded')

    console.log('🔧 BIFROST INTEGRATION TEST CONFIGURATION (TypeScript)')
    console.log('='.repeat(80))

    // Bifrost configuration
    const bifrost = this.getBifrostConfig()
    console.log('\n🌉 BIFROST GATEWAY:')
    console.log(`  Base URL: ${bifrost.base_url}`)
    console.log('  Endpoints:')
    for (const [integration, endpoint] of Object.entries(bifrost.endpoints)) {
      const fullUrl = `${bifrost.base_url.replace(/\/$/, '')}/${endpoint}`
      console.log(`    ${integration}: ${fullUrl}`)
    }

    // Model configurations
    console.log('\n🤖 MODEL CONFIGURATIONS (via providers):')
    for (const [integration, provider] of Object.entries(INTEGRATION_TO_PROVIDER_MAP)) {
      if (this.config.providers?.[provider]) {
        const models = this.config.providers[provider]
        console.log(`  ${integration.toUpperCase()} → ${provider}:`)
        console.log(`    Chat: ${models.chat || 'N/A'}`)
        console.log(`    Vision: ${models.vision || 'N/A'}`)
        console.log(`    Tools: ${models.tools || 'N/A'}`)
        const alternatives = models.alternatives
        console.log(`    Alternatives: ${Array.isArray(alternatives) ? alternatives.length : 0} models`)
      }
    }

    // API settings
    const apiConfig = this.getApiConfig()
    console.log('\n⚙️  API SETTINGS:')
    console.log(`  Timeout: ${apiConfig.timeout}s`)
    console.log(`  Max Retries: ${apiConfig.max_retries}`)
    console.log(`  Retry Delay: ${apiConfig.retry_delay}s`)

    console.log(`\n✅ Configuration loaded successfully from: ${this.configPath}`)
  }

  getProviderModel(provider: string, capability: string = 'chat'): string {
    if (!this.config?.providers) {
      return ''
    }

    const providerModels = this.config.providers[provider]
    if (!providerModels) {
      return ''
    }

    const model = providerModels[capability]
    return typeof model === 'string' ? model : ''
  }

  getProviderApiKeyEnv(provider: string): string {
    if (!this.config?.provider_api_keys) {
      return ''
    }
    return this.config.provider_api_keys[provider] || ''
  }

  isProviderAvailable(provider: string): boolean {
    const envVar = this.getProviderApiKeyEnv(provider)
    if (!envVar) {
      return false
    }

    const apiKey = process.env[envVar]
    return apiKey !== undefined && apiKey.trim() !== ''
  }

  getAvailableProviders(): string[] {
    if (!this.config?.providers) {
      return []
    }

    const available: string[] = []
    for (const provider of Object.keys(this.config.providers)) {
      if (this.isProviderAvailable(provider)) {
        available.push(provider)
      }
    }

    return available
  }

  providerSupportsScenario(provider: string, scenario: string): boolean {
    if (!this.config?.provider_scenarios?.[provider]) {
      return false
    }

    return this.config.provider_scenarios[provider][scenario] === true
  }

  getProvidersForScenario(scenario: string): string[] {
    const availableProviders = this.getAvailableProviders()
    const providers: string[] = []

    for (const provider of availableProviders) {
      if (this.providerSupportsScenario(provider, scenario)) {
        providers.push(provider)
      }
    }

    return providers
  }

  getScenarioCapability(scenario: string): string {
    if (!this.config?.scenario_capabilities) {
      return 'chat'
    }

    return this.config.scenario_capabilities[scenario] || 'chat'
  }

  getVirtualKey(): string {
    if (!this.config?.virtual_key?.enabled) {
      return ''
    }
    return this.config.virtual_key.value || ''
  }

  isVirtualKeyConfigured(): boolean {
    const vk = this.getVirtualKey()
    return vk.trim() !== ''
  }
}

// Global configuration instance
let configLoader: ConfigLoader | null = null

export function getConfig(): ConfigLoader {
  if (!configLoader) {
    configLoader = new ConfigLoader()
  }
  return configLoader
}

export function getIntegrationUrl(integration: string): string {
  return getConfig().getIntegrationUrl(integration)
}

export function getModel(integration: string, modelType: string = 'chat'): string {
  return getConfig().getModel(integration, modelType)
}

export function getModelCapabilities(model: string): Record<string, unknown> {
  return getConfig().getModelCapabilities(model)
}

export function supportsCapability(model: string, capability: string): boolean {
  return getConfig().supportsCapability(model, capability)
}

export function getProviderModel(provider: string, capability: string = 'chat'): string {
  return getConfig().getProviderModel(provider, capability)
}

export function isProviderAvailable(provider: string): boolean {
  return getConfig().isProviderAvailable(provider)
}

export function getAvailableProviders(): string[] {
  return getConfig().getAvailableProviders()
}

export function providerSupportsScenario(provider: string, scenario: string): boolean {
  return getConfig().providerSupportsScenario(provider, scenario)
}

export function getProvidersForScenario(scenario: string): string[] {
  return getConfig().getProvidersForScenario(scenario)
}

export function getVirtualKey(): string {
  return getConfig().getVirtualKey()
}

export function isVirtualKeyConfigured(): boolean {
  return getConfig().isVirtualKeyConfigured()
}

export function getApiConfig(): ApiConfig {
  return getConfig().getApiConfig()
}

export function getTestSettings(): TestSettings {
  return getConfig().getTestSettings()
}

export function getIntegrationSettings(integration: string): Record<string, unknown> {
  return getConfig().getIntegrationSettings(integration)
}

// Export class for direct use if needed
export { ConfigLoader }
