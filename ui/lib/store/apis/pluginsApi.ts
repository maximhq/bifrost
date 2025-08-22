import { CreatePluginRequest, Plugin, PluginsResponse, UpdatePluginRequest } from "@/lib/types/plugins";
import { baseApi } from "./baseApi";

export const pluginsApi = baseApi.injectEndpoints({
	endpoints: (builder) => ({
		// Get all plugins
		getPlugins: builder.query<Plugin[], void>({
			query: () => "/plugins",
			providesTags: ["Plugins"],
			transformResponse: (response: PluginsResponse) => response.plugins,
		}),

		// Create new plugin
		createPlugin: builder.mutation<Plugin, CreatePluginRequest>({
			query: (data) => ({
				url: "/plugins",
				method: "POST",
				body: data,
			}),
			invalidatesTags: ["Plugins"],
		}),

		// Update existing plugin
		updatePlugin: builder.mutation<Plugin, { name: string; data: UpdatePluginRequest }>({
			query: ({ name, data }) => ({
				url: `/plugins/${name}`,
				method: "PUT",
				body: data,
			}),
			invalidatesTags: ["Plugins"],
		}),

		// Delete plugin
		deletePlugin: builder.mutation<Plugin, string>({
			query: (name) => ({
				url: `/plugins/${name}`,
				method: "DELETE",
			}),
			invalidatesTags: ["Plugins"],
		}),
	}),
});

export const { useGetPluginsQuery, useCreatePluginMutation, useUpdatePluginMutation, useDeletePluginMutation, useLazyGetPluginsQuery } =
	pluginsApi;
