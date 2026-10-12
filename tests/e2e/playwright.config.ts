import { defineConfig, devices, type PlaywrightTestConfig } from '@playwright/test'
import { existsSync } from 'fs'
import { resolve } from 'path'
import { loadPlan, REPORTS_DIR, storageStatePath } from './plan'

/**
 * Playwright configuration for Bifrost E2E tests.
 *
 * `make run-e2e-ui WORKERS=n` (scripts/run-e2e.mjs) boots n isolated Bifrost
 * instances, schedules spec files across them and hands the plan over via
 * E2E_PLAN. Without a plan, E2E_BASE_URL runs everything against one server.
 * @see https://playwright.dev/docs/test-configuration
 */
const plan = loadPlan()
const debugArtifacts = process.env.E2E_DEBUG_ARTIFACTS === '1'
const sharedBaseURL = process.env.E2E_BASE_URL || 'http://localhost:8080'

const workerProjects = (name: string, baseURL: string, files: string[] | null): NonNullable<PlaywrightTestConfig['projects']> => [
  {
    name: `${name}:auth`,
    testDir: './core',
    testMatch: /auth\.setup\.ts/,
    metadata: { worker: name },
    use: { baseURL },
  },
  {
    name,
    testDir: './features',
    // Paths from the plan are relative to tests/e2e; match on the absolute path.
    testMatch: files ? files.map((f) => resolve(__dirname, f)) : '**/*.spec.ts',
    // Accessibility audits run via playwright.a11y.config.ts.
    testIgnore: ['**/accessibility/**'],
    dependencies: [`${name}:auth`],
    // One spec at a time per server: specs share its config and entities.
    workers: 1,
    metadata: { worker: name },
    use: {
      ...devices['Desktop Chrome'],
      baseURL,
      storageState: storageStatePath(name),
    },
  },
]

const projects = plan
  ? plan.flatMap((w) => workerProjects(w.name, `http://localhost:${w.port}`, w.files))
  : workerProjects('shared', sharedBaseURL, null)

const enterpriseFeaturesDir = resolve(__dirname, '../../../bifrost-enterprise/tests/e2e/features')
if (process.env.BIFROST_E2E_INCLUDE_ENTERPRISE === '1' && existsSync(enterpriseFeaturesDir)) {
  // The runner boots OSS builds only, so enterprise specs need a server of their own.
  if (!process.env.E2E_BASE_URL) {
    throw new Error('BIFROST_E2E_INCLUDE_ENTERPRISE=1 needs E2E_BASE_URL pointing at a running enterprise build')
  }
  projects.push({
    name: 'enterprise',
    testDir: enterpriseFeaturesDir,
    testMatch: ['**/*.spec.ts'],
    use: { ...devices['Desktop Chrome'], baseURL: sharedBaseURL },
  })
}

export default defineConfig({
  testDir: '.',

  // Fail the build on CI if you accidentally left test.only in the source code
  forbidOnly: !!process.env.CI,

  retries: process.env.CI ? 2 : 0,

  // One browser per planned Bifrost; each project is capped at 1 so they never share a server.
  workers: plan ? plan.length : 1,

  reporter: [
    ['html', { outputFolder: 'playwright-report', open: 'never' }],
    ['./reporters/failures.ts', { outputFile: process.env.E2E_RESULTS_FILE || resolve(REPORTS_DIR, 'e2e-results.json') }],
    [process.env.CI ? 'dot' : 'list'],
  ],

  use: {
    // OSS setup lock: while dashboard auth is not active, every /api call needs the
    // operator's setup token. Matches setup_token in the test server's config.json (or
    // its BIFROST_SETUP_TOKEN). The setup-screen spec clears it with test.use().
    extraHTTPHeaders: {
      'X-Bifrost-Setup-Token': process.env.BIFROST_E2E_SETUP_TOKEN || process.env.BIFROST_SETUP_TOKEN || 'bifrost-e2e-setup-token',
    },

    // Recording video + trace for every test is a large CPU cost. Normal runs keep
    // only failure screenshots; E2E_DEBUG_ARTIFACTS=1 (set by RERUN) records everything.
    trace: debugArtifacts ? 'retain-on-failure' : 'off',
    screenshot: 'only-on-failure',
    video: debugArtifacts ? 'retain-on-failure' : 'off',
    actionTimeout: 10000,
    navigationTimeout: 30000,
    // Grant clipboard permissions so copy-to-clipboard tests work on localhost
    permissions: ['clipboard-read', 'clipboard-write'],
  },

  timeout: 60000,

  expect: {
    timeout: 10000,
  },

  projects,

  // Builds the test plugin and starts the MCP demo servers (reused if already up).
  globalSetup: require.resolve('./global-setup'),
})
