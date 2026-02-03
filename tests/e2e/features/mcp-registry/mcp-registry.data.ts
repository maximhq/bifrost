import { resolve } from 'path'
import { MCPClientConfig, MCPConnectionType, MCPAuthType } from './pages/mcp-registry.page'

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
 * Create SSE MCP client data
 * Uses http-no-ping-server SSE endpoint from examples/mcps
 */
export function createSSEClientData(overrides: Partial<MCPClientConfig> = {}): MCPClientConfig {
  return createMCPClientData({
    connectionType: 'sse',
    connectionUrl: 'http://localhost:3001/sse', // StreamableHTTP provides /sse endpoint
    authType: 'none',
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
  const serverPath = resolve(__dirname, '../../../../examples/mcps/test-tools-server/dist/index.js')

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
