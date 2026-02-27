import { APIRequestContext, APIResponse } from '@playwright/test'

/**
 * API helper functions for test setup and cleanup
 */

const API_BASE = '/api'

/**
 * Handle API response with error checking
 */
async function handleResponse<T>(response: APIResponse, operation: string): Promise<T> {
  if (!response.ok()) {
    throw new Error(`${operation} failed: ${response.status()} ${response.statusText()}`)
  }
  return response.json() as Promise<T>
}

/**
 * Provider API helpers
 */
export const providersApi = {
  /**
   * Get all providers
   */
  async getAll(request: APIRequestContext) {
    const response = await request.get(`${API_BASE}/providers`)
    return handleResponse(response, 'Get all providers')
  },

  /**
   * Get a specific provider
   */
  async get(request: APIRequestContext, name: string) {
    const response = await request.get(`${API_BASE}/providers/${name}`)
    return handleResponse(response, `Get provider ${name}`)
  },

  /**
   * Create a provider
   */
  async create(request: APIRequestContext, data: unknown) {
    const response = await request.post(`${API_BASE}/providers`, {
      data,
    })
    return handleResponse(response, 'Create provider')
  },

  /**
   * Update a provider
   */
  async update(request: APIRequestContext, name: string, data: unknown) {
    const response = await request.put(`${API_BASE}/providers/${name}`, {
      data,
    })
    return handleResponse(response, `Update provider ${name}`)
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
    return handleResponse(response, 'Get all virtual keys')
  },

  /**
   * Get a specific virtual key
   */
  async get(request: APIRequestContext, id: string) {
    const response = await request.get(`${API_BASE}/governance/virtual-keys/${id}`)
    return handleResponse(response, `Get virtual key ${id}`)
  },

  /**
   * Create a virtual key
   */
  async create(request: APIRequestContext, data: unknown) {
    const response = await request.post(`${API_BASE}/governance/virtual-keys`, {
      data,
    })
    return handleResponse(response, 'Create virtual key')
  },

  /**
   * Update a virtual key
   */
  async update(request: APIRequestContext, id: string, data: unknown) {
    const response = await request.put(`${API_BASE}/governance/virtual-keys/${id}`, {
      data,
    })
    return handleResponse(response, `Update virtual key ${id}`)
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
    return handleResponse(response, 'Get all teams')
  },

  /**
   * Create a team
   */
  async create(request: APIRequestContext, data: unknown) {
    const response = await request.post(`${API_BASE}/governance/teams`, {
      data,
    })
    return handleResponse(response, 'Create team')
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
    return handleResponse(response, 'Get all customers')
  },

  /**
   * Create a customer
   */
  async create(request: APIRequestContext, data: unknown) {
    const response = await request.post(`${API_BASE}/governance/customers`, {
      data,
    })
    return handleResponse(response, 'Create customer')
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
