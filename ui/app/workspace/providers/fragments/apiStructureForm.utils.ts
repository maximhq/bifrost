import type { FormCustomProviderConfig } from "@/lib/types/config";

// withToolsFlagDefaults fills in the tools flags a stored config predates.
//
// form.reset replaces the whole form with whatever it is handed, so resetting to the raw stored
// config would leave these two undefined despite the form's defaultValues, and the next save
// would send an allowed_requests block without them — which the gateway reads as false,
// disabling Search and Fetch. Only these two are filled: the rest of the block is returned
// exactly as stored, and a genuinely absent allowed_requests stays absent (nil there means
// every operation is allowed, so materializing one would change what the config means).
export const withToolsFlagDefaults = (config?: FormCustomProviderConfig): FormCustomProviderConfig | undefined => {
	if (!config?.allowed_requests) return config;
	return {
		...config,
		allowed_requests: {
			...config.allowed_requests,
			search: config.allowed_requests.search ?? true,
			fetch: config.allowed_requests.fetch ?? true,
		},
	};
};
