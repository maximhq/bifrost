import { DefaultNetworkConfig, DefaultPerformanceConfig } from "@/lib/constants/config";
import { ModelProvider, UpdateProviderRequest } from "@/lib/types/config";

const PROVIDERS_WITH_PASSTHROUGH_EXTRA_PARAMS_DEFAULT: Record<string, true> = { vllm: true, sgl: true, deepseek: true };

/**
 * The Network tab knows the provider but not the request type, so it can only
 * resolve named provider defaults. Request-specific defaults (such as image
 * generation) remain a runtime concern.
 */
export const resolvePassthroughExtraParams = (provider: Pick<ModelProvider, "name" | "passthrough_extra_params">): boolean =>
	provider.passthrough_extra_params ?? PROVIDERS_WITH_PASSTHROUGH_EXTRA_PARAMS_DEFAULT[provider.name.toLowerCase()] === true;

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
		openai_config: updates.openai_config ?? provider.openai_config,
		passthrough_extra_params: updates.passthrough_extra_params ?? provider.passthrough_extra_params,
	};
};