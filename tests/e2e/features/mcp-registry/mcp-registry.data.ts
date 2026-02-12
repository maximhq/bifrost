import { join, resolve } from 'path'
import { MCPClientConfig, EnvVarLike } from './pages/mcp-registry.page'

/** Normalize header value from env (string or EnvVarLike) to EnvVarLike */
function toEnvVarLike(v: string | EnvVarLike): EnvVarLike {
  if (typeof v === 'object' && v !== null && 'value' in v) return v as EnvVarLike
  return { value: String(v), env_var: '', from_env: false }
}

/**
 * Resolve header value: if string starts with "env.", use process.env[VAR_NAME].
 */
function resolveHeaderValue(v: EnvVarLike): EnvVarLike {
  if (v.value.startsWith('env.')) {
    const envVar = v.value.slice(4)
    const resolved = process.env[envVar]
    if (resolved !== undefined) {
      return { value: resolved, env_var: envVar, from_env: true }
    }
  }
  return v
}

/**
 * Parse MCP_SSE_HEADERS: supports single object, array of objects, or concatenated objects.
 * e.g. {"Authorization":"Bearer ..."},{"ENV_EXA_API_KEY":"..."} → merged into one record
 */
function parseSSEHeadersRaw(raw: string): Record<string, string | EnvVarLike> {
  const trimmed = raw.trim()
  if (!trimmed) return {}
  try {
    const parsed = JSON.parse(trimmed)
    if (typeof parsed === 'object' && parsed !== null && !Array.isArray(parsed)) {
      return parsed
    }
  } catch {
    // Fallback: concatenated objects {"a":1},{"b":2} → wrap in [ ] and merge
  }
  try {
    const asArray = JSON.parse('[' + trimmed + ']')
    if (Array.isArray(asArray) && asArray.every((o) => typeof o === 'object' && o !== null)) {
      return Object.assign({}, ...asArray)
    }
  } catch {
    // ignore
  }
  return {}
}

/**
 * Create basic MCP client data
 */
export function createMCPClientData(overrides: Partial<MCPClientConfig> = {}): MCPClientConfig {
  return {
    name: `test_client_${Date.now()}`,
    connectionType: 'http',
    connectionUrl: 'http://localhost:3001/',
    ...overrides,
  }
}

/**
 * Create HTTP MCP client data
 * Uses http-no-ping-server from examples/mcps
 */
export function createHTTPClientData(overrides: Partial<MCPClientConfig> = {}): MCPClientConfig {
  return createMCPClientData({
    connectionType: 'http',
    connectionUrl: 'http://localhost:3001/', // http-no-ping-server
    authType: 'none',
    isPingAvailable: false, // http-no-ping-server doesn't support ping
    ...overrides,
  })
}

/**
 * Normalize parsed headers to EnvVarLike format (handles both plain values and nested EnvVar objects).
 */
function normalizeHeaders(parsed: Record<string, string | EnvVarLike>): Record<string, EnvVarLike> {
  const out: Record<string, EnvVarLike> = {}
  for (const [k, v] of Object.entries(parsed)) {
    if (v !== undefined && v !== null) {
      out[k] = resolveHeaderValue(toEnvVarLike(v))
    }
  }
  return out
}

/**
 * Create SSE MCP client data.
 * URL and headers are injected via workflow env: MCP_SSE_URL, MCP_SSE_HEADERS (JSON).
 * Supports: {"Authorization":"Bearer ...","K":"V"} or {"Authorization":{"value":"...","env_var":"","from_env":false}}.
 */
export function createSSEClientData(overrides: Partial<MCPClientConfig> = {}): MCPClientConfig {
  const connectionUrl = "https://ts-mcp-sse-proxy.fly.dev/npx%20-y%20exa-mcp-server/sse"
  const raw = process.env.MCP_SSE_HEADERS
  const parsed = raw ? parseSSEHeadersRaw(raw) : {}
  const headers = normalizeHeaders(parsed)
  return createMCPClientData({
    connectionType: 'sse',
    connectionUrl,
    authType: 'headers',
    headers: headers,
    isPingAvailable: false,
    ...overrides,
  })
}

/**
 * Create STDIO MCP client data
 * Uses test-tools-server from examples/mcps
 */
export function createSTDIOClientData(overrides: Partial<MCPClientConfig> = {}): MCPClientConfig {
  // Use the built test-tools-server
  const REPO_ROOT = resolve(__dirname, '..', '..', '..', '..')

  // Then for the stdio server:
  const serverPath = join(REPO_ROOT, 'examples', 'mcps', 'test-tools-server', 'dist', 'index.js')
  return createMCPClientData({
    name: `stdio_client_${Date.now()}`,
    connectionType: 'stdio',
    command: 'node',
    args: serverPath, // Run the actual MCP server
    ...overrides,
  })
}

/**
 * Create HTTP client with headers auth
 */
export function createHTTPClientWithHeaders(overrides: Partial<MCPClientConfig> = {}): MCPClientConfig {
  return createMCPClientData({
    connectionType: 'http',
    connectionUrl: 'http://localhost:3001/', // http-no-ping-server
    authType: 'headers',
    isPingAvailable: false,
    ...overrides,
  })
}

/**
 * Create HTTP client with OAuth auth (minimal config for testing)
 * Note: http-no-ping-server doesn't support OAuth, so this is for UI testing only
 */
export function createHTTPClientWithOAuth(overrides: Partial<MCPClientConfig> = {}): MCPClientConfig {
  return createMCPClientData({
    name: `oauth_client_${Date.now()}`,
    connectionType: 'http',
    connectionUrl: 'http://localhost:3001/', // http-no-ping-server
    authType: 'oauth',
    oauthClientId: 'test-client-id',
    oauthAuthorizeUrl: 'http://localhost:3001/oauth/authorize',
    oauthTokenUrl: 'http://localhost:3001/oauth/token',
    isPingAvailable: false,
    ...overrides,
  })
}

/**
 * Create client with code mode enabled
 */
export function createCodeModeClientData(overrides: Partial<MCPClientConfig> = {}): MCPClientConfig {
  return createMCPClientData({
    isCodeMode: true,
    ...overrides,
  })
}
