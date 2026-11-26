// Pages whose data is served entirely from the logs store. They stay usable when
// Bifrost runs from config.json alone (no config store), as long as a logs store
// is connected.
export const LOGS_STORE_ONLY_ROUTES = ["/workspace/logs", "/workspace/mcp-logs", "/workspace/dashboard"] as const;

export function isLogsStoreOnlyRoute(pathname: string): boolean {
	return LOGS_STORE_ONLY_ROUTES.some((route) => pathname === route || pathname.startsWith(`${route}/`));
}

// Whether a page can render given which stores are connected. A config store
// unlocks every page; a logs store alone unlocks only the logs-backed ones.
export function canRenderWithStores(pathname: string, stores: { isDbConnected: boolean; isLogsConnected: boolean }): boolean {
	if (stores.isDbConnected) {
		return true;
	}
	return stores.isLogsConnected && isLogsStoreOnlyRoute(pathname);
}