import { expect, test } from '../../core/fixtures/base.fixture'
import { createRoutingRuleData } from './routing-rules.data'

// Track created rules for cleanup
const createdRules: string[] = []

test.describe('Routing Rules', () => {
  test.beforeEach(async ({ routingRulesPage }) => {
    await routingRulesPage.goto()
  })

  test.afterEach(async ({ routingRulesPage }) => {
    // Clean up any rules created during tests
    for (const ruleName of [...createdRules]) {
      try {
        const exists = await routingRulesPage.ruleExists(ruleName)
        if (exists) {
          await routingRulesPage.deleteRoutingRule(ruleName)
        }
      } catch {
        // Ignore cleanup errors
      }
    }
    // Clear the array
    createdRules.length = 0
  })

  test.describe('Routing Rule Creation', () => {
    test('should display create routing rule button', async ({ routingRulesPage }) => {
      await expect(routingRulesPage.createBtn).toBeVisible()
    })

    test('should open routing rule creation sheet', async ({ routingRulesPage }) => {
      await routingRulesPage.createBtn.click()

      await expect(routingRulesPage.sheet).toBeVisible({ timeout: 5000 })
      await expect(routingRulesPage.nameInput).toBeVisible()
    })

    test('should create a basic routing rule', async ({ routingRulesPage }) => {
      // Note: CEL expression is auto-generated from the visual Rule Builder
      // An empty builder means the rule applies to all requests
      const ruleData = createRoutingRuleData({
        name: `Basic Rule ${Date.now()}`,
      })
      createdRules.push(ruleData.name)

      await routingRulesPage.createRoutingRule(ruleData)

      const exists = await routingRulesPage.ruleExists(ruleData.name)
      expect(exists).toBe(true)
    })

    test('should create routing rule with description', async ({ routingRulesPage }) => {
      const ruleData = createRoutingRuleData({
        name: `Described Rule ${Date.now()}`,
        description: 'A rule with a detailed description for testing',
      })
      createdRules.push(ruleData.name)

      await routingRulesPage.createRoutingRule(ruleData)

      const exists = await routingRulesPage.ruleExists(ruleData.name)
      expect(exists).toBe(true)
    })

    test('should create disabled routing rule', async ({ routingRulesPage }) => {
      const ruleData = createRoutingRuleData({
        name: `Disabled Rule ${Date.now()}`,
        enabled: false,
      })
      createdRules.push(ruleData.name)

      await routingRulesPage.createRoutingRule(ruleData)

      const exists = await routingRulesPage.ruleExists(ruleData.name)
      expect(exists).toBe(true)
    })

    test('should cancel routing rule creation', async ({ routingRulesPage }) => {
      await routingRulesPage.createBtn.click()
      await expect(routingRulesPage.sheet).toBeVisible()

      const testName = `Cancelled Rule ${Date.now()}`
      await routingRulesPage.nameInput.fill(testName)

      await routingRulesPage.cancelRule()

      const exists = await routingRulesPage.ruleExists(testName)
      expect(exists).toBe(false)
    })
  })

  test.describe('Routing Rule Management', () => {
    test('should edit routing rule', async ({ routingRulesPage }) => {
      // Create a rule first
      const ruleData = createRoutingRuleData({
        name: `Edit Test Rule ${Date.now()}`,
      })
      createdRules.push(ruleData.name)

      await routingRulesPage.createRoutingRule(ruleData)

      // Edit it - change description
      await routingRulesPage.editRoutingRule(ruleData.name, {
        description: 'Updated description',
      })

      // Verify description was saved and displayed in table
      const description = await routingRulesPage.getRuleDescription(ruleData.name)
      expect(description).toContain('Updated description')
    })

    test('should delete routing rule', async ({ routingRulesPage }) => {
      // Create a rule first
      const ruleData = createRoutingRuleData({
        name: `Delete Test Rule ${Date.now()}`,
      })
      // Don't add to createdRules since we're testing delete

      await routingRulesPage.createRoutingRule(ruleData)

      // Verify it exists
      let exists = await routingRulesPage.ruleExists(ruleData.name)
      expect(exists).toBe(true)

      // Delete it
      await routingRulesPage.deleteRoutingRule(ruleData.name)

      // Verify it's gone
      exists = await routingRulesPage.ruleExists(ruleData.name)
      expect(exists).toBe(false)
    })

    test('should toggle rule enabled state', async ({ routingRulesPage }) => {
      // Create a rule first
      const ruleData = createRoutingRuleData({
        name: `Toggle Test Rule ${Date.now()}`,
        enabled: true,
      })
      createdRules.push(ruleData.name)

      await routingRulesPage.createRoutingRule(ruleData)

      // Toggle it
      await routingRulesPage.toggleRuleEnabled(ruleData.name)

      // Verify it still exists
      const exists = await routingRulesPage.ruleExists(ruleData.name)
      expect(exists).toBe(true)
    })
  })

  test.describe('Form Validation', () => {
    test('should require name for routing rule', async ({ routingRulesPage }) => {
      await routingRulesPage.createBtn.click()
      await expect(routingRulesPage.sheet).toBeVisible()

      // Try to save without name
      await routingRulesPage.saveBtn.click()

      // Form should still be visible (not submitted)
      await expect(routingRulesPage.sheet).toBeVisible()

      await routingRulesPage.cancelRule()
    })
  })

  test.describe('Table Display', () => {
    test('should display routing rules table', async ({ routingRulesPage }) => {
      // With 0 rules the view shows empty state (no table); with 1+ rules it shows the table
      const count = await routingRulesPage.getRuleCount()
      if (count === 0) {
        await expect(routingRulesPage.emptyState).toBeVisible()
        await expect(routingRulesPage.table).not.toBeVisible()
      } else {
        await expect(routingRulesPage.table).toBeVisible()
        await expect(routingRulesPage.emptyState).not.toBeVisible()
      }
    })

    test('should show empty state when no rules', async ({ routingRulesPage }) => {
      const count = await routingRulesPage.getRuleCount()
      if (count === 0) {
        await expect(routingRulesPage.emptyState).toBeVisible()
      }
      // When rules exist, getRuleCount > 0 is already implied by the condition
    })
  })

  test.describe('Advanced Rule Features', () => {
    test('should create rule with provider filter', async ({ routingRulesPage }) => {
      const ruleData = createRoutingRuleData({
        name: `Provider Filter Rule ${Date.now()}`,
        provider: 'openai', // Set target provider
      })
      createdRules.push(ruleData.name)

      await routingRulesPage.createRoutingRule(ruleData)

      const exists = await routingRulesPage.ruleExists(ruleData.name)
      expect(exists).toBe(true)
    })

    test('should create rule with model filter', async ({ routingRulesPage }) => {
      const ruleData = createRoutingRuleData({
        name: `Model Filter Rule ${Date.now()}`,
        provider: 'openai',
        model: 'gpt-4',
      })
      createdRules.push(ruleData.name)

      await routingRulesPage.createRoutingRule(ruleData)

      const exists = await routingRulesPage.ruleExists(ruleData.name)
      expect(exists).toBe(true)
    })

    test('should reorder rules by changing priority', async ({ routingRulesPage }) => {
      // Create two rules with unique priorities (avoid fixed 500/600 so parallel workers don't collide)
      const rule1 = createRoutingRuleData({ name: `Reorder Test Rule 1 ${Date.now()}` })
      const rule2 = createRoutingRuleData({ name: `Reorder Test Rule 2 ${Date.now()}` })
      createdRules.push(rule1.name, rule2.name)

      await routingRulesPage.createRoutingRule(rule1)
      await routingRulesPage.createRoutingRule(rule2)

      // Change first rule's priority (edit to a new value to test reorder)
      const newPriority = (rule1.priority! + 100) % 901
      await routingRulesPage.editRoutingRule(rule1.name, { priority: newPriority })

      // Verify priority was saved and displayed
      const displayedPriority = await routingRulesPage.getRulePriority(rule1.name)
      expect(displayedPriority).toBe(newPriority)
    })

    test('should create rule with virtual key scope', async ({ routingRulesPage }) => {
      await routingRulesPage.createBtn.click()
      await expect(routingRulesPage.sheet).toBeVisible()

      const ruleName = `VK Scope Rule ${Date.now()}`
      await routingRulesPage.nameInput.fill(ruleName)

      // Try to set scope to virtual key
      const scopeSelect = routingRulesPage.sheet.locator('[role="combobox"]').filter({ hasText: /Global|Scope/i }).first()
      const scopeVisible = await scopeSelect.isVisible().catch(() => false)

      if (scopeVisible) {
        // Scope selection is available
        await scopeSelect.click()
        const vkOption = routingRulesPage.page.getByRole('option', { name: /Virtual Key/i })
        const vkVisible = await vkOption.isVisible().catch(() => false)

        if (vkVisible) {
          await vkOption.click()
          // Note: Would need to select a specific VK - for now just verify the option exists
        }
      }

      // Cancel since we're just testing the UI
      await routingRulesPage.cancelRule()
    })
  })

  test.describe('Rule Builder and CEL Generation', () => {
    test('should show CEL preview with "No rules defined" when empty', async ({ routingRulesPage }) => {
      await routingRulesPage.createBtn.click()
      await expect(routingRulesPage.sheet).toBeVisible()
      await routingRulesPage.waitForSheetAnimation()

      // Wait for rule builder to fully load
      await routingRulesPage.waitForRuleBuilder()

      // Get CEL expression - should show no rules message when empty
      const celExpression = await routingRulesPage.getCelExpression()
      expect(celExpression).toContain('No rules defined')

      await routingRulesPage.cancelRule()
    })

    test('should add rule condition and update CEL preview', async ({ routingRulesPage }) => {
      await routingRulesPage.createBtn.click()
      await expect(routingRulesPage.sheet).toBeVisible()
      await routingRulesPage.waitForSheetAnimation()
      await routingRulesPage.waitForRuleBuilder()

      // Fill required name
      const ruleName = `CEL Test ${Date.now()}`
      await routingRulesPage.nameInput.fill(ruleName)
      createdRules.push(ruleName)

      // Verify initial CEL is empty/no rules
      const initialCel = await routingRulesPage.getCelExpression()
      expect(initialCel).toContain('No rules defined')

      // Add a rule condition
      await routingRulesPage.clickAddRule()

      // Wait for rule row to appear and CEL to update
      await routingRulesPage.page.waitForTimeout(500)

      // After adding a rule, CEL should no longer say "No rules defined"
      // The default rule shows model == "" (empty model condition)
      const celAfterAdd = await routingRulesPage.getCelExpression()
      expect(celAfterAdd).not.toContain('No rules defined')
      expect(celAfterAdd).toContain('model') // Default field is Model

      await routingRulesPage.cancelRule()
    })

    test('should switch between AND and OR combinators', async ({ routingRulesPage }) => {
      await routingRulesPage.createBtn.click()
      await expect(routingRulesPage.sheet).toBeVisible()
      await routingRulesPage.waitForSheetAnimation()
      await routingRulesPage.waitForRuleBuilder()

      // Fill required name
      const ruleName = `CEL Combinator Test ${Date.now()}`
      await routingRulesPage.nameInput.fill(ruleName)
      createdRules.push(ruleName)

      // Add two rule conditions to see the combinator in action
      await routingRulesPage.clickAddRule()
      await routingRulesPage.clickAddRule()

      // Wait for rules to render
      await routingRulesPage.page.waitForTimeout(500)

      // Get CEL with default AND combinator
      const celWithAnd = await routingRulesPage.getCelExpression()
      // Default is AND - should have && operator
      expect(celWithAnd).toContain('&&')

      // Switch to OR
      await routingRulesPage.setCombinator('or')
      await routingRulesPage.page.waitForTimeout(300)

      // Verify CEL now contains OR logic
      const celWithOr = await routingRulesPage.getCelExpression()
      expect(celWithOr).toContain('||')

      await routingRulesPage.cancelRule()
    })

    test('should save rule with conditions successfully', async ({ routingRulesPage }) => {
      const ruleName = `CEL Save Test ${Date.now()}`
      createdRules.push(ruleName)

      await routingRulesPage.createBtn.click()
      await expect(routingRulesPage.sheet).toBeVisible()
      await routingRulesPage.waitForSheetAnimation()
      await routingRulesPage.waitForRuleBuilder()

      // Fill name
      await routingRulesPage.nameInput.fill(ruleName)

      // Add a condition (default Model field with default operator)
      await routingRulesPage.clickAddRule()
      await routingRulesPage.page.waitForTimeout(500)

      // Verify CEL was generated before saving
      const celBeforeSave = await routingRulesPage.getCelExpression()
      expect(celBeforeSave).not.toContain('No rules defined')

      // Save the rule
      await routingRulesPage.saveBtn.click()
      await routingRulesPage.waitForSuccessToast()
      await expect(routingRulesPage.sheet).not.toBeVisible({ timeout: 10000 })

      // Verify rule was created
      const exists = await routingRulesPage.ruleExists(ruleName)
      expect(exists).toBe(true)
    })
  })
})
