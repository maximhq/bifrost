import { Locator, Page, expect } from '@playwright/test'
import { BasePage } from '../../../core/pages/base.page'
import { waitForNetworkIdle } from '../../../core/utils/test-helpers'

/** One entry of GET /api/config/vector-stores. */
export interface StoredVectorStore {
  name: string
  type: string
  primary: boolean
  status: 'connected' | 'failed' | 'disabled'
  error: string
  shared_with: string
  endpoint: string
  retention_days: number
  max_size_mb: number
}

export interface StoredVectorStores {
  editable: boolean
  vector_stores: StoredVectorStore[]
}

export const VECTOR_STORES_API = '/api/config/vector-stores'

/** Settings > Vector Stores: one card per store configured in config.json. */
export class VectorStoresPage extends BasePage {
  readonly view: Locator
  readonly emptyState: Locator
  readonly readOnlyNote: Locator

  constructor(page: Page) {
    super(page)
    this.view = page.getByTestId('vector-stores-view')
    this.emptyState = page.getByTestId('vector-stores-empty')
    this.readOnlyNote = page.getByTestId('vector-stores-read-only-note')
  }

  async goto(): Promise<void> {
    await this.page.goto('/workspace/config/vector-stores')
    await waitForNetworkIdle(this.page)
    await expect(this.view).toBeVisible()
  }

  card(name: string): Locator {
    return this.page.getByTestId(`vector-store-card-${name}`)
  }

  status(name: string): Locator {
    return this.page.getByTestId(`vector-store-status-${name}`)
  }

  primaryBadge(name: string): Locator {
    return this.page.getByTestId(`vector-store-primary-badge-${name}`)
  }

  /** A setting's row, which shows its value and opens to its field. */
  settingToggle(name: string, setting: 'retention' | 'max-size'): Locator {
    return this.page.getByTestId(`vector-store-${setting}-toggle-${name}`)
  }

  settingSummary(name: string, setting: 'retention' | 'max-size'): Locator {
    return this.page.getByTestId(`vector-store-${setting}-summary-${name}`)
  }

  /** Opens both of a store's settings, leaving any already open as they are. */
  async openSettings(name: string): Promise<void> {
    for (const setting of ['retention', 'max-size'] as const) {
      const toggle = this.settingToggle(name, setting)
      if ((await toggle.getAttribute('aria-expanded')) !== 'true') await toggle.click()
      await expect(toggle).toHaveAttribute('aria-expanded', 'true')
    }
  }

  retentionInput(name: string): Locator {
    return this.page.getByTestId(`vector-store-retention-input-${name}`)
  }

  maxSizeInput(name: string): Locator {
    return this.page.getByTestId(`vector-store-max-size-input-${name}`)
  }

  retentionError(name: string): Locator {
    return this.page.getByTestId(`vector-store-retention-error-${name}`)
  }

  maxSizeError(name: string): Locator {
    return this.page.getByTestId(`vector-store-max-size-error-${name}`)
  }

  /** The page's one Save, which saves every store that changed. */
  saveBtn(): Locator {
    return this.page.getByTestId('vector-stores-save-btn')
  }

  sharedSettingsNote(name: string): Locator {
    return this.page.getByTestId(`vector-store-shared-settings-note-${name}`)
  }

  /** Reads the stores straight from the API. */
  async fetchStores(): Promise<StoredVectorStores> {
    const res = await this.page.request.get(VECTOR_STORES_API)
    expect(res.status(), await res.text()).toBe(200)
    return (await res.json()) as StoredVectorStores
  }

  /** Writes one store's settings straight through the API. */
  async putStore(name: string, retentionDays: number, maxSizeMB: number): Promise<void> {
    const res = await this.page.request.put(`${VECTOR_STORES_API}/${encodeURIComponent(name)}`, {
      data: { retention_days: retentionDays, max_size_mb: maxSizeMB },
    })
    expect(res.status(), await res.text()).toBe(200)
  }

  /** Clicks Save and returns the status of the store's PUT once the success toast shows. */
  async save(name: string): Promise<number> {
    const saved = this.page.waitForResponse(
      (r) =>
        new URL(r.url()).pathname === `${VECTOR_STORES_API}/${encodeURIComponent(name)}` &&
        r.request().method() === 'PUT',
    )
    await this.saveBtn().click()
    const res = await saved
    expect(res.status(), await res.text()).toBe(200)
    await this.waitForSuccessToast()
    return res.status()
  }
}
