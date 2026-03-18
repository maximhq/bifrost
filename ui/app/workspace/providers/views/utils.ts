import { DefaultNetworkConfig, DefaultPerformanceConfig } from "@/lib/constants/config";
import { ModelProvider, UpdateProviderRequest } from "@/lib/types/config";

export const keysRequired = (selectedProvider: string) => selectedProvider.toLowerCase() === "custom" || !["ollama", "sgl"].includes(selectedProvider.toLowerCase());

export const buildProviderUpdatePayload = (provider: ModelProvider, updates: Partial<UpdateProviderRequest>) => {
	const { name } = provider;

	return {
		name,
		network_config: updates.network_config ?? provider.network_config ?? DefaultNetworkConfig,
		concurrency_and_buffer_size: updates.concurrency_and_buffer_size ?? provider.concurrency_and_buffer_size ?? DefaultPerformanceConfig,
		proxy_config: updates.proxy_config ?? provider.proxy_config,
		send_back_raw_request: updates.send_back_raw_request ?? provider.send_back_raw_request,
		send_back_raw_response: updates.send_back_raw_response ?? provider.send_back_raw_response,
		store_raw_request_response: updates.store_raw_request_response ?? provider.store_raw_request_response,
		custom_provider_config: updates.custom_provider_config ?? provider.custom_provider_config,
		pricing_overrides: updates.pricing_overrides ?? provider.pricing_overrides,
	};
};
