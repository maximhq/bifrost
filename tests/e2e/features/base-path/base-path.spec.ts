import http from 'node:http'
import type { AddressInfo } from 'node:net'
import type { APIRequestContext, Page, TestInfo } from '@playwright/test'
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

/** Resolves once a WebSocket to `url` opens, or rejects with the close code when it cannot. */
async function websocketOpens(page: Page, url: string): Promise<boolean> {
  return page.evaluate(
    (target) =>
      new Promise<boolean>((resolve) => {
        const socket = new WebSocket(target)
        socket.onopen = () => {
          socket.close()
          resolve(true)
        }
        socket.onerror = () => resolve(false)
        socket.onclose = (event) => resolve(event.wasClean)
      }),
    url,
  )
}

/**
 * A reverse proxy that strips `basePath` before forwarding to `target`, the second
 * deployment mode the schema promises ("proxies that strip it work too").
 */
async function startStrippingProxy(target: string, basePath: string): Promise<{ origin: string; close: () => Promise<void> }> {
  const upstream = new URL(target)
  const server = http.createServer((req, res) => {
    const incoming = req.url ?? '/'
    const stripped = incoming === basePath ? '/' : incoming.startsWith(`${basePath}/`) ? incoming.slice(basePath.length) : incoming
    const forwarded = http.request(
      { hostname: upstream.hostname, port: upstream.port, method: req.method, path: stripped, headers: { ...req.headers, host: upstream.host, 'x-forwarded-prefix': basePath } },
      (upstreamRes) => {
        res.writeHead(upstreamRes.statusCode ?? 502, upstreamRes.headers)
        upstreamRes.pipe(res)
      },
    )
    forwarded.on('error', () => {
      res.statusCode = 502
      res.end()
    })
    req.pipe(forwarded)
  })
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve))
  const { port } = server.address() as AddressInfo
  return {
    origin: `http://127.0.0.1:${port}`,
    close: () => new Promise<void>((resolve, reject) => server.close((err) => (err ? reject(err) : resolve()))),
  }
}

async function createVirtualMcp(request: APIRequestContext, basePath: string, slug: string): Promise<number> {
  const res = await request.post(`${basePath}/api/mcp/virtual-mcps`, { data: { name: `Base path ${slug}`, endpoint_slug: slug, tools: [] } })
  expect(res.status(), await res.text()).toBe(200)
  return ((await res.json()) as { id: number }).id
}

async function createVirtualKey(request: APIRequestContext, basePath: string, name: string): Promise<string> {
  const res = await request.post(`${basePath}/api/governance/virtual-keys`, { data: { name } })
  expect(res.status(), await res.text()).toBe(200)
  return ((await res.json()) as { virtual_key: { id: string } }).virtual_key.id
}

// Every URL the dashboard prints about Bifrost itself: copy buttons, generated
// client configs and scrape targets must carry the prefix, or the proxy 404s them.
test.describe('Base path: self URLs', () => {
  test('the virtual MCP endpoint copies the prefixed gateway URL', async ({ page, request, context }, testInfo) => {
    const basePath = basePathOf(testInfo)
    const origin = originOf(testInfo)
    const slug = `bp-${Date.now()}`
    const id = await createVirtualMcp(request, basePath, slug)
    try {
      await context.grantPermissions(['clipboard-read', 'clipboard-write'])
      await page.goto(`${basePath}/workspace/virtual-mcps`)
      const endpoint = page.getByTestId(`virtual-mcp-endpoint-copy-${slug}`)
      await expect(endpoint).toHaveText(new RegExp(`^${basePath}/mcp/${slug}`))
      await endpoint.click()
      await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(`${origin}${basePath}/mcp/${slug}`)
    } finally {
      await request.delete(`${basePath}/api/mcp/virtual-mcps/${id}`)
    }
  })

  test('the MCP usage guide generates commands against the prefixed /mcp', async ({ page, request }, testInfo) => {
    const basePath = basePathOf(testInfo)
    const origin = originOf(testInfo)
    const vkId = await createVirtualKey(request, basePath, `base-path-guide-${Date.now()}`)
    try {
      for (const harness of ['claude-code', 'codex', 'cursor']) {
        await page.goto(`${basePath}/workspace/mcp-registry?install=true&harness=${harness}&auth=virtual_key&vk=${vkId}`)
        const command = page.getByTestId('mcp-usage-guide-command')
        await expect(command, harness).toContainText(`${origin}${basePath}/mcp`)
        expect(await command.textContent(), `${harness} must not print the unprefixed gateway`).not.toMatch(new RegExp(`${origin}/mcp\\b`))
      }
    } finally {
      await request.delete(`${basePath}/api/governance/virtual-keys/${vkId}`)
    }
  })

  test('the Prometheus scrape target carries the prefix', async ({ page }, testInfo) => {
    const basePath = basePathOf(testInfo)
    const origin = originOf(testInfo)
    await page.goto(`${basePath}/workspace/observability`)
    await page.getByTestId('observability-provider-btn-prometheus').click()
    await expect(page.getByTestId('prometheus-metrics-endpoint')).toHaveText(`${origin}${basePath}/metrics`)
  })

  test('the command palette keeps the prefix in the address bar and in new tabs', async ({ page, context }, testInfo) => {
    const basePath = basePathOf(testInfo)
    const origin = originOf(testInfo)
    await page.goto(`${basePath}/workspace/dashboard`)

    await page.keyboard.press('ControlOrMeta+k')
    await page.getByTestId('command-palette-input').fill('logs')
    await page.getByTestId('command-palette-item-/workspace/logs').click()
    await expect(page).toHaveURL(new RegExp(`^${origin}${basePath}/workspace/logs`))
    await page.reload()
    await expect(page).toHaveURL(new RegExp(`^${origin}${basePath}/workspace/logs`))

    await page.keyboard.press('ControlOrMeta+k')
    await page.getByTestId('command-palette-input').fill('providers')
    const popup = context.waitForEvent('page')
    await page.keyboard.press('ControlOrMeta+Enter')
    const newTab = await popup
    await newTab.waitForLoadState()
    expect(newTab.url()).toMatch(new RegExp(`^${origin}${basePath}/workspace/providers`))
    await newTab.close()
  })

  test('the MCP gateway, discovery documents and WebSocket answer under the prefix', async ({ page, request }, testInfo) => {
    const basePath = basePathOf(testInfo)
    const origin = originOf(testInfo)

    // Same handler either way. /mcp and /metrics are always routed, so neither may be the
    // 404 an unrouted path gets; jwks.json answers 404 by design while OAuth discovery is
    // off, so it is only held to the same status as its unprefixed twin.
    for (const [method, path, alwaysRouted] of [
      ['POST', '/mcp', true],
      ['GET', '/metrics', true],
      ['GET', '/.well-known/jwks.json', false],
    ] as const) {
      const send = (target: string) =>
        method === 'POST'
          ? request.post(target, { data: { jsonrpc: '2.0', id: 1, method: 'initialize', params: { protocolVersion: '2025-06-18', capabilities: {}, clientInfo: { name: 'e2e', version: '0' } } } })
          : request.get(target)
      const unprefixed = await send(path)
      const prefixed = await send(`${basePath}${path}`)
      if (alwaysRouted) expect(unprefixed.status(), `${method} ${path}: ${await unprefixed.text()}`).not.toBe(404)
      expect(prefixed.status(), `${method} ${basePath}${path}: ${await prefixed.text()}`).toBe(unprefixed.status())
    }

    await page.goto(`${basePath}/workspace/dashboard`)
    const wsOrigin = origin.replace(/^http/, 'ws')
    expect(await websocketOpens(page, `${wsOrigin}${basePath}/ws`), 'WebSocket under the prefix').toBe(true)
  })

  test('works through a proxy that strips the prefix before forwarding', async ({ page, request }, testInfo) => {
    const basePath = basePathOf(testInfo)
    const proxy = await startStrippingProxy(originOf(testInfo), basePath)
    const slug = `bp-proxy-${Date.now()}`
    const id = await createVirtualMcp(request, basePath, slug)
    try {
      const responses = recordSameOriginResponses(page, proxy.origin)
      await page.goto(`${proxy.origin}${basePath}/workspace/virtual-mcps`)
      await expect(page.getByTestId(`virtual-mcp-endpoint-copy-${slug}`)).toHaveText(new RegExp(`^${basePath}/mcp/${slug}`))
      await page.waitForLoadState('networkidle')
      const outsidePrefix = responses.filter((r) => r.url.pathname !== basePath && !r.url.pathname.startsWith(`${basePath}/`))
      expect(outsidePrefix.map((r) => r.url.pathname), 'requests that bypassed the stripping proxy prefix').toEqual([])
      expect(responses.filter((r) => r.status >= 400).map((r) => `${r.status} ${r.url.pathname}`), 'failed requests through the proxy').toEqual([])
    } finally {
      await request.delete(`${basePath}/api/mcp/virtual-mcps/${id}`)
      await proxy.close()
    }
  })
})
