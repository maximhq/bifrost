/**
 * Global test setup for Vitest
 *
 * This file is loaded before all tests run.
 * It sets up environment variables and global configuration.
 */

import { config } from 'dotenv'
import { resolve, dirname } from 'path'
import { fileURLToPath } from 'url'

// ES module compatibility - __dirname is not available in ESM
const __filename = fileURLToPath(import.meta.url)
const __dirname = dirname(__filename)

// Load environment variables from .env file in project root
config({ path: resolve(__dirname, '../.env') })

// Also try loading from workspace root
config({ path: resolve(__dirname, '../../../../.env') })

// Set default environment variables if not present
if (!process.env.BIFROST_BASE_URL) {
  process.env.BIFROST_BASE_URL = 'http://localhost:8080'
}

// Log test environment info
console.log('\n🧪 Bifrost TypeScript Integration Tests')
console.log('='.repeat(50))
console.log(`📍 Bifrost URL: ${process.env.BIFROST_BASE_URL}`)
console.log(`🕐 Started at: ${new Date().toISOString()}`)

// Check for available API keys
const apiKeys = {
  OpenAI: !!process.env.OPENAI_API_KEY,
  Anthropic: !!process.env.ANTHROPIC_API_KEY,
  Google: !!process.env.GEMINI_API_KEY,
  Bedrock: !!process.env.AWS_ACCESS_KEY_ID,
  Cohere: !!process.env.COHERE_API_KEY,
  Azure: !!process.env.AZURE_API_KEY,
}

console.log('\n🔑 Available API Keys:')
for (const [provider, available] of Object.entries(apiKeys)) {
  const status = available ? '✅' : '❌'
  console.log(`  ${status} ${provider}`)
}
console.log('='.repeat(50) + '\n')

// Global test timeout (can be overridden per test)
// This is set in vitest.config.ts but documented here
// Default: 300000ms (5 minutes) for integration tests

// Export for use in tests if needed
export const testEnvironment = {
  bifrostUrl: process.env.BIFROST_BASE_URL,
  availableProviders: Object.entries(apiKeys)
    .filter(([, available]) => available)
    .map(([provider]) => provider.toLowerCase()),
}
