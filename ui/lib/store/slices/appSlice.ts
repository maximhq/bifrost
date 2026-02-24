import { createSlice, PayloadAction } from "@reduxjs/toolkit";

// Define the shape of our app state
export interface AppState {	
	// UI State
	sidebarCollapsed: boolean;
	theme: "light" | "dark" | "system";

	// Loading states for global operations
	isInitializing: boolean;
	isOnline: boolean;

	// Current user/session info
	currentUser: {
		id?: string;
		name?: string;
		email?: string;
	} | null;

	// Global notifications/toasts
	notifications: {
		id: string;
		type: "success" | "error" | "warning" | "info";
		title: string;
		message: string;
		timestamp: number;
		read: boolean;
	}[];

	// Application settings
	settings: {
		autoRefresh: boolean;
		refreshInterval: number; // in seconds
		maxLogEntries: number;
		defaultPageSize: number;
	};

	// Global error state
	globalError: {
		message: string;
		code?: string;
		timestamp: number;
	} | null;

	// Selected items
	selectedItems: {
		providers: string[];
		virtualKeys: string[];
		teams: string[];
		customers: string[];
		logs: string[];
	};

	// Feature flags
	features: {
		enableMCP: boolean;
		enableCaching: boolean;
		enableLogging: boolean;
	};
}

// Define initial state
const initialState: AppState = {
	sidebarCollapsed: false,
	theme: "system",
	isInitializing: true,
	isOnline: true,
	currentUser: null,
	notifications: [],
	settings: {
		autoRefresh: false,
		refreshInterval: 30,
		maxLogEntries: 1000,
		defaultPageSize: 50,
	},
	globalError: null,
	features: {
		enableMCP: false,
		enableCaching: false,
		enableLogging: false,
	},
	selectedItems: {
		providers: [],
		virtualKeys: [],
		teams: [],
		customers: [],
		logs: [],
	},
};

// Create the slice
const appSlice = createSlice({
	name: "app",
	initialState,
	reducers: {
		// UI Actions
		toggleSidebar: (state) => {
			state.sidebarCollapsed = !state.sidebarCollapsed;
		},

		setSidebarCollapsed: (state, action: PayloadAction<boolean>) => {
			state.sidebarCollapsed = action.payload;
		},

		setTheme: (state, action: PayloadAction<"light" | "dark" | "system">) => {
			state.theme = action.payload;
		},

		// App State Actions
		setInitializing: (state, action: PayloadAction<boolean>) => {
			state.isInitializing = action.payload;
		},

		setOnlineStatus: (state, action: PayloadAction<boolean>) => {
			state.isOnline = action.payload;
		},

		// Notification Actions
		addNotification: (state, action: PayloadAction<Omit<AppState["notifications"][0], "id" | "timestamp" | "read">>) => {
			const notification = {
				...action.payload,
				id: Date.now().toString(),
				timestamp: Date.now(),
				read: false,
			};
			state.notifications.unshift(notification);

			// Keep only last 50 notifications
			if (state.notifications.length > 50) {
				state.notifications = state.notifications.slice(0, 50);
			}
		},

		markNotificationRead: (state, action: PayloadAction<string>) => {
			const notification = state.notifications.find((n) => n.id === action.payload);
			if (notification) {
				notification.read = true;
			}
		},

		removeNotification: (state, action: PayloadAction<string>) => {
			state.notifications = state.notifications.filter((n) => n.id !== action.payload);
		},

		clearAllNotifications: (state) => {
			state.notifications = [];
		},

		markAllNotificationsRead: (state) => {
			state.notifications.forEach((notification) => {
				notification.read = true;
			});
		},

		// Settings Actions
		updateSettings: (state, action: PayloadAction<Partial<AppState["settings"]>>) => {
			state.settings = { ...state.settings, ...action.payload };
		},

		// Error Actions
		setGlobalError: (state, action: PayloadAction<AppState["globalError"]>) => {
			state.globalError = action.payload;
		},

		clearGlobalError: (state) => {
			state.globalError = null;
		},

		// Feature Flags Actions
		updateFeatures: (state, action: PayloadAction<Partial<AppState["features"]>>) => {
			state.features = { ...state.features, ...action.payload };
		},
		// Reset app state (useful for logout)
		resetAppState: () => initialState,
	},
});

// Export actions
export const {
	// UI Actions
	toggleSidebar,
	setSidebarCollapsed,
	setTheme,

	// App State Actions
	setInitializing,
	setOnlineStatus,

	// Notification Actions
	addNotification,
	markNotificationRead,
	removeNotification,
	clearAllNotifications,
	markAllNotificationsRead,

	// Settings Actions
	updateSettings,

	// Error Actions
	setGlobalError,
	clearGlobalError,

	// Feature Flags Actions
	updateFeatures,

	// Reset
	resetAppState,
} = appSlice.actions;

// Export reducer
export default appSlice.reducer;

// Selectors
export const selectSidebarCollapsed = (state: { app: AppState }) => state.app.sidebarCollapsed;
export const selectTheme = (state: { app: AppState }) => state.app.theme;
export const selectIsInitializing = (state: { app: AppState }) => state.app.isInitializing;
export const selectIsOnline = (state: { app: AppState }) => state.app.isOnline;
export const selectCurrentUser = (state: { app: AppState }) => state.app.currentUser;
export const selectNotifications = (state: { app: AppState }) => state.app.notifications;
export const selectUnreadNotificationsCount = (state: { app: AppState }) => state.app.notifications.filter((n) => !n.read).length;
export const selectSettings = (state: { app: AppState }) => state.app.settings;
export const selectGlobalError = (state: { app: AppState }) => state.app.globalError;
export const selectFeatures = (state: { app: AppState }) => state.app.features;
