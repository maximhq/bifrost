import { configureStore } from "@reduxjs/toolkit";
import { baseApi } from "./apis/baseApi";
import { appReducer, pluginReducer, providerReducer } from "./slices";

// Import enterprise types for TypeScript
type EnterpriseState = {
	//@ts-ignore
	scim?: import("@enterprise/lib/store/slices/scimSlice").SCIMState;
	//@ts-ignore
	user?: import("@enterprise/lib/store/slices/userSlice").UserState;
};

// Get enterprise reducers if they are available
let enterpriseReducers = {};
try {
	const enterprise = require("@enterprise/lib/store/slices");
	if (enterprise.scimReducer) {
		enterpriseReducers = {
			...enterpriseReducers,
			scim: enterprise.scimReducer,
		};
	}
	if (enterprise.userReducer) {
		enterpriseReducers = {
			...enterpriseReducers,
			user: enterprise.userReducer,
		};
	}
} catch (e) {
	// Enterprise reducers not available, continue without them
}

// Inject enterprise APIs if they are available
try {
	const enterpriseApis = require("@enterprise/lib/store/apis");
	if (enterpriseApis.scimApi) {
		// APIs are already injected into baseApi via injectEndpoints
		// This just ensures the module is loaded
	}
	if (enterpriseApis.userApi) {
		// APIs are already injected into baseApi via injectEndpoints
		// This just ensures the module is loaded
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
