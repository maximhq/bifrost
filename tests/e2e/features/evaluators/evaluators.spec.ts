import { expect, test } from '../../core/fixtures/base.fixture'
import { expectListLoadError } from '../../core/utils/test-helpers'
import { createEvaluatorData } from './evaluators.data'
import { EvaluatorsPage } from './pages/evaluators.page'

const createdIds: string[] = []

test.describe('Evaluators', () => {
  test.afterEach(async ({ request }) => {
    for (const id of [...createdIds]) {
      try {
        await EvaluatorsPage.deleteViaApi(request, id)
      } catch (e) {
        console.error(`[CLEANUP] Failed to delete evaluator ${id}:`, e)
      }
    }
    createdIds.length = 0
  })

  test('should offer a catalog and a create action', async ({ evaluatorsPage, page }) => {
    await evaluatorsPage.goto()
    // An instance with no evaluators shows the onboarding state; any other shows the toolbar.
    const empty = page.getByTestId('evaluators-empty-state')
    // Wait until the list has settled into one of the two states. isVisible() alone
    // does not wait, and would pick the wrong branch while the list is still loading.
    await expect(empty.or(page.getByTestId('evaluators-catalog-link'))).toBeVisible()
    if (await empty.isVisible()) {
      await expect(page.getByTestId('evaluators-empty-catalog-link')).toBeVisible()
      await expect(page.getByTestId('evaluators-empty-create-link')).toBeVisible()
    } else {
      await expect(page.getByTestId('evaluators-catalog-link')).toBeVisible()
      await expect(evaluatorsPage.createBtn).toBeVisible()
    }
  })

  test('should show the load error instead of the empty state when evaluators fail to load', async ({ evaluatorsPage, page }) => {
    await evaluatorsPage.goto()
    await expectListLoadError(page, '/api/evals/evaluators', 'evaluators-load-error', 'evaluators-empty-state')
  })

  test('should create a code evaluator and list it', async ({ evaluatorsPage, page }) => {
    const data = createEvaluatorData()
    const id = await evaluatorsPage.createCodeEvaluator(data)
    createdIds.push(id)

    await expect(page.getByTestId('evaluator-editor-version')).toContainText('1')
    // The starter code reads one variable, and the editor learns that from the gateway.
    await expect(page.getByTestId('evaluator-variables')).toContainText('output')

    await evaluatorsPage.goto()
    await evaluatorsPage.search(data.name)
    await expect(evaluatorsPage.getRow(id)).toBeVisible()
    await expect(page.getByTestId(`evaluators-name-${id}`)).toHaveText(data.name)
  })

  test('should run a draft in the test panel without saving it', async ({ evaluatorsPage, page }) => {
    await evaluatorsPage.gotoNew('programmatic')

    // An empty required variable is "not applicable", never a failure.
    await expect(page.getByTestId('evaluator-test-variable-output')).toBeVisible()
    await evaluatorsPage.runTestBtn.click()
    await expect(evaluatorsPage.testOutcome).toHaveText('Not applicable')

    await page.getByTestId('evaluator-test-variable-output').fill('The refund window is 30 days.')
    await evaluatorsPage.runTestBtn.click()
    await expect(evaluatorsPage.testOutcome).toHaveText('Passed')
    await expect(page.getByTestId('evaluator-test-score')).toContainText('Yes')
  })

  test('should keep the test sheet open while typing into the test panel', async ({ evaluatorsPage, page }) => {
    // Too narrow for two panels: the test bench opens in a sheet.
    await page.setViewportSize({ width: 800, height: 900 })
    await evaluatorsPage.gotoNew('programmatic')
    await page.getByTestId('evaluator-editor-open-test').click()
    const sheet = page.getByTestId('evaluator-test-sheet')
    await expect(sheet).toBeVisible()

    const input = page.getByTestId('evaluator-test-variable-output')
    await input.click()
    await input.fill('typed inside the sheet')
    await expect(sheet).toBeVisible()
    await expect(input).toHaveValue('typed inside the sheet')
  })

  test('should save a changed config as a new version', async ({ evaluatorsPage, page }) => {
    const id = await evaluatorsPage.createCodeEvaluator(createEvaluatorData())
    createdIds.push(id)

    // "is Yes" becomes "is not Yes": the same output now fails.
    await page.getByTestId('evaluator-entry-operator').click()
    await page.getByRole('option', { name: 'is not' }).click()
    await expect(page.getByTestId('evaluator-editor-unsaved')).toBeVisible()
    await expect(evaluatorsPage.saveBtn).toContainText('version 2')
    await evaluatorsPage.saveBtn.click()
    await expect(page.getByTestId('evaluator-editor-version')).toContainText('2')
    await expect(page.getByTestId('evaluator-editor-unsaved')).toHaveCount(0)

    await page.getByTestId('evaluator-test-variable-output').fill('anything')
    await evaluatorsPage.runTestBtn.click()
    await expect(evaluatorsPage.testOutcome).toHaveText('Failed')
  })

  test('should not keep an old validation error once the config changed and the next check failed', async ({ evaluatorsPage, page }) => {
    const data = createEvaluatorData()
    const id = await evaluatorsPage.createCodeEvaluator(data)
    createdIds.push(id)

    // The first check after this point reports the config invalid; every later check fails to arrive.
    let checks = 0
    await page.route('**/api/evals/evaluators/validate', async (route) => {
      checks += 1
      if (checks === 1) {
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ valid: false, error: 'invalid config', variables: [] }) })
        return
      }
      await route.abort()
    })

    await page.getByTestId('evaluator-entry-operator').click()
    await page.getByRole('option', { name: 'is not' }).click()
    await expect(evaluatorsPage.saveBtn).toBeDisabled()

    // Back to the saved config, with a new name so there is something to save. Its check fails,
    // and the answer for the earlier config must not block saving this one.
    await page.getByTestId('evaluator-entry-operator').click()
    await page.getByRole('option', { name: 'is', exact: true }).click()
    await evaluatorsPage.nameInput.fill(`${data.name}-renamed`)
    await expect.poll(() => checks).toBeGreaterThan(1)
    await expect(evaluatorsPage.saveBtn).toBeEnabled()
  })

  test('should keep Run disabled until the variables of the config on screen are known', async ({ evaluatorsPage, page }) => {
    await evaluatorsPage.gotoNew('api')
    await page.getByTestId('evaluator-api-url-input').fill('https://scorer.example.com/{{question}}')
    await expect(page.getByTestId('evaluator-test-variable-question')).toBeVisible()
    await expect(evaluatorsPage.runTestBtn).toBeEnabled()

    // From here the check of the config is held until the test releases it.
    let release!: () => void
    const held = new Promise<void>((resolve) => {
      release = resolve
    })
    let heldChecks = 0
    await page.route('**/api/evals/evaluators/validate', async (route) => {
      heldChecks += 1
      await held
      await route.continue()
    })

    await page.getByTestId('evaluator-api-url-input').fill('https://scorer.example.com/{{answer}}')
    await expect.poll(() => heldChecks).toBeGreaterThan(0)
    // The panel still lists the old variable, so Run must wait for the new answer.
    await expect(evaluatorsPage.runTestBtn).toBeDisabled()

    release()
    await expect(page.getByTestId('evaluator-test-variable-answer')).toBeVisible()
    await expect(page.getByTestId('evaluator-test-variable-question')).toHaveCount(0)
    await expect(evaluatorsPage.runTestBtn).toBeEnabled()
  })

  test('should keep edits made while a save is in flight', async ({ evaluatorsPage, page }) => {
    const data = createEvaluatorData()
    const id = await evaluatorsPage.createCodeEvaluator(data)
    createdIds.push(id)

    let release!: () => void
    const held = new Promise<void>((resolve) => {
      release = resolve
    })
    await page.route(`**/api/evals/evaluators/${id}`, async (route) => {
      if (route.request().method() === 'PUT') await held
      await route.continue()
    })

    await evaluatorsPage.nameInput.fill(`${data.name}-renamed`)
    const saved = page.waitForResponse((response) => response.request().method() === 'PUT' && response.url().endsWith(`/api/evals/evaluators/${id}`))
    const refetched = page.waitForResponse(
      (response) => response.request().method() === 'GET' && response.url().endsWith(`/api/evals/evaluators/${id}`),
    )
    await evaluatorsPage.saveBtn.click()
    // The save is held; the user keeps typing.
    const description = page.getByTestId('evaluator-description-input')
    await description.fill('edited while saving')

    release()
    const saveResponse = await saved
    expect(saveResponse.status(), await saveResponse.text()).toBe(200)
    await refetched
    await expect(evaluatorsPage.nameInput).toHaveValue(`${data.name}-renamed`)
    await expect(description).toHaveValue('edited while saving')
    await expect(page.getByTestId('evaluator-editor-unsaved')).toBeVisible()
  })

  test('should discard unsaved changes', async ({ evaluatorsPage, page }) => {
    const data = createEvaluatorData()
    const id = await evaluatorsPage.createCodeEvaluator(data)
    createdIds.push(id)

    await evaluatorsPage.nameInput.fill(`${data.name}-renamed`)
    await expect(page.getByTestId('evaluator-editor-unsaved')).toBeVisible()
    await page.getByTestId('evaluator-editor-discard').click()
    await expect(evaluatorsPage.nameInput).toHaveValue(data.name)
    await expect(page.getByTestId('evaluator-editor-unsaved')).toHaveCount(0)
  })

  test('should filter the list by type and source', async ({ evaluatorsPage, page }) => {
    const data = createEvaluatorData()
    const id = await evaluatorsPage.createCodeEvaluator(data)
    createdIds.push(id)

    await evaluatorsPage.goto()
    await evaluatorsPage.search(data.name)
    await expect(evaluatorsPage.getRow(id)).toBeVisible()

    await page.getByTestId('evaluators-filter-type-api').click()
    await expect(evaluatorsPage.getRow(id)).toHaveCount(0)
    await expect(page.getByTestId('evaluators-no-results')).toBeVisible()

    await page.getByTestId('evaluators-filter-type-programmatic').click()
    await expect(evaluatorsPage.getRow(id)).toBeVisible()

    // A code evaluator is custom, so the built-in filter hides it.
    await page.getByTestId('evaluators-filter-source-builtin').click()
    await expect(evaluatorsPage.getRow(id)).toHaveCount(0)

    await page.getByTestId('evaluators-filter-reset').click()
    await expect(evaluatorsPage.getRow(id)).toBeVisible()

    // A code evaluator calls no model, so it is always ready and never "no eval key".
    await page.getByTestId('evaluators-filter-status-no_key').click()
    await expect(evaluatorsPage.getRow(id)).toHaveCount(0)
    await page.getByTestId('evaluators-filter-status-no_key').click()
    await page.getByTestId('evaluators-filter-status-ready').click()
    await expect(evaluatorsPage.getRow(id)).toBeVisible()
  })

  test('should add a built-in metric from the catalog', async ({ evaluatorsPage, page }) => {
    const data = createEvaluatorData()
    await evaluatorsPage.gotoCatalog()

    await page.getByTestId('evaluator-catalog-search-input').fill('BLEU')
    await expect(page.getByTestId('evaluator-catalog-card-bleu')).toBeVisible()

    // The model facet narrows the catalog: BLEU needs no model.
    await page.getByTestId('evaluator-catalog-filter-model-judge').click()
    await expect(page.getByTestId('evaluator-catalog-card-bleu')).toHaveCount(0)
    await page.getByTestId('evaluator-catalog-filter-reset').click()

    await page.getByTestId('evaluator-catalog-add-bleu').click()
    await expect(page.getByTestId('evaluator-install-sheet')).toBeVisible()
    await expect(page.getByTestId('evaluator-install-variables')).toContainText('expected_output')
    await page.getByTestId('evaluator-install-name-input').fill(data.name)
    await page.getByTestId('evaluator-install-submit').click()

    // Installing opens the new evaluator, whose metric is fixed.
    await expect(page).toHaveURL(/[?&]id=[0-9a-f-]{36}/)
    const id = evaluatorsPage.evaluatorId()
    createdIds.push(id)
    await expect(evaluatorsPage.nameInput).toHaveValue(data.name)
    await expect(page.getByTestId('evaluator-builtin-notice')).toBeVisible()

    // Identical texts score 1, which clears the default threshold.
    await page.getByTestId('evaluator-test-variable-output').fill('the cat sat on the mat today')
    await page.getByTestId('evaluator-test-variable-expected_output').fill('the cat sat on the mat today')
    await evaluatorsPage.runTestBtn.click()
    await expect(evaluatorsPage.testOutcome).toHaveText('Passed')
  })

  test('should duplicate and delete an evaluator from the list', async ({ evaluatorsPage, page }) => {
    const data = createEvaluatorData()
    const id = await evaluatorsPage.createCodeEvaluator(data)
    createdIds.push(id)

    await evaluatorsPage.goto()
    await evaluatorsPage.search(data.name)
    await page.getByTestId(`evaluators-actions-${id}`).click()
    const duplicated = page.waitForResponse(
      (response) => response.request().method() === 'POST' && response.url().includes(`/api/evals/evaluators/${id}/duplicate`),
    )
    await page.getByTestId(`evaluators-duplicate-${id}`).click()
    // The copy is registered for cleanup before any assertion that could fail.
    const duplicateResponse = await duplicated
    expect(duplicateResponse.status(), await duplicateResponse.text()).toBe(200)
    const duplicate: { evaluator: { id: string } } = await duplicateResponse.json()
    createdIds.push(duplicate.evaluator.id)
    // The copy carries the original's name, so the search now matches two rows.
    await expect(evaluatorsPage.table.locator('[data-testid^="evaluators-row-"]')).toHaveCount(2)

    await evaluatorsPage.deleteFromList(id)
    await expect(evaluatorsPage.table.locator('[data-testid^="evaluators-row-"]')).toHaveCount(1)
  })

  test('should keep the test bench and its running dataset test when the layout narrows', async ({ evaluatorsPage, page, request }) => {
    const datasetName = `bench-layout-${Date.now()}`
    const created = await request.post('/api/evals/datasets', {
      data: { name: datasetName, columns: [{ name: 'output', type: 'output' }] },
    })
    expect(created.ok(), await created.text()).toBeTruthy()
    const datasetId: string = (await created.json()).dataset.id
    try {
      const added = await request.post(`/api/evals/datasets/${datasetId}/rows`, {
        data: { rows: [{ output: 'The refund window is 30 days.' }, { output: 'Refunds take a week.' }] },
      })
      expect(added.ok(), await added.text()).toBeTruthy()
      const rowIds: string[] = (await added.json()).rows.map((row: { id: string }) => row.id)

      await page.setViewportSize({ width: 1600, height: 900 })
      await evaluatorsPage.gotoNew('programmatic')
      await page.getByTestId('evaluator-test-variable-output').fill('typed before the resize')

      // The second row's variables are held until the test releases them, so the batch is
      // still running when the layout changes.
      let release!: () => void
      const held = new Promise<void>((resolve) => {
        release = resolve
      })
      let heldRequests = 0
      await page.route(`**/api/evals/datasets/${datasetId}/rows/${rowIds[1]}/variables**`, async (route) => {
        heldRequests += 1
        await held
        await route.continue()
      })

      await page.getByTestId('evaluator-test-load-dataset-select').click()
      await page.getByRole('option', { name: datasetName }).click()
      await page.getByTestId('evaluator-test-load-scope-select').click()
      await page.getByRole('option', { name: 'Full dataset' }).click()
      await evaluatorsPage.runTestBtn.click()
      await expect.poll(() => heldRequests).toBeGreaterThan(0)
      await expect(page.getByTestId('evaluator-test-batch-progress')).toHaveText('1 of 2 rows')

      // Too narrow for two panels: the bench moves into the sheet, the same instance.
      await page.setViewportSize({ width: 800, height: 900 })
      await page.getByTestId('evaluator-editor-open-test').click()
      const sheet = page.getByTestId('evaluator-test-sheet')
      await expect(sheet.getByTestId('evaluator-test-batch-progress')).toHaveText('1 of 2 rows')

      release()
      await expect(sheet.getByTestId('evaluator-test-batch-progress')).toHaveText('2 of 2 rows')
      await expect(sheet.getByTestId('evaluator-test-run-button')).toBeEnabled()

      // Back to one row: the value typed before the resize is still there.
      await sheet.getByTestId('evaluator-test-load-scope-select').click()
      await page.getByRole('option', { name: 'One row' }).click()
      await expect(sheet.getByTestId('evaluator-test-variable-output')).toHaveValue('typed before the resize')
    } finally {
      const removed = await request.delete(`/api/evals/datasets/${datasetId}`)
      if (!removed.ok() && removed.status() !== 404) {
        console.error(`[CLEANUP] Failed to delete dataset ${datasetId}: ${removed.status()}`)
      }
    }
  })

  test('should send no execute call for a row whose variables arrive after Stop', async ({ evaluatorsPage, page, request }) => {
    const datasetName = `bench-stop-${Date.now()}`
    const created = await request.post('/api/evals/datasets', {
      data: { name: datasetName, columns: [{ name: 'output', type: 'output' }] },
    })
    expect(created.ok(), await created.text()).toBeTruthy()
    const datasetId: string = (await created.json()).dataset.id
    try {
      const added = await request.post(`/api/evals/datasets/${datasetId}/rows`, {
        data: { rows: [{ output: 'The refund window is 30 days.' }] },
      })
      expect(added.ok(), await added.text()).toBeTruthy()
      const rowId: string = (await added.json()).rows[0].id

      await evaluatorsPage.gotoNew('programmatic')
      await expect(page.getByTestId('evaluator-test-variable-output')).toBeVisible()

      let executeRequests = 0
      page.on('request', (req) => {
        if (req.method() === 'POST' && req.url().includes('/api/evals/evaluators/execute')) executeRequests += 1
      })

      // The row's variables are held until the test releases them, after Stop.
      let release!: () => void
      const held = new Promise<void>((resolve) => {
        release = resolve
      })
      let heldRequests = 0
      await page.route(`**/api/evals/datasets/${datasetId}/rows/${rowId}/variables**`, async (route) => {
        heldRequests += 1
        await held
        await route.continue()
      })

      await page.getByTestId('evaluator-test-load-dataset-select').click()
      await page.getByRole('option', { name: datasetName }).click()
      await page.getByTestId('evaluator-test-load-scope-select').click()
      await page.getByRole('option', { name: 'Full dataset' }).click()
      await evaluatorsPage.runTestBtn.click()
      await expect.poll(() => heldRequests).toBeGreaterThan(0)

      await page.getByTestId('evaluator-test-stop-button').click()
      await expect(evaluatorsPage.runTestBtn).toBeEnabled()

      const variablesLoaded = page.waitForResponse((response) => response.url().includes(`/rows/${rowId}/variables`))
      release()
      const variablesResponse = await variablesLoaded
      expect(variablesResponse.status(), await variablesResponse.text()).toBe(200)
      // Nothing may follow the late variables. Give a stray call time to go out before counting.
      await page.waitForTimeout(1000)
      expect(executeRequests).toBe(0)
    } finally {
      const removed = await request.delete(`/api/evals/datasets/${datasetId}`)
      if (!removed.ok() && removed.status() !== 404) {
        console.error(`[CLEANUP] Failed to delete dataset ${datasetId}: ${removed.status()}`)
      }
    }
  })

  test('should run once on the typed values when the evaluator loses its variables after a dataset was chosen', async ({ evaluatorsPage, page, request }) => {
    const datasetName = `bench-novars-${Date.now()}`
    const created = await request.post('/api/evals/datasets', {
      data: { name: datasetName, columns: [{ name: 'output', type: 'output' }] },
    })
    expect(created.ok(), await created.text()).toBeTruthy()
    const datasetId: string = (await created.json()).dataset.id
    try {
      // Two rows, so a batch run would send two execute calls instead of one.
      const added = await request.post(`/api/evals/datasets/${datasetId}/rows`, {
        data: { rows: [{ output: 'first' }, { output: 'second' }] },
      })
      expect(added.ok(), await added.text()).toBeTruthy()

      await evaluatorsPage.gotoNew('api')
      await page.getByTestId('evaluator-api-url-input').fill('https://scorer.example.com/{{output}}')
      await expect(page.getByTestId('evaluator-test-variable-output')).toBeVisible()

      await page.getByTestId('evaluator-test-load-dataset-select').click()
      await page.getByRole('option', { name: datasetName }).click()
      await page.getByTestId('evaluator-test-load-scope-select').click()
      await page.getByRole('option', { name: 'Full dataset' }).click()

      // The evaluator no longer reads a variable, so the dataset control goes away.
      await page.getByTestId('evaluator-api-url-input').fill('https://scorer.example.com/score')
      await expect(page.getByTestId('evaluator-test-no-variables')).toBeVisible()
      await expect(page.getByTestId('evaluator-test-load-from-dataset')).toHaveCount(0)
      await expect(evaluatorsPage.runTestBtn).toBeEnabled()

      let executeRequests = 0
      let rowListRequests = 0
      // The execute call is answered here, so the run never reaches the example scorer.
      await page.route('**/api/evals/evaluators/execute', async (route) => {
        executeRequests += 1
        await route.abort()
      })
      page.on('request', (req) => {
        if (req.method() === 'GET' && req.url().includes(`/api/evals/datasets/${datasetId}/rows`)) rowListRequests += 1
      })

      await evaluatorsPage.runTestBtn.click()
      await expect(page.getByTestId('evaluator-test-failure')).toBeVisible()
      await expect(evaluatorsPage.runTestBtn).toBeEnabled()
      expect(executeRequests).toBe(1)
      expect(rowListRequests).toBe(0)
      await expect(page.getByTestId('evaluator-test-batch-result')).toHaveCount(0)
    } finally {
      const removed = await request.delete(`/api/evals/datasets/${datasetId}`)
      if (!removed.ok() && removed.status() !== 404) {
        console.error(`[CLEANUP] Failed to delete dataset ${datasetId}: ${removed.status()}`)
      }
    }
  })

  test('should not offer to run a human evaluator', async ({ evaluatorsPage, page }) => {
    await evaluatorsPage.gotoNew('human')
    await expect(page.getByTestId('evaluator-human-notice')).toBeVisible()
    await expect(evaluatorsPage.runTestBtn).toBeDisabled()
    await expect(page.getByTestId('evaluator-test-blocked')).toBeVisible()
  })
})
