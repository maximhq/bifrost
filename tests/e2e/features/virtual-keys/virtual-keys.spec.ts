import { test, expect } from '../../core/fixtures/base.fixture'
import {
  createVirtualKeyData,
  createVirtualKeyWithProvider,
  createVirtualKeyWithBudget,
  createVirtualKeyWithRateLimit,
  createVirtualKeyWithMultipleProviders,
  SAMPLE_BUDGETS,
  SAMPLE_RATE_LIMITS,
} from './virtual-keys.data'

test.describe('Virtual Keys', () => {
  test.beforeEach(async ({ virtualKeysPage }) => {
    await virtualKeysPage.goto()
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
      
      await virtualKeysPage.createVirtualKey(vkData)
      
      // Verify virtual key appears in table
      const vkExists = await virtualKeysPage.virtualKeyExists(vkData.name)
      expect(vkExists).toBe(true)
    })

    test('should create virtual key with single provider', async ({ virtualKeysPage }) => {
      const vkData = createVirtualKeyWithProvider('openai', {
        name: `OpenAI VK ${Date.now()}`,
      })
      
      await virtualKeysPage.createVirtualKey(vkData)
      
      const vkExists = await virtualKeysPage.virtualKeyExists(vkData.name)
      expect(vkExists).toBe(true)
    })

    test('should create inactive virtual key', async ({ virtualKeysPage }) => {
      const vkData = createVirtualKeyData({
        name: `Inactive VK ${Date.now()}`,
        isActive: false,
      })
      
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
      
      await virtualKeysPage.createVirtualKey(vkData)
      
      const vkExists = await virtualKeysPage.virtualKeyExists(vkData.name)
      expect(vkExists).toBe(true)
    })

    test('should create virtual key with medium budget', async ({ virtualKeysPage }) => {
      const vkData = createVirtualKeyWithBudget(SAMPLE_BUDGETS.medium, {
        name: `Medium Budget VK ${Date.now()}`,
      })
      
      await virtualKeysPage.createVirtualKey(vkData)
      
      const vkExists = await virtualKeysPage.virtualKeyExists(vkData.name)
      expect(vkExists).toBe(true)
    })

    test('should create virtual key with daily budget', async ({ virtualKeysPage }) => {
      const vkData = createVirtualKeyWithBudget(SAMPLE_BUDGETS.daily, {
        name: `Daily Budget VK ${Date.now()}`,
      })
      
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
      
      await virtualKeysPage.createVirtualKey(vkData)
      
      const vkExists = await virtualKeysPage.virtualKeyExists(vkData.name)
      expect(vkExists).toBe(true)
    })

    test('should create virtual key with request rate limit', async ({ virtualKeysPage }) => {
      const vkData = createVirtualKeyWithRateLimit(SAMPLE_RATE_LIMITS.requestOnly, {
        name: `Request Limit VK ${Date.now()}`,
      })
      
      await virtualKeysPage.createVirtualKey(vkData)
      
      const vkExists = await virtualKeysPage.virtualKeyExists(vkData.name)
      expect(vkExists).toBe(true)
    })

    test('should create virtual key with combined rate limits', async ({ virtualKeysPage }) => {
      const vkData = createVirtualKeyWithRateLimit(SAMPLE_RATE_LIMITS.conservative, {
        name: `Combined Limits VK ${Date.now()}`,
      })
      
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
      
      await virtualKeysPage.createVirtualKey(vkData)
      
      const vkExists = await virtualKeysPage.virtualKeyExists(vkData.name)
      expect(vkExists).toBe(true)
    })
  })
})

test.describe('Virtual Key Management', () => {
  test.beforeEach(async ({ virtualKeysPage }) => {
    await virtualKeysPage.goto()
  })

  test('should edit virtual key name', async ({ virtualKeysPage }) => {
    // First create a virtual key
    const originalName = `Edit Test VK ${Date.now()}`
    const vkData = createVirtualKeyData({ name: originalName })
    
    await virtualKeysPage.createVirtualKey(vkData)
    
    // Now edit it
    const updatedName = `${originalName} Updated`
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
    
    // Delete it
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
    
    await virtualKeysPage.createVirtualKey(vkData)
    
    // Click to view details
    await virtualKeysPage.viewVirtualKey(vkName)
    
    // Detail sheet should be visible
    await expect(virtualKeysPage.page.locator('[role="dialog"]')).toBeVisible()
  })

  test('should copy virtual key value', async ({ virtualKeysPage }) => {
    const vkName = `Copy Value VK ${Date.now()}`
    const vkData = createVirtualKeyData({ name: vkName })
    
    await virtualKeysPage.createVirtualKey(vkData)
    
    // Copy the key value
    await virtualKeysPage.copyVirtualKeyValue(vkName)
    
    // Should show success toast
    // (Toast assertion is in the copyVirtualKeyValue method)
  })

  test('should toggle key visibility', async ({ virtualKeysPage }) => {
    const vkName = `Toggle Visibility VK ${Date.now()}`
    const vkData = createVirtualKeyData({ name: vkName })
    
    await virtualKeysPage.createVirtualKey(vkData)
    
    // Toggle visibility (show key)
    await virtualKeysPage.toggleKeyVisibility(vkName)
    
    // Toggle again (hide key)
    await virtualKeysPage.toggleKeyVisibility(vkName)
    
    // Virtual key should still exist
    const vkExists = await virtualKeysPage.virtualKeyExists(vkName)
    expect(vkExists).toBe(true)
  })
})

test.describe('Virtual Keys Table', () => {
  test.beforeEach(async ({ virtualKeysPage }) => {
    await virtualKeysPage.goto()
  })

  test('should display virtual keys table', async ({ virtualKeysPage }) => {
    await expect(virtualKeysPage.table).toBeVisible()
  })

  test('should show empty state when no virtual keys', async ({ virtualKeysPage }) => {
    // This test depends on the state of the database
    // If there are no virtual keys, empty state should show
    const count = await virtualKeysPage.getVirtualKeyCount()
    
    if (count === 0) {
      const emptyMessage = virtualKeysPage.page.getByText('No virtual keys found')
      await expect(emptyMessage).toBeVisible()
    } else {
      // If there are keys, the table should have rows
      expect(count).toBeGreaterThan(0)
    }
  })
})

test.describe('Form Validation', () => {
  test.beforeEach(async ({ virtualKeysPage }) => {
    await virtualKeysPage.goto()
  })

  test('should require name for virtual key', async ({ virtualKeysPage }) => {
    await virtualKeysPage.createBtn.click()
    await expect(virtualKeysPage.sheet).toBeVisible()
    
    // Try to save without name
    await virtualKeysPage.saveBtn.click()
    
    // Form should still be visible (not submitted)
    await expect(virtualKeysPage.sheet).toBeVisible()
  })

  test('should accept valid budget values', async ({ virtualKeysPage }) => {
    await virtualKeysPage.createBtn.click()
    await expect(virtualKeysPage.sheet).toBeVisible()
    
    // Fill name
    await virtualKeysPage.nameInput.fill(`Valid Budget Test ${Date.now()}`)
    
    // Fill budget
    const budgetInput = virtualKeysPage.page.locator('#budgetMaxLimit')
    await budgetInput.fill('100')
    
    // Should be valid (no error shown)
    const formError = virtualKeysPage.page.locator('[role="alert"]')
    const errorCount = await formError.count()
    expect(errorCount).toBe(0)
  })
})
