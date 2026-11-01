import type { Page, TestInfo } from '@playwright/test'
import { expect, test } from '../../core/fixtures/base.fixture'

/**
 * Bifrost mounted under a URL prefix (server.base_path). scripts/run-e2e.mjs
 * starts this folder's worker with --base-path; requests are accepted with or
 * without the prefix, so baseURL stays the origin and every path here is
 * spelled out with the prefix.
 */

// The fixture's login handler assumes the domain root; these tests log in themselves.
test.use({ skipAutoLogin: true })

/** The prefix this worker's server is mounted under, from the project metadata. */
function basePathOf(testInfo: TestInfo): string {
  const basePath = testInfo.project.metadata.basePath
  if (typeof basePath !== 'string' || !basePath) {
    throw new Error('base-path specs need a server started with --base-path: run them via scripts/run-e2e.mjs, or set E2E_BASE_PATH with E2E_BASE_URL')
  }
  return basePath
}

/** The server origin, e.g. http://localhost:18185. */
function originOf(testInfo: TestInfo): string {
  return new URL(testInfo.project.use.baseURL as string).origin
}

/** Collects the URL and status of every same-origin response the page receives. */
function recordSameOriginResponses(page: Page, origin: string): { url: URL; status: number }[] {
  const responses: { url: URL; status: number }[] = []
  page.on('response', (response) => {
    const url = new URL(response.url())
    if (url.origin === origin) responses.push({ url, status: response.status() })
  })
  return responses
}

test.describe('Base path', () => {
  test('serves the shell, assets and API calls under the prefix', async ({ page }, testInfo) => {
    const basePath = basePathOf(testInfo)
    const origin = originOf(testInfo)
    const responses = recordSameOriginResponses(page, origin)

    await page.goto(`${basePath}/workspace/providers`)
    await expect(page.getByRole('link', { name: /providers/i }).first()).toBeVisible()
    await page.waitForLoadState('networkidle')

    expect(await page.evaluate(() => (window as unknown as { __BIFROST_BASE_PATH__?: string }).__BIFROST_BASE_PATH__)).toBe(basePath)

    const outsidePrefix = responses.filter((r) => r.url.pathname !== basePath && !r.url.pathname.startsWith(`${basePath}/`))
    expect(outsidePrefix.map((r) => r.url.pathname), 'same-origin requests that bypassed the prefix').toEqual([])

    const assets = responses.filter((r) => !r.url.pathname.startsWith(`${basePath}/api/`))
    expect(assets.some((r) => r.url.pathname.startsWith(`${basePath}/assets/`)), 'the bundle loads from the prefixed /assets').toBe(true)
    expect(assets.filter((r) => r.status >= 400).map((r) => `${r.status} ${r.url.pathname}`), 'failed asset requests').toEqual([])

    expect(responses.some((r) => r.url.pathname.startsWith(`${basePath}/api/`) && r.status === 200), 'the UI calls the API under the prefix').toBe(true)
  })

  test('keeps navigation and deep-link reloads under the prefix', async ({ page }, testInfo) => {
    const basePath = basePathOf(testInfo)
    const origin = originOf(testInfo)
    const dashboardLink = page.getByRole('link', { name: 'Dashboard', exact: true })

    await page.goto(`${basePath}/workspace/logs`)
    await expect(dashboardLink).toHaveAttribute('href', `${basePath}/workspace/dashboard`)
    await dashboardLink.click()
    await expect(page).toHaveURL(new RegExp(`^${origin}${basePath}/workspace/dashboard`))

    await page.reload()
    await expect(page).toHaveURL(new RegExp(`^${origin}${basePath}/workspace/dashboard`))
    await expect(dashboardLink).toBeVisible()
  })

  test.describe('signed out', () => {
    test.use({ storageState: { cookies: [], origins: [] } })

    test('sends a signed-out visit to the prefixed login and back after sign-in', async ({ page }, testInfo) => {
      const basePath = basePathOf(testInfo)
      const origin = originOf(testInfo)
      const username = process.env.BIFROST_ADMIN_USERNAME
      const password = process.env.BIFROST_ADMIN_PASSWORD
      if (!username || !password) throw new Error('BIFROST_ADMIN_USERNAME and BIFROST_ADMIN_PASSWORD must be set')

      await page.goto(`${basePath}/workspace/logs`)
      await expect(page).toHaveURL(new RegExp(`^${origin}${basePath}/login\\?goto=%2Fworkspace%2Flogs`))

      await page.locator('#username').fill(username)
      await page.locator('#password').fill(password)
      await page.getByRole('button', { name: /Sign in/i }).click()
      await expect(page).toHaveURL(new RegExp(`^${origin}${basePath}/workspace/logs`))
    })
  })

  test('routes prefixed and unprefixed HTTP requests through the server', async ({ request }, testInfo) => {
    const basePath = basePathOf(testInfo)

    for (const path of ['/health', '/api/session/is-auth-enabled']) {
      const prefixed = await request.get(`${basePath}${path}`)
      expect(prefixed.status(), `${basePath}${path}: ${await prefixed.text()}`).toBe(200)
      const unprefixed = await request.get(path)
      expect(unprefixed.status(), `${path}: ${await unprefixed.text()}`).toBe(200)
    }

    // An inference route: its status depends on the provider keys this server has,
    // so the prefixed request must reach the same list-models handler as the
    // unprefixed one (same status), not the 404 an unrouted path gets. Bodies are
    // not compared: without keys, the error names whichever provider failed first.
    const unprefixedModels = await request.get('/v1/models')
    const prefixedModels = await request.get(`${basePath}/v1/models`)
    expect(unprefixedModels.status(), await unprefixedModels.text()).not.toBe(404)
    expect(prefixedModels.status(), await prefixedModels.text()).toBe(unprefixedModels.status())
    if (!prefixedModels.ok()) {
      const body = await prefixedModels.json()
      expect(body.extra_fields?.request_type, JSON.stringify(body)).toBe('list_models')
    }

    // The OAuth callback redirects the browser back into the dashboard, under the prefix.
    const callback = await request.get(`${basePath}/api/oauth/callback?state=e2e-unknown-state&error=access_denied`, { maxRedirects: 0 })
    expect(callback.status(), await callback.text()).toBe(302)
    // fasthttp writes an absolute Location; the path is what carries the prefix.
    const location = new URL(callback.headers()['location'], originOf(testInfo))
    expect(location.pathname).toMatch(new RegExp(`^${basePath}/workspace/`))
  })
})
