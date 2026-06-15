import { CreatePluginRequest, Plugin, PluginsResponse, UpdatePluginRequest } from "@/lib/types/plugins";
import { baseApi } from "./baseApi";

export const pluginsApi = baseApi.injectEndpoints({
	endpoints: (builder) => ({
		// Get builtin plugin names
		getBuiltinPlugins: builder.query<string[], void>({
			query: () => "/plugins/builtins",
			providesTags: ["Plugins"],
			transformResponse: (response: { plugins: string[] }) => response.plugins || [],
		}),

		// Get the names of all currently loaded plugins (sanitized to match the names
		// embedded in their trace span names). Used by the plugin tracing sheet so it
		// lists every plugin that actually emits spans, including enterprise plugins.
		getLoadedPlugins: builder.query<string[], void>({
			query: () => "/plugins/loaded",
			providesTags: ["Plugins"],
			transformResponse: (response: { plugins: string[] }) => response.plugins || [],
		}),

		// Get all plugins
		getPlugins: builder.query<Plugin[], void>({
			query: () => "/plugins",
			providesTags: ["Plugins"],
			transformResponse: (response: PluginsResponse) => response.plugins || [],
		}),

		// Get a single plugin
		getPlugin: builder.query<Plugin, string>({
			query: (name) => `/plugins/${name}`,
			providesTags: (result, error, name) => [{ type: "Plugins", id: name }],
		}),

		// Create new plugin
		createPlugin: builder.mutation<Plugin, CreatePluginRequest>({
			query: (data) => ({
				url: "/plugins",
				method: "POST",
				body: data,
			}),
			transformResponse: (response: { message: string; plugin: Plugin }) => response.plugin,
			async onQueryStarted(arg, { dispatch, queryFulfilled }) {
				try {
					const { data: newPlugin } = await queryFulfilled;
					dispatch(
						pluginsApi.util.updateQueryData("getPlugins", undefined, (draft) => {
							draft.push(newPlugin);
						}),
					);
					// Also update the individual plugin cache
					dispatch(pluginsApi.util.updateQueryData("getPlugin", newPlugin.name, () => newPlugin));
				} catch {}
			},
		}),

		// Update existing plugin
		updatePlugin: builder.mutation<Plugin, { name: string; data: UpdatePluginRequest }>({
			query: ({ name, data }) => ({
				url: `/plugins/${name}`,
				method: "PUT",
				body: data,
			}),
			transformResponse: (response: { message: string; plugin: Plugin }) => response.plugin,
			async onQueryStarted(arg, { dispatch, queryFulfilled }) {
				try {
					const { data: updatedPlugin } = await queryFulfilled;
					dispatch(
						pluginsApi.util.updateQueryData("getPlugins", undefined, (draft) => {
							const index = draft.findIndex((p) => p.name === arg.name);
							if (index !== -1) {
								draft[index] = updatedPlugin;
							} else {
								draft.push(updatedPlugin);
							}
						}),
					);
					// Also update the individual plugin cache
					dispatch(pluginsApi.util.updateQueryData("getPlugin", arg.name, () => updatedPlugin));
				} catch {}
			},
		}),

		// Delete plugin
		deletePlugin: builder.mutation<Plugin, string>({
			query: (name) => ({
				url: `/plugins/${name}`,
				method: "DELETE",
			}),
			async onQueryStarted(pluginName, { dispatch, queryFulfilled }) {
				try {
					await queryFulfilled;
					dispatch(
						pluginsApi.util.updateQueryData("getPlugins", undefined, (draft) => {
							const index = draft.findIndex((p) => p.name === pluginName);
							if (index !== -1) {
								draft.splice(index, 1);
							}
						}),
					);
				} catch {}
			},
		}),
	}),
});

export const {
	useGetBuiltinPluginsQuery,
	useGetLoadedPluginsQuery,
	useGetPluginsQuery,
	useGetPluginQuery,
	useCreatePluginMutation,
	useUpdatePluginMutation,
	useDeletePluginMutation,
	useLazyGetPluginsQuery,
} = pluginsApi;