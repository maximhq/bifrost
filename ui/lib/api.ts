import axios, { AxiosInstance, isAxiosError } from 'axios'
import {
  ListProvidersResponse,
  ProviderResponse,
  CoreConfig,
  AddProviderRequest,
  UpdateProviderRequest,
  BifrostErrorResponse,
} from '@/lib/types/config'
import { MCPClient, CreateMCPClientRequest, UpdateMCPClientRequest } from '@/lib/types/mcp'
import { LogEntry, LogFilters, LogStats, Pagination } from './types/logs'
import {
  VirtualKey,
  Team,
  Customer,
  Budget,
  RateLimit,
  CreateVirtualKeyRequest,
  UpdateVirtualKeyRequest,
  CreateTeamRequest,
  UpdateTeamRequest,
  CreateCustomerRequest,
  UpdateCustomerRequest,
  UpdateBudgetRequest,
  UpdateRateLimitRequest,
  ResetUsageRequest,
  GetVirtualKeysResponse,
  GetTeamsResponse,
  GetCustomersResponse,
  GetBudgetsResponse,
  GetRateLimitsResponse,
  GetUsageStatsResponse,
  DebugStatsResponse,
  HealthCheckResponse,
} from '@/lib/types/governance'
import { getApiBaseUrl } from '@/lib/utils/port'

type ServiceResponse<T> = Promise<[T | null, string | null]>

class ApiService {
  private client: AxiosInstance

  constructor() {
    // Use the centralized port utility for API base URL generation
    const baseURL = getApiBaseUrl()

    this.client = axios.create({
      baseURL,
      headers: {
        'Content-Type': 'application/json',
      },
    })
  }

  private getErrorMessage(error: unknown): string {
    if (isAxiosError(error) && error.response) {
      const errorData = error.response.data as BifrostErrorResponse
      if (errorData.error && errorData.error.message) {
        return errorData.error.message
      }
    }
    if (error instanceof Error) {
      return error.message || 'An unexpected error occurred.'
    }
    return 'An unexpected error occurred.'
  }

  async getLogs(
    filters: LogFilters,
    pagination: Pagination,
  ): ServiceResponse<{
    logs: LogEntry[]
    pagination: Pagination
    stats: LogStats
  }> {
    try {
      const params: Record<string, string | number> = {
        limit: pagination.limit,
        offset: pagination.offset,
        sort_by: pagination.sort_by,
        order: pagination.order,
      }

      // Add filters to params if they exist
      if (filters.providers && filters.providers.length > 0) {
        params.providers = filters.providers.join(',')
      }
      if (filters.models && filters.models.length > 0) {
        params.models = filters.models.join(',')
      }
      if (filters.status && filters.status.length > 0) {
        params.status = filters.status.join(',')
      }
      if (filters.objects && filters.objects.length > 0) {
        params.objects = filters.objects.join(',')
      }
      if (filters.start_time) params.start_time = filters.start_time
      if (filters.end_time) params.end_time = filters.end_time
      if (filters.min_latency) params.min_latency = filters.min_latency
      if (filters.max_latency) params.max_latency = filters.max_latency
      if (filters.min_tokens) params.min_tokens = filters.min_tokens
      if (filters.max_tokens) params.max_tokens = filters.max_tokens
      if (filters.content_search) params.content_search = filters.content_search

      const response = await this.client.get('/logs', { params })
      return [response.data, null]
    } catch (error) {
      return [null, this.getErrorMessage(error)]
    }
  }

  async getDroppedRequests(): ServiceResponse<{ dropped_requests: number }> {
    try {
      const response = await this.client.get('/logs/dropped')
      return [response.data, null]
    } catch (error) {
      return [null, this.getErrorMessage(error)]
    }
  }

  // Provider endpoints
  async getProviders(): ServiceResponse<ListProvidersResponse> {
    try {
      const response = await this.client.get('/providers')
      return [response.data, null]
    } catch (error) {
      return [null, this.getErrorMessage(error)]
    }
  }

  async getProvider(provider: string): ServiceResponse<ProviderResponse> {
    try {
      const response = await this.client.get(`/providers/${provider}`)
      return [response.data, null]
    } catch (error) {
      return [null, this.getErrorMessage(error)]
    }
  }

  async createProvider(data: AddProviderRequest): ServiceResponse<ProviderResponse> {
    try {
      const response = await this.client.post('/providers', data)
      return [response.data, null]
    } catch (error) {
      return [null, this.getErrorMessage(error)]
    }
  }

  async updateProvider(provider: string, data: UpdateProviderRequest): ServiceResponse<ProviderResponse> {
    try {
      const response = await this.client.put(`/providers/${provider}`, data)
      return [response.data, null]
    } catch (error) {
      return [null, this.getErrorMessage(error)]
    }
  }

  async deleteProvider(provider: string): ServiceResponse<ProviderResponse> {
    try {
      const response = await this.client.delete(`/providers/${provider}`)
      return [response.data, null]
    } catch (error) {
      return [null, this.getErrorMessage(error)]
    }
  }

  // MCP endpoints
  async getMCPClients(): ServiceResponse<MCPClient[]> {
    try {
      const response = await this.client.get('/mcp/clients')
      return [response.data, null]
    } catch (error) {
      return [null, this.getErrorMessage(error)]
    }
  }

  async createMCPClient(data: CreateMCPClientRequest): ServiceResponse<null> {
    try {
      await this.client.post('/mcp/client', data)
      return [null, null]
    } catch (error) {
      return [null, this.getErrorMessage(error)]
    }
  }

  async updateMCPClient(name: string, data: UpdateMCPClientRequest): ServiceResponse<null> {
    try {
      await this.client.put(`/mcp/client/${name}`, data)
      return [null, null]
    } catch (error) {
      return [null, this.getErrorMessage(error)]
    }
  }

  async deleteMCPClient(name: string): ServiceResponse<null> {
    try {
      await this.client.delete(`/mcp/client/${name}`)
      return [null, null]
    } catch (error) {
      return [null, this.getErrorMessage(error)]
    }
  }

  async reconnectMCPClient(name: string): ServiceResponse<null> {
    try {
      await this.client.post(`/mcp/client/${name}/reconnect`)
      return [null, null]
    } catch (error) {
      return [null, this.getErrorMessage(error)]
    }
  }

  // Config endpoints

  async getCoreConfig(fromDB: boolean = false): ServiceResponse<CoreConfig> {
    try {
      const response = await this.client.get(`/config?from_db=${fromDB}`)
      return [response.data, null]
    } catch (error) {
      return [null, this.getErrorMessage(error)]
    }
  }

  async updateCoreConfig(data: CoreConfig): ServiceResponse<null> {
    try {
      await this.client.put('/config', data)
      return [null, null]
    } catch (error) {
      return [null, this.getErrorMessage(error)]
    }
  }

  // Governance endpoints

  // Virtual Keys
  async getVirtualKeys(): ServiceResponse<GetVirtualKeysResponse> {
    try {
      const response = await this.client.get('/governance/virtual-keys')
      return [response.data, null]
    } catch (error) {
      return [null, this.getErrorMessage(error)]
    }
  }

  async getVirtualKey(vkId: string): ServiceResponse<{ virtual_key: VirtualKey }> {
    try {
      const response = await this.client.get(`/governance/virtual-keys/${vkId}`)
      return [response.data, null]
    } catch (error) {
      return [null, this.getErrorMessage(error)]
    }
  }

  async createVirtualKey(data: CreateVirtualKeyRequest): ServiceResponse<{ message: string; virtual_key: VirtualKey }> {
    try {
      const response = await this.client.post('/governance/virtual-keys', data)
      return [response.data, null]
    } catch (error) {
      return [null, this.getErrorMessage(error)]
    }
  }

  async updateVirtualKey(vkId: string, data: UpdateVirtualKeyRequest): ServiceResponse<{ message: string; virtual_key: VirtualKey }> {
    try {
      const response = await this.client.put(`/governance/virtual-keys/${vkId}`, data)
      return [response.data, null]
    } catch (error) {
      return [null, this.getErrorMessage(error)]
    }
  }

  async deleteVirtualKey(vkId: string): ServiceResponse<{ message: string }> {
    try {
      const response = await this.client.delete(`/governance/virtual-keys/${vkId}`)
      return [response.data, null]
    } catch (error) {
      return [null, this.getErrorMessage(error)]
    }
  }

  // Teams
  async getTeams(customerId?: string): ServiceResponse<GetTeamsResponse> {
    try {
      const params = customerId ? { customer_id: customerId } : {}
      const response = await this.client.get('/governance/teams', { params })
      return [response.data, null]
    } catch (error) {
      return [null, this.getErrorMessage(error)]
    }
  }

  async getTeam(teamId: string): ServiceResponse<{ team: Team }> {
    try {
      const response = await this.client.get(`/governance/teams/${teamId}`)
      return [response.data, null]
    } catch (error) {
      return [null, this.getErrorMessage(error)]
    }
  }

  async createTeam(data: CreateTeamRequest): ServiceResponse<{ message: string; team: Team }> {
    try {
      const response = await this.client.post('/governance/teams', data)
      return [response.data, null]
    } catch (error) {
      return [null, this.getErrorMessage(error)]
    }
  }

  async updateTeam(teamId: string, data: UpdateTeamRequest): ServiceResponse<{ message: string; team: Team }> {
    try {
      const response = await this.client.put(`/governance/teams/${teamId}`, data)
      return [response.data, null]
    } catch (error) {
      return [null, this.getErrorMessage(error)]
    }
  }

  async deleteTeam(teamId: string): ServiceResponse<{ message: string }> {
    try {
      const response = await this.client.delete(`/governance/teams/${teamId}`)
      return [response.data, null]
    } catch (error) {
      return [null, this.getErrorMessage(error)]
    }
  }

  // Customers
  async getCustomers(): ServiceResponse<GetCustomersResponse> {
    try {
      const response = await this.client.get('/governance/customers')
      return [response.data, null]
    } catch (error) {
      return [null, this.getErrorMessage(error)]
    }
  }

  async getCustomer(customerId: string): ServiceResponse<{ customer: Customer }> {
    try {
      const response = await this.client.get(`/governance/customers/${customerId}`)
      return [response.data, null]
    } catch (error) {
      return [null, this.getErrorMessage(error)]
    }
  }

  async createCustomer(data: CreateCustomerRequest): ServiceResponse<{ message: string; customer: Customer }> {
    try {
      const response = await this.client.post('/governance/customers', data)
      return [response.data, null]
    } catch (error) {
      return [null, this.getErrorMessage(error)]
    }
  }

  async updateCustomer(customerId: string, data: UpdateCustomerRequest): ServiceResponse<{ message: string; customer: Customer }> {
    try {
      const response = await this.client.put(`/governance/customers/${customerId}`, data)
      return [response.data, null]
    } catch (error) {
      return [null, this.getErrorMessage(error)]
    }
  }

  async deleteCustomer(customerId: string): ServiceResponse<{ message: string }> {
    try {
      const response = await this.client.delete(`/governance/customers/${customerId}`)
      return [response.data, null]
    } catch (error) {
      return [null, this.getErrorMessage(error)]
    }
  }

  // Budgets
  async getBudgets(): ServiceResponse<GetBudgetsResponse> {
    try {
      const response = await this.client.get('/governance/budgets')
      return [response.data, null]
    } catch (error) {
      return [null, this.getErrorMessage(error)]
    }
  }

  async getBudget(budgetId: string): ServiceResponse<{ budget: Budget }> {
    try {
      const response = await this.client.get(`/governance/budgets/${budgetId}`)
      return [response.data, null]
    } catch (error) {
      return [null, this.getErrorMessage(error)]
    }
  }

  async updateBudget(budgetId: string, data: UpdateBudgetRequest): ServiceResponse<{ message: string; budget: Budget }> {
    try {
      const response = await this.client.put(`/governance/budgets/${budgetId}`, data)
      return [response.data, null]
    } catch (error) {
      return [null, this.getErrorMessage(error)]
    }
  }

  async deleteBudget(budgetId: string): ServiceResponse<{ message: string }> {
    try {
      const response = await this.client.delete(`/governance/budgets/${budgetId}`)
      return [response.data, null]
    } catch (error) {
      return [null, this.getErrorMessage(error)]
    }
  }

  // Rate Limits
  async getRateLimits(): ServiceResponse<GetRateLimitsResponse> {
    try {
      const response = await this.client.get('/governance/rate-limits')
      return [response.data, null]
    } catch (error) {
      return [null, this.getErrorMessage(error)]
    }
  }

  async getRateLimit(rateLimitId: string): ServiceResponse<{ rate_limit: RateLimit }> {
    try {
      const response = await this.client.get(`/governance/rate-limits/${rateLimitId}`)
      return [response.data, null]
    } catch (error) {
      return [null, this.getErrorMessage(error)]
    }
  }

  async updateRateLimit(rateLimitId: string, data: UpdateRateLimitRequest): ServiceResponse<{ message: string; rate_limit: RateLimit }> {
    try {
      const response = await this.client.put(`/governance/rate-limits/${rateLimitId}`, data)
      return [response.data, null]
    } catch (error) {
      return [null, this.getErrorMessage(error)]
    }
  }

  async deleteRateLimit(rateLimitId: string): ServiceResponse<{ message: string }> {
    try {
      const response = await this.client.delete(`/governance/rate-limits/${rateLimitId}`)
      return [response.data, null]
    } catch (error) {
      return [null, this.getErrorMessage(error)]
    }
  }

  async getUsageStats(virtualKeyId?: string): ServiceResponse<GetUsageStatsResponse> {
    try {
      const params = virtualKeyId ? { virtual_key_id: virtualKeyId } : {}
      const response = await this.client.get('/governance/usage-stats', { params })
      return [response.data, null]
    } catch (error) {
      return [null, this.getErrorMessage(error)]
    }
  }

  async resetUsage(data: ResetUsageRequest): ServiceResponse<{ message: string }> {
    try {
      const response = await this.client.post('/governance/usage-reset', data)
      return [response.data, null]
    } catch (error) {
      return [null, this.getErrorMessage(error)]
    }
  }

  // Debug endpoints
  async getGovernanceDebugStats(): ServiceResponse<DebugStatsResponse> {
    try {
      const response = await this.client.get('/governance/debug/stats')
      return [response.data, null]
    } catch (error) {
      return [null, this.getErrorMessage(error)]
    }
  }

  async getGovernanceHealth(): ServiceResponse<HealthCheckResponse> {
    try {
      const response = await this.client.get('/governance/debug/health')
      return [response.data, null]
    } catch (error) {
      return [null, this.getErrorMessage(error)]
    }
  }
}

export const apiService = new ApiService()
