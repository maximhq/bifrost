import type { APIRequestContext, Page } from '@playwright/test'
import { expect, test } from '../../core/fixtures/base.fixture'
import { waitForNetworkIdle } from '../../core/utils/test-helpers'

// gpt-4o-mini is in the bundled pricing datasheet, so it is listed for openai without a live key.
const PROVIDER = 'openai'
const MODEL = 'gpt-4o-mini'
const ROW_KEY = 'gpt-4o-mini-openai'

type ModelDetailsResponse = { models: Array<{ name: string; provider: string; tags?: string[] }>; total: number }

async function modelTags(request: APIRequestContext): Promise<string[]> {
  const response = await request.get(`/api/models/details?provider=${PROVIDER}&query=${MODEL}&unfiltered=true&limit=50`)
  expect(response.ok()).toBeTruthy()
  const body = (await response.json()) as ModelDetailsResponse
  return body.models.find((m) => m.name === MODEL)?.tags ?? []
}

// MODEL is a shared bundled model, so the tests restore whatever tags it carried before they ran
// instead of clearing it.
async function setModelTags(request: APIRequestContext, tags: string[]): Promise<void> {
  const response = await request.put('/api/models/tags', { data: [{ provider: PROVIDER, model: MODEL, tags }] })
  expect(response.status()).toBe(204)
}

async function openModelSheet(page: Page): Promise<void> {
  await page.goto(`/workspace/model-catalog?tab=attributes&provider=${PROVIDER}&search=${MODEL}`)
  await waitForNetworkIdle(page)
  await page.getByTestId(`model-catalog-edit-${ROW_KEY}`).click()
  await expect(page.getByTestId('model-catalog-attribute-sheet')).toBeVisible()
}

test.describe('Model Catalog Tags', () => {
  test.describe.configure({ mode: 'serial' })

  test('should tag a model, filter the models list by tag and remove the tag', async ({ page, request }) => {
    const tag = `e2e-model-${Date.now()}`
    const originalTags = await modelTags(request)
    try {
      await openModelSheet(page)
      const tagsInput = page.getByTestId('model-catalog-tags-input')
      await tagsInput.fill(tag)
      await tagsInput.press('Enter')
      await page.getByTestId('model-catalog-attribute-submit').click()
      await expect(page.getByTestId('model-catalog-attribute-sheet')).toBeHidden()
      expect(await modelTags(request)).toEqual([...originalTags, tag].sort())

      // The row shows the tag, and the tags filter keeps only models carrying it. The cell shows
      // two tags and folds the rest into a "+N" button, so the new tag is either a visible badge
      // or listed in that button's accessible name.
      const tagsCell = page.getByTestId(`model-catalog-tags-${ROW_KEY}`)
      await expect(
        tagsCell.getByText(tag, { exact: true }).or(tagsCell.getByRole('button', { name: tag })),
      ).toBeVisible()
      await page.goto(`/workspace/model-catalog?tab=attributes&tags=${tag}`)
      await waitForNetworkIdle(page)
      const rows = page.getByTestId('model-catalog-attributes-table').locator('tbody tr')
      await expect(rows).toHaveCount(1)
      await expect(page.getByTestId(`model-catalog-row-${ROW_KEY}`)).toBeVisible()

      // Removing the tag in the sheet leaves the model's original tags.
      await openModelSheet(page)
      await page.getByTestId('model-catalog-attribute-sheet').getByRole('button', { name: `Remove ${tag}` }).click()
      await page.getByTestId('model-catalog-attribute-submit').click()
      await expect(page.getByTestId('model-catalog-attribute-sheet')).toBeHidden()
      expect(await modelTags(request)).toEqual(originalTags)
    } finally {
      await setModelTags(request, originalTags)
    }
  })

  test('should reject an invalid model tag before saving', async ({ page, request }) => {
    const originalTags = await modelTags(request)
    await openModelSheet(page)
    const tagsInput = page.getByTestId('model-catalog-tags-input')
    await tagsInput.fill('bad tag')
    await tagsInput.press('Enter')
    await expect(page.getByTestId('model-catalog-tags-error')).toContainText('Invalid tag "bad tag"')
    await expect(page.getByTestId('model-catalog-attribute-submit')).toBeDisabled()
    expect(await modelTags(request)).toEqual(originalTags)
  })
})
