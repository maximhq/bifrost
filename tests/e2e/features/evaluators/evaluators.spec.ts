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

  test('should duplicate and delete an evaluator from the list', async ({ evaluatorsPage, page, request }) => {
    const data = createEvaluatorData()
    const id = await evaluatorsPage.createCodeEvaluator(data)
    createdIds.push(id)

    await evaluatorsPage.goto()
    await evaluatorsPage.search(data.name)
    await page.getByTestId(`evaluators-actions-${id}`).click()
    await page.getByTestId(`evaluators-duplicate-${id}`).click()
    // The copy carries the original's name, so the search now matches two rows.
    await expect(evaluatorsPage.table.locator('[data-testid^="evaluators-row-"]')).toHaveCount(2)
    const listed = await request.get(`/api/evals/evaluators?search=${encodeURIComponent(data.name)}`)
    const body: { evaluators: { id: string }[] } = await listed.json()
    for (const evaluator of body.evaluators) {
      if (!createdIds.includes(evaluator.id)) createdIds.push(evaluator.id)
    }

    await evaluatorsPage.deleteFromList(id)
    await expect(evaluatorsPage.table.locator('[data-testid^="evaluators-row-"]')).toHaveCount(1)
  })

  test('should not offer to run a human evaluator', async ({ evaluatorsPage, page }) => {
    await evaluatorsPage.gotoNew('human')
    await expect(page.getByTestId('evaluator-human-notice')).toBeVisible()
    await expect(evaluatorsPage.runTestBtn).toBeDisabled()
    await expect(page.getByTestId('evaluator-test-blocked')).toBeVisible()
  })
})
