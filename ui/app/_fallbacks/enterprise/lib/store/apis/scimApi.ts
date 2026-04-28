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
