import { expect, test } from '../../core/fixtures/base.fixture'
import { createModelLimitData } from './model-limits.data'

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

  test('should create a model limit with budget', async ({ modelLimitsPage }) => {
    const limitData = createModelLimitData({
      modelName: 'gpt-4o-mini',
      provider: 'openai',
      budget: { maxLimit: 5, resetDuration: '1M' },
    })

    createdLimits.push({ modelName: limitData.modelName, provider: limitData.provider })

    await modelLimitsPage.createModelLimit(limitData)

    const exists = await modelLimitsPage.modelLimitExists(limitData.modelName, limitData.provider)
    expect(exists).toBe(true)
  })

  test('should edit a model limit', async ({ modelLimitsPage }) => {
    const limitData = createModelLimitData({
      modelName: 'gpt-4o-mini',
      provider: 'openai',
      budget: { maxLimit: 5 },
    })

    createdLimits.push({ modelName: limitData.modelName, provider: limitData.provider })
    await modelLimitsPage.createModelLimit(limitData)

    await modelLimitsPage.editModelLimit(limitData.modelName, limitData.provider, {
      budget: { maxLimit: 10 },
    })

    const exists = await modelLimitsPage.modelLimitExists(limitData.modelName, limitData.provider)
    expect(exists).toBe(true)
  })

  test('should delete a model limit', async ({ modelLimitsPage }) => {
    const limitData = createModelLimitData({
      modelName: 'gpt-4o-mini',
      provider: 'openai',
      budget: { maxLimit: 5 },
    })

    await modelLimitsPage.createModelLimit(limitData)

    let exists = await modelLimitsPage.modelLimitExists(limitData.modelName, limitData.provider)
    expect(exists).toBe(true)

    await modelLimitsPage.deleteModelLimit(limitData.modelName, limitData.provider)

    exists = await modelLimitsPage.modelLimitExists(limitData.modelName, limitData.provider)
    expect(exists).toBe(false)
  })
})
