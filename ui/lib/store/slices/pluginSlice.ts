import { Plugin } from "@/lib/types/plugins";
import { createSlice, PayloadAction } from "@reduxjs/toolkit";
import { pluginsApi } from "../apis";

interface PluginState {
	selectedPlugin?: Plugin;
	isDirty: boolean;
}

const initialState: PluginState = {
	selectedPlugin: undefined,
	isDirty: false,
};

const pluginSlice = createSlice({
	name: "plugin",
	initialState,
	reducers: {
		setPluginFormDirtyState: (state, action: PayloadAction<boolean>) => {
			state.isDirty = action.payload;
		},
		setSelectedPlugin: (state, action: PayloadAction<Plugin | undefined>) => {
			state.selectedPlugin = action.payload;
		},
	},

	extraReducers: (builder) => {
		// Listen to getPlugins fulfilled to update selected plugin if it has changed
		builder.addMatcher(pluginsApi.endpoints.getPlugins.matchFulfilled, (state, action) => {
			const updatedPlugins = action.payload;

			// If we have a selected plugin, check if it has been updated
			if (state.selectedPlugin && updatedPlugins) {
				const updatedSelectedPlugin = updatedPlugins.find((plugin) => plugin.name === state.selectedPlugin!.name);
				// If the selected plugin exists in the updated list, update it
				if (updatedSelectedPlugin) {
					state.selectedPlugin = updatedSelectedPlugin;
				}
			}
		});

		// Listen to updatePlugin fulfilled to update selected plugin if it's the same one
		builder.addMatcher(pluginsApi.endpoints.updatePlugin.matchFulfilled, (state, action) => {
			const updatedPluginName = action.meta.arg.originalArgs.name;
			// If the updated plugin is the currently selected one, update it
			if (state.selectedPlugin && updatedPluginName === state.selectedPlugin.name) {
				// Update the selected plugin with the new data
				const updatedPlugin = action.payload;
				state.selectedPlugin = updatedPlugin;
			}
		});

		// Listen to createPlugin fulfilled to add the new plugin to the list
		builder.addMatcher(pluginsApi.endpoints.createPlugin.matchFulfilled, (state, action) => {
			const newPlugin = action.payload;
		});

		// Listen to deletePlugin fulfilled to remove the plugin from the list
		builder.addMatcher(pluginsApi.endpoints.deletePlugin.matchFulfilled, (state, action) => {
			const deletedPluginName = action.meta.arg.originalArgs;
			// If the deleted plugin was selected, clear the selection
			if (state.selectedPlugin && state.selectedPlugin.name === deletedPluginName) {
				state.selectedPlugin = undefined;
			}
		});
	},
});

export const { setPluginFormDirtyState, setSelectedPlugin } = pluginSlice.actions;

export default pluginSlice.reducer;

// Selectors
export const selectSelectedPlugin = (state: { plugin: PluginState }) => state.plugin.selectedPlugin;
export const selectPluginFormIsDirty = (state: { plugin: PluginState }) => state.plugin.isDirty;
