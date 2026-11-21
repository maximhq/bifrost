import { expect, test } from '../../core/fixtures/base.fixture'
import { DatasetsPage } from '../datasets/pages/datasets.page'
import { TestRunsPage } from './pages/test-runs.page'

const uniqueName = (label: string) => `e2e ${label} ${Date.now()}-${Math.floor(Math.random() * 1e6)}`

const ROWS = [
  { input: '2+2', expected_output: '4', output: '4' },
  { input: 'capital of France', expected_output: 'Paris', output: 'Lyon' },
]

// These runs score outputs that are already in the dataset with a programmatic
// evaluator, so they need no provider key and call no model.
test.describe('Test runs', () => {
  const runIds: string[] = []
  const datasetIds: string[] = []
  const evaluatorIds: string[] = []

  test.afterEach(async ({ request }) => {
    // Runs go first: a dataset cannot be deleted while a run reads it.
    for (const id of runIds.splice(0)) {
      await TestRunsPage.deleteViaApi(request, id).catch((e) => console.error(`[CLEANUP] run ${id}:`, e))
    }
    for (const id of datasetIds.splice(0)) {
      await DatasetsPage.deleteViaApi(request, id).catch((e) => console.error(`[CLEANUP] dataset ${id}:`, e))
    }
    for (const id of evaluatorIds.splice(0)) {
      await request.delete(`/api/evals/evaluators/${id}`).catch((e) => console.error(`[CLEANUP] evaluator ${id}:`, e))
    }
  })

  test('should start a run on stored outputs and show its report', async ({ testRunsPage, page, request }) => {
    const datasetName = uniqueName('dataset')
    const evaluatorName = uniqueName('exact match')
    const runName = uniqueName('run')
    datasetIds.push(await TestRunsPage.createDatasetViaApi(request, datasetName, ROWS))
    evaluatorIds.push(await TestRunsPage.createExactMatchViaApi(request, evaluatorName))

    await testRunsPage.openNewRun()
    await page.getByTestId('run-form-name-input').fill(runName)
    await page.getByTestId('run-form-target-dataset').check()
    await testRunsPage.pick('run-form-dataset-select', datasetName)
    await testRunsPage.pickEvaluator(evaluatorName)
    // The server checks the settings; the button turns on once nothing blocks the run.
    await expect(page.getByTestId('run-form-submit-btn')).toBeEnabled()
    await page.getByTestId('run-form-submit-btn').click()

    await expect(page.getByTestId('run-report-page')).toBeVisible()
    const id = testRunsPage.runId()
    runIds.push(id)
    await testRunsPage.waitForStatus('completed')

    const run = await TestRunsPage.getViaApi(request, id)
    expect(run.total).toBe(2)
    expect(run.passed).toBe(1)
    expect(run.dataset_version).toBe(1)

    await expect(page.getByTestId('run-card-pass-rate')).toContainText('50%')
    await expect(page.getByTestId(`run-evaluator-row-${evaluatorName}`)).toContainText('50%')
    await expect(page.getByTestId('run-entries-table').locator('[data-tone="pass"]')).toHaveCount(1)
    await expect(page.getByTestId('run-entries-table').locator('[data-tone="fail"]')).toHaveCount(1)

    // Opening a row shows the entry in full beside the table.
    await page.getByTestId('run-entries-filter-notPassed').click()
    await page.getByTestId('run-entry-row-1').click()
    await expect(page.getByTestId('run-entry-panel')).toContainText('Lyon')
    await expect(page.getByTestId(`run-entry-result-${evaluatorName}`)).toBeVisible()
  })

  test('should block a run whose evaluator needs a variable the dataset lacks', async ({ testRunsPage, page, request }) => {
    const datasetName = uniqueName('no output')
    const evaluatorName = uniqueName('exact match')
    const created = await request.post('/api/evals/datasets', { data: { name: datasetName, template: 'qa' } })
    const datasetId = (await created.json()).dataset.id
    datasetIds.push(datasetId)
    await request.post(`/api/evals/datasets/${datasetId}/rows`, { data: { rows: [{ input: 'q', expected_output: 'a' }] } })
    evaluatorIds.push(await TestRunsPage.createExactMatchViaApi(request, evaluatorName))

    await testRunsPage.openNewRun()
    await page.getByTestId('run-form-name-input').fill(uniqueName('blocked'))
    await page.getByTestId('run-form-target-dataset').check()
    await testRunsPage.pick('run-form-dataset-select', datasetName)
    await testRunsPage.pickEvaluator(evaluatorName)

    await expect(page.getByTestId('run-form-problems')).toContainText('output')
    await expect(page.getByTestId('run-form-submit-btn')).toBeDisabled()
  })

  test('should compare two runs on the same dataset', async ({ testRunsPage, page, request }) => {
    const datasetId = await TestRunsPage.createDatasetViaApi(request, uniqueName('compare'), ROWS)
    datasetIds.push(datasetId)
    const evaluatorName = uniqueName('exact match')
    const evaluatorId = await TestRunsPage.createExactMatchViaApi(request, evaluatorName)
    evaluatorIds.push(evaluatorId)

    const names = [uniqueName('first'), uniqueName('second')]
    for (const name of names) {
      const started = await request.post('/api/evals/runs', {
        data: { name, dataset_id: datasetId, target: { type: 'dataset' }, evaluators: [{ id: evaluatorId }] },
      })
      expect(started.ok()).toBeTruthy()
      const id = (await started.json()).run.id
      runIds.push(id)
      await expect.poll(async () => (await TestRunsPage.getViaApi(request, id)).status, { timeout: 60_000 }).toBe('completed')
    }

    await testRunsPage.goto()
    await expect(page.getByTestId('runs-compare-btn')).toBeDisabled()
    for (const name of names) {
      await page.getByTestId('runs-search-input').fill(name)
      await page.getByTestId(`run-select-${name}`).click()
    }
    await page.getByTestId('runs-compare-btn').click()

    await expect(page.getByTestId('run-compare-page')).toBeVisible()
    await expect(page.getByTestId('run-compare-legend')).toContainText(names[0])
    await expect(page.getByTestId('run-compare-legend')).toContainText(names[1])
    await expect(page.getByTestId(`run-compare-average-${evaluatorName}`)).toContainText('0.5')
    await expect(page.getByTestId('run-compare-row-2')).toBeVisible()
  })
})
