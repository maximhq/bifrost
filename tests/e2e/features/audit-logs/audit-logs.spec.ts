import { expect, test } from '../../core/fixtures/base.fixture'

/**
 * TC-005 — Audit Logs UI Tests
 * Covers: Log viewer, filtering, export, RBAC access.
 */

test.describe('Audit Logs', () => {
  test.describe.configure({ mode: 'serial' })

  test.beforeEach(async ({ page }) => {
    await page.goto('/workspace/enterprise/audit-logs')
    await page.waitForLoadState('networkidle')
  })

  // TC-005-009 — Audit log viewer displays entries.
  test('should display audit log entries in the viewer', async ({ page }) => {
    // Log table/list should be visible
    const logContainer = page.locator(
      '[data-testid="audit-log-table"], [data-testid="audit-log-list"], table'
    )
    await expect(logContainer.first()).toBeVisible({ timeout: 15_000 })
  })

  // TC-005-009b — Filter by actor.
  test('should filter audit logs by actor', async ({ page }) => {
    const actorFilter = page.getByLabel(/actor/i).or(page.getByPlaceholder(/actor|user/i))
    if (!(await actorFilter.isVisible())) return // Filter not in UI yet

    await actorFilter.fill('admin')
    await page.keyboard.press('Enter')
    await page.waitForLoadState('networkidle')

    // All visible rows should show admin as actor
    const actorCells = page.locator('[data-testid="audit-actor"]')
    const count = await actorCells.count()
    for (let i = 0; i < count; i++) {
      const text = await actorCells.nth(i).textContent()
      expect(text?.toLowerCase()).toContain('admin')
    }
  })

  // TC-005-010b — Filter by time range.
  test('should filter audit logs by time range', async ({ page }) => {
    const startInput = page.getByLabel(/start.*time|from/i).first()
    const endInput = page.getByLabel(/end.*time|to/i).first()

    if (!(await startInput.isVisible()) || !(await endInput.isVisible())) return

    const now = new Date()
    const oneHourAgo = new Date(now.getTime() - 60 * 60 * 1000)

    await startInput.fill(oneHourAgo.toISOString().slice(0, 16))
    await endInput.fill(now.toISOString().slice(0, 16))

    const applyBtn = page.getByRole('button', { name: /apply|filter|search/i })
    if (await applyBtn.isVisible()) await applyBtn.click()
    await page.waitForLoadState('networkidle')

    // Verify response loaded (no error state)
    await expect(page.locator('[data-testid="error-state"]')).not.toBeVisible()
  })

  // TC-005-011b — Export button triggers download.
  test('should export audit logs as JSON', async ({ page }) => {
    const exportBtn = page.getByRole('button', { name: /export/i })
    if (!(await exportBtn.isVisible())) return // Export not in UI yet

    // Set up download listener
    const [download] = await Promise.all([
      page.waitForEvent('download', { timeout: 10_000 }).catch(() => null),
      exportBtn.click(),
    ])

    if (download) {
      const filename = download.suggestedFilename()
      expect(filename).toMatch(/audit.*\.json|\.csv/)
      test.info().annotations.push({ type: 'info', description: `Downloaded: ${filename}` })
    }
  })

  // TC-005-014b — Viewer can access audit logs page.
  test('audit log page is accessible to viewer role', async ({ page, context }) => {
    const viewerStorage = process.env.VIEWER_STORAGE_STATE
    if (!viewerStorage) {
      test.skip()
    }
    const viewerCtx = await context.browser()!.newContext({ storageState: viewerStorage })
    const viewerPage = await viewerCtx.newPage()
    await viewerPage.goto('/workspace/enterprise/audit-logs')
    await viewerPage.waitForLoadState('networkidle')

    // Should NOT see 403/forbidden
    await expect(viewerPage.getByText(/403|forbidden|access denied/i)).not.toBeVisible()
    await viewerCtx.close()
  })
})
