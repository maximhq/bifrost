import { expect, test } from '../../core/fixtures/base.fixture'

test.use({ skipAutoLogin: true })

test('OAuth consent shows the exact callback before granting access', async ({ page }) => {
  await page.route('**/api/**', async route => {
    if (route.request().url().includes('/oauth2/consent/flows/review-flow')) {
      await route.fulfill({ json: {
        client_name: 'Review client', redirect_uri: 'https://approved.example/callback?tenant=team-a',
        redirect_host: 'approved.example', available_modes: ['vk'], expires_at: '2099-01-01T00:00:00Z',
      } })
    } else {
      await route.fulfill({ json: { is_auth_enabled: false, has_valid_token: false } })
    }
  })
  await page.goto('/oauth/consent?flow=review-flow')
  await expect(page.getByTestId('oauth-consent-redirect-uri')).toHaveText('https://approved.example/callback?tenant=team-a')
  await expect(page.getByRole('heading', { name: 'Review client wants to connect' })).toBeVisible()
  await expect(page.getByText('Continue only if you recognize this application and its callback destination:')).toBeVisible()
  await expect(page.getByTestId('oauth-consent-redirect-host')).toHaveText('approved.example')
  // A registered client publishes no metadata document and returns to a remote callback.
  await expect(page.getByTestId('oauth-consent-client-host')).toHaveCount(0)
  await expect(page.getByTestId('oauth-consent-localhost-warning')).toHaveCount(0)
})

test('OAuth consent shows where a metadata-document client is published and warns on localhost-only callbacks', async ({ page }) => {
  await page.route('**/api/**', async route => {
    if (route.request().url().includes('/oauth2/consent/flows/cimd-flow')) {
      await route.fulfill({ json: {
        client_name: 'Claude', redirect_uri: 'http://127.0.0.1:3000/callback', redirect_host: '127.0.0.1',
        client_host: 'claude.example', localhost_only_redirect: true,
        available_modes: ['vk'], expires_at: '2099-01-01T00:00:00Z',
      } })
    } else {
      await route.fulfill({ json: { is_auth_enabled: false, has_valid_token: false } })
    }
  })
  await page.goto('/oauth/consent?flow=cimd-flow')
  await expect(page.getByRole('heading', { name: 'Claude wants to connect' })).toBeVisible()
  await expect(page.getByTestId('oauth-consent-client-host')).toHaveText('Published by claude.example')
  await expect(page.getByTestId('oauth-consent-redirect-host')).toHaveText('127.0.0.1')
  await expect(page.getByTestId('oauth-consent-localhost-warning')).toBeVisible()
})
