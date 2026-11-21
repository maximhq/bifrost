import type { APIRequestContext, Locator, Page } from '@playwright/test'
import { expect } from '../../../core/fixtures/base.fixture'
import { BasePage } from '../../../core/pages/base.page'
import { waitForNetworkIdle } from '../../../core/utils/test-helpers'

export type DatasetStart = 'empty' | 'qa' | 'retrieval' | 'agent' | 'csv'

export class DatasetsPage extends BasePage {
  readonly saveVersionBtn: Locator
  readonly versionPicker: Locator
  readonly grid: Locator

  constructor(page: Page) {
    super(page)
    this.saveVersionBtn = page.getByTestId('dataset-save-version-btn')
    this.versionPicker = page.getByTestId('dataset-version-picker')
    this.grid = page.getByTestId('dataset-grid')
  }

  async goto(): Promise<void> {
    await this.page.goto('/workspace/evals/datasets')
    await waitForNetworkIdle(this.page)
  }

  async gotoDataset(id: string): Promise<void> {
    await this.page.goto(`/workspace/evals/datasets?id=${id}`)
    await expect(this.page.getByTestId('dataset-detail-page')).toBeVisible()
  }

  /** The id of the selected dataset, read from the URL. */
  datasetId(): string {
    return new URL(this.page.url()).searchParams.get('id') ?? ''
  }

  /** Creates a dataset from the sidebar, which selects it. Returns its id. */
  async create(name: string, start: DatasetStart): Promise<string> {
    await this.goto()
    // An instance with no datasets shows the onboarding state; any other shows the sidebar,
    // where "New dataset" sits in the add menu.
    const emptyCreate = this.page.getByTestId('datasets-empty-create-btn')
    if (await emptyCreate.isVisible().catch(() => false)) {
      await emptyCreate.click()
    } else {
      await this.page.getByTestId('datasets-add-menu').click()
      await this.page.getByTestId('datasets-create-btn').click()
    }
    await this.page.getByTestId('dataset-form-name-input').fill(name)
    await this.page.getByTestId(`dataset-form-start-${start}`).check()
    await this.page.getByTestId('dataset-form-submit-btn').click()
    await expect(this.page).toHaveURL(/datasets\?.*id=[0-9a-f-]{36}/)
    await expect(this.page.getByTestId('dataset-detail-page')).toBeVisible()
    return this.datasetId()
  }

  /** Imports CSV text through the import dialog, keeping the suggested column mapping. */
  async importCsv(csv: string): Promise<void> {
    const dialog = this.page.getByTestId('dataset-import-dialog')
    if (!(await dialog.isVisible().catch(() => false))) {
      await this.page.getByTestId('dataset-import-btn').click()
    }
    await this.page.getByTestId('dataset-import-file-input').setInputFiles({
      name: 'rows.csv',
      mimeType: 'text/csv',
      buffer: Buffer.from(csv),
    })
    await expect(this.page.getByTestId('dataset-import-target-0')).toBeVisible()
    await this.page.getByTestId('dataset-import-submit-btn').click()
    await expect(dialog).toBeHidden()
  }

  async createSplit(name: string): Promise<void> {
    await this.page.getByTestId('dataset-new-split-btn').click()
    await this.page.getByTestId('dataset-split-name-input').fill(name)
    await this.page.getByTestId('dataset-split-create-btn').click()
    await expect(this.page.getByTestId(`dataset-split-tab-${name}`)).toBeVisible()
  }

  /** Reads a dataset through the API. The grid is drawn on a canvas, so rows are checked here. */
  static async getViaApi(request: APIRequestContext, id: string) {
    const response = await request.get(`/api/evals/datasets/${id}`)
    expect(response.ok()).toBeTruthy()
    return (await response.json()).dataset
  }

  static async rowsViaApi(request: APIRequestContext, id: string, version?: number) {
    const response = await request.get(`/api/evals/datasets/${id}/rows${version ? `?version=${version}` : ''}`)
    expect(response.ok()).toBeTruthy()
    return await response.json()
  }

  /** Removes a dataset through the API. A missing one is not an error. */
  static async deleteViaApi(request: APIRequestContext, id: string): Promise<void> {
    const response = await request.delete(`/api/evals/datasets/${id}`)
    if (!response.ok() && response.status() !== 404) {
      throw new Error(`Delete dataset ${id} failed: ${response.status()} ${response.statusText()}`)
    }
  }
}
