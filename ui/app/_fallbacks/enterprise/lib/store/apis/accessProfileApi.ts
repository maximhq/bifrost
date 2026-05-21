import { GetAccessProfilesParams, GetAccessProfilesResponse, GetUserAccessProfilesResponse } from "@enterprise/lib/types/accessProfile";

// OSS build has no access-profile backend — return undefined data so consumers
// fall back gracefully (empty lists, no AP-specific UI rendered).
export const useGetAccessProfilesQuery = (
	_params?: GetAccessProfilesParams | void,
	_opts?: { skip?: boolean },
): {
	data: GetAccessProfilesResponse | undefined;
	isLoading: boolean;
	isError: boolean;
	error: null;
} => ({
	data: undefined,
	isLoading: false,
	isError: false,
	error: null,
});

export const useGetUserAccessProfilesQuery = (
	_userId: string,
	_opts?: { skip?: boolean; pollingInterval?: number },
): {
	data: GetUserAccessProfilesResponse | undefined;
	isLoading: boolean;
	isError: boolean;
	error: null;
} => ({
	data: undefined,
	isLoading: false,
	isError: false,
	error: null,
});