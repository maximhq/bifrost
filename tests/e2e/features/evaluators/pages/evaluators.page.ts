import type { APIRequestContext, Locator, Page } from '@playwright/test'
import { expect } from '../../../core/fixtures/base.fixture'
import { BasePage } from '../../../core/pages/base.page'
import { waitForNetworkIdle } from '../../../core/utils/test-helpers'
import type { EvaluatorData } from '../evaluators.data'

export type CreatableEvaluatorType = 'ai' | 'programmatic' | 'api' | 'human'

export class EvaluatorsPage extends BasePage {
  readonly createBtn: Locator
  readonly searchInput: Locator
  readonly table: Locator
  readonly saveBtn: Locator
  readonly nameInput: Locator
  readonly runTestBtn: Locator
  readonly testOutcome: Locator

  constructor(page: Page) {
    super(page)
    this.createBtn = page.getByTestId('evaluators-create-button')
    this.searchInput = page.getByTestId('evaluators-search-input')
    this.table = page.getByTestId('evaluators-table')
    this.saveBtn = page.getByTestId('evaluator-editor-save')
    this.nameInput = page.getByTestId('evaluator-name-input')
    this.runTestBtn = page.getByTestId('evaluator-test-run-button')
    this.testOutcome = page.getByTestId('evaluator-test-outcome')
  }

  async goto(): Promise<void> {
    await this.page.goto('/workspace/evals/evaluators')
    await waitForNetworkIdle(this.page)
  }

  async gotoCatalog(): Promise<void> {
    await this.page.goto('/workspace/evals/evaluators/catalog')
    await waitForNetworkIdle(this.page)
  }

  async gotoNew(type: CreatableEvaluatorType): Promise<void> {
    await this.page.goto(`/workspace/evals/evaluators/editor?type=${type}`)
    await expect(this.nameInput).toBeVisible()
  }

  async gotoEditor(id: string): Promise<void> {
    await this.page.goto(`/workspace/evals/evaluators/editor?id=${id}`)
    await expect(this.nameInput).toBeVisible()
  }

  getRow(id: string): Locator {
    return this.page.getByTestId(`evaluators-row-${id}`)
  }

  /** The id of the evaluator open in the editor, read from the URL. */
  evaluatorId(): string {
    return new URL(this.page.url()).searchParams.get('id') ?? ''
  }

  /**
   * Creates a code evaluator from the starter code, which passes any non-empty output.
   * Returns its id. A code evaluator runs in the gateway, so no provider key is needed.
   */
  async createCodeEvaluator(data: EvaluatorData): Promise<string> {
    await this.gotoNew('programmatic')
    await this.nameInput.fill(data.name)
    await this.page.getByTestId('evaluator-description-input').fill(data.description)
    await expect(this.saveBtn).toBeEnabled()
    await this.saveBtn.click()
    await expect(this.page).toHaveURL(/[?&]id=[0-9a-f-]{36}/)
    await expect(this.page.getByTestId('evaluator-editor-version')).toBeVisible()
    return this.evaluatorId()
  }

  async search(term: string): Promise<void> {
    await this.searchInput.fill(term)
    await waitForNetworkIdle(this.page)
  }

  async deleteFromList(id: string): Promise<void> {
    await this.page.getByTestId(`evaluators-actions-${id}`).click()
    await this.page.getByTestId(`evaluators-delete-${id}`).click()
    await this.page.getByTestId('evaluators-delete-confirm').click()
    await expect(this.getRow(id)).toHaveCount(0)
  }

  /** Removes an evaluator through the API. A missing one is not an error. */
  static async deleteViaApi(request: APIRequestContext, id: string): Promise<void> {
    const response = await request.delete(`/api/evals/evaluators/${id}`)
    if (!response.ok() && response.status() !== 404) {
      throw new Error(`Delete evaluator ${id} failed: ${response.status()} ${response.statusText()}`)
    }
  }
}
