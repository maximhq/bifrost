import type { Page } from '@playwright/test'
import { expect, test } from '../../core/fixtures/base.fixture'

// A resolved notification stays in the tray with its outcome, drops its action,
// and stops counting toward the badge. The feed is mocked so the row states are fixed.

const createdAt = new Date(Date.now() - 60_000).toISOString()
const expiresAt = new Date(Date.now() + 24 * 60 * 60 * 1000).toISOString()

const openRow = {
  id: 'n-open',
  audience: 'all',
  severity: 'info',
  title: 'Budget increase requested',
  message: 'Alice asks for +$100.00 on dev-key, this cycle only.',
  action_label: 'Review request',
  action_path: '/workspace/home?tab=approvals&request=req-open',
  subject_type: 'budget_request',
  subject_id: 'req-open',
  created_at: createdAt,
  expires_at: expiresAt,
}

const resolvedRow = {
  ...openRow,
  id: 'n-resolved',
  action_path: '/workspace/home?tab=approvals&request=req-done',
  subject_id: 'req-done',
  resolved_at: new Date().toISOString(),
  resolution: 'Approved by Bob',
}

async function mockFeeds(page: Page, notifications: object[] = [resolvedRow, openRow]) {
  await page.route('**/api/notifications*', (route) => {
    if (route.request().method() !== 'GET') return route.fallback()
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ notifications }),
    })
  })
  // Jobs and news also feed the badge, so they are emptied to isolate the notification count.
  await page.route('**/api/sidekiq/jobs', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ jobs: [] }) }),
  )
  await page.route('**/api/rss/items*', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: [] }) }),
  )
}

test.describe('Resolved notifications', () => {
  test('keeps a resolved row with its outcome, without its action or a badge count', async ({ page }) => {
    await mockFeeds(page)
    await page.goto('/workspace/dashboard')

    await expect(page.getByTestId('topbar-notifications-badge')).toHaveText('1')
    await page.getByTestId('topbar-notifications-btn').click()

    const resolved = page.getByTestId('notification-item-n-resolved')
    await expect(resolved).toBeVisible()
    await expect(page.getByTestId('notification-resolution-n-resolved')).toHaveText('Approved by Bob')
    await expect(resolved.getByText('Review request')).toHaveCount(0)

    const open = page.getByTestId('notification-item-n-open')
    await expect(open.getByText('Review request')).toBeVisible()
    await expect(page.getByTestId('notification-resolution-n-open')).toHaveCount(0)
  })

  test('resolves an open row in place when the resolve event arrives over the websocket', async ({ page }) => {
    await mockFeeds(page, [openRow])
    // The app socket still reaches the server; the test only injects the resolve event into it.
    let pushToPage: ((message: string) => void) | undefined
    await page.routeWebSocket(/\/ws(\?|$)/, (ws) => {
      ws.connectToServer()
      pushToPage = (message) => ws.send(message)
    })
    await page.goto('/workspace/dashboard')

    await expect(page.getByTestId('topbar-notifications-badge')).toHaveText('1')
    await page.getByTestId('topbar-notifications-btn').click()
    const row = page.getByTestId('notification-item-n-open')
    await expect(row.getByText('Review request')).toBeVisible()

    await expect.poll(() => pushToPage !== undefined).toBe(true)
    pushToPage?.(
      JSON.stringify({
        type: 'notification',
        data: { ...openRow, resolved_at: new Date().toISOString(), resolution: 'Denied by Bob' },
      }),
    )

    await expect(page.getByTestId('notification-resolution-n-open')).toHaveText('Denied by Bob')
    await expect(row.getByText('Review request')).toHaveCount(0)
    await expect(page.getByTestId('topbar-notifications-badge')).toHaveCount(0)
  })
})
