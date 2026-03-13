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
