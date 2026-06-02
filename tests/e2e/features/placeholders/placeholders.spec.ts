import { expect, test } from '../../core/fixtures/base.fixture'

test.describe('Placeholder and Enterprise Pages', () => {
  test('should load prompt-repo coming soon page', async ({ page }) => {
    await page.goto('/workspace/prompt-repo')
    await expect(page.getByText(/Prompt repository is coming soon/i)).toBeVisible({ timeout: 10000 })
  })

  test('should load alerting channels page', async ({ page }) => {
    await page.goto('/workspace/alerting/channels')
    await page.waitForLoadState('networkidle')
    await expect(page.getByText('Unlock alerting channels for proactive monitoring')).toBeVisible()
    const readMore = page.getByRole('button', { name: /Read more/i })
    await expect(readMore).toBeVisible()
    const [popup] = await Promise.all([page.waitForEvent('popup'), readMore.click()])
    await expect(popup).toHaveURL(/^https:\/\/docs\.getbifrost\.ai\/enterprise\/alerting(\?|$)/)
    await popup.close()
  })

  test('should load alerting rules page', async ({ page }) => {
    await page.goto('/workspace/alerting/rules')
    await page.waitForLoadState('networkidle')
    await expect(page.getByText('Unlock alerting rules for proactive monitoring')).toBeVisible()
  })

  test('should load alerting history page', async ({ page }) => {
    await page.goto('/workspace/alerting/history')
    await page.waitForLoadState('networkidle')
    await expect(page.getByText('Unlock alerting history for proactive monitoring')).toBeVisible()
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
