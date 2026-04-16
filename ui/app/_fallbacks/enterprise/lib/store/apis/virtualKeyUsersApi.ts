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
