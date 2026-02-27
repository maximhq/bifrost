import { Locator, Page, expect } from '@playwright/test'
import { BasePage } from '../../../core/pages/base.page'
import { waitForNetworkIdle } from '../../../core/utils/test-helpers'

/**
 * Routing rule configuration
 * Note: CEL expression is auto-generated from the visual Rule Builder in the UI
 */
export interface RoutingRuleConfig {
  name: string
  description?: string
  provider?: string
  model?: string
  priority?: number
  enabled?: boolean
  scope?: 'global' | 'team' | 'customer' | 'virtual_key'
  scopeId?: string
  // Fallback providers
  fallbacks?: string[]
}

/**
 * Filter conditions for the rule builder
 */
export interface RuleFilterCondition {
  field: 'model' | 'provider' | 'virtualKey' | 'customer' | 'metadata'
  operator: 'equals' | 'notEquals' | 'contains' | 'startsWith' | 'endsWith' | 'regex'
  value: string
}

/**
 * Page object for the Routing Rules page
 */
export class RoutingRulesPage extends BasePage {
  // Main elements
  readonly table: Locator
  /** View-level empty state (no table is rendered when there are 0 rules) */
  readonly emptyState: Locator
  readonly createBtn: Locator

  // Sheet elements
  readonly sheet: Locator
  readonly nameInput: Locator
  readonly descriptionInput: Locator
  readonly providerSelect: Locator
  readonly modelSelect: Locator
  readonly priorityInput: Locator
  readonly enabledToggle: Locator
  readonly scopeSelect: Locator
  readonly saveBtn: Locator
  readonly cancelBtn: Locator

  constructor(page: Page) {
    super(page)

    // Main elements: scope to the routing rules table (has Priority column); no data-testid in UI
    this.table = page.locator('table').filter({
      has: page.locator('th').filter({ hasText: /^Priority$/ })
    }).first()
    this.emptyState = page.getByText(/No routing rules yet/i)
    // Use .first() to handle both "New Rule" and "Create First Rule" buttons
    this.createBtn = page.locator('[data-testid="create-routing-rule-btn"]').or(
      page.getByRole('button', { name: /New Rule|Create First Rule/i }).first()
    )

    // Sheet elements
    this.sheet = page.locator('[role="dialog"]').or(page.locator('[data-testid="routing-rule-sheet"]'))
    // Scope inputs to the sheet to avoid matching other elements on page
    this.nameInput = page.locator('[data-testid="rule-name-input"]').or(
      page.locator('[role="dialog"]').getByLabel(/Rule Name/i)
    )
    this.descriptionInput = page.locator('[data-testid="rule-description-input"]').or(
      page.locator('[role="dialog"]').getByLabel(/Description/i)
    )
    this.providerSelect = page.locator('[data-testid="rule-provider-select"]').or(
      page.locator('button').filter({ hasText: /Provider/i })
    )
    this.modelSelect = page.locator('[data-testid="rule-model-select"]').or(
      page.locator('button').filter({ hasText: /Model/i })
    )
    this.priorityInput = page.locator('[data-testid="rule-priority-input"]').or(
      page.getByLabel(/Priority/i)
    )
    this.enabledToggle = page.locator('[data-testid="rule-enabled-toggle"]').or(
      page.locator('[role="dialog"] button[role="switch"]').first()
    )
    // Note: CEL expression is auto-generated from the visual Rule Builder - no direct input
    this.scopeSelect = page.locator('[data-testid="rule-scope-select"]').or(
      page.locator('button').filter({ hasText: /Scope/i })
    )
    // Use exact button names to avoid matching wrong buttons
    // Match both "Save Rule" (create) and "Update Rule" (edit)
    this.saveBtn = page.locator('[data-testid="save-rule-btn"]').or(
      page.locator('[role="dialog"]').getByRole('button', { name: /Save Rule|Update Rule/i })
    )
    // Cancel button specifically (not the Close/X button in header)
    this.cancelBtn = page.locator('[data-testid="cancel-rule-btn"]').or(
      page.locator('[role="dialog"]').getByRole('button', { name: 'Cancel', exact: true })
    )
  }

  /**
   * Navigate to the routing rules page
   */
  async goto(): Promise<void> {
    await this.page.goto('/workspace/routing-rules')
    // Wait for page content (create button, empty state, or table); avoid networkidle (SPA often never idles)
    await Promise.race([
      this.createBtn.waitFor({ state: 'visible', timeout: 15000 }),
      this.emptyState.waitFor({ state: 'visible', timeout: 15000 }),
      this.table.waitFor({ state: 'visible', timeout: 15000 }),
    ])
  }

  /**
   * Get routing rule row locator (tbody data row containing the rule name).
   */
  getRuleRow(name: string): Locator {
    return this.table.locator('tbody tr').filter({ hasText: name }).first()
  }

  private async waitForToastAndAssertSuccess(action: string): Promise<void> {
    const toast = this.page.locator('[data-sonner-toast]:not([data-removed="true"])').first()
    await expect(toast).toBeVisible({ timeout: 10000 })
    const toastText = await toast.textContent()
    if (toastText?.toLowerCase().includes('error') || toastText?.toLowerCase().includes('failed')) {
      throw new Error(`Failed to ${action}: ${toastText}`)
    }
    await this.dismissToasts()
  }

  /**
   * Check if routing rule exists
   */
  async ruleExists(name: string): Promise<boolean> {
    const row = this.getRuleRow(name)
    return await row.count() > 0
  }

  /**
   * Wait for a rule to appear in the table (e.g. after create)
   */
  async waitForRuleToAppear(name: string, timeoutMs: number = 10000): Promise<void> {
    await expect.poll(() => this.ruleExists(name), { timeout: timeoutMs }).toBe(true)
  }

  /**
   * Create a new routing rule
   */
  async createRoutingRule(config: RoutingRuleConfig): Promise<void> {
    await this.dismissToasts()
    await this.createBtn.click()
    await expect(this.sheet).toBeVisible({ timeout: 5000 })
    await this.waitForSheetAnimation()

    // Fill name (required) - scoped to sheet
    await this.nameInput.waitFor({ state: 'visible' })
    await this.nameInput.fill(config.name)

    // Fill description if provided
    if (config.description) {
      await this.descriptionInput.waitFor({ state: 'visible' })
      await this.descriptionInput.fill(config.description)
    }

    // Select provider if provided (in the Routing Target section)
    if (config.provider) {
      const providerCombo = this.sheet.getByRole('combobox').filter({ hasText: /Select provider/i }).first()
      if (await providerCombo.isVisible().catch(() => false)) {
        await providerCombo.click()
        await this.page.waitForSelector('[role="listbox"]', { timeout: 5000 })
        const option = this.page.getByRole('option', { name: new RegExp(config.provider, 'i') }).first()
        await option.scrollIntoViewIfNeeded()
        await option.click({ force: true })
        // Wait for dropdown to close
        await this.page.waitForSelector('[role="listbox"]', { state: 'hidden', timeout: 5000 }).catch(() => {})
      }
    }

    // Note: CEL expression is auto-generated from the Rule Builder (visual query builder)
    // The UI doesn't have a direct CEL input field - it shows a read-only preview
    // To add conditions, use the "Add Rule" button in the Rule Builder section
    // For basic tests, leaving the builder empty applies the rule to all requests

    // Set priority if provided - clear first then fill
    if (config.priority !== undefined) {
      await this.priorityInput.waitFor({ state: 'visible' })
      await this.priorityInput.clear()
      await this.priorityInput.fill(String(config.priority))
    }

    // Set enabled state if explicitly specified
    if (config.enabled !== undefined) {
      const isChecked = await this.enabledToggle.getAttribute('data-state') === 'checked'
      if (config.enabled && !isChecked) {
        await this.enabledToggle.click()
      } else if (!config.enabled && isChecked) {
        await this.enabledToggle.click()
      }
    }

    // Save
    await this.saveBtn.waitFor({ state: 'visible' })
    await this.saveBtn.click()

    await this.waitForToastAndAssertSuccess('create routing rule')
    await expect(this.sheet).not.toBeVisible({ timeout: 10000 })
    await waitForNetworkIdle(this.page)
    // Wait for the new rule to appear in the table (list may refresh async)
    await this.waitForRuleToAppear(config.name, 10000)
  }

  /**
   * Edit an existing routing rule
   */
  async editRoutingRule(name: string, updates: Partial<RoutingRuleConfig>): Promise<void> {
    await this.dismissToasts()
    const row = this.getRuleRow(name)
    await row.scrollIntoViewIfNeeded()

    // Find edit button
    const editBtn = row.locator('button').filter({ has: this.page.locator('svg.lucide-pencil') }).or(
      row.getByRole('button', { name: /Edit/i })
    )
    await editBtn.waitFor({ state: 'visible' })
    await editBtn.click()

    await expect(this.sheet).toBeVisible({ timeout: 5000 })
    await this.waitForSheetAnimation()

    // Update fields
    if (updates.name) {
      await this.nameInput.waitFor({ state: 'visible' })
      await this.nameInput.clear()
      await this.nameInput.fill(updates.name)
    }

    if (updates.description !== undefined) {
      await this.descriptionInput.waitFor({ state: 'visible' })
      await this.descriptionInput.clear()
      if (updates.description) {
        await this.descriptionInput.fill(updates.description)
      }
    }

    if (updates.priority !== undefined) {
      await this.priorityInput.waitFor({ state: 'visible' })
      await this.priorityInput.clear()
      await this.priorityInput.fill(String(updates.priority))
    }

    // Save
    await this.saveBtn.waitFor({ state: 'visible' })
    await this.saveBtn.click()

    await this.waitForToastAndAssertSuccess('edit routing rule')
    await expect(this.sheet).not.toBeVisible({ timeout: 10000 })
    await waitForNetworkIdle(this.page)
  }

  /**
   * Delete a routing rule
   */
  async deleteRoutingRule(name: string): Promise<void> {
    await this.dismissToasts()
    const row = this.getRuleRow(name)
    await row.scrollIntoViewIfNeeded()

    // Find delete button (may have lucide-trash or lucide-trash-2 icon)
    const deleteBtn = row.locator('button').filter({
      has: this.page.locator('svg.lucide-trash, svg.lucide-trash-2')
    }).first()
    await deleteBtn.waitFor({ state: 'visible' })
    await deleteBtn.click()

    // Wait for confirmation dialog (AlertDialog uses role="alertdialog")
    const alertDialog = this.page.locator('[role="alertdialog"]')
    await alertDialog.waitFor({ state: 'visible', timeout: 5000 })

    // Click confirm delete button inside the dialog
    const confirmBtn = alertDialog.getByRole('button', { name: /Delete/i })
    await confirmBtn.waitFor({ state: 'visible' })
    await confirmBtn.click()

    await this.waitForSuccessToast('deleted')
    await this.dismissToasts()
    await waitForNetworkIdle(this.page)
  }

  /**
   * Toggle rule enabled state
   */
  async toggleRuleEnabled(name: string): Promise<void> {
    await this.dismissToasts() // Dismiss any existing toasts
    const row = this.getRuleRow(name)
    await row.scrollIntoViewIfNeeded()

    // Find toggle switch in the row
    const toggle = row.locator('button[role="switch"]')
    if (await toggle.count() > 0) {
      await toggle.waitFor({ state: 'visible' })
      await toggle.click()
      await this.waitForSuccessToast()
      await this.dismissToasts() // Wait for toasts to disappear
    }
  }

  /**
   * Cancel rule creation/edit
   */
  async cancelRule(): Promise<void> {
    if (await this.sheet.isVisible()) {
      await this.cancelBtn.click()
      await expect(this.sheet).not.toBeVisible({ timeout: 5000 })
    }
  }

  /**
   * Get routing rule count.
   * When there are 0 rules, the view shows an empty state (no table in DOM).
   */
  async getRuleCount(): Promise<number> {
    const emptyVisible = await this.emptyState.isVisible().catch(() => false)
    if (emptyVisible) {
      return 0
    }
    const tableVisible = await this.table.isVisible().catch(() => false)
    if (!tableVisible) {
      return 0
    }
    const rows = this.table.locator('tbody tr')
    const count = await rows.count()
    const firstRowText = await rows.first().textContent({ timeout: 5000 }).catch(() => '')
    if (firstRowText?.includes('No routing rules')) {
      return 0
    }
    return count
  }

  /**
   * Get the rule builder container (the Query builder generic element)
   */
  getRuleBuilder(): Locator {
    return this.sheet.locator('[aria-label="Query builder"]')
  }

  /**
   * Wait for the CEL rule builder to be fully loaded
   */
  async waitForRuleBuilder(): Promise<void> {
    // Wait for the Add Rule button to be visible (indicates builder is loaded)
    const addRuleBtn = this.sheet.getByRole('button', { name: 'Add Rule', exact: true })
    await addRuleBtn.waitFor({ state: 'visible', timeout: 10000 })
    // Give time for React to fully render
    await this.page.waitForTimeout(500)
  }

  /**
   * Click the "Add Rule" button in the rule builder
   */
  async clickAddRule(): Promise<void> {
    await this.waitForRuleBuilder()
    const addRuleBtn = this.sheet.getByRole('button', { name: 'Add Rule', exact: true })
    await addRuleBtn.click()
    await this.page.waitForTimeout(500) // Wait for new rule row to appear
  }

  /**
   * Click the "Add Rule Group" button in the rule builder
   */
  async clickAddRuleGroup(): Promise<void> {
    await this.waitForRuleBuilder()
    const addGroupBtn = this.sheet.getByRole('button', { name: 'Add Rule Group', exact: true })
    await addGroupBtn.click()
    await this.page.waitForTimeout(500) // Wait for new group to appear
  }

  /**
   * Get all comboboxes for a specific rule row
   * The rule builder has a specific structure where rule rows have remove buttons (⨯)
   */
  async getRuleRowComboboxes(ruleIndex: number): Promise<{ field: Locator; operator: Locator; value: Locator }> {
    const ruleBuilder = this.getRuleBuilder()
    // Find all rows that have the remove button (⨯) - these are rule rows
    const ruleRows = ruleBuilder.locator('> div').filter({
      has: this.page.locator('button').filter({ hasText: '⨯' })
    })
    const ruleRow = ruleRows.nth(ruleIndex)

    // Within the rule row, comboboxes are in order: field, (hidden), operator, (hidden), then value area
    // Get all visible comboboxes in this row
    const allComboboxes = ruleRow.locator('[role="combobox"]')

    return {
      field: allComboboxes.first(),
      operator: allComboboxes.nth(2), // Skip the hidden one at index 1
      value: ruleRow.locator('[role="combobox"]').last() // Value selector is in a nested structure
    }
  }

  /**
   * Select field for a rule (by rule index, 0-based)
   */
  async selectRuleField(ruleIndex: number, fieldName: string): Promise<void> {
    const { field: fieldSelector } = await this.getRuleRowComboboxes(ruleIndex)
    await fieldSelector.waitFor({ state: 'visible', timeout: 5000 })
    await fieldSelector.click()
    await this.page.waitForTimeout(300)
    await this.page.getByRole('option', { name: new RegExp(`^${fieldName}$`, 'i') }).first().click({ force: true })
    await this.page.waitForTimeout(300)
  }

  /**
   * Select operator for a rule (by rule index, 0-based)
   * Operator symbols: =, !=, >, <, >=, <=, contains, starts with, ends with, matches regex
   */
  async selectRuleOperator(ruleIndex: number, operatorName: string): Promise<void> {
    const { operator: operatorSelector } = await this.getRuleRowComboboxes(ruleIndex)
    await operatorSelector.waitFor({ state: 'visible', timeout: 5000 })
    await operatorSelector.click()
    await this.page.waitForTimeout(300)
    // Match operator by exact symbol or text
    const option = this.page.getByRole('option').filter({ hasText: new RegExp(`^${operatorName}$|^${operatorName} `, 'i') }).first()
    await option.click({ force: true })
    await this.page.waitForTimeout(300)
  }

  /**
   * Set value for a rule using the value combobox/input
   * For Model/Provider fields, this is a searchable dropdown
   */
  async setRuleValue(ruleIndex: number, value: string): Promise<void> {
    const ruleBuilder = this.getRuleBuilder()
    const ruleRows = ruleBuilder.locator('> div').filter({
      has: this.page.locator('button').filter({ hasText: '⨯' })
    })
    const ruleRow = ruleRows.nth(ruleIndex)

    // Value input can be either a text input or a searchable combobox
    // Try text input first
    const textInput = ruleRow.locator('input[type="text"]').first()
    if (await textInput.isVisible().catch(() => false)) {
      await textInput.fill(value)
      await this.page.waitForTimeout(200)
      return
    }

    // Otherwise use the value combobox (for Model/Provider fields)
    // It's in a nested structure, look for "Select a model..." or similar text
    const valueArea = ruleRow.locator('div').filter({ hasText: /Select a/ }).last()
    const valueSelector = valueArea.locator('[role="combobox"]').first()

    if (await valueSelector.isVisible().catch(() => false)) {
      await valueSelector.click()
      await this.page.waitForTimeout(300)

      // Type in the search input
      const searchInput = this.page.locator('[cmdk-input]').or(
        this.page.locator('input[placeholder*="Search"]')
      ).or(
        this.page.locator('[role="listbox"] input')
      )

      if (await searchInput.isVisible().catch(() => false)) {
        await searchInput.fill(value)
        await this.page.waitForTimeout(500)
      }

      // Try to select matching option
      const option = this.page.getByRole('option', { name: new RegExp(value, 'i') }).first()
      if (await option.isVisible({ timeout: 2000 }).catch(() => false)) {
        await option.click({ force: true })
      } else {
        // Press escape and type directly in input if no option found
        await this.page.keyboard.press('Escape')
      }
      await this.page.waitForTimeout(300)
    }
  }

  /**
   * Change the combinator (AND/OR) for a rule group
   */
  async setCombinator(combinator: 'and' | 'or'): Promise<void> {
    // AND/OR are toggle buttons - click the one we want to activate
    const targetBtn = this.sheet.getByRole('button', { name: combinator.toUpperCase(), exact: true })
    await targetBtn.click()
    await this.page.waitForTimeout(300)
  }

  /**
   * Get the CEL expression preview text
   */
  async getCelExpression(): Promise<string> {
    // The CEL preview is in a readonly textarea with label "CEL Expression Preview"
    const celPreview = this.sheet.locator('textarea').last()
    await celPreview.waitFor({ state: 'visible', timeout: 5000 })
    return await celPreview.inputValue()
  }

  /**
   * Add a complete rule condition (field + operator + value)
   */
  async addRuleCondition(condition: RuleFilterCondition): Promise<void> {
    await this.clickAddRule()

    // Get the index of the new rule (last one)
    const ruleBuilder = this.getRuleBuilder()
    const ruleRows = ruleBuilder.locator('> div').filter({
      has: this.page.locator('button').filter({ hasText: '⨯' })
    })
    const ruleCount = await ruleRows.count()
    const newRuleIndex = ruleCount - 1

    // Select field
    await this.selectRuleField(newRuleIndex, condition.field)

    // Select operator (map our operator names to UI labels)
    const operatorMap: Record<string, string> = {
      'equals': '=',
      'notEquals': '!=',
      'contains': 'contains',
      'startsWith': 'starts with',
      'endsWith': 'ends with',
      'regex': 'matches regex'
    }
    await this.selectRuleOperator(newRuleIndex, operatorMap[condition.operator] || condition.operator)

    // Set value
    await this.setRuleValue(newRuleIndex, condition.value)
  }

  /**
   * Add a fallback provider
   */
  async addFallbackProvider(provider: string, model?: string): Promise<void> {
    // Find the "Add Fallback" button
    const addFallbackBtn = this.sheet.getByRole('button', { name: /Add Fallback/i }).or(
      this.sheet.locator('button').filter({ hasText: /Fallback/i })
    )

    const isVisible = await addFallbackBtn.isVisible().catch(() => false)
    if (isVisible) {
      await addFallbackBtn.click()

      // Fill in provider/model - typically in format "provider/model"
      const fallbackInput = this.sheet.locator('input[placeholder*="fallback" i], input[placeholder*="provider" i]').first()
      const value = model ? `${provider}/${model}` : provider
      await fallbackInput.fill(value)
    }
  }

  /**
   * Duplicate an existing rule
   */
  async duplicateRule(name: string): Promise<string | null> {
    await this.dismissToasts()
    const row = this.getRuleRow(name)
    await row.scrollIntoViewIfNeeded()

    // Find duplicate button
    const duplicateBtn = row.locator('button').filter({
      has: this.page.locator('svg.lucide-copy')
    }).or(
      row.getByRole('button', { name: /Duplicate/i })
    )

    const isVisible = await duplicateBtn.isVisible().catch(() => false)
    if (!isVisible) {
      return null
    }

    await duplicateBtn.click()
    await this.waitForSuccessToast()

    // NOTE: Assumes UI appends " (copy)" to duplicated rule names.
    // If this convention changes, update this return value.
    return `${name} (copy)`
  }

  /**
   * Reorder rules by drag and drop (if supported)
   * Note: Many UIs use priority field instead of drag-drop
   */
  async reorderRuleByPriority(name: string, newPriority: number): Promise<void> {
    // Edit the rule and change its priority
    await this.editRoutingRule(name, { priority: newPriority })
  }

  /**
   * Get rule's description from the table (first column contains name + description)
   */
  async getRuleDescription(name: string): Promise<string> {
    const row = this.getRuleRow(name)
    const descEl = row.getByTestId('routing-rule-description')
    const count = await descEl.count()
    if (count === 0) return ''
    return (await descEl.textContent()) ?? ''
  }

  /**
   * Get rule's current priority
   */
  async getRulePriority(name: string): Promise<number | null> {
    const row = this.getRuleRow(name)

    // Table columns: Name(0), Provider(1), Model(2), Scope(3), Priority(4), Expression(5), Status(6), Actions(7)
    const cells = row.locator('td')
    const count = await cells.count()

    // Priority is in the 5th column (index 4)
    if (count > 4) {
      const text = await cells.nth(4).textContent()
      const num = parseInt(text || '', 10)
      if (!isNaN(num) && num > 0) {
        return num
      }
    }

    return null
  }

  /**
   * Set scope for a rule in the form
   */
  async setRuleScope(scope: 'global' | 'team' | 'customer' | 'virtual_key', scopeId?: string): Promise<void> {
    // Find scope select
    const scopeSelect = this.sheet.locator('[role="combobox"]').filter({ hasText: /Scope|Global/i }).first()

    if (await scopeSelect.isVisible().catch(() => false)) {
      await scopeSelect.click()

      // Map scope values to display labels
      const labels: Record<string, string> = {
        'global': 'Global',
        'team': 'Team',
        'customer': 'Customer',
        'virtual_key': 'Virtual Key'
      }

      await this.page.getByRole('option', { name: labels[scope] }).click({ force: true })

      // If not global, fill in the scope ID
      if (scope !== 'global' && scopeId) {
        // Wait for scope ID select/input to appear
        const scopeIdInput = this.sheet.locator('[role="combobox"]').filter({ hasText: /Select/i }).last().or(
          this.sheet.locator('input[placeholder*="Select" i]').last()
        )

        if (await scopeIdInput.isVisible().catch(() => false)) {
          await scopeIdInput.click()
          await this.page.getByRole('option', { name: new RegExp(scopeId, 'i') }).first().click({ force: true })
        }
      }
    }
  }

  /**
   * Get all rule names from the table
   */
  async getAllRuleNames(): Promise<string[]> {
    const rows = this.table.locator('tbody tr')
    const count = await rows.count()
    const names: string[] = []

    for (let i = 0; i < count; i++) {
      const firstCell = rows.nth(i).locator('td').first()
      const name = await firstCell.textContent()
      if (name && !name.includes('No routing rules')) {
        names.push(name.trim())
      }
    }

    return names
  }
}
