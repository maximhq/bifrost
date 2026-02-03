import { PluginConfig } from './pages/plugins.page'
import { ensureTestPluginExists } from './plugins-test-helper'

/**
 * Sanitize plugin name to only contain letters, numbers, hyphens, and underscores
 */
function sanitizePluginName(name: string): string {
  return name
    .replace(/\s+/g, '-') // Replace spaces with hyphens
    .replace(/[^A-Za-z0-9-_]/g, '') // Remove any invalid characters
    .replace(/-+/g, '-') // Replace multiple hyphens with single hyphen
    .replace(/^-|-$/g, '') // Remove leading/trailing hyphens
}

/**
 * Get the test plugin path (builds if necessary)
 */
let testPluginPath: string | null = null
function getTestPluginPath(): string {
  if (!testPluginPath) {
    testPluginPath = ensureTestPluginExists()
  }
  return testPluginPath
}

export function createPluginData(overrides: Partial<PluginConfig> = {}): PluginConfig {
  const baseName = overrides.name || `test-plugin-${Date.now()}`
  const pluginPath = overrides.path || getTestPluginPath()
  
  return {
    name: sanitizePluginName(baseName),
    path: pluginPath,
    ...overrides,
  }
}
