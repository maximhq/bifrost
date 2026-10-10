import { DefaultNetworkConfig, DefaultPerformanceConfig } from "@/lib/constants/config";
import { ModelProvider, UpdateProviderRequest } from "@/lib/types/config";

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
		ignore_provider_cost: updates.ignore_provider_cost ?? provider.ignore_provider_cost,
		custom_provider_config: updates.custom_provider_config ?? provider.custom_provider_config,
		openai_config: updates.openai_config ?? provider.openai_config,
		prompt_cache: updates.prompt_cache ?? provider.prompt_cache,
		// null is how the web search tab clears the block, so presence decides, not ??.
		injected_tools: "injected_tools" in updates ? updates.injected_tools : provider.injected_tools,
	};
};