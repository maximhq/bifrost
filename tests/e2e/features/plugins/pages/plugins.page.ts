import { Locator, Page, expect } from '@playwright/test'
import { BasePage } from '../../../core/pages/base.page'
import { waitForNetworkIdle } from '../../../core/utils/test-helpers'

export interface PluginConfig {
  name: string
  path?: string
  type?: string
  enabled?: boolean
  config?: Record<string, any>
}

export class PluginsPage extends BasePage {
  readonly sidebar: Locator
  readonly table: Locator // Alias for sidebar (plugins page doesn't have a traditional table)
  readonly pluginList: Locator
  readonly createBtn: Locator
  readonly sheet: Locator
  readonly nameInput: Locator
  readonly pathInput: Locator
  readonly enabledToggle: Locator
  readonly saveBtn: Locator
  readonly cancelBtn: Locator

  constructor(page: Page) {
    super(page)
    // Plugins page has a sidebar with plugin buttons, not a table
    // The sidebar contains the "Plugins" label and plugin list
    this.sidebar = page.locator('div').filter({ hasText: /^Plugins$/ }).locator('..').first()
    this.table = this.sidebar // Alias for backward compatibility
    this.pluginList = page.locator('button[type="button"]').filter({ has: page.locator('svg.lucide-puzzle') })
    this.createBtn = page.getByRole('button').filter({
      hasText: /Install New Plugin/i
    }).or(
      page.locator('button').filter({ has: page.locator('svg.lucide-plus') }).filter({ hasText: /Install/i })
    )
    this.sheet = page.locator('[role="dialog"]')
    this.nameInput = page.getByLabel(/Name/i).or(page.locator('input[name="name"]'))
    this.pathInput = page.getByLabel(/Path/i).or(page.locator('input[name="path"]'))
    this.enabledToggle = page.locator('button[role="switch"]')
    this.saveBtn = page.getByRole('button', { name: /Install Plugin/i }).or(
      page.getByRole('button', { name: /Update Plugin/i })
    )
    this.cancelBtn = page.getByRole('button', { name: /Cancel/i }).filter({
      hasNot: page.locator('svg.lucide-trash-2')
    })
  }

  /**
   * Navigate to the plugins page
   */
  async goto(): Promise<void> {
    await this.page.goto('/workspace/plugins')
    await waitForNetworkIdle(this.page)
    // Wait for create button or empty state to be visible (indicates page loaded).
    // Use .first() so the locator resolves to one element when both are visible (strict mode).
    await this.createBtn.or(this.page.getByText(/No plugins installed/i))
      .first()
      .waitFor({ state: 'visible', timeout: 10000 })
    // Ensure sheet is closed (in case it was left open from previous test)
    await this.ensureSheetClosed()
  }

  /**
   * Ensure the plugin sheet is closed
   */
  async ensureSheetClosed(): Promise<void> {
    const isVisible = await this.sheet.isVisible().catch(() => false)
    if (isVisible) {
      // Try clicking cancel button first
      const cancelVisible = await this.cancelBtn.isVisible().catch(() => false)
      if (cancelVisible) {
        await this.cancelBtn.click()
        // Wait for sheet to close after cancel click
        await expect(this.sheet).not.toBeVisible({ timeout: 5000 }).catch(() => {})
      }

      // Double-check: if still visible, try Escape key
      const stillVisible = await this.sheet.isVisible().catch(() => false)
      if (stillVisible) {
        await this.page.keyboard.press('Escape')
        // Wait for sheet to close after Escape
        await expect(this.sheet).not.toBeVisible({ timeout: 5000 }).catch(() => {})
      }

      // Final check: wait for sheet to be detached or not visible
      await this.page.waitForFunction(() => {
        const sheet = document.querySelector('[role="dialog"]')
        return !sheet || window.getComputedStyle(sheet).display === 'none'
      }, { timeout: 3000 }).catch(() => {})
    }
  }

  /**
   * Get plugin button locator by name (plugins are shown as buttons in sidebar)
   */
  getPluginButton(name: string): Locator {
    // Find button that contains the plugin name and has a Puzzle icon
    return this.page.locator('button[type="button"]')
      .filter({ hasText: name })
      .filter({ has: this.page.locator('svg.lucide-puzzle') })
      .first()
  }

  /**
   * Check if a plugin exists in the sidebar
   */
  async pluginExists(name: string): Promise<boolean> {
    return await this.getPluginButton(name).count() > 0
  }

  /**
   * Get the count of plugins in the sidebar
   */
  async getPluginCount(): Promise<number> {
    const buttons = this.pluginList
    const count = await buttons.count()

    // Check if it's empty state
    const emptyMessage = this.page.getByText(/No plugins installed/i)
    const isEmptyVisible = await emptyMessage.isVisible().catch(() => false)
    if (isEmptyVisible) {
      return 0
    }

    return count
  }

  /**
   * Create a new plugin.
   * Returns true if the plugin was created successfully, false if the backend rejected it (e.g. .so load failure).
   */
  async createPlugin(config: PluginConfig): Promise<boolean> {
    await this.dismissToasts()
    // Ensure sheet is closed before starting
    await this.ensureSheetClosed()

    await this.createBtn.waitFor({ state: 'visible' })
    await this.createBtn.click()
    await expect(this.sheet).toBeVisible({ timeout: 5000 })
    await this.waitForSheetAnimation()

    // Scope inputs to the dialog sheet to avoid matching background form inputs
    // (PluginsView shows existing plugin form in background with same field names)
    const sheetNameInput = this.sheet.getByRole('textbox', { name: /Plugin Name/i })
    const sheetPathInput = this.sheet.getByRole('textbox', { name: /Plugin Path/i })

    // Fill name (required)
    await sheetNameInput.waitFor({ state: 'visible' })
    await sheetNameInput.fill(config.name)

    // Fill path (required) - use the path from config
    await sheetPathInput.waitFor({ state: 'visible' })
    const pluginPath = config.path || '/tmp/bifrost-test-plugin.so'
    await sheetPathInput.fill(pluginPath)

    // Note: enabled state is set to true by default when creating,
    // and can't be changed during creation (only during edit)

    // Save
    await this.saveBtn.waitFor({ state: 'visible' })
    await this.saveBtn.click()

    // Wait for either a success toast or an error toast (backend may fail to load .so)
    const successToast = this.getToast('success')
    const errorToast = this.getToast('error')
    await successToast.or(errorToast).waitFor({ state: 'visible', timeout: 15000 })

    const hasError = await errorToast.isVisible().catch(() => false)
    if (hasError) {
      // Backend rejected plugin creation (e.g. failed to load .so)
      console.warn(`[Plugin] Backend error on create "${config.name}" — plugin was not created`)
      await this.dismissToasts()
      // Sheet may stay open on error — close it manually
      await this.ensureSheetClosed()
      await waitForNetworkIdle(this.page)
      return false
    }

    await this.dismissToasts()

    // Wait for sheet to close with multiple checks
    await expect(this.sheet).not.toBeVisible({ timeout: 10000 })
    await this.ensureSheetClosed() // Double-check it's closed
    await waitForNetworkIdle(this.page)
    return true
  }

  /**
   * Edit an existing plugin
   * Note: Name cannot be changed after creation (it's read-only in edit mode)
   * Only enabled state, path, and config can be updated
   */
  async editPlugin(name: string, updates: Partial<PluginConfig>): Promise<void> {
    await this.dismissToasts()
    // Ensure sheet is closed before starting
    await this.ensureSheetClosed()

    // Click on the plugin button to select it (this opens the edit view)
    const pluginBtn = this.getPluginButton(name)
    await pluginBtn.waitFor({ state: 'visible' })
    await pluginBtn.click()

    // Wait for the plugin details view to load
    await waitForNetworkIdle(this.page)

    // The form is in PluginsView - wait for it to be visible
    const form = this.page.locator('form')
    await form.waitFor({ state: 'visible', timeout: 5000 })

    // Update enabled state if provided
    if (updates.enabled !== undefined) {
      const toggle = this.page.locator('button[role="switch"]').first()
      await toggle.waitFor({ state: 'visible' })
      const isChecked = await toggle.getAttribute('data-state') === 'checked'
      if (isChecked !== updates.enabled) {
        await toggle.click()
      }
    }

    // Update path if provided
    if (updates.path) {
      const pathInput = this.page.getByLabel(/Path/i).or(this.page.locator('input[name="path"]'))
      await pathInput.waitFor({ state: 'visible' })
      await pathInput.clear()
      await pathInput.fill(updates.path)
    }

    // Wait for Save Changes button to be enabled (form.isDirty must be true)
    const saveBtn = this.page.getByRole('button', { name: /Save Changes/i })
    await expect(saveBtn).toBeEnabled({ timeout: 5000 })
    await saveBtn.click()

    await this.waitForSuccessToast()
    await this.dismissToasts()
    await waitForNetworkIdle(this.page)
    // Ensure sheet is closed after edit
    await this.ensureSheetClosed()
  }

  /**
   * Delete a plugin
   */
  async deletePlugin(name: string): Promise<void> {
    await this.dismissToasts()
    // Ensure sheet is closed before starting
    await this.ensureSheetClosed()

    // Click on the plugin button to select it (this opens the details view)
    const pluginBtn = this.getPluginButton(name)
    await pluginBtn.waitFor({ state: 'visible' })
    await pluginBtn.click()

    // Wait for the plugin details view to load
    await waitForNetworkIdle(this.page)

    // Find delete button in the PluginsView (has Trash2Icon)
    const deleteBtn = this.page.getByRole('button', { name: /Delete Plugin/i })
    await deleteBtn.waitFor({ state: 'visible' })
    await deleteBtn.click()

    // Wait for the AlertDialog confirmation to appear (uses role="alertdialog")
    const alertDialog = this.page.locator('[role="alertdialog"]')
    await alertDialog.waitFor({ state: 'visible', timeout: 5000 })

    // Click the confirm Delete button inside the AlertDialog
    const confirmBtn = alertDialog.getByRole('button', { name: /^Delete$/i })
    await confirmBtn.waitFor({ state: 'visible' })
    await confirmBtn.click()

    await this.waitForSuccessToast('deleted')
    await this.dismissToasts()
    await waitForNetworkIdle(this.page)
    // Ensure sheet is closed after delete
    await this.ensureSheetClosed()
  }

  /**
   * Toggle plugin enabled state
   */
  async togglePluginEnabled(name: string): Promise<void> {
    await this.dismissToasts()
    // Ensure sheet is closed before starting
    await this.ensureSheetClosed()

    // Click on the plugin button to select it (this opens the details view)
    const pluginBtn = this.getPluginButton(name)
    await pluginBtn.waitFor({ state: 'visible' })
    await pluginBtn.click()

    // Wait for the plugin details view to load
    await waitForNetworkIdle(this.page)

    // Find toggle switch in the PluginsView form
    const toggle = this.page.locator('button[role="switch"]').first()
    await toggle.waitFor({ state: 'visible' })
    await toggle.click()

    // Wait for the Save Changes button to become enabled (form.isDirty must be true)
    const saveBtn = this.page.getByRole('button', { name: /Save Changes/i })
    await expect(saveBtn).toBeEnabled({ timeout: 5000 })
    await saveBtn.click()

    await this.waitForSuccessToast()
    await this.dismissToasts()
    await waitForNetworkIdle(this.page)
    // Ensure sheet is closed after toggle
    await this.ensureSheetClosed()
  }

  /**
   * Get plugin enabled state
   */
  async getPluginEnabledState(name: string): Promise<boolean> {
    // Ensure sheet is closed before starting
    await this.ensureSheetClosed()

    // Click on the plugin button to select it
    const pluginBtn = this.getPluginButton(name)
    await pluginBtn.waitFor({ state: 'visible' })
    await pluginBtn.click()

    // Wait for the plugin details view to load
    await waitForNetworkIdle(this.page)

    // Find toggle switch in the PluginsView form
    const toggle = this.page.locator('button[role="switch"]').first()
    let result = false
    if (await toggle.count() > 0) {
      const dataState = await toggle.getAttribute('data-state')
      result = dataState === 'checked'
    }
    await this.ensureSheetClosed()
    return result
  }

  /**
   * Cancel plugin creation/edit
   */
  async cancelPlugin(): Promise<void> {
    if (await this.sheet.isVisible()) {
      await this.cancelBtn.click()
      await expect(this.sheet).not.toBeVisible({ timeout: 5000 })
      await this.page.waitForTimeout(300) // Small delay for animation
    }
    // Double-check it's closed
    await this.ensureSheetClosed()
  }
}
