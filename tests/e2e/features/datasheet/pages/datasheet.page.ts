import type { Locator, Page } from '@playwright/test'
import { BasePage } from '../../../core/pages/base.page'

function toTestIdPart(value: string): string {
  return value.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '')
}

export function datasheetRowKey(provider: string, model: string, mode: string): string {
  return `${toTestIdPart(provider)}-${toTestIdPart(model)}-${toTestIdPart(mode)}`
}

export class DatasheetPage extends BasePage {
  readonly table: Locator
  readonly sheet: Locator
  readonly addCustomBtn: Locator
  readonly saveBtn: Locator
  readonly resetAllBtn: Locator

  constructor(page: Page) {
    super(page)
    this.table = page.getByTestId('datasheet-table')
    this.sheet = page.getByTestId('datasheet-sheet')
    this.addCustomBtn = page.getByTestId('datasheet-add-custom-btn')
    this.saveBtn = page.getByTestId('datasheet-save-btn')
    this.resetAllBtn = page.getByTestId('datasheet-reset-all-btn')
  }

  async goto(): Promise<void> {
    await this.page.goto('/workspace/datasheet-editor')
  }

  row(provider: string, model: string, mode: string): Locator {
    return this.page.getByTestId(`datasheet-row-${datasheetRowKey(provider, model, mode)}`)
  }

  async openEntry(provider: string, model: string, mode: string): Promise<void> {
    await this.page.getByTestId(`datasheet-edit-${datasheetRowKey(provider, model, mode)}`).click()
    await this.sheet.waitFor({ state: 'visible' })
  }

  limitInput(field: 'context_length' | 'max_input_tokens' | 'max_output_tokens'): Locator {
    return this.page.getByTestId(`datasheet-limit-input-${field}`)
  }

  async chooseCustomProvider(provider: string): Promise<void> {
    await this.page.getByTestId('datasheet-custom-provider').click()
    await this.page.getByTestId(`datasheet-custom-provider-option-${provider}`).click()
  }
}
