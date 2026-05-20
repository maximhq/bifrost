import { expect, test } from '../../core/fixtures/base.fixture'

test.describe('Caching - Vector Store Config', () => {
  test.describe.configure({ mode: 'serial' })

  test('should display caching page with vector store card', async ({ cachingSettingsPage }) => {
    await cachingSettingsPage.goto()
    await expect(cachingSettingsPage.page.getByRole('heading', { name: /Caching/i })).toBeVisible()
    await expect(cachingSettingsPage.vectorStoreCard).toBeVisible()
    await expect(cachingSettingsPage.enabledSwitch).toBeVisible()
  })

  test('should show provider dropdown when enabled', async ({ cachingSettingsPage }) => {
    await cachingSettingsPage.goto()
    const wasEnabled = await cachingSettingsPage.isEnabled()
    if (!wasEnabled) {
      await cachingSettingsPage.toggleEnabled()
    }
    await expect(cachingSettingsPage.providerSelect).toBeVisible()
  })

  test('should hide provider fields when disabled', async ({ cachingSettingsPage }) => {
    await cachingSettingsPage.goto()
    const wasEnabled = await cachingSettingsPage.isEnabled()
    if (wasEnabled) {
      await cachingSettingsPage.toggleEnabled()
    }
    await expect(cachingSettingsPage.providerSelect).not.toBeVisible()
  })

  test('should show redis fields by default', async ({ cachingSettingsPage }) => {
    await cachingSettingsPage.goto()
    if (!(await cachingSettingsPage.isEnabled())) {
      await cachingSettingsPage.toggleEnabled()
    }
    await cachingSettingsPage.selectProvider('Redis')
    await expect(cachingSettingsPage.redisAddr).toBeVisible()
    await expect(cachingSettingsPage.redisDb).toBeVisible()
    await expect(cachingSettingsPage.redisPoolSize).toBeVisible()
    await expect(cachingSettingsPage.redisTls).toBeVisible()
  })

  test('should show weaviate fields when selected', async ({ cachingSettingsPage }) => {
    await cachingSettingsPage.goto()
    if (!(await cachingSettingsPage.isEnabled())) {
      await cachingSettingsPage.toggleEnabled()
    }
    await cachingSettingsPage.selectProvider('Weaviate')
    await expect(cachingSettingsPage.weaviateHost).toBeVisible()
    await expect(cachingSettingsPage.weaviateScheme).toBeVisible()
    await expect(cachingSettingsPage.weaviateApiKey).toBeVisible()
    // Redis fields should not be visible
    await expect(cachingSettingsPage.redisAddr).not.toBeVisible()
  })

  test('should show qdrant fields when selected', async ({ cachingSettingsPage }) => {
    await cachingSettingsPage.goto()
    if (!(await cachingSettingsPage.isEnabled())) {
      await cachingSettingsPage.toggleEnabled()
    }
    await cachingSettingsPage.selectProvider('Qdrant')
    await expect(cachingSettingsPage.qdrantHost).toBeVisible()
    await expect(cachingSettingsPage.qdrantPort).toBeVisible()
    await expect(cachingSettingsPage.qdrantApiKey).toBeVisible()
    await expect(cachingSettingsPage.qdrantTls).toBeVisible()
  })

  test('should show pinecone fields when selected', async ({ cachingSettingsPage }) => {
    await cachingSettingsPage.goto()
    if (!(await cachingSettingsPage.isEnabled())) {
      await cachingSettingsPage.toggleEnabled()
    }
    await cachingSettingsPage.selectProvider('Pinecone')
    await expect(cachingSettingsPage.pineconeApiKey).toBeVisible()
    await expect(cachingSettingsPage.pineconeIndexHost).toBeVisible()
  })

  test('should show save button after making changes', async ({ cachingSettingsPage }) => {
    await cachingSettingsPage.goto()
    if (!(await cachingSettingsPage.isEnabled())) {
      await cachingSettingsPage.toggleEnabled()
    }
    const saveVisible = await cachingSettingsPage.isSaveVisible()
    expect(saveVisible).toBe(true)
  })

  test('should persist disabled state', async ({ cachingSettingsPage }) => {
    await cachingSettingsPage.goto()
    // Ensure enabled first
    if (!(await cachingSettingsPage.isEnabled())) {
      await cachingSettingsPage.toggleEnabled()
      await cachingSettingsPage.save()
    }
    // Disable and save
    await cachingSettingsPage.toggleEnabled()
    expect(await cachingSettingsPage.isEnabled()).toBe(false)
    await cachingSettingsPage.save()
    // Reload and verify persisted
    await cachingSettingsPage.goto()
    expect(await cachingSettingsPage.isEnabled()).toBe(false)
  })
})
