import { expect, test } from '../../core/fixtures/base.fixture'
import { expectListLoadError } from '../../core/utils/test-helpers'
import { DatasetsPage } from './pages/datasets.page'

const createdIds: string[] = []
const uniqueName = (label: string) => `e2e ${label} ${Date.now()}-${Math.floor(Math.random() * 1e6)}`

const CSV = 'input,expected_output,persona\n"hello, world",hi,admin\nsecond question,second answer,guest\n'

test.describe('Datasets', () => {
  test.afterEach(async ({ request }) => {
    for (const id of [...createdIds]) {
      try {
        await DatasetsPage.deleteViaApi(request, id)
      } catch (e) {
        console.error(`[CLEANUP] Failed to delete dataset ${id}:`, e)
      }
    }
    createdIds.length = 0
  })

  test('should show the load error instead of the empty state when datasets fail to load', async ({ datasetsPage, page }) => {
    await datasetsPage.goto()
    await expectListLoadError(page, '/api/evals/datasets', 'datasets-load-error', 'datasets-empty-state')
  })

  test('should create a dataset from a template and list it in the sidebar', async ({ datasetsPage, page, request }) => {
    const name = uniqueName('template')
    const id = await datasetsPage.create(name, 'qa')
    createdIds.push(id)

    const dataset = await DatasetsPage.getViaApi(request, id)
    expect(dataset.columns.map((column: { name: string }) => column.name)).toEqual(['input', 'expected_output'])
    await expect(datasetsPage.grid).toBeVisible()

    // Opening the screen selects nothing. The sidebar finds the dataset and opens it.
    await datasetsPage.goto()
    await expect(page.getByTestId('datasets-none-selected')).toBeVisible()
    await page.getByTestId('datasets-search-input').fill(name)
    await page.getByTestId(`dataset-row-${name}`).click()
    await expect(page.getByTestId('dataset-detail-page')).toBeVisible()
    expect(datasetsPage.datasetId()).toBe(id)
  })

  test('should file a dataset under a folder and keep it when the folder is deleted', async ({ datasetsPage, page, request }) => {
    const name = uniqueName('filed')
    const folder = uniqueName('folder')
    const id = await datasetsPage.create(name, 'qa')
    createdIds.push(id)

    await page.getByTestId('datasets-add-menu').click()
    await page.getByTestId('datasets-create-folder-btn').click()
    await page.getByTestId('dataset-folder-name-input').fill(folder)
    await page.getByTestId('dataset-folder-submit-btn').click()
    await expect(page.getByTestId(`dataset-folder-${folder}`)).toBeVisible()

    const folders = await (await request.get('/api/evals/datasets/folders')).json()
    const folderId = folders.folders.find((item: { name: string }) => item.name === folder).id
    const moved = await request.put(`/api/evals/datasets/${id}/folder`, { data: { folder_id: folderId } })
    expect(moved.ok()).toBeTruthy()
    await datasetsPage.gotoDataset(id)
    // The folder of the selected dataset opens on its own.
    await expect(page.getByTestId(`dataset-row-${name}`)).toBeVisible()

    await page.getByTestId(`dataset-folder-${folder}`).hover()
    await page.getByTestId(`dataset-folder-actions-${folder}`).click()
    await page.getByTestId('dataset-folder-delete').click()
    await page.getByTestId('dataset-folder-delete-confirm-btn').click()
    await expect(page.getByTestId(`dataset-folder-${folder}`)).toHaveCount(0)
    await expect(page.getByTestId(`dataset-row-${name}`)).toBeVisible()
    expect((await DatasetsPage.getViaApi(request, id)).folder_id).toBeUndefined()
  })

  test('should import a CSV, save it as a version and keep that version after an edit', async ({ datasetsPage, page, request }) => {
    const id = await datasetsPage.create(uniqueName('import'), 'csv')
    createdIds.push(id)

    // Starting from a CSV opens the import step. The header becomes the columns.
    await datasetsPage.importCsv(CSV)
    const dataset = await DatasetsPage.getViaApi(request, id)
    expect(dataset.columns.map((column: { name: string }) => column.name)).toEqual(['input', 'expected_output', 'persona'])
    expect(dataset.head_version).toBe(1)

    const imported = await DatasetsPage.rowsViaApi(request, id)
    expect(imported.total).toBe(2)
    const inputColumn = dataset.columns[0].id
    expect(imported.rows[0].cells[inputColumn]).toBe('hello, world')

    // Delete a row through the API, then save the draft from the page.
    const deleted = await request.post(`/api/evals/datasets/${id}/rows/delete`, { data: { row_ids: [imported.rows[0].id] } })
    expect(deleted.ok()).toBeTruthy()
    await datasetsPage.gotoDataset(id)
    await expect(datasetsPage.saveVersionBtn).toBeEnabled()
    await datasetsPage.saveVersionBtn.click()
    await expect(datasetsPage.saveVersionBtn).toBeDisabled()

    expect((await DatasetsPage.rowsViaApi(request, id)).total).toBe(1)
    expect((await DatasetsPage.rowsViaApi(request, id, 1)).total).toBe(2)

    // An old version opens read-only.
    await datasetsPage.versionPicker.click()
    await page.getByTestId('dataset-version-option-1').click()
    await expect(page.getByTestId('dataset-readonly-banner')).toBeVisible()
    await expect(datasetsPage.saveVersionBtn).toHaveCount(0)
  })

  test('should restore an earlier version from the history', async ({ datasetsPage, page, request }) => {
    const id = await datasetsPage.create(uniqueName('restore'), 'csv')
    createdIds.push(id)
    await datasetsPage.importCsv(CSV)
    const rows = await DatasetsPage.rowsViaApi(request, id)
    await request.post(`/api/evals/datasets/${id}/rows/delete`, { data: { row_ids: rows.rows.map((row: { id: string }) => row.id) } })
    await request.post(`/api/evals/datasets/${id}/versions`, { data: {} })
    expect((await DatasetsPage.rowsViaApi(request, id)).total).toBe(0)

    await datasetsPage.gotoDataset(id)
    await page.getByTestId('dataset-history-btn').click()
    await page.getByTestId('dataset-history-version-1').click()
    await page.getByTestId('dataset-version-restore-btn').click()
    await page.getByTestId('dataset-version-confirm-btn').click()

    await expect.poll(async () => (await DatasetsPage.rowsViaApi(request, id)).total).toBe(2)
    const dataset = await DatasetsPage.getViaApi(request, id)
    expect(dataset.head_version).toBe(3)
  })

  test('should create a split and export the dataset', async ({ datasetsPage, page, request }) => {
    const id = await datasetsPage.create(uniqueName('split'), 'csv')
    createdIds.push(id)
    await datasetsPage.importCsv(CSV)
    await datasetsPage.createSplit('smoke')
    expect((await DatasetsPage.getViaApi(request, id)).splits).toHaveLength(1)

    const downloadPromise = page.waitForEvent('download')
    await page.getByTestId('dataset-export-btn').click()
    await page.getByTestId('dataset-export-csv').click()
    const download = await downloadPromise
    expect(download.suggestedFilename()).toMatch(/\.csv$/)

    const exported = await request.get(`/api/evals/datasets/${id}/export`)
    expect(await exported.text()).toBe(CSV)
  })
})
