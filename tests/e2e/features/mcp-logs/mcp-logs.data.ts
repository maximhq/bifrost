/**
 * Test data factories for MCP logs tests
 */

/**
 * Sample MCP log entry data for testing
 */
export interface SampleMCPLogData {
  mcpClient: string
  tool: string
  status: 'success' | 'error' | 'pending'
  content?: string
}

/**
 * Create sample MCP log search query
 */
export function createMCPLogSearchQuery(overrides: Partial<{ query: string }> = {}): string {
  return overrides.query || `mcp-test-query-${Date.now()}`
}

/**
 * Sample MCP clients for filtering
 */
export const SAMPLE_MCP_CLIENTS = ['test-client-1', 'test-client-2'] as const

/**
 * Sample MCP tools for filtering
 */
export const SAMPLE_MCP_TOOLS = ['tool-1', 'tool-2', 'tool-3'] as const

/**
 * Sample statuses for filtering
 */
export const SAMPLE_STATUSES = ['success', 'error', 'pending'] as const
