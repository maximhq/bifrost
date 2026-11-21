import type { APIRequestContext, Page } from '@playwright/test'
import { expect } from '../../../core/fixtures/base.fixture'
import { BasePage } from '../../../core/pages/base.page'
import { waitForNetworkIdle } from '../../../core/utils/test-helpers'

export class TestRunsPage extends BasePage {
  constructor(page: Page) {
    super(page)
  }

  async goto(): Promise<void> {
    await this.page.goto('/workspace/evals/runs')
    await waitForNetworkIdle(this.page)
  }

  async gotoRun(id: string): Promise<void> {
    await this.page.goto(`/workspace/evals/runs?id=${id}`)
    await expect(this.page.getByTestId('run-report-page')).toBeVisible()
  }

  /** The id of the run whose report is open, read from the URL. */
  runId(): string {
    return new URL(this.page.url()).searchParams.get('id') ?? ''
  }

  /** Opens the New run sheet from the list, or from the onboarding state of an empty instance. */
  async openNewRun(): Promise<void> {
    await this.goto()
    const emptyCreate = this.page.getByTestId('runs-empty-create-btn')
    if (await emptyCreate.isVisible().catch(() => false)) {
      await emptyCreate.click()
    } else {
      await this.page.getByTestId('runs-create-btn').click()
    }
    await expect(this.page.getByTestId('run-form-sheet')).toBeVisible()
  }

  /** Picks an option of a select by its visible name. */
  async pick(selectTestId: string, option: string): Promise<void> {
    await this.page.getByTestId(selectTestId).click()
    await this.page.getByRole('option', { name: option, exact: true }).click()
  }

  /** Picks an evaluator in the New run sheet's evaluator picker, then closes the picker. */
  async pickEvaluator(name: string): Promise<void> {
    await this.page.getByTestId('run-form-evaluators-select').click()
    await this.page.getByRole('option').filter({ hasText: name }).first().click()
    await this.page.keyboard.press('Escape')
  }

  /** Waits until the report shows the run in the given status. */
  async waitForStatus(status: string): Promise<void> {
    await expect(this.page.getByTestId('run-status-tag')).toContainText(new RegExp(status, 'i'), { timeout: 60_000 })
  }

  /** Creates a dataset with input, expected_output and output columns and the given rows. */
  static async createDatasetViaApi(request: APIRequestContext, name: string, rows: Record<string, string>[]): Promise<string> {
    const created = await request.post('/api/evals/datasets', {
      data: { name, template: 'qa', columns: [{ name: 'output', type: 'output' }] },
    })
    expect(created.ok()).toBeTruthy()
    const id = (await created.json()).dataset.id
    const added = await request.post(`/api/evals/datasets/${id}/rows`, { data: { rows } })
    expect(added.ok()).toBeTruthy()
    return id
  }

  /** Creates a programmatic evaluator that passes when the output equals the expected output. */
  static async createExactMatchViaApi(request: APIRequestContext, name: string): Promise<string> {
    const created = await request.post('/api/evals/evaluators', {
      data: {
        name,
        type: 'programmatic',
        config: { code: 'def validate(output, expected_output):\n    return output == expected_output\n', response_type: 'yes_no' },
      },
    })
    expect(created.ok()).toBeTruthy()
    return (await created.json()).evaluator.id
  }

  static async getViaApi(request: APIRequestContext, id: string) {
    const response = await request.get(`/api/evals/runs/${id}`)
    expect(response.ok()).toBeTruthy()
    return (await response.json()).run
  }

  /** Removes a run through the API. A missing one is not an error. */
  static async deleteViaApi(request: APIRequestContext, id: string): Promise<void> {
    const response = await request.delete(`/api/evals/runs/${id}`)
    if (!response.ok() && response.status() !== 404) {
      throw new Error(`Delete run ${id} failed: ${response.status()} ${response.statusText()}`)
    }
  }
}
