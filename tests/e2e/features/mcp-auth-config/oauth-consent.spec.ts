import { expect, test } from '../../core/fixtures/base.fixture'

test.use({ skipAutoLogin: true })

test('OAuth consent shows the exact callback before granting access', async ({ page }) => {
  await page.route('**/api/**', async route => {
    if (route.request().url().includes('/oauth2/consent/flows/review-flow')) {
      await route.fulfill({ json: {
        client_name: 'Review client', redirect_uri: 'https://approved.example/callback?tenant=team-a',
        available_modes: ['vk'], expires_at: '2099-01-01T00:00:00Z',
      } })
    } else {
      await route.fulfill({ json: { is_auth_enabled: false, has_valid_token: false } })
    }
  })
  await page.goto('/oauth/consent?flow=review-flow')
  await expect(page.getByTestId('oauth-consent-redirect-uri')).toHaveText('https://approved.example/callback?tenant=team-a')
  await expect(page.getByRole('heading', { name: 'Review client wants to connect' })).toBeVisible()
  await expect(page.getByText('Continue only if you recognize this application and its callback destination:')).toBeVisible()
})
