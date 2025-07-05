import axios, { AxiosInstance, isAxiosError } from "axios";
import {
	ListProvidersResponse,
	ProviderResponse,
	CoreConfig,
	AddProviderRequest,
	UpdateProviderRequest,
	BifrostErrorResponse,
} from "@/lib/types/config";
import { MCPClient, CreateMCPClientRequest, UpdateMCPClientRequest } from "@/lib/types/mcp";
import { LogEntry, LogFilters, LogStats, Pagination } from "./types/logs";

type ServiceResponse<T> = Promise<[T | null, string | null]>;

class ApiService {
	private client: AxiosInstance;

	constructor() {
		const port = process.env.NEXT_PUBLIC_BIFROST_PORT || "8080";
		this.client = axios.create({
			baseURL: `http://localhost:${port}`,
			headers: {
				"Content-Type": "application/json",
			},
		});
	}

	private getErrorMessage(error: unknown): string {
		if (isAxiosError(error) && error.response) {
			const errorData = error.response.data as BifrostErrorResponse;
			if (errorData.error && errorData.error.message) {
				return errorData.error.message;
			}
		}
		if (error instanceof Error) {
			return error.message || "An unexpected error occurred.";
		}
		return "An unexpected error occurred.";
	}

	async getLogs(
		filters: LogFilters,
		pagination: Pagination,
	): ServiceResponse<{
		logs: LogEntry[];
		pagination: Pagination;
		stats: LogStats;
	}> {
		try {
			const params: Record<string, string | number> = {
				limit: pagination.limit,
				offset: pagination.offset,
				sort_by: pagination.sort_by,
				order: pagination.order,
			};

			// Add filters to params if they exist
			if (filters.providers && filters.providers.length > 0) {
				params.providers = filters.providers.join(",");
			}
			if (filters.models && filters.models.length > 0) {
				params.models = filters.models.join(",");
			}
			if (filters.status && filters.status.length > 0) {
				params.status = filters.status.join(",");
			}
			if (filters.objects && filters.objects.length > 0) {
				params.objects = filters.objects.join(",");
			}
			if (filters.start_time) params.start_time = filters.start_time;
			if (filters.end_time) params.end_time = filters.end_time;
			if (filters.min_latency) params.min_latency = filters.min_latency;
			if (filters.max_latency) params.max_latency = filters.max_latency;
			if (filters.min_tokens) params.min_tokens = filters.min_tokens;
			if (filters.max_tokens) params.max_tokens = filters.max_tokens;
			if (filters.content_search) params.content_search = filters.content_search;

			const response = await this.client.get("/v1/logs", { params });
			return [response.data, null];
		} catch (error) {
			return [null, this.getErrorMessage(error)];
		}
	}

	// Provider endpoints
	async getProviders(): ServiceResponse<ListProvidersResponse> {
		try {
			const response = await this.client.get("/providers");
			return [response.data, null];
		} catch (error) {
			return [null, this.getErrorMessage(error)];
		}
	}

	async createProvider(data: AddProviderRequest): ServiceResponse<ProviderResponse> {
		try {
			const response = await this.client.post("/providers", data);
			return [response.data, null];
		} catch (error) {
			return [null, this.getErrorMessage(error)];
		}
	}

	async updateProvider(providerId: string, data: UpdateProviderRequest): ServiceResponse<ProviderResponse> {
		try {
			const response = await this.client.put(`/providers/${providerId}`, data);
			return [response.data, null];
		} catch (error) {
			return [null, this.getErrorMessage(error)];
		}
	}

	async deleteProvider(providerId: string): ServiceResponse<null> {
		try {
			await this.client.delete(`/providers/${providerId}`);
			return [null, null];
		} catch (error) {
			return [null, this.getErrorMessage(error)];
		}
	}

	// Config endpoints
	async saveConfig(): ServiceResponse<null> {
		try {
			await this.client.post("/config/save");
			return [null, null];
		} catch (error) {
			return [null, this.getErrorMessage(error)];
		}
	}

	async reloadConfig(): ServiceResponse<null> {
		try {
			await this.client.put("/config");
			return [null, null];
		} catch (error) {
			return [null, this.getErrorMessage(error)];
		}
	}

	async getCoreConfig(): ServiceResponse<CoreConfig> {
		try {
			const response = await this.client.get("/config/core");
			return [response.data, null];
		} catch (error) {
			return [null, this.getErrorMessage(error)];
		}
	}

	async updateCoreConfig(data: CoreConfig): ServiceResponse<null> {
		try {
			await this.client.put("/config", data);
			return [null, null];
		} catch (error) {
			return [null, this.getErrorMessage(error)];
		}
	}

	// MCP endpoints
	async getMCPClients(): Promise<[MCPClient[] | null, string | null]> {
		try {
			const res = await this.client.get<MCPClient[]>("/mcp/clients");
			return [res.data, null];
		} catch (err) {
			return [null, this.getErrorMessage(err)];
		}
	}

	async reconnectMCPClient(name: string): Promise<[null, string | null]> {
		try {
			const res = await this.client.post(`/mcp/client/${name}/reconnect`);
			return [res.data, null];
		} catch (err) {
			return [null, this.getErrorMessage(err)];
		}
	}

	async createMCPClient(payload: CreateMCPClientRequest): Promise<[{ status: string; message: string } | null, string | null]> {
		try {
			const res = await this.client.post<{ status: string; message: string }>("/mcp/client", payload);
			return [res.data, null];
		} catch (err) {
			return [null, this.getErrorMessage(err)];
		}
	}

	async updateMCPClient(
		name: string,
		payload: UpdateMCPClientRequest,
	): Promise<[{ status: string; message: string } | null, string | null]> {
		try {
			const res = await this.client.put<{ status: string; message: string }>(`/mcp/client/${name}`, payload);
			return [res.data, null];
		} catch (err) {
			return [null, this.getErrorMessage(err)];
		}
	}

	async deleteMCPClient(name: string): Promise<[boolean, string | null]> {
		try {
			await this.client.delete(`/mcp/client/${name}`);
			return [true, null];
		} catch (err) {
			return [false, this.getErrorMessage(err)];
		}
	}
}

export const apiService = new ApiService();
