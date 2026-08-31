import { expect, test } from '@playwright/test'

test('login invalidates a cached core-config error', async ({ page }) => {
  let authenticated = false
  let configRequests = 0

  await page.route('**/api/session/is-auth-enabled', async (route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ is_auth_enabled: true, has_valid_token: authenticated, auth_type: 'password' }),
    })
  })
  await page.route('**/api/session/login', async (route) => {
    authenticated = true
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ message: 'Login successful' }),
    })
  })
  await page.route('**/api/config?**', async (route) => {
    configRequests++
    await route.fulfill({
      status: authenticated ? 200 : 401,
      contentType: 'application/json',
      body: authenticated
        ? JSON.stringify({ is_db_connected: true, is_logs_connected: true })
        : JSON.stringify({ error: { message: 'Unauthorized' } }),
    })
  })

  // Keep the rejected config query subscribed while the login mutation runs.
  await page.goto('/login')

  const firstResult = await page.evaluate(async () => {
    const storePath = '/lib/store/store.ts'
    const configPath = '/lib/store/apis/configApi.ts'
    const [{ store }, { configApi }] = await Promise.all([import(storePath), import(configPath)])
    const result = await store.dispatch(configApi.endpoints.getCoreConfig.initiate({}))
    return result.status
  })
  expect(firstResult).toBe('rejected')
  expect(configRequests).toBe(1)

  await page.evaluate(async () => {
    const storePath = '/lib/store/store.ts'
    const sessionPath = '/lib/store/apis/sessionApi.ts'
    const [{ store }, { sessionApi }] = await Promise.all([import(storePath), import(sessionPath)])
    await store.dispatch(sessionApi.endpoints.login.initiate({ username: 'admin', password: 'password' }))
  })

  await expect.poll(() => configRequests).toBe(2)
})
