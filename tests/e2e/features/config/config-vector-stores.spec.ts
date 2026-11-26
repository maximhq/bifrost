import { expect, test } from '../../core/fixtures/base.fixture'
import { StoredVectorStore, VECTOR_STORES_API } from './pages/vector-stores.page'

test.describe('Vector Stores Settings', () => {
  // Serial: the tests write the same store's settings.
  test.describe.configure({ mode: 'serial' })

  let stores: StoredVectorStore[] = []
  // The store the edit tests change: the first one with its own connection, when the
  // server has a config store to save to. Null otherwise.
  let target: StoredVectorStore | null = null

  test.beforeEach(async ({ vectorStoresPage }) => {
    const listed = await vectorStoresPage.fetchStores()
    stores = listed.vector_stores
    target = listed.editable ? (stores.find((s) => s.shared_with === '') ?? null) : null
    await vectorStoresPage.goto()
    test.skip(
      stores.length === 0,
      'the E2E server profile configures no vector store (vector_store / vector_stores in config.json), so there is nothing to list or edit',
    )
  })

  test.afterEach(async ({ vectorStoresPage }) => {
    // Put the edited store back as it was before the test.
    if (target) {
      await vectorStoresPage.putStore(target.name, target.retention_days, target.max_size_mb)
    }
  })

  test('should list every configured vector store', async ({ vectorStoresPage }) => {
    await expect(vectorStoresPage.emptyState).toHaveCount(0)
    for (const store of stores) {
      await expect(vectorStoresPage.card(store.name)).toBeVisible()
      await expect(vectorStoresPage.status(store.name)).toBeVisible()
      await expect(vectorStoresPage.primaryBadge(store.name)).toHaveCount(store.primary ? 1 : 0)
      // Closed, each setting shows its value; open, its field.
      await expect(vectorStoresPage.retentionInput(store.name)).toHaveCount(0)
      await expect(vectorStoresPage.settingSummary(store.name, 'retention')).toHaveText(store.retention_days ? String(store.retention_days) : 'Same as logs')
      await expect(vectorStoresPage.settingSummary(store.name, 'max-size')).toHaveText(store.max_size_mb ? String(store.max_size_mb) : 'No limit')
      await vectorStoresPage.openSettings(store.name)
      await expect(vectorStoresPage.retentionInput(store.name)).toHaveValue(store.retention_days ? String(store.retention_days) : '')
      await expect(vectorStoresPage.maxSizeInput(store.name)).toHaveValue(store.max_size_mb ? String(store.max_size_mb) : '')
      if (store.shared_with !== '') {
        // Its owner's settings apply, so it cannot be edited on its own.
        await expect(vectorStoresPage.sharedSettingsNote(store.name)).toContainText(store.shared_with)
        await expect(vectorStoresPage.retentionInput(store.name)).toBeDisabled()
        await expect(vectorStoresPage.maxSizeInput(store.name)).toBeDisabled()
      }
    }
  })

  test('should save retention and max size for a store and keep them after reload', async ({ vectorStoresPage }) => {
    test.skip(!target, 'no vector store can be edited: the server runs without a config store, or every store shares another one\'s connection')
    const store = target as StoredVectorStore
    const retention = store.retention_days === 7 ? '14' : '7'
    const maxSize = store.max_size_mb === 2048 ? '4096' : '2048'

    await vectorStoresPage.openSettings(store.name)
    await vectorStoresPage.retentionInput(store.name).fill(retention)
    await vectorStoresPage.maxSizeInput(store.name).fill(maxSize)
    expect(await vectorStoresPage.save(store.name)).toBe(200)

    await vectorStoresPage.goto()
    await expect(vectorStoresPage.settingSummary(store.name, 'retention')).toHaveText(retention)
    await expect(vectorStoresPage.settingSummary(store.name, 'max-size')).toHaveText(maxSize)
    await vectorStoresPage.openSettings(store.name)
    await expect(vectorStoresPage.retentionInput(store.name)).toHaveValue(retention)
    await expect(vectorStoresPage.maxSizeInput(store.name)).toHaveValue(maxSize)

    const saved = (await vectorStoresPage.fetchStores()).vector_stores.find((s) => s.name === store.name)
    expect(saved?.retention_days).toBe(Number(retention))
    expect(saved?.max_size_mb).toBe(Number(maxSize))
  })

  test('should show an inline error for an out-of-range value and not save it', async ({ vectorStoresPage }) => {
    test.skip(!target, 'no vector store can be edited: the server runs without a config store, or every store shares another one\'s connection')
    const store = target as StoredVectorStore
    const page = vectorStoresPage.page
    const puts: string[] = []
    page.on('request', (req) => {
      if (req.method() === 'PUT' && new URL(req.url()).pathname.startsWith(`${VECTOR_STORES_API}/`)) puts.push(req.url())
    })

    // Longer than the server can hold.
    await vectorStoresPage.openSettings(store.name)
    await vectorStoresPage.retentionInput(store.name).fill('106752')
    await expect(vectorStoresPage.retentionError(store.name)).toBeVisible()
    await expect(vectorStoresPage.saveBtn()).toBeDisabled()
    await vectorStoresPage.retentionInput(store.name).fill('106751')
    await expect(vectorStoresPage.retentionError(store.name)).toHaveCount(0)

    // Larger than the server accepts, and a fraction of a MB.
    await vectorStoresPage.maxSizeInput(store.name).fill('8796093022208')
    await expect(vectorStoresPage.maxSizeError(store.name)).toBeVisible()
    await expect(vectorStoresPage.saveBtn()).toBeDisabled()
    await vectorStoresPage.maxSizeInput(store.name).fill('1.5')
    await expect(vectorStoresPage.maxSizeError(store.name)).toBeVisible()
    await expect(vectorStoresPage.saveBtn()).toBeDisabled()
    // A row with an invalid value stays open, so the error that blocks the save stays in view.
    await vectorStoresPage.settingToggle(store.name, 'max-size').click()
    await expect(vectorStoresPage.maxSizeError(store.name)).toBeVisible()

    expect(puts).toEqual([])
    const stored = (await vectorStoresPage.fetchStores()).vector_stores.find((s) => s.name === store.name)
    expect(stored?.retention_days).toBe(store.retention_days)
    expect(stored?.max_size_mb).toBe(store.max_size_mb)
  })
})
