import { defineConfig, devices, type PlaywrightTestConfig } from '@playwright/test'
import { existsSync } from 'fs'
import { resolve } from 'path'

const enterpriseFeaturesDir = resolve(__dirname, '../../../bifrost-enterprise/tests/e2e/features')
const includeEnterprise = process.env.BIFROST_E2E_INCLUDE_ENTERPRISE === '1' && existsSync(enterpriseFeaturesDir)

const projects: NonNullable<PlaywrightTestConfig['projects']> = [
  {
    name: 'chromium',
    testDir: './features',
    use: { ...devices['Desktop Chrome'] },
    testIgnore: ['**/config/**', '**/plugins/**', '**/virtual-keys/**', '**/mcp-registry/**', '**/model-limits/**', '**/providers/**'],
  },
  {
    name: 'chromium-serial',
    testDir: './features',
    use: { ...devices['Desktop Chrome'] },
    testMatch: ['**/plugins/**/*.spec.ts', '**/virtual-keys/**/*.spec.ts', '**/mcp-registry/**/*.spec.ts', '**/model-limits/**/*.spec.ts', '**/providers/**/*.spec.ts'],
    fullyParallel: false,
  },
  {
    name: 'chromium-config',
    testDir: './features',
    use: { ...devices['Desktop Chrome'] },
    testMatch: ['**/config/**/*.spec.ts'],
    dependencies: ['chromium', 'chromium-serial'],
  },
]

if (includeEnterprise) {
  projects.push({
    name: 'chromium-enterprise',
    testDir: enterpriseFeaturesDir,
    use: { ...devices['Desktop Chrome'] },
    testMatch: ['**/*.spec.ts'],
  })
}

/**
 * Playwright configuration for Bifrost E2E tests
 * @see https://playwright.dev/docs/test-configuration
 */
export default defineConfig({
  testDir: '.',

  // Run tests in files in parallel
  fullyParallel: true,

  // Fail the build on CI if you accidentally left test.only in the source code
  forbidOnly: !!process.env.CI,

  // Retry on CI only
  retries: process.env.CI ? 2 : 0,

  // Opt out of parallel tests on CI for stability
  workers: process.env.CI ? 1 : undefined,

  // Reporter to use
  reporter: [
    [
      'html',
      {
        // Report in tests/e2e/playwright-report so `npx playwright show-report`
        // (run from tests/e2e) finds it. CI uploads tests/e2e/playwright-report/.
        outputFolder: 'playwright-report',
        open: 'never',
      },
    ],
    ['list'],
  ],

  // Shared settings for all the projects below
  use: {
    // Base URL for the application
    baseURL: process.env.BASE_URL || 'http://localhost:3000',

    // Collect trace when retrying the failed test
    trace: 'on-first-retry',

    // Take screenshot only on failure
    screenshot: 'only-on-failure',

    // Record video only on failure
    video: 'retain-on-failure',

    // Timeout for each action
    actionTimeout: 10000,

    // Timeout for navigation
    navigationTimeout: 30000,

    // Grant clipboard permissions so copy-to-clipboard tests work on localhost
    permissions: ['clipboard-read', 'clipboard-write'],
  },

  // Global timeout for each test
  timeout: 60000,

  // Expect timeout
  expect: {
    timeout: 10000,
  },

  // Configure projects: run all tests first, then config last (via dependency order).
  projects,

  // Run local dev server before starting tests 
  // Set SKIP_WEB_SERVER=1 to skip auto-starting the dev server
  webServer: process.env.SKIP_WEB_SERVER ? undefined : {
    command: 'npm run dev',
    url: 'http://localhost:3000',
    reuseExistingServer: true,
    cwd: '../../ui',
    timeout: 120000,
    env: {
      ...process.env,
      BIFROST_DISABLE_PROFILER: '1',
    },
  },

  // Global setup: Build test plugin and start MCP servers; returns teardown to stop MCP servers
  globalSetup: require.resolve('./global-setup'),
})
