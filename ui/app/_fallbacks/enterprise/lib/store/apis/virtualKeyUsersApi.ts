import { User } from "@enterprise/lib/types/user";

export interface GetVirtualKeyUsersResponse {
	users: User[];
}

// OSS build has no VK-user-attachment backend — return undefined data so the
// consumer treats the VK as unassigned (no AP-managed detection happens).
export const useGetVirtualKeyUsersQuery = (
	_vkId: string,
	_opts?: { skip?: boolean },
): {
	data: GetVirtualKeyUsersResponse | undefined;
	isLoading: boolean;
	isError: boolean;
	error: null;
} => ({
	data: undefined,
	isLoading: false,
	isError: false,
	error: null,
});

// Attach/detach exist so OSS callers (the virtual key sheet's assignment section)
// type-check and link. The UI never reaches them: the "Assign to User" option is
// gated on the user picker registry, which OSS leaves unregistered.
const unavailable = () => {
	throw new Error("Virtual key user assignment is an enterprise feature");
};

export const useAttachVirtualKeyUsersMutation = (): [
	(_args: { vkId: string; data: { user_ids: string[]; preserve_usage?: boolean } }) => { unwrap: () => Promise<{ message: string }> },
	{ isLoading: boolean },
] => [() => ({ unwrap: unavailable }), { isLoading: false }];

export const useDetachVirtualKeyUserMutation = (): [
	(_args: { vkId: string; userId: string }) => { unwrap: () => Promise<{ message: string }> },
	{ isLoading: boolean },
] => [() => ({ unwrap: unavailable }), { isLoading: false }];