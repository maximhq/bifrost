import { expect, test } from '../../core/fixtures/base.fixture'
import { customersApi } from '../../core/actions/api'
import { createCustomerData, createTeamData } from './governance.data'

const createdTeams: string[] = []
const createdCustomers: string[] = []

test.describe('Governance - Teams', () => {
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

    const newName = `E2E Edited Team ${Date.now()}`
    createdTeams[createdTeams.length - 1] = newName
    await governancePage.editTeam(teamData.name, { name: newName })

    const oldExists = await governancePage.teamExists(teamData.name)
    const newExists = await governancePage.teamExists(newName)
    expect(oldExists).toBe(false)
    expect(newExists).toBe(true)
  })

  test('should create team with customer assignment', async ({ governancePage, request }) => {
    const customerData = createCustomerData({ name: `E2E Customer For Team ${Date.now()}` })
    createdCustomers.push(customerData.name)
    await governancePage.gotoCustomers()
    await governancePage.createCustomer(customerData)

    const customers = await customersApi.getAll(request)
    const customer = customers.find((c: { name: string }) => c.name === customerData.name)
    expect(customer).toBeDefined()
    const customerId = (customer as { id: string }).id

    await governancePage.gotoTeams()
    const teamData = createTeamData({
      name: `E2E Team With Customer ${Date.now()}`,
      customerId,
    })
    createdTeams.push(teamData.name)
    await governancePage.createTeam(teamData)

    const exists = await governancePage.teamExists(teamData.name)
    expect(exists).toBe(true)
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
