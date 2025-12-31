import { APIRequestContext } from '@playwright/test'

/**
 * API helper functions for test setup and cleanup
 */

const API_BASE = '/api'

/**
 * Provider API helpers
 */
export const providersApi = {
  /**
   * Get all providers
   */
  async getAll(request: APIRequestContext) {
    const response = await request.get(`${API_BASE}/providers`)
    return response.json()
  },

  /**
   * Get a specific provider
   */
  async get(request: APIRequestContext, name: string) {
    const response = await request.get(`${API_BASE}/providers/${name}`)
    return response.json()
  },

  /**
   * Create a provider
   */
  async create(request: APIRequestContext, data: any) {
    const response = await request.post(`${API_BASE}/providers`, {
      data,
    })
    return response.json()
  },

  /**
   * Update a provider
   */
  async update(request: APIRequestContext, name: string, data: any) {
    const response = await request.put(`${API_BASE}/providers/${name}`, {
      data,
    })
    return response.json()
  },

  /**
   * Delete a provider
   */
  async delete(request: APIRequestContext, name: string) {
    const response = await request.delete(`${API_BASE}/providers/${name}`)
    return response.ok()
  },
}

/**
 * Virtual Keys API helpers
 */
export const virtualKeysApi = {
  /**
   * Get all virtual keys
   */
  async getAll(request: APIRequestContext) {
    const response = await request.get(`${API_BASE}/governance/virtual-keys`)
    return response.json()
  },

  /**
   * Get a specific virtual key
   */
  async get(request: APIRequestContext, id: string) {
    const response = await request.get(`${API_BASE}/governance/virtual-keys/${id}`)
    return response.json()
  },

  /**
   * Create a virtual key
   */
  async create(request: APIRequestContext, data: any) {
    const response = await request.post(`${API_BASE}/governance/virtual-keys`, {
      data,
    })
    return response.json()
  },

  /**
   * Update a virtual key
   */
  async update(request: APIRequestContext, id: string, data: any) {
    const response = await request.put(`${API_BASE}/governance/virtual-keys/${id}`, {
      data,
    })
    return response.json()
  },

  /**
   * Delete a virtual key
   */
  async delete(request: APIRequestContext, id: string) {
    const response = await request.delete(`${API_BASE}/governance/virtual-keys/${id}`)
    return response.ok()
  },
}

/**
 * Teams API helpers
 */
export const teamsApi = {
  /**
   * Get all teams
   */
  async getAll(request: APIRequestContext) {
    const response = await request.get(`${API_BASE}/governance/teams`)
    return response.json()
  },

  /**
   * Create a team
   */
  async create(request: APIRequestContext, data: any) {
    const response = await request.post(`${API_BASE}/governance/teams`, {
      data,
    })
    return response.json()
  },

  /**
   * Delete a team
   */
  async delete(request: APIRequestContext, id: string) {
    const response = await request.delete(`${API_BASE}/governance/teams/${id}`)
    return response.ok()
  },
}

/**
 * Customers API helpers
 */
export const customersApi = {
  /**
   * Get all customers
   */
  async getAll(request: APIRequestContext) {
    const response = await request.get(`${API_BASE}/governance/customers`)
    return response.json()
  },

  /**
   * Create a customer
   */
  async create(request: APIRequestContext, data: any) {
    const response = await request.post(`${API_BASE}/governance/customers`, {
      data,
    })
    return response.json()
  },

  /**
   * Delete a customer
   */
  async delete(request: APIRequestContext, id: string) {
    const response = await request.delete(`${API_BASE}/governance/customers/${id}`)
    return response.ok()
  },
}

/**
 * Cleanup helper - delete all test data
 */
export async function cleanupTestData(
  request: APIRequestContext,
  options: {
    virtualKeyIds?: string[]
    teamIds?: string[]
    customerIds?: string[]
    providerNames?: string[]
  }
): Promise<void> {
  const { virtualKeyIds = [], teamIds = [], customerIds = [], providerNames = [] } = options

  // Delete virtual keys first (they may depend on teams/customers)
  for (const id of virtualKeyIds) {
    try {
      await virtualKeysApi.delete(request, id)
    } catch (e) {
      // Ignore errors during cleanup
    }
  }

  // Delete teams
  for (const id of teamIds) {
    try {
      await teamsApi.delete(request, id)
    } catch (e) {
      // Ignore errors during cleanup
    }
  }

  // Delete customers
  for (const id of customerIds) {
    try {
      await customersApi.delete(request, id)
    } catch (e) {
      // Ignore errors during cleanup
    }
  }

  // Delete custom providers
  for (const name of providerNames) {
    try {
      await providersApi.delete(request, name)
    } catch (e) {
      // Ignore errors during cleanup
    }
  }
}
