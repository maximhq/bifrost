import AxeBuilder from '@axe-core/playwright'
import type { Page } from '@playwright/test'
import { readFileSync } from 'fs'
import { resolve } from 'path'
import { expect, test } from '../../core/fixtures/base.fixture'

/**
 * Accessibility audit: runs axe-core against every static route in the UI and
 * attaches the results so a11y-reporter.ts can compute the coverage score.
 *
 * Routes come from the generated TanStack route tree, so new pages are picked up
 * without editing this file. Run with `npm run test:a11y` (or `make run-a11y-audit`).
 */

// WCAG 2.2 level A and AA, which is what most accessibility requirements ask for.
const WCAG_TAGS = ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa']

// Routes that only make sense mid-flow (OAuth callbacks, handovers) or are dev tooling.
const SKIPPED_ROUTES = new Set([
  '/pprof',
  '/agent/handover',
  '/oauth/consent',
  '/workspace/mcp-registry/oauth-callback',
  '/workspace/mcp-sessions/auth',
  '/workspace/mcp-sessions/auth-failed',
  '/workspace/mcp-sessions/auth-success',
  '/workspace/scim/oauth-discover-callback',
])

function loadRoutes(): string[] {
  const routeTree = readFileSync(resolve(__dirname, '../../../../ui/app/routeTree.gen.ts'), 'utf8')
  const block = routeTree.match(/fullPaths:([\s\S]*?)fileRoutesByTo:/)
  if (!block) {
    throw new Error('could not find fullPaths in ui/app/routeTree.gen.ts')
  }
  const paths = [...block[1].matchAll(/'([^']+)'/g)].map(m => m[1].replace(/\/$/, '') || '/')
  return [...new Set(paths)].filter(p => !p.includes('$') && !SKIPPED_ROUTES.has(p)).sort()
}

const ROUTES = loadRoutes()
// A11Y_THEME=dark audits dark mode; unset uses the app default.
const THEME = process.env.A11Y_THEME
const ROUTE_FILTER = process.env.A11Y_ROUTES?.split(',').map(r => r.trim()).filter(Boolean)

// The shared login handler only fires during locator actions, which this spec never
// performs before checking the URL, so log in explicitly instead.
test.use({ skipAutoLogin: true })

async function loginIfRedirected(page: Page, route: string) {
  const username = process.env.BIFROST_ADMIN_USERNAME
  const password = process.env.BIFROST_ADMIN_PASSWORD
  if (route === '/login' || !new URL(page.url()).pathname.startsWith('/login') || !username || !password) {
    return
  }
  await page.locator('#username').fill(username)
  await page.locator('#password').fill(password)
  await page.getByRole('button', { name: /Sign in/i }).click()
  await page.waitForURL(url => !url.pathname.startsWith('/login'), { timeout: 15000 })
  // The login form always lands on /workspace, so go back to the audited route.
  await page.goto(route)
  await page.waitForLoadState('load')
}

test.describe('Accessibility audit', () => {
  for (const route of ROUTES) {
    if (ROUTE_FILTER?.length && !ROUTE_FILTER.some(f => route.includes(f))) continue

    test(`a11y ${route}`, async ({ page }, testInfo) => {
      if (THEME) {
        await page.addInitScript(theme => window.localStorage.setItem('theme', theme), THEME)
      }
      await page.goto(route)
      await page.waitForLoadState('load')
      await loginIfRedirected(page, route)
      // Pages with live polling never go fully idle, so treat idle as best effort.
      await page.waitForLoadState('networkidle', { timeout: 5000 }).catch(() => {})

      if (THEME === 'dark') {
        // The app background is translucent and relies on the browser's dark canvas
        // (color-scheme: dark). axe assumes a white canvas, so paint the real one.
        await page.addStyleTag({ content: 'html { background: Canvas !important; }' })
      }

      // Scoring the login page in place of every route would report a fake 100%.
      const finalPath = new URL(page.url()).pathname
      if (route !== '/login' && finalPath.startsWith('/login')) {
        throw new Error(
          `${route} redirected to /login. Run against a Bifrost with dashboard auth off, or set BIFROST_ADMIN_USERNAME/BIFROST_ADMIN_PASSWORD.`,
        )
      }

      const results = await new AxeBuilder({ page }).withTags(WCAG_TAGS).analyze()

      const summary = {
        route,
        finalPath,
        passes: results.passes.map(r => ({ id: r.id, nodes: r.nodes.length })),
        violations: results.violations.map(r => ({
          id: r.id,
          impact: r.impact,
          help: r.help,
          helpUrl: r.helpUrl,
          nodes: r.nodes.map(n => ({ target: n.target.join(' '), html: n.html.slice(0, 300), summary: n.failureSummary })),
        })),
        incomplete: results.incomplete.map(r => ({ id: r.id, nodes: r.nodes.length })),
      }
      await testInfo.attach('a11y-results', {
        body: JSON.stringify(summary),
        contentType: 'application/json',
      })

      if (process.env.A11Y_STRICT === '1') {
        expect(summary.violations.map(v => `${v.id} (${v.impact}): ${v.help}`)).toEqual([])
      }
    })
  }
})
