// OSS build has no SCIM/auth-type backend — return undefined so consumers
// fall back to showing the password section.
export const useGetAuthTypeQuery = (
	_args?: undefined,
	_opts?: { skip?: boolean },
): {
	data: { type: string; provider?: string } | undefined;
	isLoading: boolean;
	isError: boolean;
	error: null;
} => ({
	data: undefined,
	isLoading: false,
	isError: false,
	error: null,
});

// OSS stub for SCIM providers — returns an empty list so the onboarding
// widget's enterprise-only "configure SCIM" step is always considered
// incomplete (the step itself is hidden in OSS via IS_ENTERPRISE).
// Element type mirrors the fields OSS consumers read off the enterprise
// response (e.g. onboarding's `provider.enabled` gate); `unknown` elements
// force every consumer into a per-site cast.
export const useGetSCIMProvidersQuery = (
	_args?: undefined,
	_opts?: { skip?: boolean },
): {
	data: { enabled: boolean }[] | undefined;
	isLoading: boolean;
	isError: boolean;
	error: null;
} => ({
	data: [],
	isLoading: false,
	isError: false,
	error: null,
});