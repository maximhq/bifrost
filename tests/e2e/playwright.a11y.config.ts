import { defineConfig, devices } from '@playwright/test'

/**
 * Accessibility audit config. Kept separate from playwright.config.ts so the scan
 * skips the global setup (test plugin build, MCP servers) it does not need.
 *
 * Usage: npm run test:a11y   (see a11y-reporter.ts for the score and env knobs)
 */
export default defineConfig({
  testDir: './features/accessibility',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  workers: process.env.CI ? 2 : undefined,
  reporter: [['list'], ['./a11y-reporter.ts']],
  use: {
    ...devices['Desktop Chrome'],
    baseURL: process.env.BASE_URL || 'http://localhost:3000',
    actionTimeout: 10000,
    navigationTimeout: 30000,
  },
  timeout: 60000,
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
})
