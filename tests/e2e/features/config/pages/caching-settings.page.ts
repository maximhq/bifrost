import { Locator, Page } from '@playwright/test'
import { BasePage } from '../../../core/pages/base.page'
import { waitForNetworkIdle } from '../../../core/utils/test-helpers'

export class CachingSettingsPage extends BasePage {
  readonly vectorStoreCard: Locator
  readonly enabledSwitch: Locator
  readonly providerSelect: Locator
  readonly saveBtn: Locator

  // Redis fields
  readonly redisAddr: Locator
  readonly redisDb: Locator
  readonly redisPoolSize: Locator
  readonly redisTls: Locator

  // Weaviate fields
  readonly weaviateHost: Locator
  readonly weaviateScheme: Locator
  readonly weaviateApiKey: Locator

  // Qdrant fields
  readonly qdrantHost: Locator
  readonly qdrantPort: Locator
  readonly qdrantApiKey: Locator
  readonly qdrantTls: Locator

  // Pinecone fields
  readonly pineconeApiKey: Locator
  readonly pineconeIndexHost: Locator

  constructor(page: Page) {
    super(page)
    this.vectorStoreCard = page.getByTestId('vector-store-card')
    this.enabledSwitch = page.getByTestId('vs-enabled-switch')
    this.providerSelect = page.getByTestId('vs-provider-select')
    this.saveBtn = page.getByTestId('vs-save-btn')

    // Redis
    this.redisAddr = page.locator('#vs-redis-addr')
    this.redisDb = page.locator('#vs-redis-db')
    this.redisPoolSize = page.locator('#vs-redis-pool')
    this.redisTls = page.locator('#vs-redis-tls')

    // Weaviate
    this.weaviateHost = page.locator('#vs-weaviate-host')
    this.weaviateScheme = page.locator('#vs-weaviate-scheme')
    this.weaviateApiKey = page.locator('#vs-weaviate-apikey')

    // Qdrant
    this.qdrantHost = page.locator('#vs-qdrant-host')
    this.qdrantPort = page.locator('#vs-qdrant-port')
    this.qdrantApiKey = page.locator('#vs-qdrant-apikey')
    this.qdrantTls = page.locator('#vs-qdrant-tls')

    // Pinecone
    this.pineconeApiKey = page.locator('#vs-pinecone-apikey')
    this.pineconeIndexHost = page.locator('#vs-pinecone-host')
  }

  async goto(): Promise<void> {
    await this.page.goto('/workspace/config/caching')
    await waitForNetworkIdle(this.page)
  }

  async toggleEnabled(): Promise<void> {
    await this.enabledSwitch.click()
  }

  async isEnabled(): Promise<boolean> {
    const state = await this.enabledSwitch.getAttribute('data-state')
    return state === 'checked'
  }

  async selectProvider(provider: string): Promise<void> {
    await this.providerSelect.click()
    await this.page.getByRole('option', { name: provider }).click()
  }

  async getSelectedProvider(): Promise<string> {
    return (await this.providerSelect.textContent()) ?? ''
  }

  async save(): Promise<void> {
    await this.saveBtn.click()
    await this.waitForSuccessToast()
  }

  async isSaveVisible(): Promise<boolean> {
    return await this.saveBtn.isVisible().catch(() => false)
  }
}
