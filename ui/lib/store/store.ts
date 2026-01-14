import { configureStore } from "@reduxjs/toolkit";
import { baseApi } from "./apis/baseApi";
import { appReducer, pluginReducer, providerReducer } from "./slices";

// Import enterprise types for TypeScript
type EnterpriseState = {} & import("@enterprise/lib/store/slices").EnterpriseState;

// Get enterprise reducers if they are available
let enterpriseReducers = {};
try {
	const enterprise = require("@enterprise/lib/store/slices");
	// Use the explicit reducers map from enterprise slices
	if (enterprise.reducers) {
		enterpriseReducers = enterprise.reducers;
	}
} catch (e) {
	// Enterprise reducers not available, continue without them
}

// Inject enterprise APIs if they are available
try {
	const enterpriseApis = require("@enterprise/lib/store/apis");
	// Access the apis array to ensure all API modules are loaded
	// APIs are already injected into baseApi via injectEndpoints
	if (enterpriseApis.apis) {
		// Just accessing the array ensures all APIs are loaded
		baseApi.injectEndpoints(enterpriseApis.apis);
	}
} catch (e) {
	// Enterprise APIs not available, continue without them
}

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
		// Enterprise reducers (if available)
		...enterpriseReducers,
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

export type RootState = ReturnType<typeof store.getState> & EnterpriseState;
export type AppDispatch = typeof store.dispatch;
