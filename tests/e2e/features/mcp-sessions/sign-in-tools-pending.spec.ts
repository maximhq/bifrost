import type { Page } from '@playwright/test'
import { expect, test } from '../../core/fixtures/base.fixture'

// Per-user sign-in stores the credential even when the MCP client's tools cannot
// be loaded with it. The OAuth callback then redirects with ?tools=pending&client=
// and the headers submit returns discovery_error; these pages tell the caller to
// connect to the client again.

const SUBMITTED = { status: 'success', credential_id: 'cred-1', updated_at: '2026-10-26T00:00:00Z' }

// stubHeadersFlow serves a pending headers flow "flow-1" for the client "notes"
// to an anonymous visitor holding the flow's link, and answers its submit with
// submitResponse.
async function stubHeadersFlow(page: Page, submitResponse: object) {
  await page.route('**/api/**', async route => {
    if (route.request().url().includes('/mcp/per-user-headers/flows/flow-1')) {
      if (route.request().method() === 'PUT') {
        await route.fulfill({ json: submitResponse })
        return
      }
      await route.fulfill({ json: {
        id: 'flow-1', flow_mode: 'user', status: 'pending', mcp_client: { client_id: 'client-notes', name: 'notes' },
        user_id: 'alice', expires_at: '2099-01-01T00:00:00Z', created_at: '2026-10-26T00:00:00Z',
        required_header_keys: ['x-api-key'], has_active_credential: false,
      } })
      return
    }
    await route.fulfill({ json: { is_auth_enabled: true, has_valid_token: false } })
  })
}

async function submitHeaders(page: Page) {
  await page.goto('/workspace/mcp-sessions/auth?flow=flow-1&kind=headers#t=temp-token-1')
  await page.getByTestId('mcp-headers-submit-input-x-api-key').fill('key-1')
  await page.getByTestId('mcp-headers-submit-submit-btn').click()
  await expect(page.getByRole('heading', { name: 'Headers saved' })).toBeVisible()
}

test.describe('public sign-in pages', () => {
  test.use({ skipAutoLogin: true })

  test('OAuth success page says discovery is not finished yet when the callback marks it pending', async ({ page }) => {
    await page.goto('/workspace/mcp-sessions/auth-success?tools=pending&client=notes')
    await expect(page.getByRole('heading', { name: 'Authentication successful' })).toBeVisible()
    await expect(page.getByTestId('mcp-sessions-tools-pending-alert')).toContainText('connect to notes again')
  })

  test('OAuth success page shows no notice after a sign-in whose discovery finished', async ({ page }) => {
    await page.goto('/workspace/mcp-sessions/auth-success')
    await expect(page.getByRole('heading', { name: 'Authentication successful' })).toBeVisible()
    await expect(page.getByTestId('mcp-sessions-tools-pending-alert')).toHaveCount(0)
  })

  test('headers success card says discovery is not finished yet, with the reason', async ({ page }) => {
    await stubHeadersFlow(page, { ...SUBMITTED, discovery_error: 'failed to store the per-user discovery of MCP client notes: disk full' })
    await submitHeaders(page)
    await expect(page.getByTestId('mcp-sessions-tools-pending-alert')).toContainText('connect to notes again')
    await expect(page.getByTestId('mcp-sessions-tools-pending-error')).toHaveText('failed to store the per-user discovery of MCP client notes: disk full')
  })

  test('headers success card shows no notice when the submit reports no discovery error', async ({ page }) => {
    await stubHeadersFlow(page, SUBMITTED)
    await submitHeaders(page)
    await expect(page.getByTestId('mcp-sessions-tools-pending-alert')).toHaveCount(0)
  })
})

test('sessions page says discovery is not finished yet for a dashboard user the callback marks pending', async ({ page }) => {
  await page.goto('/workspace/mcp-sessions?completed=1&tools=pending&client=notes')
  await expect(page.getByTestId('mcp-sessions-tools-pending-alert')).toContainText('connect to notes again')
})

test('sessions page notice can be dismissed, which drops it from the URL', async ({ page }) => {
  await page.goto('/workspace/mcp-sessions?completed=1&tools=pending&client=notes')
  await expect(page.getByTestId('mcp-sessions-tools-pending-alert')).toBeVisible()
  await page.getByTestId('mcp-sessions-tools-pending-dismiss').click()
  await expect(page.getByTestId('mcp-sessions-tools-pending-alert')).toHaveCount(0)
  await expect(page).not.toHaveURL(/tools=pending/)
  await expect(page).not.toHaveURL(/client=notes/)
})

test('sessions page keeps the notice when the sessions list fails to load', async ({ page }) => {
  await page.route('**/api/mcp/sessions?**', route => route.fulfill({ status: 500, json: { error: { message: 'boom' } } }))
  await page.goto('/workspace/mcp-sessions?completed=1&tools=pending&client=notes')
  await expect(page.getByTestId('mcp-sessions-tools-pending-alert')).toContainText('connect to notes again')
})
