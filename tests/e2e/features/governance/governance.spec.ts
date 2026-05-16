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
