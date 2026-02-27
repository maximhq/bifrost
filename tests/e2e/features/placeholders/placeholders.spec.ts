import { expect, test } from '../../core/fixtures/base.fixture'

test.describe('Placeholder and Enterprise Pages', () => {
  test('should load prompt-repo coming soon page', async ({ page }) => {
    await page.goto('/workspace/prompt-repo')
    await expect(page.getByText(/Prompt repository is coming soon/i)).toBeVisible({ timeout: 10000 })
  })

  test('should load alert-channels page', async ({ page }) => {
    await page.goto('/workspace/alert-channels')
    await page.waitForLoadState('networkidle')
    await expect(page.getByText('Unlock alert channels for better observability')).toBeVisible()
    const readMore = page.getByRole('button', { name: /Read more/i })
    await expect(readMore).toBeVisible()
    const [popup] = await Promise.all([page.waitForEvent('popup'), readMore.click()])
    await expect(popup).toHaveURL(/^https:\/\/docs\.getbifrost\.ai\/enterprise\/alert-channels(\?|$)/)
    await popup.close()
  })

  test('should load guardrails page', async ({ page }) => {
    await page.goto('/workspace/guardrails')
    await page.waitForLoadState('networkidle')
    await expect(page).toHaveURL(/\/workspace\/guardrails/)
  })

  test('should load audit-logs page', async ({ page }) => {
    await page.goto('/workspace/audit-logs')
    await page.waitForLoadState('networkidle')
    await expect(page).toHaveURL(/\/workspace\/audit-logs/)
  })

  test('should load cluster page', async ({ page }) => {
    await page.goto('/workspace/cluster')
    await page.waitForLoadState('networkidle')
    await expect(page).toHaveURL(/\/workspace\/cluster/)
  })

  test('should load custom-pricing page', async ({ page }) => {
    await page.goto('/workspace/custom-pricing')
    await page.waitForLoadState('networkidle')
    await expect(page).toHaveURL(/\/workspace\/custom-pricing/)
  })

  test('should load rbac page', async ({ page }) => {
    await page.goto('/workspace/rbac')
    await page.waitForLoadState('networkidle')
    await expect(page).toHaveURL(/\/workspace\/rbac/)
  })

  test('should load scim page', async ({ page }) => {
    await page.goto('/workspace/scim')
    await page.waitForLoadState('networkidle')
    await expect(page).toHaveURL(/\/workspace\/scim/)
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
    await expect(page).toHaveURL(/\/workspace\/guardrails\/configuration/)
  })

  test('should load guardrails providers page', async ({ page }) => {
    await page.goto('/workspace/guardrails/providers')
    await page.waitForLoadState('networkidle')
    await expect(page).toHaveURL(/\/workspace\/guardrails\/providers/)
  })
})
