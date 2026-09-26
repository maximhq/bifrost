import {
	AccessProfileEntityKind,
	GetEntityAccessProfileResponse,
	GetUserAccessProfilesResponse,
	VKCreationPolicyResponse,
} from "@enterprise/lib/types/accessProfile";

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
// OSS build has no access-profile backend, so no entity can hold one: the budget editors that ask
// this in order to lock themselves stay unlocked, which is correct here because the entity's own
// budget is the only thing enforcing anything.
//
// This is a settled answer, not a pending one - isFetching is false and there is no error - so a
// caller that gates on "do we know yet" reads it as "no profile" and never waits. refetch exists
// only so a retry affordance compiles; there is nothing to fetch.
export const useGetEntityAccessProfileQuery = (
	_arg: { entityType: AccessProfileEntityKind; entityId: string },
	_opts?: { skip?: boolean },
): {
	data: GetEntityAccessProfileResponse | undefined;
	isLoading: boolean;
	isFetching: boolean;
	isError: boolean;
	error: null;
	refetch: () => void;
} => ({
	data: undefined,
	isLoading: false,
	isFetching: false,
	isError: false,
	error: null,
	refetch: () => {},
});