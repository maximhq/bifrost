import { customersApi } from '../../core/actions/api'
import { expect, test } from '../../core/fixtures/base.fixture'
import { createCustomerData, createTeamData } from './governance.data'

const createdTeams: string[] = []
const createdCustomers: string[] = []

test.describe('Governance - Teams', () => {
  test.describe.configure({ mode: 'serial' })
  test.beforeEach(async ({ governancePage }) => {
    await governancePage.gotoTeams()
  })

  test.afterEach(async ({ governancePage }) => {
    await governancePage.closeTeamDialog()
    for (const name of [...createdTeams]) {
      try {
        const exists = await governancePage.teamExists(name)
        if (exists) {
          await governancePage.deleteTeam(name)
        }
      } catch (e) {
        console.error(`[CLEANUP] Failed to delete team ${name}:`, e)
      }
    }
    createdTeams.length = 0
    for (const name of [...createdCustomers]) {
      try {
        await governancePage.gotoCustomers()
        const exists = await governancePage.customerExists(name)
        if (exists) {
          await governancePage.deleteCustomer(name)
        }
      } catch (e) {
        console.error(`[CLEANUP] Failed to delete customer ${name}:`, e)
      }
    }
    createdCustomers.length = 0
  })

  test('should display create team button or empty state', async ({ governancePage }) => {
    const createVisible = await governancePage.teamsCreateBtn.isVisible().catch(() => false)
    const emptyAddVisible = await governancePage.page.getByTestId('team-button-add').isVisible().catch(() => false)
    expect(createVisible || emptyAddVisible).toBe(true)
  })

  test('should create a team', async ({ governancePage }) => {
    const teamData = createTeamData({ name: `E2E Test Team ${Date.now()}` })
    createdTeams.push(teamData.name)

    await governancePage.createTeam(teamData)

    const exists = await governancePage.teamExists(teamData.name)
    expect(exists).toBe(true)
  })

  test('should edit a team', async ({ governancePage }) => {
    const teamData = createTeamData({ name: `E2E Edit Team ${Date.now()}` })
    createdTeams.push(teamData.name)
    await governancePage.createTeam(teamData)

    await governancePage.editTeam(teamData.name, { budget: { maxLimit: 129 } })

    const exists = await governancePage.teamExists(teamData.name)
    expect(exists).toBe(true)
  })

  test('should create team with customer assignment', async ({ governancePage }) => {
    // 1. Create a customer (UI)
    const customerData = createCustomerData({ name: `E2E Customer For Team ${Date.now()}` })
    createdCustomers.push(customerData.name)
    await governancePage.gotoCustomers()
    await governancePage.createCustomer(customerData)

    // 2. Go to Teams and create a team, assign the customer from the create-team dropdown (UI)
    await governancePage.gotoTeams()
    const teamData = createTeamData({
      name: `E2E Team With Customer ${Date.now()}`,
      customerName: customerData.name,
    })
    createdTeams.push(teamData.name)
    await governancePage.createTeam(teamData)

    // 3. Validate in UI that the customer was assigned (via data-testid)
    const exists = await governancePage.teamExists(teamData.name)
    expect(exists).toBe(true)
    const customerCell = governancePage.getTeamRowCustomerCell(teamData.name)
    await expect(customerCell).toContainText(customerData.name)
  })

  test('customer picker reaches customers past the first page', async ({ governancePage, request }) => {
    // The picker fetches 20 rows a page; seed more than that under one prefix.
    const prefix = `E2E Pager ${Date.now()}`
    const ids: string[] = []
    try {
      for (let i = 0; i < 23; i++) {
        const created = await customersApi.create(request, { name: `${prefix} ${String(i).padStart(2, '0')}` })
        ids.push((created as { customer: { id: string } }).customer.id)
      }

      await governancePage.teamsCreateBtn.click()
      await expect(governancePage.teamDialog).toBeVisible({ timeout: 5000 })
      await governancePage.page.getByTestId('team-customer-selector').getByRole('combobox').click()
      const search = governancePage.page.getByPlaceholder('Search customers...')
      await search.fill(prefix)

      const options = governancePage.page.getByRole('option').filter({ hasText: prefix })
      await expect(options).toHaveCount(20, { timeout: 10000 })

      // Scrolling to the bottom loads the next page.
      const list = governancePage.page.locator('[data-slot="search-select-list"]')
      await expect
        .poll(
          async () => {
            await list.evaluate((el) => el.scrollTo({ top: el.scrollHeight }))
            return options.count()
          },
          { timeout: 10000 },
        )
        .toBe(23)
    } finally {
      for (const id of ids) await customersApi.delete(request, id)
    }
  })

  test('should delete a team', async ({ governancePage }) => {
    const teamData = createTeamData({ name: `E2E Delete Team ${Date.now()}` })
    createdTeams.push(teamData.name)
    await governancePage.createTeam(teamData)

    let exists = await governancePage.teamExists(teamData.name)
    expect(exists).toBe(true)

    await governancePage.deleteTeam(teamData.name)
    const idx = createdTeams.indexOf(teamData.name)
    if (idx >= 0) createdTeams.splice(idx, 1)

    exists = await governancePage.teamExists(teamData.name)
    expect(exists).toBe(false)
  })
})

test.describe('Governance - Customers', () => {
  test.describe.configure({ mode: 'serial' })
  test.beforeEach(async ({ governancePage }) => {
    await governancePage.gotoCustomers()
  })

  test.afterEach(async ({ governancePage }) => {
    for (const name of [...createdCustomers]) {
      try {
        const exists = await governancePage.customerExists(name)
        if (exists) {
          await governancePage.deleteCustomer(name)
        }
      } catch (e) {
        console.error(`[CLEANUP] Failed to delete customer ${name}:`, e)
      }
    }
    createdCustomers.length = 0
  })

  test('should display create customer button or empty state', async ({ governancePage }) => {
    const createVisible = await governancePage.customersCreateBtn.isVisible().catch(() => false)
    const emptyCreateVisible = await governancePage.page.getByTestId('customer-button-create').isVisible().catch(() => false)
    expect(createVisible || emptyCreateVisible).toBe(true)
  })

  test('should not show a limit value as the rate limit placeholder', async ({ governancePage }) => {
    await governancePage.customersCreateBtn.click()
    await expect(governancePage.customerDialog).toBeVisible({ timeout: 5000 })

    // An empty field means no limit, so the placeholder must not read like a value.
    for (const testId of ['customer-token-max-limit-input', 'customer-request-max-limit-input']) {
      await expect(governancePage.customerDialog.getByTestId(testId)).toHaveAttribute('placeholder', 'No limit')
    }

    await governancePage.page.keyboard.press('Escape')
    await expect(governancePage.customerDialog).not.toBeVisible({ timeout: 5000 })
  })

  test('should create a customer', async ({ governancePage }) => {
    const customerData = createCustomerData({ name: `E2E Test Customer ${Date.now()}` })
    createdCustomers.push(customerData.name)

    await governancePage.createCustomer(customerData)

    const exists = await governancePage.customerExists(customerData.name)
    expect(exists).toBe(true)
  })

  test('should edit a customer', async ({ governancePage }) => {
    const customerData = createCustomerData({ name: `E2E Edit Customer ${Date.now()}` })
    createdCustomers.push(customerData.name)
    await governancePage.createCustomer(customerData)

    const newName = `E2E Edited Customer ${Date.now()}`
    createdCustomers[createdCustomers.length - 1] = newName
    await governancePage.editCustomer(customerData.name, { name: newName })

    const oldExists = await governancePage.customerExists(customerData.name)
    const newExists = await governancePage.customerExists(newName)
    expect(oldExists).toBe(false)
    expect(newExists).toBe(true)
  })

  test('should delete a customer', async ({ governancePage }) => {
    const customerData = createCustomerData({ name: `E2E Delete Customer ${Date.now()}` })
    createdCustomers.push(customerData.name)
    await governancePage.createCustomer(customerData)

    let exists = await governancePage.customerExists(customerData.name)
    expect(exists).toBe(true)

    await governancePage.deleteCustomer(customerData.name)
    const idx = createdCustomers.indexOf(customerData.name)
    if (idx >= 0) createdCustomers.splice(idx, 1)

    exists = await governancePage.customerExists(customerData.name)
    expect(exists).toBe(false)
  })
})
