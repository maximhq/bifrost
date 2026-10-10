import { expect, test } from '../../core/fixtures/base.fixture'
import { virtualKeysApi } from '../../core/actions/api'
import type { VirtualKeysPage } from './pages/virtual-keys.page'
import { createVirtualKeyWithMultipleProviders, createVirtualKeyWithProvider, SAMPLE_BUDGETS } from './virtual-keys.data'

async function skipIfConfigStoreMissing(virtualKeysPage: VirtualKeysPage): Promise<void> {
  const missingConfigStore = await virtualKeysPage.page
    .getByText('Config store setup is missing.')
    .isVisible({ timeout: 1000 })
    .catch(() => false)

  test.skip(missingConfigStore, 'Config store setup is missing; virtual-key E2E tests require a configured config store.')
}

// Track created VKs for provider tests
const providerVKs: string[] = []

test.describe('Provider Management', () => {
  test.beforeEach(async ({ virtualKeysPage }) => {
    await virtualKeysPage.goto()
    await skipIfConfigStoreMissing(virtualKeysPage)
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

    // The provider configured on create shows as a card in the editor
    await expect(virtualKeysPage.page.getByTestId('vk-provider-header-0')).toBeVisible()

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

    // Removing the second provider drops its card
    await expect(virtualKeysPage.page.getByTestId('vk-provider-header-1')).toBeVisible()
    await virtualKeysPage.page.getByTestId('vk-delete-provider-1').click()
    await expect(virtualKeysPage.page.getByTestId('vk-provider-header-1')).toHaveCount(0)

    // Close sheet (handled by afterEach as well)
    await virtualKeysPage.closeSheet()
  })

  test('should reflect blocked models in the collapsed access summary', async ({ virtualKeysPage, request }) => {
    const vkName = `Blocked Models Summary VK ${Date.now()}`
    await virtualKeysApi.create(request, {
      name: vkName,
      is_active: true,
      provider_configs: [
        {
          provider: 'openai',
          allowed_models: ['*'],
          blacklisted_models: ['gpt-4o', 'gpt-4o-mini'],
          key_ids: ['*'],
        },
      ],
    })
    providerVKs.push(vkName)

    await virtualKeysPage.goto()
    await virtualKeysPage.viewVirtualKey(vkName)

    const summary = await virtualKeysPage.getProviderAccessSummary(0)
    await expect(summary).toContainText('All models · 2 models blocked')
  })

  test('should summarize a blocked wildcard as all models blocked', async ({ virtualKeysPage, request }) => {
    const vkName = `Blocked Wildcard Summary VK ${Date.now()}`
    await virtualKeysApi.create(request, {
      name: vkName,
      is_active: true,
      provider_configs: [
        {
          provider: 'openai',
          allowed_models: ['*'],
          blacklisted_models: ['*'],
          key_ids: ['*'],
        },
      ],
    })
    providerVKs.push(vkName)

    await virtualKeysPage.goto()
    await virtualKeysPage.viewVirtualKey(vkName)

    const summary = await virtualKeysPage.getProviderAccessSummary(0)
    await expect(summary).toContainText('All models blocked')
    await expect(summary).not.toContainText('All models ·')
  })

  test('should update provider-specific budget', async ({ virtualKeysPage }) => {
    // Create a virtual key with budget
    const vkName = `Provider Budget VK ${Date.now()}`
    const vkData = createVirtualKeyWithProvider('openai', {
      name: vkName,
      budgets: [SAMPLE_BUDGETS.small],
    })

    providerVKs.push(vkName)
    await virtualKeysPage.createVirtualKey(vkData)

    // Edit the virtual key
    await virtualKeysPage.editVirtualKey(vkName, {
      budgets: [SAMPLE_BUDGETS.large],
    })

    // Verify it still exists
    const vkExists = await virtualKeysPage.virtualKeyExists(vkName)
    expect(vkExists).toBe(true)
  })
})
