import { createSelector, createSlice, PayloadAction } from "@reduxjs/toolkit";
import type { Notification } from "@/lib/types/notifications";

/** How many notifications the tray keeps in memory, matching the API page size. */
const MAX_NOTIFICATIONS = 50;

/**
 * Newest first. Do not compare created_at as strings: Go marshals RFC3339 with
 * trailing zeros in the fractional second stripped, so within the same second
 * "…:00.5Z" and "…:00Z" order by "." vs "Z" rather than by time.
 */
function byNewestFirst(a: Notification, b: Notification) {
	return Date.parse(b.created_at) - Date.parse(a.created_at);
}

/**
 * The server prunes expired rows hourly, so a tray that is open across the
 * boundary can still be holding rows the API would no longer return. Expiry is
 * applied on every write, and pruneExpiredNotifications keeps a long-lived tab
 * converging. Doing it in the reducer rather than the selector is deliberate:
 * createSelector memoises on its inputs and would never recompute as the clock
 * moves.
 */
function isLive(notification: Notification, now: number) {
	const expiresAt = Date.parse(notification.expires_at);
	return Number.isNaN(expiresAt) || expiresAt > now;
}

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

	notifications: Notification[];
	notificationPreferences: {
		scope: string | null;
		readIds: string[];
		dismissedIds: string[];
	};

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
	notificationPreferences: { scope: null, readIds: [], dismissedIds: [] },
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
		setNotifications: (state, action: PayloadAction<Notification[]>) => {
			const now = Date.now();
			const byID = new Map(state.notifications.map((notification) => [notification.id, notification]));
			action.payload.forEach((notification) => byID.set(notification.id, notification));
			state.notifications = Array.from(byID.values())
				.filter((notification) => isLive(notification, now))
				.sort(byNewestFirst)
				.slice(0, MAX_NOTIFICATIONS);
		},

		addNotification: (state, action: PayloadAction<Notification>) => {
			const now = Date.now();
			state.notifications = [action.payload, ...state.notifications.filter((notification) => notification.id !== action.payload.id)]
				.filter((notification) => isLive(notification, now))
				.sort(byNewestFirst)
				.slice(0, MAX_NOTIFICATIONS);
		},

		/** Drops rows whose expires_at has passed. Dispatched on a timer by useNotificationSync. */
		pruneExpiredNotifications: (state) => {
			const now = Date.now();
			const live = state.notifications.filter((notification) => isLive(notification, now));
			// Only reassign on an actual change, so the periodic tick does not
			// invalidate every notification selector on a quiet deployment.
			if (live.length !== state.notifications.length) {
				state.notifications = live;
			}
		},

		hydrateNotificationPreferences: (state, action: PayloadAction<{ scope: string; readIds: string[]; dismissedIds: string[] }>) => {
			if (state.notificationPreferences.scope !== action.payload.scope) {
				state.notifications = [];
			}
			state.notificationPreferences = action.payload;
		},

		markNotificationRead: (state, action: PayloadAction<string>) => {
			if (!state.notificationPreferences.readIds.includes(action.payload)) {
				state.notificationPreferences.readIds.push(action.payload);
			}
		},

		removeNotification: (state, action: PayloadAction<string>) => {
			if (!state.notificationPreferences.dismissedIds.includes(action.payload)) {
				state.notificationPreferences.dismissedIds.push(action.payload);
			}
		},

		clearAllNotifications: (state) => {
			state.notificationPreferences.dismissedIds = Array.from(
				new Set([...state.notificationPreferences.dismissedIds, ...state.notifications.map((notification) => notification.id)]),
			);
		},

		markAllNotificationsRead: (state) => {
			state.notificationPreferences.readIds = Array.from(
				new Set([...state.notificationPreferences.readIds, ...state.notifications.map((notification) => notification.id)]),
			);
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
	setNotifications,
	pruneExpiredNotifications,
	hydrateNotificationPreferences,
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
const selectReadNotificationIds = (state: { app: AppState }) => state.app.notificationPreferences.readIds;
const selectDismissedNotificationIds = (state: { app: AppState }) => state.app.notificationPreferences.dismissedIds;
export const selectVisibleNotifications = createSelector(
	[selectNotifications, selectDismissedNotificationIds],
	(notifications, dismissedIds) => notifications.filter((notification) => !dismissedIds.includes(notification.id)),
);
export const selectUnreadNotificationsCount = createSelector(
	[selectVisibleNotifications, selectReadNotificationIds],
	(notifications, readIds) => notifications.filter((notification) => !readIds.includes(notification.id)).length,
);
export const selectNotificationPreferences = (state: { app: AppState }) => state.app.notificationPreferences;
export const selectSettings = (state: { app: AppState }) => state.app.settings;
export const selectGlobalError = (state: { app: AppState }) => state.app.globalError;
export const selectFeatures = (state: { app: AppState }) => state.app.features;
