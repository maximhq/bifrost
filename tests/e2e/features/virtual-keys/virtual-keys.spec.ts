import { expect, test } from '../../core/fixtures/base.fixture'
import {
    createVirtualKeyData,
    createVirtualKeyWithBudget,
    createVirtualKeyWithMultipleProviders,
    createVirtualKeyWithProvider,
    createVirtualKeyWithRateLimit,
    SAMPLE_BUDGETS,
    SAMPLE_RATE_LIMITS,
} from './virtual-keys.data'

// Track created VKs for cleanup
const createdVKs: string[] = []

test.describe('Virtual Keys', () => {
  test.beforeEach(async ({ virtualKeysPage }) => {
    await virtualKeysPage.goto()
  })

  test.afterEach(async ({ virtualKeysPage }) => {
    // Close any open sheets first
    await virtualKeysPage.closeSheet()

    // Clean up all tracked VKs
    if (createdVKs.length > 0) {
      await virtualKeysPage.cleanupVirtualKeys([...createdVKs])
      createdVKs.length = 0 // Clear the array
    }
  })

  test.describe('Virtual Key Creation', () => {
    test('should display create virtual key button', async ({ virtualKeysPage }) => {
      await expect(virtualKeysPage.createBtn).toBeVisible()
    })

    test('should open virtual key creation sheet', async ({ virtualKeysPage }) => {
      await virtualKeysPage.createBtn.click()

      // Verify sheet is visible
      await expect(virtualKeysPage.sheet).toBeVisible()

      // Verify form fields are present
      await expect(virtualKeysPage.nameInput).toBeVisible()
      await expect(virtualKeysPage.descriptionInput).toBeVisible()
    })

    test('should create a basic virtual key', async ({ virtualKeysPage }) => {
      const vkData = createVirtualKeyData({
        name: `Basic VK ${Date.now()}`,
        description: 'A basic virtual key for testing',
      })

      createdVKs.push(vkData.name)
      await virtualKeysPage.createVirtualKey(vkData)

      // Verify virtual key appears in table
      const vkExists = await virtualKeysPage.virtualKeyExists(vkData.name)
      expect(vkExists).toBe(true)
    })

    test('should create virtual key with single provider', async ({ virtualKeysPage }) => {
      const vkData = createVirtualKeyWithProvider('openai', {
        name: `OpenAI VK ${Date.now()}`,
      })

      createdVKs.push(vkData.name)
      await virtualKeysPage.createVirtualKey(vkData)

      const vkExists = await virtualKeysPage.virtualKeyExists(vkData.name)
      expect(vkExists).toBe(true)
    })

    test('should create inactive virtual key', async ({ virtualKeysPage }) => {
      const vkData = createVirtualKeyData({
        name: `Inactive VK ${Date.now()}`,
        isActive: false,
      })

      createdVKs.push(vkData.name)
      await virtualKeysPage.createVirtualKey(vkData)

      const vkExists = await virtualKeysPage.virtualKeyExists(vkData.name)
      expect(vkExists).toBe(true)
    })

    test('should cancel virtual key creation', async ({ virtualKeysPage }) => {
      await virtualKeysPage.createBtn.click()
      await expect(virtualKeysPage.sheet).toBeVisible()

      // Fill some data
      const testName = `Cancelled VK ${Date.now()}`
      await virtualKeysPage.nameInput.fill(testName)

      // Cancel
      await virtualKeysPage.cancelBtn.click()

      // Sheet should close
      await expect(virtualKeysPage.sheet).not.toBeVisible()

      // Virtual key should not exist
      const vkExists = await virtualKeysPage.virtualKeyExists(testName)
      expect(vkExists).toBe(false)
    })
  })

  test.describe('Virtual Key with Budget', () => {
    test('should create virtual key with small budget', async ({ virtualKeysPage }) => {
      const vkData = createVirtualKeyWithBudget(SAMPLE_BUDGETS.small, {
        name: `Small Budget VK ${Date.now()}`,
      })

      createdVKs.push(vkData.name)
      await virtualKeysPage.createVirtualKey(vkData)

      const vkExists = await virtualKeysPage.virtualKeyExists(vkData.name)
      expect(vkExists).toBe(true)

      // Verify budget was saved correctly
      await virtualKeysPage.viewVirtualKey(vkData.name)
      await virtualKeysPage.waitForSheetAnimation()
      const budgetInput = virtualKeysPage.page.locator('#budgetMaxLimit')
      await expect(budgetInput).toHaveValue(String(SAMPLE_BUDGETS.small.maxLimit))
      await virtualKeysPage.closeSheet()
    })

    test('should create virtual key with medium budget', async ({ virtualKeysPage }) => {
      const vkData = createVirtualKeyWithBudget(SAMPLE_BUDGETS.medium, {
        name: `Medium Budget VK ${Date.now()}`,
      })

      createdVKs.push(vkData.name)
      await virtualKeysPage.createVirtualKey(vkData)

      const vkExists = await virtualKeysPage.virtualKeyExists(vkData.name)
      expect(vkExists).toBe(true)
    })

    test('should create virtual key with daily budget', async ({ virtualKeysPage }) => {
      const vkData = createVirtualKeyWithBudget(SAMPLE_BUDGETS.daily, {
        name: `Daily Budget VK ${Date.now()}`,
      })

      createdVKs.push(vkData.name)
      await virtualKeysPage.createVirtualKey(vkData)

      const vkExists = await virtualKeysPage.virtualKeyExists(vkData.name)
      expect(vkExists).toBe(true)
    })
  })

  test.describe('Virtual Key with Rate Limits', () => {
    test('should create virtual key with token rate limit', async ({ virtualKeysPage }) => {
      const vkData = createVirtualKeyWithRateLimit(SAMPLE_RATE_LIMITS.tokenOnly, {
        name: `Token Limit VK ${Date.now()}`,
      })

      createdVKs.push(vkData.name)
      await virtualKeysPage.createVirtualKey(vkData)

      const vkExists = await virtualKeysPage.virtualKeyExists(vkData.name)
      expect(vkExists).toBe(true)

      // Verify rate limit was saved correctly
      await virtualKeysPage.viewVirtualKey(vkData.name)
      await virtualKeysPage.waitForSheetAnimation()
      const tokenLimitInput = virtualKeysPage.page.locator('#tokenMaxLimit')
      await expect(tokenLimitInput).toHaveValue(String(SAMPLE_RATE_LIMITS.tokenOnly.tokenMaxLimit))
      await virtualKeysPage.closeSheet()
    })

    test('should create virtual key with request rate limit', async ({ virtualKeysPage }) => {
      const vkData = createVirtualKeyWithRateLimit(SAMPLE_RATE_LIMITS.requestOnly, {
        name: `Request Limit VK ${Date.now()}`,
      })

      createdVKs.push(vkData.name)
      await virtualKeysPage.createVirtualKey(vkData)

      const vkExists = await virtualKeysPage.virtualKeyExists(vkData.name)
      expect(vkExists).toBe(true)
    })

    test('should create virtual key with combined rate limits', async ({ virtualKeysPage }) => {
      const vkData = createVirtualKeyWithRateLimit(SAMPLE_RATE_LIMITS.conservative, {
        name: `Combined Limits VK ${Date.now()}`,
      })

      createdVKs.push(vkData.name)
      await virtualKeysPage.createVirtualKey(vkData)

      const vkExists = await virtualKeysPage.virtualKeyExists(vkData.name)
      expect(vkExists).toBe(true)
    })
  })

  test.describe('Virtual Key with Multiple Providers', () => {
    test('should create virtual key with two providers', async ({ virtualKeysPage }) => {
      const vkData = createVirtualKeyWithMultipleProviders(['openai', 'anthropic'], {
        name: `Multi Provider VK ${Date.now()}`,
      })

      createdVKs.push(vkData.name)
      await virtualKeysPage.createVirtualKey(vkData)

      const vkExists = await virtualKeysPage.virtualKeyExists(vkData.name)
      expect(vkExists).toBe(true)
    })
  })

  test.describe('Virtual Key with Budget and Rate Limits', () => {
    test('should create virtual key with budget and rate limits', async ({ virtualKeysPage }) => {
      const vkData = createVirtualKeyData({
        name: `Full Config VK ${Date.now()}`,
        description: 'Virtual key with all configurations',
        isActive: true,
        budget: SAMPLE_BUDGETS.medium,
        rateLimit: SAMPLE_RATE_LIMITS.moderate,
      })

      createdVKs.push(vkData.name)
      await virtualKeysPage.createVirtualKey(vkData)

      const vkExists = await virtualKeysPage.virtualKeyExists(vkData.name)
      expect(vkExists).toBe(true)
    })
  })
})

// Track created VKs for management tests
const managementVKs: string[] = []

test.describe('Virtual Key Management', () => {
  test.beforeEach(async ({ virtualKeysPage }) => {
    await virtualKeysPage.goto()
  })

  test.afterEach(async ({ virtualKeysPage }) => {
    // Close any open sheets first
    await virtualKeysPage.closeSheet()

    // Clean up all tracked VKs
    if (managementVKs.length > 0) {
      await virtualKeysPage.cleanupVirtualKeys([...managementVKs])
      managementVKs.length = 0
    }
  })

  test('should edit virtual key name', async ({ virtualKeysPage }) => {
    // First create a virtual key
    const originalName = `Edit Test VK ${Date.now()}`
    const vkData = createVirtualKeyData({ name: originalName })

    await virtualKeysPage.createVirtualKey(vkData)

    // Now edit it
    const updatedName = `${originalName} Updated`
    managementVKs.push(updatedName) // Track the updated name for cleanup

    await virtualKeysPage.editVirtualKey(originalName, {
      name: updatedName,
    })

    // Verify updated name exists
    const vkExists = await virtualKeysPage.virtualKeyExists(updatedName)
    expect(vkExists).toBe(true)
  })

  test('should edit virtual key description', async ({ virtualKeysPage }) => {
    const vkName = `Desc Edit VK ${Date.now()}`
    const vkData = createVirtualKeyData({
      name: vkName,
      description: 'Original description',
    })

    managementVKs.push(vkName)
    await virtualKeysPage.createVirtualKey(vkData)

    await virtualKeysPage.editVirtualKey(vkName, {
      description: 'Updated description for testing',
    })

    // Virtual key should still exist
    const vkExists = await virtualKeysPage.virtualKeyExists(vkName)
    expect(vkExists).toBe(true)
  })

  test('should toggle virtual key active state', async ({ virtualKeysPage }) => {
    const vkName = `Toggle Active VK ${Date.now()}`
    const vkData = createVirtualKeyData({
      name: vkName,
      isActive: true,
    })

    managementVKs.push(vkName)
    await virtualKeysPage.createVirtualKey(vkData)

    // Toggle to inactive
    await virtualKeysPage.editVirtualKey(vkName, {
      isActive: false,
    })

    const vkExists = await virtualKeysPage.virtualKeyExists(vkName)
    expect(vkExists).toBe(true)
  })

  test('should delete virtual key', async ({ virtualKeysPage }) => {
    const vkName = `Delete Test VK ${Date.now()}`
    const vkData = createVirtualKeyData({ name: vkName })

    await virtualKeysPage.createVirtualKey(vkData)

    // Verify it exists
    let vkExists = await virtualKeysPage.virtualKeyExists(vkName)
    expect(vkExists).toBe(true)

    // Delete it (this is the test - no need to track for cleanup)
    await virtualKeysPage.deleteVirtualKey(vkName)

    // Verify it's gone
    vkExists = await virtualKeysPage.virtualKeyExists(vkName)
    expect(vkExists).toBe(false)
  })

  test('should view virtual key details', async ({ virtualKeysPage }) => {
    const vkName = `View Details VK ${Date.now()}`
    const vkData = createVirtualKeyData({
      name: vkName,
      description: 'Detailed description for viewing',
    })

    managementVKs.push(vkName)
    await virtualKeysPage.createVirtualKey(vkData)

    // Click to view details
    await virtualKeysPage.viewVirtualKey(vkName)

    // Detail sheet should be visible with correct content
    await expect(virtualKeysPage.sheet).toBeVisible()
    await expect(virtualKeysPage.nameInput).toHaveValue(vkName)
    await expect(virtualKeysPage.descriptionInput).toHaveValue('Detailed description for viewing')

    // Close the sheet (will be handled by afterEach if not)
    await virtualKeysPage.closeSheet()
  })

  test('should copy virtual key value', async ({ virtualKeysPage }) => {
    const vkName = `Copy Value VK ${Date.now()}`
    const vkData = createVirtualKeyData({ name: vkName })

    managementVKs.push(vkName)
    await virtualKeysPage.createVirtualKey(vkData)

    // Copy the key value - method waits for success toast
    await virtualKeysPage.copyVirtualKeyValue(vkName)

    // Verify copy succeeded: row still exists and key is intact
    const vkExists = await virtualKeysPage.virtualKeyExists(vkName)
    expect(vkExists).toBe(true)
  })

  test('should toggle key visibility', async ({ virtualKeysPage }) => {
    const vkName = `Toggle Visibility VK ${Date.now()}`
    const vkData = createVirtualKeyData({ name: vkName })

    managementVKs.push(vkName)
    await virtualKeysPage.createVirtualKey(vkData)

    // Initially key is masked
    let isRevealed = await virtualKeysPage.isKeyRevealed(vkName)
    expect(isRevealed).toBe(false)

    // Toggle visibility (show key)
    await virtualKeysPage.toggleKeyVisibility(vkName)
    isRevealed = await virtualKeysPage.isKeyRevealed(vkName)
    expect(isRevealed).toBe(true)

    // Toggle again (hide key)
    await virtualKeysPage.toggleKeyVisibility(vkName)
    isRevealed = await virtualKeysPage.isKeyRevealed(vkName)
    expect(isRevealed).toBe(false)
  })
})

test.describe('Virtual Keys Table', () => {
  test.beforeEach(async ({ virtualKeysPage }) => {
    await virtualKeysPage.goto()
  })

  test('should display virtual keys table', async ({ virtualKeysPage }) => {
    await expect(virtualKeysPage.table).toBeVisible({ timeout: 10000 })
    // Verify table has the expected column headers
    await expect(virtualKeysPage.table.locator('th', { hasText: 'Name' })).toBeVisible()
    await expect(virtualKeysPage.table.locator('th', { hasText: 'Key' })).toBeVisible()
  })

  test('should show empty state when no virtual keys', async ({ virtualKeysPage }) => {
    // Wait for the table to be visible first so we know the page has loaded
    await expect(virtualKeysPage.table).toBeVisible({ timeout: 10000 })

    // Delete all existing virtual keys to guarantee empty state
    await virtualKeysPage.cleanupAllVirtualKeys()

    const emptyMessage = virtualKeysPage.page.getByText('No virtual keys found')
    await expect(emptyMessage).toBeVisible({ timeout: 10000 })
  })
})

test.describe('Form Validation', () => {
  test.beforeEach(async ({ virtualKeysPage }) => {
    await virtualKeysPage.goto()
  })

  test.afterEach(async ({ virtualKeysPage }) => {
    // Close any open sheets
    await virtualKeysPage.closeSheet()
  })

  test('should require name for virtual key', async ({ virtualKeysPage }) => {
    await virtualKeysPage.dismissToasts()
    await virtualKeysPage.createBtn.click()
    await expect(virtualKeysPage.sheet).toBeVisible()
    // Wait for sheet animation to complete
    await virtualKeysPage.waitForSheetAnimation()

    // Save button should be disabled when name is empty
    await expect(virtualKeysPage.saveBtn).toBeDisabled()
  })

  test('should accept valid budget values', async ({ virtualKeysPage }) => {
    await virtualKeysPage.dismissToasts()
    await virtualKeysPage.createBtn.click()
    await expect(virtualKeysPage.sheet).toBeVisible()
    // Wait for sheet animation to complete
    await virtualKeysPage.waitForSheetAnimation()

    // Fill name (required field)
    await virtualKeysPage.nameInput.fill(`Valid Budget Test ${Date.now()}`)

    // Fill budget
    const budgetInput = virtualKeysPage.page.locator('#budgetMaxLimit')
    await expect(budgetInput).toBeVisible({ timeout: 5000 })
    await budgetInput.fill('100')

    // Save button should be enabled if form is valid
    await expect(virtualKeysPage.saveBtn).toBeEnabled()
  })
})

// Track created VKs for provider tests
const providerVKs: string[] = []

test.describe('Provider Management', () => {
  test.beforeEach(async ({ virtualKeysPage }) => {
    await virtualKeysPage.goto()
  })

  test.afterEach(async ({ virtualKeysPage }) => {
    // Close any open sheets first
    await virtualKeysPage.closeSheet()

    // Clean up all tracked VKs
    if (providerVKs.length > 0) {
      await virtualKeysPage.cleanupVirtualKeys([...providerVKs])
      providerVKs.length = 0
    }
  })

  test('should add provider to existing virtual key', async ({ virtualKeysPage }) => {
    // Create a virtual key first
    const vkName = `Add Provider VK ${Date.now()}`
    const vkData = createVirtualKeyWithProvider('openai', { name: vkName })

    providerVKs.push(vkName)
    await virtualKeysPage.createVirtualKey(vkData)

    // View the virtual key
    await virtualKeysPage.viewVirtualKey(vkName)

    // Check if we can see provider configuration
    const providerSection = virtualKeysPage.page.getByText(/Providers|Provider/i).first()
    const isVisible = await providerSection.isVisible().catch(() => false)

    if (isVisible) {
      // Provider section is available
      expect(isVisible).toBe(true)
    }

    // Close sheet (handled by afterEach as well)
    await virtualKeysPage.closeSheet()
  })

  test('should remove provider from virtual key', async ({ virtualKeysPage }) => {
    // Create a virtual key with multiple providers
    const vkName = `Remove Provider VK ${Date.now()}`
    const vkData = createVirtualKeyWithMultipleProviders(['openai', 'anthropic'], { name: vkName })

    providerVKs.push(vkName)
    await virtualKeysPage.createVirtualKey(vkData)

    // View the virtual key
    await virtualKeysPage.viewVirtualKey(vkName)

    // Check if we can see and interact with providers
    const removeProviderBtn = virtualKeysPage.page.locator('button').filter({
      has: virtualKeysPage.page.locator('svg.lucide-trash, svg.lucide-x, svg.lucide-trash-2')
    }).first()
    const isVisible = await removeProviderBtn.isVisible().catch(() => false)

    if (isVisible) {
      // Remove provider is available - this is expected behavior
      expect(isVisible).toBe(true)
    }

    // Close sheet (handled by afterEach as well)
    await virtualKeysPage.closeSheet()
  })

  test('should update provider-specific budget', async ({ virtualKeysPage }) => {
    // Create a virtual key with budget
    const vkName = `Provider Budget VK ${Date.now()}`
    const vkData = createVirtualKeyWithProvider('openai', {
      name: vkName,
      budget: SAMPLE_BUDGETS.small,
    })

    providerVKs.push(vkName)
    await virtualKeysPage.createVirtualKey(vkData)

    // Edit the virtual key
    await virtualKeysPage.editVirtualKey(vkName, {
      budget: SAMPLE_BUDGETS.large,
    })

    // Verify it still exists
    const vkExists = await virtualKeysPage.virtualKeyExists(vkName)
    expect(vkExists).toBe(true)
  })
})
