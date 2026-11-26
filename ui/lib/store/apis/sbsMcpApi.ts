// SBS fork: MCP endpoints and per-namespace tool overrides.
import { SBSEndpoint, SBSEndpointRequest, SBSOverridesMap } from "@/lib/types/sbsMcp";
import { baseApi } from "./baseApi";

export const sbsMcpApi = baseApi.injectEndpoints({
	endpoints: (builder) => ({
		getSBSEndpoints: builder.query<SBSEndpoint[], void>({
			query: () => ({ url: "/sbs/mcp/endpoints" }),
			transformResponse: (response: { endpoints: SBSEndpoint[] }) => response.endpoints ?? [],
			providesTags: ["SBSEndpoints"],
		}),
		createSBSEndpoint: builder.mutation<SBSEndpoint, SBSEndpointRequest>({
			query: (body) => ({ url: "/sbs/mcp/endpoints", method: "POST", body }),
			invalidatesTags: ["SBSEndpoints", "VirtualMCPs", "VirtualKeys"],
		}),
		updateSBSEndpoint: builder.mutation<SBSEndpoint, { id: number; data: SBSEndpointRequest }>({
			query: ({ id, data }) => ({ url: `/sbs/mcp/endpoints/${id}`, method: "PUT", body: data }),
			invalidatesTags: ["SBSEndpoints", "VirtualMCPs", "VirtualKeys"],
		}),
		deleteSBSEndpoint: builder.mutation<{ deleted: number }, number>({
			query: (id) => ({ url: `/sbs/mcp/endpoints/${id}`, method: "DELETE" }),
			invalidatesTags: ["SBSEndpoints"],
		}),
		putSBSToolOverrides: builder.mutation<unknown, { id: number; overrides: SBSOverridesMap }>({
			query: ({ id, overrides }) => ({ url: `/sbs/mcp/virtual-mcps/${id}/overrides`, method: "PUT", body: { overrides } }),
			invalidatesTags: (_result, _error, { id }) => ["VirtualMCPs", { type: "VirtualMCPs", id }],
		}),
	}),
});

export const {
	useGetSBSEndpointsQuery,
	useCreateSBSEndpointMutation,
	useUpdateSBSEndpointMutation,
	useDeleteSBSEndpointMutation,
	usePutSBSToolOverridesMutation,
} = sbsMcpApi;
