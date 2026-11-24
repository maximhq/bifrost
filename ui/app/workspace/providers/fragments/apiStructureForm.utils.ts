import type { FormCustomProviderConfig } from "@/lib/types/config";

// withToolsFlagDefaults fills in the tools flags a stored config predates.
//
// The gateway reads an omitted boolean as false, so a stored allowed_requests block without
// these two already means Search and Fetch are disabled. form.reset replaces the whole form
// with whatever it is handed, so resetting to the raw stored config would leave them
// undefined; filling them with false keeps the form showing what the gateway enforces, where
// filling them with true would silently enable the operations on the next save. Only these
// two are filled: the rest of the block is returned exactly as stored, and a genuinely
// absent allowed_requests stays absent (nil there means every operation is allowed, so
// materializing one would change what the config means).
export const withToolsFlagDefaults = (config?: FormCustomProviderConfig): FormCustomProviderConfig | undefined => {
	if (!config?.allowed_requests) return config;
	return {
		...config,
		allowed_requests: {
			...config.allowed_requests,
			search: config.allowed_requests.search ?? false,
			fetch: config.allowed_requests.fetch ?? false,
		},
	};
};
