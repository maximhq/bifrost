import type { Locator } from '@playwright/test'
import { expect, test } from '../../core/fixtures/base.fixture'
import { virtualKeysApi } from '../../core/actions/api'
import type { VirtualKeysManagementPage } from './pages/virtualKeysManagement.page'
import { createVirtualKeyData } from './virtual-keys.data'

type VirtualKeyApiResponse = {
  virtual_key: {
    id: string
    name: string
    value: string
  }
}

type BulkRotateVirtualKeysApiResponse = {
  virtual_keys: Array<{
    id: string
    name: string
    value: string
  }>
  errors?: Record<string, string>
}

async function skipIfConfigStoreMissing(virtualKeysManagementPage: VirtualKeysManagementPage): Promise<void> {
  const missingConfigStore = await virtualKeysManagementPage.page
    .getByText('Config store setup is missing.')
    .isVisible({ timeout: 1000 })
    .catch(() => false)

  test.skip(missingConfigStore, 'Config store setup is missing; virtual-key E2E tests require a configured config store.')
}

// Track created VKs for management tests
const managementVKs: string[] = []

test.describe('Virtual Key Management', () => {
  test.beforeEach(async ({ virtualKeysManagementPage }) => {
    await virtualKeysManagementPage.goto()
    await skipIfConfigStoreMissing(virtualKeysManagementPage)
  })

  test.afterEach(async ({ virtualKeysManagementPage }) => {
    // Close any open sheets first
    await virtualKeysManagementPage.closeSheet()

    // Clean up all tracked VKs
    if (managementVKs.length > 0) {
      await virtualKeysManagementPage.cleanupVirtualKeys([...managementVKs])
      managementVKs.length = 0
    }
  })

  test('details sheet lists the Virtual MCPs the key is assigned to', async ({ virtualKeysManagementPage, request }) => {
    const stamp = Date.now()
    const vkName = `e2e-vk-details-vmcp-${stamp}`
    const vk = (await virtualKeysApi.create(request, { name: vkName, is_active: true })) as VirtualKeyApiResponse
    managementVKs.push(vkName)

    const vmcpName = `e2e_vk_details_vmcp_${stamp}`
    const vmcp = await request.post('/api/mcp/virtual-mcps', { data: { name: vmcpName, tools: [] } })
    expect(vmcp.status(), await vmcp.text()).toBe(201)
    const vmcpId: number = (await vmcp.json()).virtual_mcp.id

    try {
      const page = virtualKeysManagementPage.page
      await virtualKeysManagementPage.searchVirtualKeys(vkName)
      await virtualKeysManagementPage.getVirtualKeyRow(vkName).getByText(vkName, { exact: true }).click()
      await expect(page.getByTestId('vk-details-vmcp-none')).toBeVisible({ timeout: 10000 })
      await expect(page.getByTestId('vk-details-vmcp-none')).toHaveText('No Virtual MCPs assigned')
      await page.keyboard.press('Escape')

      const attached = await request.post(`/api/mcp/virtual-mcps/${vmcpId}/virtual-keys/${vk.virtual_key.id}`)
      expect(attached.ok(), await attached.text()).toBeTruthy()

      await virtualKeysManagementPage.goto()
      await virtualKeysManagementPage.searchVirtualKeys(vkName)
      await virtualKeysManagementPage.getVirtualKeyRow(vkName).getByText(vkName, { exact: true }).click()
      const badge = page.getByTestId(`vk-details-vmcp-${vmcpId}`)
      await expect(badge).toBeVisible({ timeout: 10000 })
      await expect(badge).toHaveText(vmcpName)
      await expect(page.getByTestId('vk-details-vmcp-none')).toHaveCount(0)
    } finally {
      await request.delete(`/api/mcp/virtual-mcps/${vmcpId}`).catch(() => undefined)
    }
  })

  test('should edit virtual key name', async ({ virtualKeysManagementPage }) => {
    // First create a virtual key
    const originalName = `Edit Test VK ${Date.now()}`
    const vkData = createVirtualKeyData({ name: originalName })

    await virtualKeysManagementPage.createVirtualKey(vkData)

    // Now edit it
    const updatedName = `${originalName} Updated`
    managementVKs.push(updatedName) // Track the updated name for cleanup

    await virtualKeysManagementPage.editVirtualKey(originalName, {
      name: updatedName,
    })

    // Verify updated name exists
    const vkExists = await virtualKeysManagementPage.virtualKeyExists(updatedName)
    expect(vkExists).toBe(true)
  })

  test('should edit virtual key description', async ({ virtualKeysManagementPage }) => {
    const vkName = `Desc Edit VK ${Date.now()}`
    const vkData = createVirtualKeyData({
      name: vkName,
      description: 'Original description',
    })

    managementVKs.push(vkName)
    await virtualKeysManagementPage.createVirtualKey(vkData)

    await virtualKeysManagementPage.editVirtualKey(vkName, {
      description: 'Updated description for testing',
    })

    // Virtual key should still exist
    const vkExists = await virtualKeysManagementPage.virtualKeyExists(vkName)
    expect(vkExists).toBe(true)
  })

  test('should toggle virtual key active state', async ({ virtualKeysManagementPage }) => {
    const vkName = `Toggle Active VK ${Date.now()}`
    const vkData = createVirtualKeyData({
      name: vkName,
      isActive: true,
    })

    managementVKs.push(vkName)
    await virtualKeysManagementPage.createVirtualKey(vkData)

    // Toggle to inactive
    await virtualKeysManagementPage.editVirtualKey(vkName, {
      isActive: false,
    })

    const vkExists = await virtualKeysManagementPage.virtualKeyExists(vkName)
    expect(vkExists).toBe(true)
  })

  test('should delete virtual key', async ({ virtualKeysManagementPage }) => {
    const vkName = `Delete Test VK ${Date.now()}`
    const vkData = createVirtualKeyData({ name: vkName })

    await virtualKeysManagementPage.createVirtualKey(vkData)

    // Verify it exists
    let vkExists = await virtualKeysManagementPage.virtualKeyExists(vkName)
    expect(vkExists).toBe(true)

    // Delete it (this is the test - no need to track for cleanup)
    await virtualKeysManagementPage.deleteVirtualKey(vkName)

    // Verify it's gone
    vkExists = await virtualKeysManagementPage.virtualKeyExists(vkName)
    expect(vkExists).toBe(false)
  })

  test('should view virtual key details', async ({ virtualKeysManagementPage }) => {
    const vkName = `View Details VK ${Date.now()}`
    const vkData = createVirtualKeyData({
      name: vkName,
      description: 'Detailed description for viewing',
    })

    managementVKs.push(vkName)
    await virtualKeysManagementPage.createVirtualKey(vkData)

    // Click to view details
    await virtualKeysManagementPage.viewVirtualKey(vkName)

    // Detail sheet should be visible with correct content
    await expect(virtualKeysManagementPage.sheet).toBeVisible()
    await expect(virtualKeysManagementPage.nameInput).toHaveValue(vkName)
    await expect(virtualKeysManagementPage.descriptionInput).toHaveValue('Detailed description for viewing')

    // Close the sheet (will be handled by afterEach if not)
    await virtualKeysManagementPage.closeSheet()
  })

  test('should copy virtual key value', async ({ virtualKeysManagementPage }) => {
    const vkName = `Copy Value VK ${Date.now()}`
    const vkData = createVirtualKeyData({ name: vkName })

    managementVKs.push(vkName)
    await virtualKeysManagementPage.createVirtualKey(vkData)

    // Copy the key value - method waits for success toast
    await virtualKeysManagementPage.copyVirtualKeyValue(vkName)

    // Verify copy succeeded: row still exists and key is intact
    const vkExists = await virtualKeysManagementPage.virtualKeyExists(vkName)
    expect(vkExists).toBe(true)
  })

  test('should toggle key visibility', async ({ virtualKeysManagementPage }) => {
    const vkName = `Toggle Visibility VK ${Date.now()}`
    const vkData = createVirtualKeyData({ name: vkName })

    managementVKs.push(vkName)
    await virtualKeysManagementPage.createVirtualKey(vkData)

    // Initially key is masked
    let isRevealed = await virtualKeysManagementPage.isKeyRevealed(vkName)
    expect(isRevealed).toBe(false)

    // Toggle visibility (show key)
    await virtualKeysManagementPage.toggleKeyVisibility(vkName)
    isRevealed = await virtualKeysManagementPage.isKeyRevealed(vkName)
    expect(isRevealed).toBe(true)

    // Toggle again (hide key)
    await virtualKeysManagementPage.toggleKeyVisibility(vkName)
    isRevealed = await virtualKeysManagementPage.isKeyRevealed(vkName)
    expect(isRevealed).toBe(false)
  })

  test('should set and clear content logging for a virtual key', async ({ virtualKeysManagementPage }) => {
    const vkName = `Content Logging VK ${Date.now()}`
    const vkData = createVirtualKeyData({
      name: vkName,
      contentLogging: 'disabled',
    })

    managementVKs.push(vkName)
    await virtualKeysManagementPage.createVirtualKey(vkData)

    // The choice made on create is what the editor shows when the key is reopened.
    await virtualKeysManagementPage.viewVirtualKey(vkName)
    expect(await virtualKeysManagementPage.getContentLogging()).toBe('disabled')
    await virtualKeysManagementPage.closeSheet()

    // Back to inherit: the update sends null, and the editor must not read that as "off".
    await virtualKeysManagementPage.editVirtualKey(vkName, { contentLogging: 'inherit' })
    await virtualKeysManagementPage.viewVirtualKey(vkName)
    expect(await virtualKeysManagementPage.getContentLogging()).toBe('inherit')
    await virtualKeysManagementPage.closeSheet()

    // And the third state is a decision of its own, distinct from inherit.
    await virtualKeysManagementPage.editVirtualKey(vkName, { contentLogging: 'enabled' })
    await virtualKeysManagementPage.viewVirtualKey(vkName)
    expect(await virtualKeysManagementPage.getContentLogging()).toBe('enabled')
    await virtualKeysManagementPage.closeSheet()
  })

  test.describe('Virtual Key Rotation', () => {
    test('should rotate a virtual key from the edit sheet', async ({ virtualKeysManagementPage }) => {
      const vkName = `Rotate VK ${Date.now()}`
      const vkData = createVirtualKeyData({ name: vkName })

      managementVKs.push(vkName)
      await virtualKeysManagementPage.createVirtualKey(vkData)

      const oldValue = await virtualKeysManagementPage.getDisplayedVirtualKeyValue(vkName)
      expect(oldValue).toMatch(/^sk-bf-/)

      await virtualKeysManagementPage.rotateVirtualKey(vkName)

      const newValue = await virtualKeysManagementPage.waitForVirtualKeyValueToChange(vkName, oldValue)
      expect(newValue).toMatch(/^sk-bf-/)
      expect(newValue).not.toBe(oldValue)
      expect(await virtualKeysManagementPage.virtualKeyExists(vkName)).toBe(true)
    })

    test('should leave the virtual key unchanged when rotation is cancelled', async ({ virtualKeysManagementPage }) => {
      const vkName = `Cancel Rotate VK ${Date.now()}`
      const vkData = createVirtualKeyData({ name: vkName })

      managementVKs.push(vkName)
      await virtualKeysManagementPage.createVirtualKey(vkData)

      const oldValue = await virtualKeysManagementPage.getDisplayedVirtualKeyValue(vkName)

      await virtualKeysManagementPage.cancelRotateVirtualKey(vkName)

      const currentValue = await virtualKeysManagementPage.getDisplayedVirtualKeyValue(vkName)
      expect(currentValue).toBe(oldValue)
    })

    // `bulkRotateVirtualKeys` searches per name to tick each checkbox, so this also
    // pins that a selection survives being filtered out of the current search.
    test('should bulk rotate selected virtual keys only', async ({ virtualKeysManagementPage }) => {
      const selectedOne = `Bulk Rotate One ${Date.now()}`
      const selectedTwo = `Bulk Rotate Two ${Date.now()}`
      const unselected = `Bulk Rotate Unselected ${Date.now()}`

      for (const name of [selectedOne, selectedTwo, unselected]) {
        managementVKs.push(name)
        await virtualKeysManagementPage.createVirtualKey(createVirtualKeyData({ name }))
      }

      const oldSelectedOne = await virtualKeysManagementPage.getDisplayedVirtualKeyValue(selectedOne)
      const oldSelectedTwo = await virtualKeysManagementPage.getDisplayedVirtualKeyValue(selectedTwo)
      const oldUnselected = await virtualKeysManagementPage.getDisplayedVirtualKeyValue(unselected)

      await virtualKeysManagementPage.bulkRotateVirtualKeys([selectedOne, selectedTwo])

      const newSelectedOne = await virtualKeysManagementPage.waitForVirtualKeyValueToChange(selectedOne, oldSelectedOne)
      const newSelectedTwo = await virtualKeysManagementPage.waitForVirtualKeyValueToChange(selectedTwo, oldSelectedTwo)
      const currentUnselected = await virtualKeysManagementPage.getDisplayedVirtualKeyValue(unselected)

      expect(newSelectedOne).not.toBe(oldSelectedOne)
      expect(newSelectedTwo).not.toBe(oldSelectedTwo)
      expect(currentUnselected).toBe(oldUnselected)
    })

    // A reload failure cannot be forced from the browser, so these rotate for real and add the server's warning to the response.
    test('should warn when a rotated key is saved but not loaded', async ({ virtualKeysManagementPage }) => {
      const vkName = `Rotate Warning VK ${Date.now()}`
      managementVKs.push(vkName)
      await virtualKeysManagementPage.createVirtualKey(createVirtualKeyData({ name: vkName }))
      const oldValue = await virtualKeysManagementPage.getDisplayedVirtualKeyValue(vkName)

      await virtualKeysManagementPage.page.route('**/api/governance/virtual-keys/*/rotate', async (route) => {
        const response = await route.fetch()
        await route.fulfill({ response, json: { ...(await response.json()), warning: 'not loaded' } })
      })
      await virtualKeysManagementPage.rotateVirtualKey(vkName, ['The old value may still work'])

      const newValue = await virtualKeysManagementPage.waitForVirtualKeyValueToChange(vkName, oldValue)
      expect(newValue).toMatch(/^sk-bf-/)
    })

    for (const withError of [false, true]) {
      test(`should warn on bulk rotation with ${withError ? 'warnings and errors' : 'only warnings'}`, async ({ virtualKeysManagementPage }) => {
        const first = `Bulk Warning One ${Date.now()}`
        const second = `Bulk Warning Two ${Date.now()}`
        for (const name of [first, second]) {
          managementVKs.push(name)
          await virtualKeysManagementPage.createVirtualKey(createVirtualKeyData({ name }))
        }
        const oldFirst = await virtualKeysManagementPage.getDisplayedVirtualKeyValue(first)
        const oldSecond = await virtualKeysManagementPage.getDisplayedVirtualKeyValue(second)

        await virtualKeysManagementPage.page.route('**/api/governance/virtual-keys/rotate', async (route) => {
          const response = await route.fetch()
          const body = (await response.json()) as BulkRotateVirtualKeysApiResponse
          const warnings = Object.fromEntries(body.virtual_keys.map((vk) => [vk.id, 'not loaded']))
          const errors = withError ? { 'missing-vk-id': 'virtual key not found' } : undefined
          await route.fulfill({ response, json: { ...body, warnings, errors } })
        })
        await virtualKeysManagementPage.bulkRotateVirtualKeys(
          [first, second],
          [withError ? 'Rotated 2 virtual keys. 1 failed.' : 'Rotated 2 virtual keys.', 'Keys saved but possibly not loaded: 2', 'old values may still work'],
        )

        expect(await virtualKeysManagementPage.waitForVirtualKeyValueToChange(first, oldFirst)).toMatch(/^sk-bf-/)
        expect(await virtualKeysManagementPage.waitForVirtualKeyValueToChange(second, oldSecond)).toMatch(/^sk-bf-/)
      })
    }

    test('should rotate a virtual key through the API', async ({ request }) => {
      const vkName = `API Rotate VK ${Date.now()}`
      const createResp = (await virtualKeysApi.create(request, {
        name: vkName,
        description: 'API rotation test',
        is_active: true,
      })) as VirtualKeyApiResponse

      managementVKs.push(vkName)
      const oldValue = createResp.virtual_key.value

      const rotateResp = (await virtualKeysApi.rotate(request, createResp.virtual_key.id)) as VirtualKeyApiResponse
      expect(rotateResp.virtual_key.value).toMatch(/^sk-bf-/)
      expect(rotateResp.virtual_key.value).not.toBe(oldValue)

      const getResp = (await virtualKeysApi.get(request, createResp.virtual_key.id)) as VirtualKeyApiResponse
      expect(getResp.virtual_key.value).toBe(rotateResp.virtual_key.value)
    })

    test('should bulk rotate valid API IDs and report missing IDs', async ({ request }) => {
      const firstName = `API Bulk Rotate One ${Date.now()}`
      const secondName = `API Bulk Rotate Two ${Date.now()}`

      const first = (await virtualKeysApi.create(request, {
        name: firstName,
        description: 'API bulk rotation test one',
        is_active: true,
      })) as VirtualKeyApiResponse
      const second = (await virtualKeysApi.create(request, {
        name: secondName,
        description: 'API bulk rotation test two',
        is_active: true,
      })) as VirtualKeyApiResponse

      managementVKs.push(firstName, secondName)

      const bulkResp = (await virtualKeysApi.bulkRotate(request, [
        first.virtual_key.id,
        'missing-vk-id',
        second.virtual_key.id,
      ])) as BulkRotateVirtualKeysApiResponse

      expect(bulkResp.virtual_keys.map((vk) => vk.id).sort()).toEqual([
        first.virtual_key.id,
        second.virtual_key.id,
      ].sort())
      expect(bulkResp.errors?.['missing-vk-id']).toBe('virtual key not found')

      const rotatedByID = new Map(bulkResp.virtual_keys.map((vk) => [vk.id, vk.value]))
      expect(rotatedByID.get(first.virtual_key.id)).not.toBe(first.virtual_key.value)
      expect(rotatedByID.get(second.virtual_key.id)).not.toBe(second.virtual_key.value)
    })
  })
})

const VK_SHEET_WIDTH_KEY = 'bifrost.sheet.width.virtual-key'

test.describe('Virtual Key Sheet Resizing', () => {
  test.beforeEach(async ({ virtualKeysManagementPage }) => {
    await virtualKeysManagementPage.goto()
    await skipIfConfigStoreMissing(virtualKeysManagementPage)
    await virtualKeysManagementPage.page.evaluate((key) => localStorage.removeItem(key), VK_SHEET_WIDTH_KEY)
  })

  test.afterEach(async ({ virtualKeysManagementPage }) => {
    await virtualKeysManagementPage.closeSheet()
    await virtualKeysManagementPage.page.evaluate((key) => localStorage.removeItem(key), VK_SHEET_WIDTH_KEY)
  })

  async function openSheet(virtualKeysManagementPage: VirtualKeysManagementPage) {
    await virtualKeysManagementPage.createBtn.click()
    await expect(virtualKeysManagementPage.sheet).toBeVisible()
    // The sheet slides in with a transform, and the fixed handle moves with it until the
    // animation ends. Wait for it so handle positions read below are final.
    await virtualKeysManagementPage.sheet.evaluate((el) => Promise.all(el.getAnimations().map((a) => a.finished)))
    const handle = virtualKeysManagementPage.sheet.getByTestId('sheet-resize-handle')
    await expect(handle).toBeVisible()
    return handle
  }

  async function expectHandleInViewport(handle: Locator, viewportWidth: number) {
    const box = await handle.boundingBox()
    expect(box, 'resize handle has no bounding box').not.toBeNull()
    expect(box!.x).toBeGreaterThanOrEqual(0)
    expect(box!.x + box!.width).toBeLessThanOrEqual(viewportWidth)
  }

  async function sheetWidth(virtualKeysManagementPage: VirtualKeysManagementPage): Promise<number> {
    const box = await virtualKeysManagementPage.sheet.boundingBox()
    expect(box, 'sheet has no bounding box').not.toBeNull()
    return Math.round(box!.width)
  }

  test('dragging the left edge widens the sheet and the width is restored after a reload', async ({
    virtualKeysManagementPage,
  }) => {
    const page = virtualKeysManagementPage.page
    const handle = await openSheet(virtualKeysManagementPage)
    // The sheet slides in; wait for the width to settle before measuring the default.
    await expect.poll(() => sheetWidth(virtualKeysManagementPage)).toBeGreaterThan(300)
    const defaultWidth = await sheetWidth(virtualKeysManagementPage)

    const handleBox = await handle.boundingBox()
    expect(handleBox, 'resize handle has no bounding box').not.toBeNull()
    const y = handleBox!.y + handleBox!.height / 2
    await page.mouse.move(handleBox!.x + handleBox!.width / 2, y)
    await page.mouse.down()
    // Release over the overlay: a drag must not dismiss the sheet.
    await page.mouse.move(handleBox!.x + handleBox!.width / 2 - 200, y, { steps: 8 })
    await page.mouse.up()

    await expect(virtualKeysManagementPage.sheet).toBeVisible()
    const widened = await sheetWidth(virtualKeysManagementPage)
    expect(Math.abs(widened - (defaultWidth + 200))).toBeLessThanOrEqual(2)
    const stored = await page.evaluate((key) => localStorage.getItem(key), VK_SHEET_WIDTH_KEY)
    expect(Number(stored)).toBe(widened)

    await virtualKeysManagementPage.closeSheet()
    await page.reload()
    await openSheet(virtualKeysManagementPage)
    await expect.poll(() => sheetWidth(virtualKeysManagementPage)).toBe(widened)

    // Double-click resets to the default width and forgets the saved one.
    await virtualKeysManagementPage.sheet.getByTestId('sheet-resize-handle').dblclick()
    await expect.poll(() => sheetWidth(virtualKeysManagementPage)).toBe(defaultWidth)
    expect(await page.evaluate((key) => localStorage.getItem(key), VK_SHEET_WIDTH_KEY)).toBeNull()
  })

  test('the resize handle works from the keyboard', async ({ virtualKeysManagementPage }) => {
    const handle = await openSheet(virtualKeysManagementPage)
    await expect.poll(() => sheetWidth(virtualKeysManagementPage)).toBeGreaterThan(300)
    const defaultWidth = await sheetWidth(virtualKeysManagementPage)

    await expect(handle).toHaveAttribute('role', 'separator')
    await expect(handle).toHaveAttribute('aria-orientation', 'vertical')
    await handle.focus()

    await handle.press('ArrowLeft')
    await expect(handle).toHaveAttribute('aria-valuenow', String(defaultWidth + 16))
    await expect.poll(() => sheetWidth(virtualKeysManagementPage)).toBe(defaultWidth + 16)

    await handle.press('ArrowRight')
    await handle.press('ArrowRight')
    await expect(handle).toHaveAttribute('aria-valuenow', String(defaultWidth - 16))

    await handle.press('Home')
    await expect.poll(() => sheetWidth(virtualKeysManagementPage)).toBe(defaultWidth)
    expect(
      await virtualKeysManagementPage.page.evaluate((key) => localStorage.getItem(key), VK_SHEET_WIDTH_KEY),
    ).toBeNull()
  })

  test('a saved width wider than the viewport keeps the handle on screen', async ({ virtualKeysManagementPage }) => {
    const page = virtualKeysManagementPage.page
    await page.setViewportSize({ width: 1280, height: 800 })
    await page.evaluate((key) => localStorage.setItem(key, '1800'), VK_SHEET_WIDTH_KEY)

    const handle = await openSheet(virtualKeysManagementPage)
    // CSS caps the sheet at the viewport less the 16px gutter, and the handle follows it.
    await expect.poll(() => sheetWidth(virtualKeysManagementPage)).toBe(1264)
    await expect(handle).toHaveAttribute('aria-valuenow', '1264')
    await expectHandleInViewport(handle, 1280)
    // The saved preference is kept for when the window is wide again.
    expect(await page.evaluate((key) => localStorage.getItem(key), VK_SHEET_WIDTH_KEY)).toBe('1800')
  })

  test('the handle follows the sheet when the viewport shrinks while it is open', async ({ virtualKeysManagementPage }) => {
    const page = virtualKeysManagementPage.page
    await page.setViewportSize({ width: 1920, height: 1000 })
    await page.evaluate((key) => localStorage.setItem(key, '1700'), VK_SHEET_WIDTH_KEY)

    const handle = await openSheet(virtualKeysManagementPage)
    await expect.poll(() => sheetWidth(virtualKeysManagementPage)).toBe(1700)
    await expect(handle).toHaveAttribute('aria-valuenow', '1700')

    await page.setViewportSize({ width: 1280, height: 800 })
    await expect.poll(() => sheetWidth(virtualKeysManagementPage)).toBe(1264)
    await expect(handle).toHaveAttribute('aria-valuenow', '1264')
    await expectHandleInViewport(handle, 1280)
  })
})
