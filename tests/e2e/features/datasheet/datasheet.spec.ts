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

function entry(overridden: boolean, override = savedOverride, model = 'e2e-datasheet-model') {
  return {
    provider: 'openai',
    model,
    mode: 'chat',
    is_custom: false,
    upstream: { ...upstream, model },
    effective: overridden ? { ...upstream, model, context_length: 64000 } : { ...upstream, model },
    overrides: overridden ? [override] : [],
    drifted_fields: overridden ? ['pricing.context_length'] : [],
  }
}

type MockOptions = {
  override?: typeof savedOverride
  upstreamParams?: Record<string, unknown>
  model?: string
}

async function mockDatasheet(page: Page, overridden: boolean, opts: MockOptions = {}): Promise<{ writes: Request[] }> {
  const writes: Request[] = []
  const row = entry(overridden, opts.override, opts.model)
  const params = opts.upstreamParams ?? { provider: 'openai', supports_mcp: false }
  await page.route('**/api/models/datasheet?*', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ entries: [row], count: 1, total_count: 1, limit: 25, offset: 0 }),
    }),
  )
  await page.route('**/api/models/datasheet/entry*', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ ...row, upstream_params: params, effective_params: params }),
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

  test('pricing errors clear when correcting or removing a field', async ({ page, datasheetPage }) => {
    const { writes } = await mockDatasheet(page, false)
    await datasheetPage.goto()
    await datasheetPage.openEntry('openai', 'e2e-datasheet-model', 'chat')

    const key = 'input_cost_per_token'
    const input = page.getByTestId(`pricing-override-field-input-${key}`)
    await page.getByTestId(`pricing-field-activate-${key}`).click()
    await input.fill('-1')
    await datasheetPage.saveBtn.click()
    const error = datasheetPage.sheet.getByText('Must be >= 0', { exact: true })
    await expect(error).toBeVisible()
    expect(writes).toHaveLength(0)

    await input.fill('2')
    await expect(error).toBeHidden()
    expect(writes).toHaveLength(0)

    await input.fill('-1')
    await datasheetPage.saveBtn.click()
    await expect(error).toBeVisible()
    await page.getByTestId(`pricing-field-deactivate-${key}`).click()
    await page.getByTestId(`pricing-field-activate-${key}`).click()
    await expect(error).toBeHidden()
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

  test('the limit reset control shows only for a typed value or a saved cleared limit', async ({ page, datasheetPage }) => {
    const { writes } = await mockDatasheet(page, true, {
      override: { ...savedOverride, pricing_patch: { context_length: null, max_input_tokens: 1000 } },
    })
    await datasheetPage.goto()
    await datasheetPage.openEntry('openai', 'e2e-datasheet-model', 'chat')

    // Empty and never saved: nothing to reset.
    await expect(datasheetPage.limitInput('max_output_tokens')).toHaveValue('')
    await expect(datasheetPage.limitReset('max_output_tokens')).toHaveCount(0)
    // "0" is a typed value, so it can be reset even though it is falsy as a number.
    await datasheetPage.limitInput('max_output_tokens').fill('0')
    await expect(datasheetPage.limitReset('max_output_tokens')).toBeVisible()
    await datasheetPage.limitReset('max_output_tokens').click()
    await expect(datasheetPage.limitInput('max_output_tokens')).toHaveValue('')
    await expect(datasheetPage.limitReset('max_output_tokens')).toHaveCount(0)

    // A saved null shows an empty input, but the override still clears the limit.
    await expect(datasheetPage.limitInput('context_length')).toHaveValue('')
    await expect(datasheetPage.limitReset('context_length')).toBeVisible()
    await datasheetPage.limitInput('context_length').fill('32000')
    await datasheetPage.limitReset('context_length').click()
    await expect(datasheetPage.limitInput('context_length')).toHaveValue('')
    await expect(datasheetPage.limitReset('context_length')).toHaveCount(0)

    await datasheetPage.saveBtn.click()
    await expect.poll(() => writes.length).toBe(1)
    // Both the typed value and the saved null are gone; only the untouched limit is sent.
    expect(writes[0].postDataJSON().pricing_patch).toEqual({ max_input_tokens: 1000 })
  })

  test('capabilities with no upstream value follow the datasheet without a value', async ({ page, datasheetPage }) => {
    await mockDatasheet(page, false, {
      upstreamParams: { provider: 'openai', supports_function_calling: null, supports_tool_choice: false },
    })
    await datasheetPage.goto()
    await datasheetPage.openEntry('openai', 'e2e-datasheet-model', 'chat')

    // undefined (key absent) and null both read as no value.
    await expect(datasheetPage.flagSelect('supports_reasoning')).toHaveText('Follow datasheet')
    await expect(datasheetPage.flagSelect('supports_function_calling')).toHaveText('Follow datasheet')
    // false is a real value and is shown.
    await expect(datasheetPage.flagSelect('supports_tool_choice')).toHaveText('Follow datasheet (false)')
  })

  test('a truncated model name shows its full value in a tooltip', async ({ page, datasheetPage }) => {
    const model = `e2e-datasheet-${'very-long-model-name-'.repeat(8)}end`
    await mockDatasheet(page, false, { model })
    await datasheetPage.goto()

    const label = datasheetPage.row('openai', model, 'chat').getByText(model, { exact: true })
    await expect(label).toBeVisible()
    await label.hover()
    await expect(page.getByRole('tooltip')).toHaveText(model)
  })

  test('the edit action stays pinned while the table scrolls sideways', async ({ page, datasheetPage }) => {
    await page.setViewportSize({ width: 900, height: 800 })
    await mockDatasheet(page, false)
    await datasheetPage.goto()

    const edit = datasheetPage.editBtn('openai', 'e2e-datasheet-model', 'chat')
    await expect(edit).toBeVisible()
    const scroller = datasheetPage.table.locator('[data-slot="table-container"]')
    const canScroll = await scroller.evaluate((el) => el.scrollWidth > el.clientWidth)
    expect(canScroll).toBe(true)

    const atStart = await edit.boundingBox()
    await scroller.evaluate((el) => {
      el.scrollLeft = el.scrollWidth
    })
    const atEnd = await edit.boundingBox()
    expect(atStart).not.toBeNull()
    expect(atEnd?.x).toBe(atStart?.x)
    await edit.click()
    await expect(datasheetPage.sheet).toBeVisible()
  })

  test('filters and the add button stay reachable on a narrow screen', async ({ page, datasheetPage }) => {
    await page.setViewportSize({ width: 390, height: 800 })
    await mockDatasheet(page, false)
    await datasheetPage.goto()

    for (const testId of ['datasheet-search-input', 'datasheet-provider-filter', 'datasheet-overridden-only-switch', 'datasheet-add-custom-btn']) {
      const control = page.getByTestId(testId)
      await expect(control).toBeInViewport()
      const box = await control.boundingBox()
      expect(box && box.x >= 0 && box.x + box.width <= 390, `${testId} fits in the viewport`).toBe(true)
    }
    await datasheetPage.addCustomBtn.click()
    await expect(datasheetPage.sheet).toBeVisible()
  })
})
