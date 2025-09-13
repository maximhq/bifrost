import { ModelProvider } from "@/lib/types/config";
import { createSlice, PayloadAction } from "@reduxjs/toolkit";
import { providersApi } from "../apis";

interface ProviderState {
	selectedProvider: ModelProvider | null;
	isConfigureDialogOpen: boolean;
	providers: ModelProvider[];
}

const initialState: ProviderState = {
	selectedProvider: null,
	isConfigureDialogOpen: false,
	providers: [],
};

const providerSlice = createSlice({
	name: "provider",
	initialState,
	reducers: {
		setSelectedProvider: (state, action: PayloadAction<ModelProvider | null>) => {
			state.selectedProvider = action.payload;
		},
		setIsConfigureDialogOpen: (state, action: PayloadAction<boolean>) => {
			state.isConfigureDialogOpen = action.payload;
		},
		setProviders: (state, action: PayloadAction<ModelProvider[]>) => {
			state.providers = action.payload;
		},
		openConfigureDialog: (state, action: PayloadAction<ModelProvider | null>) => {
			state.selectedProvider = action.payload;
			state.isConfigureDialogOpen = true;
		},
		closeConfigureDialog: (state) => {
			state.selectedProvider = null;
			state.isConfigureDialogOpen = false;
		},
	},
	extraReducers: (builder) => {
		// Listen to getProviders fulfilled to update selected provider if it has changed
		builder.addMatcher(providersApi.endpoints.getProviders.matchFulfilled, (state, action) => {
			const updatedProviders = action.payload;

			// If we have a selected provider, check if it has been updated
			if (state.selectedProvider && updatedProviders) {
				const updatedSelectedProvider = updatedProviders.find((provider) => provider.name === state.selectedProvider!.name);
				// If the selected provider exists in the updated list, update it
				if (updatedSelectedProvider) {
					// Check if the provider has actually changed
					state.selectedProvider = updatedSelectedProvider;
				}
			} else {
				// Selection no longer valid
				state.selectedProvider = null;
			}
		});

		// Listen to updateProvider fulfilled to update selected provider if it's the same one
		builder.addMatcher(providersApi.endpoints.updateProvider.matchFulfilled, (state, action) => {
			const updatedProvider = action.meta.arg.originalArgs;

			// If the updated provider is the currently selected one, update it
			if (state.selectedProvider && updatedProvider.name === state.selectedProvider.name) {
				state.selectedProvider = updatedProvider;
			} else {
				// Selection no longer valid
				state.selectedProvider = null;
			}
		});
	},
});

export const { setSelectedProvider, setIsConfigureDialogOpen, setProviders, openConfigureDialog, closeConfigureDialog } =
	providerSlice.actions;

export default providerSlice.reducer;
