import { expect, test } from '../../core/fixtures/base.fixture'
import { coreConfigApi, virtualKeysApi } from '../../core/actions/api'
import type { VirtualKeysPage } from './pages/virtual-keys.page'
import {
    createVirtualKeyData,
    createVirtualKeyWithBudget,
    createVirtualKeyWithMultipleProviders,
    createVirtualKeyWithProvider,
    createVirtualKeyWithRateLimit,
    SAMPLE_BUDGETS,
    SAMPLE_RATE_LIMITS,
} from './virtual-keys.data'

type VirtualKeyApiResponse = {
  virtual_key: {
    id: string
    name: string
    value: string
  }
}

type VirtualKeyListApiResponse = {
  virtual_keys: Array<{
    id: string
    name: string
    expires_at?: string | null
    delete_after_expire?: boolean
  }>
}

async function findVirtualKeyByName(request: Parameters<typeof virtualKeysApi.getAll>[0], name: string) {
  const list = (await virtualKeysApi.getAll(request)) as VirtualKeyListApiResponse
  const vk = list.virtual_keys.find((candidate) => candidate.name === name)
  expect(vk, `virtual key ${name} should exist`).toBeDefined()
  return vk!
}

async function skipIfConfigStoreMissing(virtualKeysPage: VirtualKeysPage): Promise<void> {
  const missingConfigStore = await virtualKeysPage.page
    .getByText('Config store setup is missing.')
    .isVisible({ timeout: 1000 })
    .catch(() => false)

  test.skip(missingConfigStore, 'Config store setup is missing; virtual-key E2E tests require a configured config store.')
}

// Track created VKs for cleanup
const createdVKs: string[] = []

test.describe('Virtual Keys', () => {
  test.beforeEach(async ({ virtualKeysPage }) => {
    await virtualKeysPage.goto()
    await skipIfConfigStoreMissing(virtualKeysPage)
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
    test('should create virtual key with daily budget', async ({ virtualKeysPage }) => {
      const vkData = createVirtualKeyWithBudget([SAMPLE_BUDGETS.daily], {
        name: `Daily Budget VK ${Date.now()}`,
      })

      createdVKs.push(vkData.name)
      await virtualKeysPage.createVirtualKey(vkData)

      const vkExists = await virtualKeysPage.virtualKeyExists(vkData.name)
      expect(vkExists).toBe(true)

      // Verify budget was saved correctly
      await virtualKeysPage.viewVirtualKey(vkData.name)
      await virtualKeysPage.waitForSheetAnimation()
      const amountInput = virtualKeysPage.page.getByTestId('vk-budget-lines-amount-0')
      await expect(amountInput).toHaveValue(String(SAMPLE_BUDGETS.daily.maxLimit))
      await virtualKeysPage.closeSheet()
    })

    test('should create virtual key with every-minute budget', async ({ virtualKeysPage }) => {
      const vkData = createVirtualKeyWithBudget([SAMPLE_BUDGETS.everyMinute], {
        name: `Minute Budget VK ${Date.now()}`,
      })

      createdVKs.push(vkData.name)
      await virtualKeysPage.createVirtualKey(vkData)

      const vkExists = await virtualKeysPage.virtualKeyExists(vkData.name)
      expect(vkExists).toBe(true)

      // Verify budget was saved correctly
      await virtualKeysPage.viewVirtualKey(vkData.name)
      await virtualKeysPage.waitForSheetAnimation()
      const amountInput = virtualKeysPage.page.getByTestId('vk-budget-lines-amount-0')
      await expect(amountInput).toHaveValue(String(SAMPLE_BUDGETS.everyMinute.maxLimit))
      await virtualKeysPage.closeSheet()
    })

    test('should create virtual key with multiple budgets', async ({ virtualKeysPage }) => {
      const vkData = createVirtualKeyWithBudget(
        [SAMPLE_BUDGETS.daily, SAMPLE_BUDGETS.everyMinute],
        { name: `Multi Budget VK ${Date.now()}` }
      )

      createdVKs.push(vkData.name)
      await virtualKeysPage.createVirtualKey(vkData)

      const vkExists = await virtualKeysPage.virtualKeyExists(vkData.name)
      expect(vkExists).toBe(true)

      await virtualKeysPage.viewVirtualKey(vkData.name)
      await virtualKeysPage.waitForSheetAnimation()
      await expect(virtualKeysPage.page.getByTestId('vk-budget-lines-amount-0')).toHaveValue(String(SAMPLE_BUDGETS.daily.maxLimit))
      await expect(virtualKeysPage.page.getByTestId('vk-budget-lines-amount-1')).toHaveValue(String(SAMPLE_BUDGETS.everyMinute.maxLimit))
      await virtualKeysPage.closeSheet()
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
        budgets: [SAMPLE_BUDGETS.medium],
        rateLimit: SAMPLE_RATE_LIMITS.moderate,
      })

      createdVKs.push(vkData.name)
      await virtualKeysPage.createVirtualKey(vkData)

      const vkExists = await virtualKeysPage.virtualKeyExists(vkData.name)
      expect(vkExists).toBe(true)
    })
  })

  test.describe('Virtual Key Expiry', () => {
    test('should hide the delete-after-expire checkbox until an expiry is picked', async ({ virtualKeysPage }) => {
      await virtualKeysPage.createBtn.click()
      await expect(virtualKeysPage.sheet).toBeVisible()

      await expect(virtualKeysPage.deleteAfterExpireCheckbox).toHaveCount(0)
      await virtualKeysPage.page.getByTestId('vk-expiry-preset-24-hours').click()
      await expect(virtualKeysPage.deleteAfterExpireCheckbox).toBeVisible()
      await virtualKeysPage.page.getByTestId('vk-expiry-never').click()
      await expect(virtualKeysPage.deleteAfterExpireCheckbox).toHaveCount(0)
    })

    test('should persist delete-after-expire with the expiry and clear it with the expiry', async ({
      virtualKeysPage,
      request,
    }) => {
      // With the client default on, a ticked box matches it and stores no override,
      // so pin the default off for the explicit true assertions below.
      const initialDefault = (await coreConfigApi.get(request)).client_config.delete_expired_virtual_keys ?? false
      await coreConfigApi.updateClientConfig(request, { delete_expired_virtual_keys: false })
      try {
        // Reload so the sheet reads the pinned default.
        await virtualKeysPage.goto()

        const vkData = createVirtualKeyData({
          name: `Auto Delete VK ${Date.now()}`,
          expiryPreset: '24 hours',
          deleteAfterExpire: true,
        })

        createdVKs.push(vkData.name)
        await virtualKeysPage.createVirtualKey(vkData)

        const created = await findVirtualKeyByName(request, vkData.name)
        expect(created.expires_at).toBeTruthy()
        expect(created.delete_after_expire).toBe(true)

        // Unticking the box keeps the expiry but drops the flag.
        await virtualKeysPage.editVirtualKey(vkData.name, { deleteAfterExpire: false })
        const unticked = await findVirtualKeyByName(request, vkData.name)
        expect(unticked.expires_at).toBeTruthy()
        expect(unticked.delete_after_expire ?? false).toBe(false)

        // Re-tick, then clearing the expiry resets the flag server-side.
        await virtualKeysPage.editVirtualKey(vkData.name, { deleteAfterExpire: true })
        expect((await findVirtualKeyByName(request, vkData.name)).delete_after_expire).toBe(true)

        await virtualKeysPage.editVirtualKey(vkData.name, { expiryPreset: 'Never' })
        const cleared = await findVirtualKeyByName(request, vkData.name)
        expect(cleared.expires_at ?? null).toBeNull()
        expect(cleared.delete_after_expire ?? false).toBe(false)
      } finally {
        await coreConfigApi.updateClientConfig(request, { delete_expired_virtual_keys: initialDefault })
      }
    })

    test('should reject delete_after_expire without an expiry through the API', async ({ request }) => {
      // Tracked so a regression that accepts the request doesn't leave the key behind.
      const name = `API No Expiry VK ${Date.now()}`
      createdVKs.push(name)
      const response = await request.post('/api/governance/virtual-keys', {
        data: {
          name,
          delete_after_expire: true,
        },
      })
      expect(response.status()).toBe(400)
      expect(await response.text()).toContain('delete_after_expire requires expires_at')
    })

    test('should follow the client default and store only explicit overrides', async ({ virtualKeysPage, request }) => {
      const initialDefault = (await coreConfigApi.get(request)).client_config.delete_expired_virtual_keys ?? false
      await coreConfigApi.updateClientConfig(request, { delete_expired_virtual_keys: true })
      try {
        // Reload so the sheet reads the new default.
        await virtualKeysPage.goto()

        // With the default on, a fresh key with an expiry starts with the switch on.
        await virtualKeysPage.createBtn.click()
        await expect(virtualKeysPage.sheet).toBeVisible()
        await virtualKeysPage.page.getByTestId('vk-expiry-preset-24-hours').click()
        await expect(virtualKeysPage.deleteAfterExpireCheckbox).toHaveAttribute('data-state', 'checked')
        await virtualKeysPage.cancelBtn.click()

        // Leaving the switch at the default stores no per-key value: the key inherits.
        const inherits = createVirtualKeyData({ name: `Inherit Delete VK ${Date.now()}`, expiryPreset: '24 hours' })
        createdVKs.push(inherits.name)
        await virtualKeysPage.createVirtualKey(inherits)
        const inheritsVK = await findVirtualKeyByName(request, inherits.name)
        expect(inheritsVK.expires_at).toBeTruthy()
        expect(inheritsVK.delete_after_expire ?? null).toBeNull()

        // Turning the switch off against the default stores an explicit false.
        await virtualKeysPage.editVirtualKey(inherits.name, { deleteAfterExpire: false })
        expect((await findVirtualKeyByName(request, inherits.name)).delete_after_expire).toBe(false)

        // Turning it back on matches the default again, so the override is cleared.
        await virtualKeysPage.editVirtualKey(inherits.name, { deleteAfterExpire: true })
        expect((await findVirtualKeyByName(request, inherits.name)).delete_after_expire ?? null).toBeNull()
      } finally {
        await coreConfigApi.updateClientConfig(request, { delete_expired_virtual_keys: initialDefault })
      }
    })

    test('should accept null to reset delete_after_expire to inherit through the API', async ({ request }) => {
      const expiresAt = new Date(Date.now() + 60 * 60 * 1000).toISOString()
      const name = `API Inherit Reset VK ${Date.now()}`
      const created = (await virtualKeysApi.create(request, {
        name,
        expires_at: expiresAt,
        delete_after_expire: false,
      })) as VirtualKeyApiResponse
      createdVKs.push(name)
      expect((await findVirtualKeyByName(request, name)).delete_after_expire).toBe(false)

      await virtualKeysApi.update(request, created.virtual_key.id, { delete_after_expire: null })
      expect((await findVirtualKeyByName(request, name)).delete_after_expire ?? null).toBeNull()

      const response = await request.put(`/api/governance/virtual-keys/${created.virtual_key.id}`, {
        data: { expires_at: '', delete_after_expire: true },
      })
      expect(response.status()).toBe(400)
      expect(await response.text()).toContain('delete_after_expire requires expires_at')
    })
  })
})
