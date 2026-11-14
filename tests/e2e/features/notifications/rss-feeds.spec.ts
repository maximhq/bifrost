import type { Page } from '@playwright/test'
import { expect, test } from '../../core/fixtures/base.fixture'

// The notification panel's News tab renders whatever GET /api/rss/items
// returns, and the Sources sheet manages /api/rss/feeds. Both are mocked so the
// spec never depends on a live third-party feed.

interface MockFeed {
  id: number
  slug: string
  name: string
  url: string
  icon?: string
  source: 'builtin' | 'custom'
  enabled: boolean
  created_at: string
  updated_at: string
}

const now = new Date().toISOString()
const hourAgo = new Date(Date.now() - 60 * 60 * 1000).toISOString()

function builtinFeeds(): MockFeed[] {
  return [
    { id: 1, slug: 'bifrost', name: 'Bifrost Announcements', url: 'https://www.getbifrost.ai/announcements/rss.xml', icon: 'bifrost', source: 'builtin', enabled: true, created_at: now, updated_at: now },
    { id: 2, slug: 'openai', name: 'OpenAI News', url: 'https://openai.com/news/rss.xml', icon: 'openai', source: 'builtin', enabled: false, created_at: now, updated_at: now },
  ]
}

async function mockRSS(page: Page, feeds: MockFeed[], settings = { enabled: true }) {
  const updates: { id: number; body: Record<string, unknown> }[] = []
  await page.route('**/api/rss/settings', (route) => {
    if (route.request().method() === 'PUT') {
      Object.assign(settings, route.request().postDataJSON())
    }
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(settings) })
  })
  await page.route('**/api/rss/items*', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(!settings.enabled ? { enabled: false, items: [], errors: [] } : {
        enabled: true,
        items: [
          { id: 'bifrost:launch', feed_id: 1, feed_slug: 'bifrost', feed_name: 'Bifrost Announcements', feed_icon: 'bifrost', feed_url: feeds[0].url, title: 'Bifrost 2.3 is out', link: 'https://www.getbifrost.ai/announcements/2-3', summary: 'Agent Gateway and more.', published_at: hourAgo },
          { id: 'bifrost:older', feed_id: 1, feed_slug: 'bifrost', feed_name: 'Bifrost Announcements', feed_icon: 'bifrost', feed_url: feeds[0].url, title: 'Bifrost 2.2 is out', link: 'https://www.getbifrost.ai/announcements/2-2', published_at: hourAgo },
        ],
        errors: [{ feed_id: 9, feed_name: 'Broken Feed', message: 'feed could not be loaded' }],
      }),
    }),
  )
  await page.route('**/api/rss/feeds', (route) => {
    if (route.request().method() === 'POST') {
      const body = route.request().postDataJSON() as { name: string; url: string }
      if (feeds.some((f) => f.name === body.name)) {
        return route.fulfill({ status: 409, contentType: 'application/json', body: JSON.stringify({ error: { message: 'A feed with this name already exists' } }) })
      }
      if (!body.url.endsWith('.atom')) {
        return route.fulfill({ status: 400, contentType: 'application/json', body: JSON.stringify({ error: { message: 'URL did not return a valid RSS or Atom feed' } }) })
      }
      const feed: MockFeed = { id: 100, slug: 'custom-releases-1', name: body.name, url: body.url, source: 'custom', enabled: true, created_at: now, updated_at: now }
      feeds.push(feed)
      return route.fulfill({ status: 201, contentType: 'application/json', body: JSON.stringify(feed) })
    }
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ feeds }) })
  })
  await page.route('**/api/rss/feeds/*', (route) => {
    const id = Number(route.request().url().split('/').at(-1))
    const index = feeds.findIndex((f) => f.id === id)
    if (route.request().method() === 'DELETE') {
      feeds.splice(index, 1)
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ message: 'Feed deleted' }) })
    }
    const body = route.request().postDataJSON() as Record<string, unknown>
    updates.push({ id, body })
    Object.assign(feeds[index], body)
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(feeds[index]) })
  })
  return updates
}

async function openNewsTab(page: Page) {
  await page.getByTestId('topbar-notifications-btn').click()
  await page.getByTestId('notification-tab-news').click()
}

test.describe('Notification panel news feeds', () => {
  test('lists news items with their source and marks them read when opened', async ({ page, context }) => {
    await mockRSS(page, builtinFeeds())
    await page.goto('/workspace/dashboard')

    await expect(page.getByTestId('topbar-notifications-badge')).toBeVisible()
    await openNewsTab(page)

    const item = page.getByTestId('rss-item-bifrost:launch')
    await expect(item).toBeVisible()
    await expect(item).toContainText('Bifrost 2.3 is out')
    await expect(item).toContainText('Bifrost Announcements')
    await expect(item).toHaveAttribute('target', '_blank')
    await expect(item).toHaveAttribute('rel', 'noopener noreferrer')
    await expect(page.getByTestId('rss-failed-feeds')).toContainText('Broken Feed')
    await expect(item.getByLabel('Unread')).toBeVisible()

    const popup = context.waitForEvent('page')
    await item.click()
    await (await popup).close()
    await expect(item.getByLabel('Unread')).toHaveCount(0)

    const markAllRead = page.getByTestId('rss-mark-all-read')
    await expect(markAllRead).toBeVisible()
    await expect(page.getByTestId('rss-clear-all')).toBeVisible()
    await markAllRead.click()
    await expect(page.getByTestId('notification-tray').getByLabel('Unread')).toHaveCount(0)
    await expect(markAllRead).toHaveCount(0)
  })

  test('toggles a built-in feed from the sources sheet', async ({ page }) => {
    const updates = await mockRSS(page, builtinFeeds())
    await page.goto('/workspace/dashboard')
    await openNewsTab(page)
    await page.getByTestId('rss-sources-btn').click()

    const sheet = page.getByTestId('rss-sources-sheet')
    await expect(sheet).toBeVisible()
    await expect(page.getByTestId('rss-feed-row-bifrost')).toBeVisible()
    await expect(page.getByTestId('rss-feed-delete-1')).toHaveCount(0)

    const toggle = page.getByTestId('rss-feed-toggle-openai')
    await expect(toggle).toHaveAttribute('data-state', 'unchecked')
    await toggle.click()
    await expect(toggle).toHaveAttribute('data-state', 'checked')
    await expect.poll(() => updates).toEqual([{ id: 2, body: { enabled: true } }])
  })

  test('adds a custom feed, surfaces server validation under the right field, and removes it', async ({ page }) => {
    await mockRSS(page, builtinFeeds())
    await page.goto('/workspace/dashboard')
    await openNewsTab(page)
    await page.getByTestId('rss-sources-btn').click()

    await page.getByTestId('rss-add-feed-name').fill('OpenAI News')
    await page.getByTestId('rss-add-feed-url').fill('https://github.com/maximhq/bifrost/releases.atom')
    await page.getByTestId('rss-add-feed-submit').click()
    await expect(page.getByTestId('rss-add-feed-name-error')).toContainText('already exists')
    await expect(page.getByTestId('rss-add-feed-error')).toHaveCount(0)

    await page.getByTestId('rss-add-feed-name').fill('Releases')
    await page.getByTestId('rss-add-feed-url').fill('https://example.com/not-a-feed')
    await page.getByTestId('rss-add-feed-submit').click()
    await expect(page.getByTestId('rss-add-feed-error')).toContainText('valid RSS or Atom feed')

    await page.getByTestId('rss-add-feed-url').fill('https://github.com/maximhq/bifrost/releases.atom')
    await page.getByTestId('rss-add-feed-submit').click()
    await expect(page.getByTestId('rss-feed-row-custom-releases-1')).toBeVisible()

    await page.getByTestId('rss-feed-delete-100').click()
    await expect(page.getByTestId('rss-feed-row-custom-releases-1')).toHaveCount(0)
    await expect(page.getByTestId('rss-custom-feeds-empty')).toBeVisible()
  })

  test('keeps a pending delete disabled while another feed is deleted', async ({ page }) => {
    const custom = (id: number, name: string): MockFeed => ({ id, slug: `custom-${id}`, name, url: `https://example.com/${id}.atom`, source: 'custom', enabled: true, created_at: now, updated_at: now })
    const feeds = [...builtinFeeds(), custom(100, 'Feed A'), custom(101, 'Feed B')]
    await mockRSS(page, feeds)
    let releaseA = () => {}
    const heldA = new Promise<void>((resolve) => { releaseA = resolve })
    let deletesA = 0
    await page.route('**/api/rss/feeds/100', async (route) => {
      if (route.request().method() !== 'DELETE') return route.fallback()
      deletesA++
      await heldA
      feeds.splice(feeds.findIndex((f) => f.id === 100), 1)
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ message: 'Feed deleted' }) })
    })
    await page.goto('/workspace/dashboard')
    await openNewsTab(page)
    await page.getByTestId('rss-sources-btn').click()

    const deleteA = page.getByTestId('rss-feed-delete-100')
    await deleteA.click()
    await expect(deleteA).toBeDisabled()
    await page.getByTestId('rss-feed-delete-101').click()
    await expect(page.getByTestId('rss-feed-row-custom-101')).toHaveCount(0)
    await expect(deleteA).toBeDisabled()

    releaseA()
    await expect(page.getByTestId('rss-feed-row-custom-100')).toHaveCount(0)
    expect(deletesA).toBe(1)
  })

  test('clears news per browser, and the cleared state survives a reload', async ({ page }) => {
    await mockRSS(page, builtinFeeds())
    await page.goto('/workspace/dashboard')
    await openNewsTab(page)

    const launch = page.getByTestId('rss-item-bifrost:launch')
    await launch.hover()
    await page.getByTestId('rss-dismiss-bifrost:launch').click()
    await expect(launch).toHaveCount(0)
    await expect(page.getByTestId('rss-item-bifrost:older')).toBeVisible()

    await page.getByTestId('rss-clear-all').click()
    await expect(page.getByTestId('rss-item-bifrost:older')).toHaveCount(0)
    await expect(page.getByTestId('rss-cleared-state')).toBeVisible()

    await page.reload()
    await openNewsTab(page)
    await expect(page.getByTestId('rss-cleared-state')).toBeVisible()
    await expect(page.getByTestId('rss-item-bifrost:launch')).toHaveCount(0)
  })

  test('switching news off hides the tab for the gateway, and an admin can turn it back on', async ({ page }) => {
    const settings = { enabled: false }
    await mockRSS(page, builtinFeeds(), settings)
    await page.goto('/workspace/dashboard')

    await page.getByTestId('topbar-notifications-btn').click()
    await expect(page.getByTestId('notification-tab-news')).toHaveCount(0)

    await page.getByTestId('rss-sources-btn').click()
    const toggle = page.getByTestId('rss-news-enabled-toggle')
    await expect(toggle).toHaveAttribute('data-state', 'unchecked')
    await toggle.click()
    await expect(toggle).toHaveAttribute('data-state', 'checked')
    await expect.poll(() => settings.enabled).toBe(true)

    await page.keyboard.press('Escape')
    await page.getByTestId('topbar-notifications-btn').click()
    await expect(page.getByTestId('notification-tab-news')).toBeVisible()
  })

  test('keeps the news switch usable when the feed list fails to load', async ({ page }) => {
    const settings = { enabled: false }
    await mockRSS(page, builtinFeeds(), settings)
    await page.route('**/api/rss/feeds', (route) =>
      route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: { message: 'store unavailable' } }) }),
    )
    await page.goto('/workspace/dashboard')

    await page.getByTestId('topbar-notifications-btn').click()
    await page.getByTestId('rss-sources-btn').click()
    await expect(page.getByTestId('rss-sources-sheet')).toContainText('Sources could not be loaded')
    const toggle = page.getByTestId('rss-news-enabled-toggle')
    await expect(toggle).toHaveAttribute('data-state', 'unchecked')
    await toggle.click()
    await expect(toggle).toHaveAttribute('data-state', 'checked')
    await expect.poll(() => settings.enabled).toBe(true)
  })
})
