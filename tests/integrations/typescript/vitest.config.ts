import { resolve } from 'path'
import { defineConfig } from 'vitest/config'

export default defineConfig({
  test: {
    // Test discovery
    include: ['tests/**/*.test.ts'],
    exclude: ['node_modules', 'dist'],

    // Global test settings
    globals: true,
    environment: 'node',

    // Timeout settings (5 minutes per test, matching Python)
    testTimeout: 300000,
    hookTimeout: 60000,

    // Run tests sequentially to avoid API rate limiting
    pool: 'forks',
    poolOptions: {
      forks: {
        singleFork: true,
      },
    },

    // Reporter configuration
    reporters: ['verbose'],

    // Setup files
    setupFiles: ['./tests/setup.ts'],

    // Retry flaky tests (matching Python pytest-rerunfailures)
    retry: 2,

    // Coverage configuration
    coverage: {
      provider: 'v8',
      reporter: ['text', 'html', 'json'],
      include: ['src/**/*.ts'],
      exclude: ['node_modules', 'dist', 'tests'],
    },

    // Environment variables
    env: {
      NODE_ENV: 'test',
    },
  },

  resolve: {
    alias: {
      '@': resolve(__dirname, './src'),
    },
  },
})
