import { expect, test } from '../../core/fixtures/base.fixture'

test.describe('Placeholder and Enterprise Pages', () => {
  // Stub the external docs site so the "Read more" popups don't depend on live network access.
  test.beforeEach(async ({ page }) => {
    await page.context().route('https://docs.getbifrost.ai/**', (route) =>
      route.fulfill({ status: 200, contentType: 'text/html', body: '<html></html>' }),
    )
  })

  test('should load prompt-repo page', async ({ page }) => {
    await page.goto('/workspace/prompt-repo')
    await expect(page.getByText(/Build, test, and version your prompts/i)).toBeVisible({ timeout: 10000 })
  })

  test('should load alerting page', async ({ page }) => {
    await page.goto('/workspace/alerting')
    await page.waitForLoadState('networkidle')
    await expect(page.getByText(/Unlock alerting rules/i)).toBeVisible()
    const readMore = page.getByTestId('alert-rules-read-more')
    await expect(readMore).toBeVisible()
    const [popup] = await Promise.all([page.waitForEvent('popup'), readMore.click()])
    await expect(popup).toHaveURL(/^https:\/\/docs\.getbifrost\.ai\/enterprise\/alerting\/alert-rules(\?|$)/)
    await popup.close()
  })

  test('should load guardrails page', async ({ page }) => {
    await page.goto('/workspace/guardrails')
    await page.waitForLoadState('networkidle')
    await expect(page).toHaveURL(/\/workspace\/guardrails(?:\?.*)?$/)
  })

  test('should load audit-logs page', async ({ page }) => {
    await page.goto('/workspace/audit-logs')
    await page.waitForLoadState('networkidle')
    await expect(page).toHaveURL(/\/workspace\/audit-logs(?:\?.*)?$/)
  })

  test('should load cluster page', async ({ page }) => {
    await page.goto('/workspace/cluster')
    await page.waitForLoadState('networkidle')
    await expect(page).toHaveURL(/\/workspace\/cluster(?:\?.*)?$/)
  })

  test('should load custom-pricing page', async ({ page }) => {
    await page.goto('/workspace/custom-pricing')
    await page.waitForLoadState('networkidle')
    await expect(page).toHaveURL(/\/workspace\/custom-pricing(?:\?.*)?$/)
  })

  test('should load rbac page', async ({ page }) => {
    await page.goto('/workspace/rbac')
    await page.waitForLoadState('networkidle')
    await expect(page).toHaveURL(/\/workspace\/governance\/rbac(?:\?.*)?$/)
  })

  test('should load scim page', async ({ page }) => {
    await page.goto('/workspace/scim')
    await page.waitForLoadState('networkidle')
    await expect(page).toHaveURL(/\/workspace\/scim(?:\?.*)?$/)
  })

  test('should load adaptive-routing page', async ({ page }) => {
    await page.goto('/workspace/adaptive-routing')
    await page.waitForLoadState('networkidle')
    await expect(page.getByText('Unlock adaptive routing for better performance')).toBeVisible()
    const readMore = page.getByRole('button', { name: /Read more/i })
    await expect(readMore).toBeVisible()
    const [popup] = await Promise.all([page.waitForEvent('popup'), readMore.click()])
    await expect(popup).toHaveURL(/^https:\/\/docs\.getbifrost\.ai\/enterprise\/adaptive-load-balancing(\?|$)/)
    await popup.close()
  })

  test('should load guardrails configuration page', async ({ page }) => {
    await page.goto('/workspace/guardrails/configuration')
    await page.waitForLoadState('networkidle')
    await expect(page).toHaveURL(/\/workspace\/guardrails\/configuration(?:\?.*)?$/)
  })

  test('should load guardrails providers page', async ({ page }) => {
    await page.goto('/workspace/guardrails/providers')
    await page.waitForLoadState('networkidle')
    await expect(page).toHaveURL(/\/workspace\/guardrails\/providers(?:\?.*)?$/)
  })
})
