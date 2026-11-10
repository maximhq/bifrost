import { expect, test } from '../../core/fixtures/base.fixture'
import { createModelLimitData } from './model-limits.data'
import { expectListLoadError } from '../../core/utils/test-helpers'

const createdLimits: { modelName: string; provider: string }[] = []

test.describe('Model Limits', () => {
  test.beforeEach(async ({ modelLimitsPage }) => {
    await modelLimitsPage.goto()
  })

  test.afterEach(async ({ modelLimitsPage }) => {
    await modelLimitsPage.closeSheet()
    for (const { modelName, provider } of [...createdLimits]) {
      try {
        const exists = await modelLimitsPage.modelLimitExists(modelName, provider)
        if (exists) {
          await modelLimitsPage.deleteModelLimit(modelName, provider)
        }
      } catch (e) {
        console.error(`[CLEANUP] Failed to delete model limit ${modelName}:`, e)
      }
    }
    createdLimits.length = 0
  })

  test('should display create button or empty state', async ({ modelLimitsPage }) => {
    const createVisible = await modelLimitsPage.createBtn.isVisible().catch(() => false)
    expect(createVisible).toBe(true)
  })

  test('should show the load error instead of the empty state when model limits fail to load', async ({ page }) => {
    await expectListLoadError(page, '/api/governance/model-configs', 'model-limits-load-error', 'model-limits-button-read-more')
  })

  test('should create a model limit with budget and rate limit', async ({ modelLimitsPage }) => {
    const limitData = createModelLimitData({
      provider: 'openai',
      budget: { maxLimit: 5, resetDuration: '1M' },
      rateLimit: { tokenMaxLimit: 500, requestMaxLimit: 20 },
    })

    const modelName = await modelLimitsPage.createModelLimit(limitData)
    createdLimits.push({ modelName, provider: limitData.provider })

    const exists = await modelLimitsPage.modelLimitExists(modelName, limitData.provider)
    expect(exists).toBe(true)
  })

  // The Models page offers Shared and Model on every tier, never MCP. Each case takes the deployment's
  // limit for its tier out of the way and puts it back afterwards.
  for (const tier of [
    { name: 'all models on all providers', modelName: '*', provider: 'all' },
    { name: 'all models on one provider', modelName: '*', provider: 'openai' },
    { name: 'one model on one provider', modelName: 'gpt-4o-mini', provider: 'openai' },
  ]) {
    test(`saves Model-scoped budget and request lines on ${tier.name}`, async ({ modelLimitsPage }) => {
      const page = modelLimitsPage.page
      const sheet = modelLimitsPage.sheet
      const saved = await modelLimitsPage.takeGlobalModelConfig(tier.modelName, tier.provider)
      try {
        await modelLimitsPage.goto()
        await modelLimitsPage.openCreateSheet(tier.modelName, tier.provider)
        if (tier.modelName === '*') {
          // A new limit starts on All Models, and picking a provider keeps it.
          await expect(sheet.getByTestId('model-limit-model-select')).toContainText('All Models')
        }

        await sheet.getByTestId('model-limit-budget-lines-add-btn').click()
        await sheet.getByTestId('model-limit-budget-lines-amount-0').fill('7')
        await expect(sheet.getByTestId('model-limit-budget-lines-scope-0-trigger')).toHaveText('Shared')
        await sheet.getByTestId('model-limit-budget-lines-scope-0-trigger').click()
        await expect(page.getByTestId('model-limit-budget-lines-scope-0-option-shared')).toBeVisible()
        await expect(page.getByTestId('model-limit-budget-lines-scope-0-option-mcp')).toHaveCount(0)
        await page.getByTestId('model-limit-budget-lines-scope-0-option-model').click()

        await sheet.getByTestId('model-limit-request-limit-lines-add-btn').click()
        await sheet.getByTestId('model-limit-request-limit-lines-amount-0').fill('11')
        await sheet.getByTestId('model-limit-request-limit-lines-scope-0-trigger').click()
        await expect(page.getByTestId('model-limit-request-limit-lines-scope-0-option-shared')).toBeVisible()
        await expect(page.getByTestId('model-limit-request-limit-lines-scope-0-option-mcp')).toHaveCount(0)
        await page.getByTestId('model-limit-request-limit-lines-scope-0-option-model').click()
        await expect(sheet.getByTestId('model-limit-token-limit-info')).toBeVisible()

        await sheet.getByTestId('model-limit-button-submit').click()
        await expect(sheet).not.toBeVisible({ timeout: 10000 })

        const stored = await modelLimitsPage.getGlobalModelConfig(tier.modelName, tier.provider)
        expect(stored, 'the limit is saved on its tier').toBeTruthy()
        expect((stored?.budgets ?? []).map((b) => `${b.scope ?? 'shared'}:${b.max_limit}`)).toEqual(['model:7'])
        expect((stored?.request_limits ?? []).map((r) => `${r.scope ?? 'shared'}:${r.request_max_limit}`)).toEqual(['model:11'])

        // Reopened, the lines keep their scope.
        await modelLimitsPage.openEditSheet(tier.modelName, tier.provider)
        await expect(sheet.getByTestId('model-limit-budget-lines-scope-0-trigger')).toHaveText('Model')
        await expect(sheet.getByTestId('model-limit-request-limit-lines-scope-0-trigger')).toHaveText('Model')
        await sheet.getByRole('button', { name: /Cancel/i }).click()
        await expect(sheet).not.toBeVisible({ timeout: 5000 })
      } finally {
        await modelLimitsPage.restoreGlobalModelConfig(tier.modelName, tier.provider, saved)
      }
    })
  }

  test('should edit a model limit budget and rate limit', async ({ modelLimitsPage }) => {
    const limitData = createModelLimitData({ provider: 'openai' })

    const modelName = await modelLimitsPage.createModelLimit(limitData)
    createdLimits.push({ modelName, provider: limitData.provider })

    await modelLimitsPage.editModelLimit(modelName, limitData.provider, {
      budget: { maxLimit: 20 },
      rateLimit: { tokenMaxLimit: 2000, requestMaxLimit: 100 },
    })

    const exists = await modelLimitsPage.modelLimitExists(modelName, limitData.provider)
    expect(exists).toBe(true)
  })

  test('should delete a model limit', async ({ modelLimitsPage }) => {
    const limitData = createModelLimitData({
      provider: 'openai',
      budget: { maxLimit: 5 },
    })

    const modelName = await modelLimitsPage.createModelLimit(limitData)
    createdLimits.push({ modelName, provider: limitData.provider })

    let exists = await modelLimitsPage.modelLimitExists(modelName, limitData.provider)
    expect(exists).toBe(true)

    await modelLimitsPage.deleteModelLimit(modelName, limitData.provider)
    const idx = createdLimits.findIndex(
      (limit) => limit.modelName === modelName && limit.provider === limitData.provider
    )
    if (idx >= 0) createdLimits.splice(idx, 1)

    exists = await modelLimitsPage.modelLimitExists(modelName, limitData.provider)
    expect(exists).toBe(false)
  })
})
