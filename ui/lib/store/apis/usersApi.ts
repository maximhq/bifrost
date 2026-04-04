import { baseApi } from "./baseApi";
import type { User } from "@/lib/types/sso";

interface UsersQueryParams {
	limit?: number;
	offset?: number;
	search?: string;
}

interface PaginatedUsersResponse {
	users: User[];
	total: number;
}

export const usersApi = baseApi.injectEndpoints({
	endpoints: (builder) => ({
		getUsers: builder.query<PaginatedUsersResponse, UsersQueryParams>({
			query: (params) => ({ url: "/users", params }),
			providesTags: ["Users"],
		}),
		getCurrentUser: builder.query<User, void>({
			query: () => ({ url: "/users/me" }),
			providesTags: ["Users"],
		}),
		updateUserRole: builder.mutation<User, { id: string; role: string }>({
			query: ({ id, ...body }) => ({ url: `/users/${id}/role`, method: "PUT", body }),
			invalidatesTags: ["Users"],
		}),
		deleteUser: builder.mutation<void, string>({
			query: (id) => ({ url: `/users/${id}`, method: "DELETE" }),
			invalidatesTags: ["Users"],
		}),
	}),
});

export const { useGetUsersQuery, useGetCurrentUserQuery, useUpdateUserRoleMutation, useDeleteUserMutation } = usersApi;
