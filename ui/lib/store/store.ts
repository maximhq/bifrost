import { configureStore } from "@reduxjs/toolkit";
import { baseApi } from "./apis/baseApi";
import { appReducer, pluginReducer, providerReducer } from "./slices";

export const store = configureStore({
	reducer: {
		// RTK Query API
		[baseApi.reducerPath]: baseApi.reducer,
		// App state slice
		app: appReducer,
		// Provider state slice
		provider: providerReducer,
		// Plugin state slice
		plugin: pluginReducer,
	},
	middleware: (getDefaultMiddleware) =>
		getDefaultMiddleware({
			serializableCheck: {
				// Ignore these action types for RTK Query
				ignoredActions: [
					"persist/PERSIST",
					"persist/REHYDRATE",
					"api/executeQuery/pending",
					"api/executeQuery/fulfilled",
					"api/executeQuery/rejected",
					"api/executeMutation/pending",
					"api/executeMutation/fulfilled",
					"api/executeMutation/rejected",
				],
				// Ignore these field paths in all actions
				ignoredActionsPaths: ["meta.arg", "payload.timestamp"],
				// Ignore these paths in the state
				ignoredPaths: ["api.queries", "api.mutations"],
			},
		}).concat(baseApi.middleware),
	devTools: process.env.NODE_ENV !== "production",
});

export type RootState = ReturnType<typeof store.getState>;
export type AppDispatch = typeof store.dispatch;
