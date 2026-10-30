import type { Page, Request } from '@playwright/test'
import { expect, test } from '../../core/fixtures/base.fixture'

// The datasheet API is mocked so each case pins exactly what the editor shows
// and what it sends; the server side is covered by the Go tests and the
// provider-harness "datasheet override" folder.

const upstream = {
  model: 'e2e-datasheet-model',
  provider: 'openai',
  mode: 'chat',
  context_length: 128000,
  max_output_tokens: 16384,
  input_cost_per_token: 0.0000025,
  output_cost_per_token: 0.00001,
}

const savedOverride = {
  id: 'e2e-datasheet-override',
  provider: 'openai',
  model: 'e2e-datasheet-model',
  mode: 'chat',
  is_custom: false,
  pricing_patch: { context_length: 64000 },
  baseline: { pricing: { context_length: 100000 } },
  created_at: '2026-10-30T00:00:00Z',
  updated_at: '2026-10-30T00:00:00Z',
}

function entry(overridden: boolean) {
  return {
    provider: 'openai',
    model: 'e2e-datasheet-model',
    mode: 'chat',
    is_custom: false,
    upstream,
    effective: overridden ? { ...upstream, context_length: 64000 } : upstream,
    overrides: overridden ? [savedOverride] : [],
    drifted_fields: overridden ? ['pricing.context_length'] : [],
  }
}

async function mockDatasheet(page: Page, overridden: boolean): Promise<{ writes: Request[] }> {
  const writes: Request[] = []
  await page.route('**/api/models/datasheet?*', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ entries: [entry(overridden)], count: 1, total_count: 1, limit: 25, offset: 0 }),
    }),
  )
  await page.route('**/api/models/datasheet/entry*', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ ...entry(overridden), upstream_params: { provider: 'openai', supports_mcp: false }, effective_params: { provider: 'openai', supports_mcp: false } }),
    }),
  )
  await page.route('**/api/models/datasheet/overrides**', async (route) => {
    writes.push(route.request())
    const isDelete = route.request().method() === 'DELETE'
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(isDelete ? { message: 'deleted' } : { message: 'saved', override: savedOverride }),
    })
  })
  return { writes }
}

test.describe('Datasheet editor', () => {
  test('shows the edited value over the upstream one and flags upstream drift', async ({ page, datasheetPage }) => {
    await mockDatasheet(page, true)
    await datasheetPage.goto()

    const row = datasheetPage.row('openai', 'e2e-datasheet-model', 'chat')
    await expect(row).toBeVisible()
    await expect(page.getByTestId('datasheet-overridden-badge-openai-e2e-datasheet-model-chat')).toBeVisible()
    await expect(page.getByTestId('datasheet-drift-badge-openai-e2e-datasheet-model-chat')).toBeVisible()
    await expect(page.getByTestId('datasheet-context-openai-e2e-datasheet-model-chat')).toContainText('64,000')

    await datasheetPage.openEntry('openai', 'e2e-datasheet-model', 'chat')
    await expect(datasheetPage.limitInput('context_length')).toHaveValue('64000')
    await expect(page.getByTestId('datasheet-drift-pricing-context_length')).toBeVisible()
  })

  test('saving an edited limit sends only that key', async ({ page, datasheetPage }) => {
    const { writes } = await mockDatasheet(page, false)
    await datasheetPage.goto()
    await datasheetPage.openEntry('openai', 'e2e-datasheet-model', 'chat')

    await datasheetPage.limitInput('context_length').fill('64000')
    await datasheetPage.saveBtn.click()

    await expect.poll(() => writes.length).toBe(1)
    expect(writes[0].method()).toBe('PUT')
    expect(writes[0].postDataJSON()).toEqual({
      provider: 'openai',
      model: 'e2e-datasheet-model',
      mode: 'chat',
      pricing_patch: { context_length: 64000 },
      params_patch: {},
    })
    await expect(datasheetPage.sheet).toBeHidden()
  })

  test('an invalid limit is reported inline and nothing is sent', async ({ page, datasheetPage }) => {
    const { writes } = await mockDatasheet(page, false)
    await datasheetPage.goto()
    await datasheetPage.openEntry('openai', 'e2e-datasheet-model', 'chat')

    await datasheetPage.limitInput('context_length').fill('1.5')
    await datasheetPage.saveBtn.click()

    await expect(datasheetPage.sheet).toContainText('Must be a positive whole number')
    expect(writes).toHaveLength(0)
  })

  test('reset to datasheet deletes the override', async ({ page, datasheetPage }) => {
    const { writes } = await mockDatasheet(page, true)
    await datasheetPage.goto()
    await datasheetPage.openEntry('openai', 'e2e-datasheet-model', 'chat')

    await datasheetPage.resetAllBtn.click()

    await expect.poll(() => writes.length).toBe(1)
    expect(writes[0].method()).toBe('DELETE')
    expect(writes[0].url()).toContain('/api/models/datasheet/overrides/e2e-datasheet-override')
  })

  test('adding a custom model sends is_custom with the entered values', async ({ page, datasheetPage }) => {
    const { writes } = await mockDatasheet(page, false)
    await datasheetPage.goto()
    await datasheetPage.addCustomBtn.click()
    await datasheetPage.sheet.waitFor({ state: 'visible' })

    await datasheetPage.chooseCustomProvider('openai')
    await page.getByTestId('datasheet-custom-model-input').fill('ft:gpt-4o:e2e')
    await datasheetPage.limitInput('max_output_tokens').fill('2048')
    await datasheetPage.saveBtn.click()

    await expect.poll(() => writes.length).toBe(1)
    expect(writes[0].postDataJSON()).toEqual({
      provider: 'openai',
      model: 'ft:gpt-4o:e2e',
      mode: 'chat',
      is_custom: true,
      pricing_patch: { max_output_tokens: 2048 },
      params_patch: { max_output_tokens: 2048 },
    })
  })
})
