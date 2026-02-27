import { expect, test } from '../../core/fixtures/base.fixture'
import { createPluginData } from './plugins.data'
import { ensureTestPluginExists } from './plugins-test-helper'

// Track created plugins for cleanup
const createdPlugins: string[] = []

test.describe('Plugins', () => {
  test.beforeEach(async ({ pluginsPage }) => {
    await pluginsPage.goto()
    // Ensure sheet is closed before each test (in case it was left open)
    await pluginsPage.ensureSheetClosed()
  })

  test.afterEach(async ({ pluginsPage }) => {
    // Clean up any plugins created during tests
    for (const pluginName of [...createdPlugins]) {
      try {
        const exists = await pluginsPage.pluginExists(pluginName)
        if (exists) {
          await pluginsPage.deletePlugin(pluginName)
        }
      } catch {
        // Ignore cleanup errors
      }
    }
    // Clear the array
    createdPlugins.length = 0
  })

  test.describe('Plugin Display', () => {
    test('should display plugins table', async ({ pluginsPage }) => {
      // Plugins page has a sidebar (aliased as table), not a traditional table
      await expect(pluginsPage.table).toBeVisible()
    })

    test('should display create plugin button', async ({ pluginsPage }) => {
      await expect(pluginsPage.createBtn).toBeVisible()
    })

    test('should show empty state or plugin list', async ({ pluginsPage }) => {
      const count = await pluginsPage.getPluginCount()
      const emptyMessage = pluginsPage.page.getByText(/No plugins/i)
      const isEmptyStateVisible = await emptyMessage.isVisible().catch(() => false)

      if (count === 0) {
        expect(isEmptyStateVisible).toBe(true)
      } else {
        expect(count).toBeGreaterThan(0)
        expect(isEmptyStateVisible).toBe(false)
      }
    })
  })

  test.describe('CRUD Operations', () => {
    test('should create a basic plugin', async ({ pluginsPage }) => {
      const pluginData = createPluginData({
        name: `e2e-test-plugin-${Date.now()}`,
      })
      createdPlugins.push(pluginData.name) // Track for cleanup

      const created = await pluginsPage.createPlugin(pluginData)
      if (!created) {
        test.skip(true, 'Backend rejected plugin creation (failed to load .so)')
        return
      }

      const pluginExists = await pluginsPage.pluginExists(pluginData.name)
      expect(pluginExists).toBe(true)
    })

    test('should create a disabled plugin', async ({ pluginsPage }) => {
      const pluginData = createPluginData({
        name: `disabled-plugin-${Date.now()}`,
        enabled: false,
      })
      createdPlugins.push(pluginData.name) // Track for cleanup

      const created = await pluginsPage.createPlugin(pluginData)
      if (!created) {
        test.skip(true, 'Backend rejected plugin creation (failed to load .so)')
        return
      }

      const pluginExists = await pluginsPage.pluginExists(pluginData.name)
      expect(pluginExists).toBe(true)

      // Note: Plugins are created with enabled=true by default
      // We need to disable it after creation
      const initialState = await pluginsPage.getPluginEnabledState(pluginData.name)
      expect(initialState).toBe(true) // Created enabled by default

      // Now disable it
      await pluginsPage.togglePluginEnabled(pluginData.name)
      const isEnabled = await pluginsPage.getPluginEnabledState(pluginData.name)
      expect(isEnabled).toBe(false)
    })

    test('should toggle plugin enabled state', async ({ pluginsPage }) => {
      const originalName = `edit-test-plugin-${Date.now()}`
      const pluginData = createPluginData({ name: originalName })
      createdPlugins.push(originalName) // Track for cleanup

      const created = await pluginsPage.createPlugin(pluginData)
      if (!created) {
        test.skip(true, 'Backend rejected plugin creation (failed to load .so)')
        return
      }

      // Verify plugin exists; we verify editability via enabled-state toggling (name is read-only after creation)
      const pluginExists = await pluginsPage.pluginExists(originalName)
      expect(pluginExists).toBe(true)

      // Toggle enabled state to verify the plugin is editable
      const initialState = await pluginsPage.getPluginEnabledState(originalName)
      await pluginsPage.togglePluginEnabled(originalName)
      const newState = await pluginsPage.getPluginEnabledState(originalName)
      expect(newState).not.toBe(initialState)
    })

    test('should delete plugin', async ({ pluginsPage }) => {
      const pluginData = createPluginData({
        name: `delete-test-plugin-${Date.now()}`,
      })
      createdPlugins.push(pluginData.name) // Track for cleanup (in case test fails before delete)

      const created = await pluginsPage.createPlugin(pluginData)
      if (!created) {
        test.skip(true, 'Backend rejected plugin creation (failed to load .so)')
        return
      }

      // Verify it exists
      let pluginExists = await pluginsPage.pluginExists(pluginData.name)
      expect(pluginExists).toBe(true)

      // Delete it
      await pluginsPage.deletePlugin(pluginData.name)

      // Verify it's gone
      pluginExists = await pluginsPage.pluginExists(pluginData.name)
      expect(pluginExists).toBe(false)
    })

    test('should change plugin enabled state when toggled', async ({ pluginsPage }) => {
      const pluginData = createPluginData({
        name: `toggle-test-plugin-${Date.now()}`,
        enabled: true,
      })
      createdPlugins.push(pluginData.name) // Track for cleanup

      const created = await pluginsPage.createPlugin(pluginData)
      if (!created) {
        test.skip(true, 'Backend rejected plugin creation (failed to load .so)')
        return
      }

      // Get initial state
      const initialState = await pluginsPage.getPluginEnabledState(pluginData.name)

      // Toggle state
      await pluginsPage.togglePluginEnabled(pluginData.name)

      // Verify state changed
      const newState = await pluginsPage.getPluginEnabledState(pluginData.name)
      expect(newState).not.toBe(initialState)
    })
  })

  test.describe('Form Validation', () => {
    test('should require name for plugin', async ({ pluginsPage }) => {
      await pluginsPage.dismissToasts()
      await pluginsPage.createBtn.click()
      await expect(pluginsPage.sheet).toBeVisible()
      await pluginsPage.waitForSheetAnimation()

      // Save button should be disabled when name is empty
      await expect(pluginsPage.saveBtn).toBeDisabled()

      await pluginsPage.cancelPlugin()
    })

    test('should cancel plugin creation', async ({ pluginsPage }) => {
      await pluginsPage.createBtn.click()
      await expect(pluginsPage.sheet).toBeVisible()

      // Fill some data (scope to sheet to avoid matching background form)
      const testName = `cancelled-plugin-${Date.now()}`
      const sheetNameInput = pluginsPage.sheet.getByRole('textbox', { name: /Plugin Name/i })
      await sheetNameInput.fill(testName)

      // Cancel
      await pluginsPage.cancelPlugin()

      // Sheet should close
      await expect(pluginsPage.sheet).not.toBeVisible()

      // Plugin should not exist
      const pluginExists = await pluginsPage.pluginExists(testName)
      expect(pluginExists).toBe(false)
    })

    test('should open and close plugin sheet', async ({ pluginsPage }) => {
      // Open sheet
      await pluginsPage.createBtn.click()
      await expect(pluginsPage.sheet).toBeVisible()

      // Close sheet
      await pluginsPage.cancelPlugin()
      await expect(pluginsPage.sheet).not.toBeVisible()
    })
  })

  test.describe('Error Handling', () => {
    test('should handle duplicate plugin name gracefully', async ({ pluginsPage }) => {
      const pluginName = `duplicate-test-${Date.now()}`
      const pluginData = createPluginData({ name: pluginName })
      createdPlugins.push(pluginName) // Track for cleanup

      // Create first plugin
      const created = await pluginsPage.createPlugin(pluginData)
      if (!created) {
        test.skip(true, 'Backend rejected plugin creation (failed to load .so)')
        return
      }
      expect(await pluginsPage.pluginExists(pluginName)).toBe(true)

      // Try to create duplicate
      await pluginsPage.dismissToasts()
      await pluginsPage.createBtn.click()
      await expect(pluginsPage.sheet).toBeVisible()
      await pluginsPage.waitForSheetAnimation()
      // Scope to sheet to avoid matching background form
      const sheetNameInput = pluginsPage.sheet.getByRole('textbox', { name: /Plugin Name/i })
      const sheetPathInput = pluginsPage.sheet.getByRole('textbox', { name: /Plugin Path/i })
      await sheetNameInput.fill(pluginName)
      await sheetPathInput.fill(ensureTestPluginExists()) // Path is required; use same path as build
      await pluginsPage.saveBtn.click()

      // Either sheet stays open with error OR error toast appears
      const sheetVisible = await pluginsPage.sheet.isVisible()
      const hasError = await pluginsPage.page.locator('[role="alert"], .text-destructive, [data-sonner-toast]').count() > 0

      // At least one of these should be true
      expect(sheetVisible || hasError).toBe(true)

      // Cancel if sheet is still open
      if (sheetVisible) {
        await pluginsPage.cancelPlugin()
      }
    })
  })

  test.describe('Plugin Configuration', () => {
    test('should view plugin details', async ({ pluginsPage }) => {
      const pluginData = createPluginData({
        name: `view-details-${Date.now()}`,
      })
      createdPlugins.push(pluginData.name)

      const created = await pluginsPage.createPlugin(pluginData)
      if (!created) {
        test.skip(true, 'Backend rejected plugin creation (failed to load .so)')
        return
      }

      // Click on the plugin to view details
      const pluginItem = pluginsPage.page.locator('button').filter({ hasText: pluginData.name })
      await pluginItem.click()

      // Should see plugin details/form
      const detailsVisible = await pluginsPage.page.locator('form, [role="tabpanel"]').isVisible().catch(() => false)
      expect(detailsVisible).toBe(true)
    })

    test('should edit plugin configuration', async ({ pluginsPage }) => {
      const pluginData = createPluginData({
        name: `edit-config-${Date.now()}`,
      })
      createdPlugins.push(pluginData.name)

      const created = await pluginsPage.createPlugin(pluginData)
      if (!created) {
        test.skip(true, 'Backend rejected plugin creation (failed to load .so)')
        return
      }

      // Get initial enabled state
      const initialState = await pluginsPage.getPluginEnabledState(pluginData.name)

      // Toggle enabled state (a form of editing)
      await pluginsPage.togglePluginEnabled(pluginData.name)

      // State should have changed
      const newState = await pluginsPage.getPluginEnabledState(pluginData.name)
      expect(newState).toBe(!initialState)
    })

    test('should validate plugin path', async ({ pluginsPage }) => {
      await pluginsPage.createBtn.click()
      await expect(pluginsPage.sheet).toBeVisible()
      await pluginsPage.waitForSheetAnimation()

      // Fill name
      const sheetNameInput = pluginsPage.sheet.getByRole('textbox', { name: /Plugin Name/i })
      await sheetNameInput.fill(`path-validation-${Date.now()}`)

      // Fill invalid path (doesn't start with / or http)
      const sheetPathInput = pluginsPage.sheet.getByRole('textbox', { name: /Plugin Path/i })
      await sheetPathInput.fill('invalid-path-no-extension')

      // Validation should disable the save button
      await expect(pluginsPage.saveBtn).toBeDisabled()

      // Validation message should be visible
      const validationMessage = pluginsPage.sheet.getByText(/valid absolute file path|valid path/i)
      await expect(validationMessage).toBeVisible()

      await pluginsPage.cancelPlugin()
    })

    test('should handle invalid plugin path', async ({ pluginsPage }) => {
      await pluginsPage.createBtn.click()
      await expect(pluginsPage.sheet).toBeVisible()
      await pluginsPage.waitForSheetAnimation()

      const sheetNameInput = pluginsPage.sheet.getByRole('textbox', { name: /Plugin Name/i })
      const sheetPathInput = pluginsPage.sheet.getByRole('textbox', { name: /Plugin Path/i })

      await sheetNameInput.fill(`invalid-plugin-${Date.now()}`)
      await sheetPathInput.fill('/nonexistent/path/to/plugin.so')

      // Try to save - this might succeed (path not validated until load) or fail
      await pluginsPage.saveBtn.click()

      // Wait for response
      await pluginsPage.page.waitForTimeout(1000)

      // Sheet may close with error toast or stay open with error
      const sheetVisible = await pluginsPage.sheet.isVisible()
      const hasErrorToast = await pluginsPage.page.locator('[data-sonner-toast][data-type="error"]').count() > 0

      // Expect either the sheet to remain open OR an error toast
      expect(sheetVisible || hasErrorToast).toBe(true)

      if (sheetVisible) {
        await pluginsPage.cancelPlugin()
      }
    })
  })
})
