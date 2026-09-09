import { GetUserAccessProfilesResponse, VKCreationPolicyResponse } from "@enterprise/lib/types/accessProfile";

// OSS build has no access-profile backend — return undefined data so consumers
// (e.g. useVirtualKeyUsage) fall back to VK-owned budget/rate-limit values.
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

// OSS build has no access-profile backend — returns undefined so the create form
// never locks (governed stays falsy).
export const useGetMyVKCreationPolicyQuery = (
	_arg?: void,
	_opts?: { skip?: boolean; refetchOnMountOrArgChange?: boolean },
): {
	data: VKCreationPolicyResponse | undefined;
	isLoading: boolean;
	isError: boolean;
	error: null;
} => ({
	data: undefined,
	isLoading: false,
	isError: false,
	error: null,
});