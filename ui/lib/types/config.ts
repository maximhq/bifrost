// Configuration types that match the Go backend structures

// ModelProvider enum matching Go's schemas.ModelProvider
export type ModelProvider = 'openai' | 'azure' | 'anthropic' | 'bedrock' | 'cohere' | 'vertex' | 'mistral' | 'ollama' | 'groq' | 'sgl'

// AzureKeyConfig matching Go's schemas.AzureKeyConfig
export interface AzureKeyConfig {
  endpoint: string
  deployments?: Record<string, string> | string // Allow string during editing
  api_version?: string
}

// VertexKeyConfig matching Go's schemas.VertexKeyConfig
export interface VertexKeyConfig {
  project_id: string
  region: string
  auth_credentials: string // Always string - JSON string or env var
}

// Key structure matching Go's schemas.Key
export interface Key {
  id: string
  value: string
  models: string[]
  weight: number
  azure_key_config?: AzureKeyConfig
  vertex_key_config?: VertexKeyConfig
}

// NetworkConfig matching Go's schemas.NetworkConfig
export interface NetworkConfig {
  base_url?: string
  extra_headers?: Record<string, string>
  default_request_timeout_in_seconds: number
  max_retries: number
  retry_backoff_initial: number // Duration in milliseconds
  retry_backoff_max: number // Duration in milliseconds
}

// ConcurrencyAndBufferSize matching Go's schemas.ConcurrencyAndBufferSize
export interface ConcurrencyAndBufferSize {
  concurrency: number
  buffer_size: number
}

// MetaConfig interface - provider-specific configuration
export interface MetaConfig {
  // AWS Bedrock specific
  secret_access_key?: string
  region?: string
  session_token?: string
  arn?: string
  inference_profiles?: Record<string, string>

  // Generic fields for extensibility
  [key: string]: unknown
}

// Proxy types matching Go's schemas.ProxyType
export type ProxyType = 'none' | 'http' | 'socks5' | 'environment'

// ProxyConfig matching Go's schemas.ProxyConfig
export interface ProxyConfig {
  type: ProxyType
  url?: string
  username?: string
  password?: string
}

// ProviderConfig matching Go's lib.ProviderConfig
export interface ProviderConfig {
  keys: Key[]
  network_config: NetworkConfig
  meta_config?: MetaConfig
  concurrency_and_buffer_size: ConcurrencyAndBufferSize
  proxy_config?: ProxyConfig
}

// ProviderResponse matching Go's ProviderResponse
export interface ProviderResponse extends ProviderConfig {
  name: ModelProvider
}

// ListProvidersResponse matching Go's ListProvidersResponse
export interface ListProvidersResponse {
  providers: ProviderResponse[]
  total: number
}

// AddProviderRequest matching Go's AddProviderRequest
export interface AddProviderRequest {
  provider: ModelProvider
  keys: Key[]
  network_config?: NetworkConfig
  meta_config?: MetaConfig
  concurrency_and_buffer_size?: ConcurrencyAndBufferSize
  proxy_config?: ProxyConfig
}

// UpdateProviderRequest matching Go's UpdateProviderRequest
export interface UpdateProviderRequest {
  keys: Key[]
  network_config: NetworkConfig
  meta_config?: MetaConfig
  concurrency_and_buffer_size: ConcurrencyAndBufferSize
  proxy_config: ProxyConfig
}

// BifrostErrorResponse matching Go's schemas.BifrostError
export interface BifrostErrorResponse {
  event_id?: string
  type?: string
  is_bifrost_error: boolean
  status_code?: number
  error: {
    message: string
    type?: string
    code?: string
    param?: string
  }
}

// Core Bifrost configuration types
export interface CoreConfig {
  drop_excess_requests?: boolean
  initial_pool_size?: number
  prometheus_labels?: string
  enable_logging?: boolean
}

// Utility types for form handling
export interface ProviderFormData {
  provider: ModelProvider | ''
  keys: Array<{ value: string; models: string[]; weight: number }>
  network_config: {
    baseURL?: string
    defaultRequestTimeoutInSeconds: number
    maxRetries: number
  }
  concurrency_and_buffer_size: {
    concurrency: number
    bufferSize: number
  }
}

// Status types
export type ProviderStatus = 'active' | 'error' | 'added' | 'updated' | 'deleted'
