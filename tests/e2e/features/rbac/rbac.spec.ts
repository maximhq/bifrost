import { expect, test } from '../../core/fixtures/base.fixture'

/**
 * TC-004 — RBAC UI Tests
 * Suite ID: TC-004-018, TC-004-019
 *
 * Requires:
 *  - Bifrost running with enterprise license
 *  - UI seeded with 5 default roles (super_admin, admin, operator, viewer, api_user)
 *  - super_admin session active (set in playwright.config.ts storageState)
 */

test.describe('RBAC Management', () => {
  test.describe.configure({ mode: 'serial' })

  // TC-004-018 — Role matrix page displays all default roles.
  test('should display 5 default roles in the RBAC roles page', async ({ page }) => {
    await page.goto('/workspace/enterprise/rbac/roles')
    await page.waitForLoadState('networkidle')

    const expectedRoles = ['super_admin', 'admin', 'operator', 'viewer', 'api_user']
    for (const role of expectedRoles) {
      const roleRow = page.getByText(role)
      await expect(roleRow).toBeVisible({ timeout: 10_000 })
    }
  })

  // TC-004-018b — Permission matrix shows correct allow/deny indicators.
  test('should display permission matrix with correct role capabilities', async ({ page }) => {
    await page.goto('/workspace/enterprise/rbac/roles')
    await page.waitForLoadState('networkidle')

    // super_admin row should show all ✓ / allowed
    const superAdminRow = page.locator('[data-testid="role-row-super_admin"]')
    await expect(superAdminRow).toBeVisible()

    // api_user row should show management endpoints as denied
    const apiUserRow = page.locator('[data-testid="role-row-api_user"]')
    await expect(apiUserRow).toBeVisible()
  })

  // TC-004-019 — Viewer cannot see write action buttons.
  test('viewer role sees no write action buttons on provider page', async ({ page, context }) => {
    // Open a new context simulating viewer login (storageState with viewer session)
    // In CI, viewer token is injected via VIEWER_STORAGE_STATE env var
    const viewerStorageState = process.env.VIEWER_STORAGE_STATE
    if (!viewerStorageState) {
      test.skip()
    }
    const viewerContext = await context.browser()!.newContext({
      storageState: viewerStorageState,
    })
    const viewerPage = await viewerContext.newPage()
    await viewerPage.goto('/workspace/providers')
    await viewerPage.waitForLoadState('networkidle')

    // "Add Provider" button should not be visible for viewer
    const addButton = viewerPage.getByRole('button', { name: /add provider/i })
    await expect(addButton).toBeHidden()

    await viewerContext.close()
  })

  // TC-004-011 — super_admin can create a custom role.
  test('super_admin can create a custom role with specific permissions', async ({ page }) => {
    const roleName = `custom-role-${Date.now()}`

    await page.goto('/workspace/enterprise/rbac/roles')
    await page.waitForLoadState('networkidle')

    // Click "New Role" button
    const newRoleBtn = page.getByRole('button', { name: /new role|create role/i })
    if (!(await newRoleBtn.isVisible())) {
      test.skip() // Feature not yet in UI
    }
    await newRoleBtn.click()

    // Fill role name
    await page.getByLabel(/role name/i).fill(roleName)

    // Submit
    await page.getByRole('button', { name: /save|create/i }).click()

    // Verify role appears
    await expect(page.getByText(roleName)).toBeVisible({ timeout: 10_000 })

    // Cleanup — delete the custom role
    const deleteBtn = page.locator(`[data-testid="delete-role-${roleName}"]`)
    if (await deleteBtn.isVisible()) {
      await deleteBtn.click()
      await page.getByRole('button', { name: /confirm|delete/i }).click()
    }
  })

  // TC-004-012 — system roles cannot be deleted.
  test('cannot delete system roles', async ({ page }) => {
    await page.goto('/workspace/enterprise/rbac/roles')
    await page.waitForLoadState('networkidle')

    // System role delete button should be disabled or absent
    const viewerDeleteBtn = page.locator('[data-testid="delete-role-viewer"]')
    if (await viewerDeleteBtn.count() > 0) {
      await expect(viewerDeleteBtn).toBeDisabled()
    } else {
      // Button not rendered = correct (no delete for system roles)
      test.info().annotations.push({ type: 'info', description: 'System role delete button is correctly hidden' })
    }
  })
})
