import {
	GetVirtualMCPsParams,
	GetVirtualMCPsResponse,
	VirtualMCP,
	VirtualMCPAccessProfileUsage,
	VirtualMCPRequest,
} from "@/lib/types/virtualMcps";
import { baseApi } from "./baseApi";

type SuccessResponse = { success: boolean };

export const virtualMcpsApi = baseApi.injectEndpoints({
	endpoints: (builder) => ({
		getVirtualMCPs: builder.query<GetVirtualMCPsResponse, GetVirtualMCPsParams | void>({
			query: (params) => ({
				url: "/mcp/virtual-mcps",
				params: {
					...(params?.search && { search: params.search }),
					...(params?.limit !== undefined && { limit: params.limit }),
					...(params?.offset !== undefined && { offset: params.offset }),
				},
			}),
			providesTags: ["VirtualMCPs"],
		}),

		getVirtualMCP: builder.query<VirtualMCP, number>({
			query: (id) => ({ url: `/mcp/virtual-mcps/${id}` }),
			transformResponse: (response: { virtual_mcp: VirtualMCP }) => response.virtual_mcp,
			providesTags: (_result, _error, id) => [{ type: "VirtualMCPs", id }],
		}),

		createVirtualMCP: builder.mutation<VirtualMCP, VirtualMCPRequest>({
			query: (body) => ({ url: "/mcp/virtual-mcps", method: "POST", body }),
			transformResponse: (response: { virtual_mcp: VirtualMCP }) => response.virtual_mcp,
			invalidatesTags: ["VirtualMCPs"],
		}),

		updateVirtualMCP: builder.mutation<VirtualMCP, { id: number; data: Partial<VirtualMCPRequest> }>({
			query: ({ id, data }) => ({ url: `/mcp/virtual-mcps/${id}`, method: "PUT", body: data }),
			transformResponse: (response: { virtual_mcp: VirtualMCP }) => response.virtual_mcp,
			invalidatesTags: (_result, _error, { id }) => ["VirtualMCPs", { type: "VirtualMCPs", id }],
		}),

		// Enterprise-only reverse lookup (the endpoint is registered by the enterprise build); the OSS
		// fallback section never calls it.
		getVirtualMCPAccessProfiles: builder.query<VirtualMCPAccessProfileUsage[], number>({
			query: (id) => ({ url: `/mcp/virtual-mcps/${id}/access-profiles` }),
			transformResponse: (response: { access_profiles: VirtualMCPAccessProfileUsage[] }) => response.access_profiles,
			providesTags: (_result, _error, id) => [{ type: "VirtualMCPs", id }],
		}),

		deleteVirtualMCP: builder.mutation<SuccessResponse, number>({
			query: (id) => ({ url: `/mcp/virtual-mcps/${id}`, method: "DELETE" }),
			invalidatesTags: ["VirtualMCPs"],
		}),

		attachVirtualMCPVirtualKey: builder.mutation<SuccessResponse, { id: number; vkId: string }>({
			query: ({ id, vkId }) => ({ url: `/mcp/virtual-mcps/${id}/virtual-keys/${vkId}`, method: "POST" }),
			invalidatesTags: (_result, _error, { id }) => ["VirtualMCPs", { type: "VirtualMCPs", id }],
		}),

		detachVirtualMCPVirtualKey: builder.mutation<SuccessResponse, { id: number; vkId: string }>({
			query: ({ id, vkId }) => ({ url: `/mcp/virtual-mcps/${id}/virtual-keys/${vkId}`, method: "DELETE" }),
			invalidatesTags: (_result, _error, { id }) => ["VirtualMCPs", { type: "VirtualMCPs", id }],
		}),
	}),
});

export const {
	useGetVirtualMCPsQuery,
	useGetVirtualMCPQuery,
	useGetVirtualMCPAccessProfilesQuery,
	useCreateVirtualMCPMutation,
	useUpdateVirtualMCPMutation,
	useDeleteVirtualMCPMutation,
	useAttachVirtualMCPVirtualKeyMutation,
	useDetachVirtualMCPVirtualKeyMutation,
} = virtualMcpsApi;
